package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sjarmak/livedocs/config"
	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor/tribal"
	"github.com/sjarmak/livedocs/mcpserver"
	"github.com/sjarmak/livedocs/semantic"
)

// gitRemoteTimeout bounds the `git remote get-url` subprocess invoked per
// factory call. The command is local-only (no network) so a short timeout is
// fine; keeping it bounded prevents a hung git process from blocking MCP
// tool handlers.
const gitRemoteTimeout = 5 * time.Second

// llmClient is the minimal interface buildMiningFactory needs from an LLM
// provider. It intentionally mirrors semantic.LLMClient (and therefore
// tribal.LLMClient) so production code can plug either implementation in.
type llmClient interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// llmClientFactory injects constructors for Claude CLI and Anthropic API
// clients. Tests supply their own factory to exercise branching without
// touching the real filesystem or network.
type llmClientFactory struct {
	// newCLI returns a client backed by the `claude` CLI with the given
	// model, or an error if the CLI binary is not available.
	newCLI func(model string) (llmClient, error)
	// newAPI returns a client backed by the Anthropic API using apiKey
	// and model, or an error if construction fails.
	newAPI func(apiKey, model string) (llmClient, error)
}

// defaultLLMClientFactory wraps the production semantic package constructors.
var defaultLLMClientFactory = llmClientFactory{
	newCLI: func(model string) (llmClient, error) {
		return semantic.NewClaudeCLIClient(model)
	},
	newAPI: func(apiKey, model string) (llmClient, error) {
		if apiKey == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY is empty")
		}
		return semantic.NewAnthropicClient(apiKey, semantic.WithModel(model))
	},
}

// buildMiningFactory returns a mcpserver.MiningServiceFactory if and only if
// tribal JIT mining is configured for production use:
//
//   - cfg.LLMEnabled must be true (explicit opt-in; deterministic extractors
//     run regardless, but LLM-classified PR comment mining is Phase 2 and
//     must not be silently enabled).
//   - cfg.DailyBudget must be > 0 (zero or negative would allow either
//     unbounded calls or silent no-ops depending on the consuming code path;
//     both are dangerous in a long-running MCP server).
//
// When either precondition fails, the returned factory is nil and the tool
// is not registered.
//
// LLM client availability is deliberately NOT checked at wire-up time
// (live_docs-m7v.23). A transient probe failure at server startup (temporary
// PATH issue, short-lived env-var glitch, race with a credential-rotation
// process) must not disable tribal_mine_on_demand for the entire MCP server
// lifetime. Instead, the returned factory closure re-resolves the LLM
// client on every invocation — so credential rotation is picked up
// automatically AND transient wire-up failures self-heal on the next call.
//
// When the LLM client is still unreachable at call time, the closure
// returns an error wrapping mcpserver.ErrLLMClientUnavailable so the caller
// (TribalMineOnDemandHandler) can surface an actionable message to the MCP
// client ("claude CLI not on PATH and ANTHROPIC_API_KEY unset") instead of
// the generic "mining service unavailable" fallback.
//
// RepoOwner / RepoName are derived from the per-repo claims DB's
// extraction_meta.RepoRoot via `git remote get-url origin`; callers that
// extract repos without a git remote simply will not have
// tribal_mine_on_demand work for that repo, but other repos remain usable.
func buildMiningFactory(cfg config.TribalConfig, llmFactory llmClientFactory) mcpserver.MiningServiceFactory {
	if !cfg.LLMEnabled {
		return nil
	}
	if cfg.DailyBudget <= 0 {
		return nil
	}

	model := cfg.Model
	budget := cfg.DailyBudget

	return func(repo string, cdb *db.ClaimsDB) (*tribal.TribalMiningService, error) {
		client, err := resolveLLMForMining(model, llmFactory)
		if err != nil {
			// Wrap the mcpserver sentinel so the handler can errors.Is
			// against it and render an actionable error result. We
			// intentionally include the resolution-error text because
			// the CLI/API helpers already redact credentials in their
			// error returns (ClaudeCLIClient returns "not found on PATH"
			// from exec.LookPath; AnthropicClient returns a construction
			// error that does not echo the key). The wrapped message
			// remains safe to log and return.
			return nil, fmt.Errorf("%w (%s)", mcpserver.ErrLLMClientUnavailable, err.Error())
		}

		owner, name, err := resolveRepoOwner(repo, cdb)
		if err != nil {
			return nil, fmt.Errorf("mining factory: resolve repo owner for %q: %w", repo, err)
		}

		return tribal.NewTribalMiningService(cdb, tribal.PRMinerConfig{
			RepoOwner:   owner,
			RepoName:    name,
			Client:      client,
			Model:       model,
			DailyBudget: budget,
		}, repo), nil
	}
}

// resolveLLMForMining prefers the Claude CLI (OAuth) and falls back to the
// Anthropic API, matching extract_cmd's precedence exactly. It is invoked
// fresh on every factory call so refreshed credentials — or a transient
// wire-up failure that has since resolved — are picked up without server
// restart.
//
// Returned errors carry the reason ("claude CLI not on PATH ...") but never
// echo the API key value.
func resolveLLMForMining(model string, f llmClientFactory) (llmClient, error) {
	if f.newCLI != nil {
		if c, err := f.newCLI(model); err == nil {
			return c, nil
		}
	}
	if f.newAPI != nil {
		if c, err := f.newAPI(os.Getenv("ANTHROPIC_API_KEY"), model); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no LLM client available (claude CLI not on PATH and ANTHROPIC_API_KEY unset)")
}

// resolveRepoOwner reads the claims DB's extraction_meta.RepoRoot and runs
// `git remote get-url origin` to extract the GitHub owner and repo name
// needed by the PR comment miner.
func resolveRepoOwner(repo string, cdb *db.ClaimsDB) (owner, name string, err error) {
	meta, err := cdb.GetExtractionMeta()
	if err != nil {
		return "", "", fmt.Errorf("read extraction_meta: %w", err)
	}
	if meta.RepoRoot == "" {
		return "", "", fmt.Errorf("extraction_meta.repo_root is empty for %q", repo)
	}

	// Confirm the directory exists to produce a clearer error than git's.
	info, err := os.Stat(meta.RepoRoot)
	if err != nil {
		return "", "", fmt.Errorf("stat repo root %q: %w", meta.RepoRoot, err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("repo root %q is not a directory", meta.RepoRoot)
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitRemoteTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", meta.RepoRoot, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("git remote get-url origin in %q: %w", meta.RepoRoot, err)
	}
	o, n, ok := parseGitRemoteURL(strings.TrimSpace(string(out)))
	if !ok {
		return "", "", fmt.Errorf("parse git remote url for %q", repo)
	}
	return o, n, nil
}

// logMiningFactoryWireup writes a single line to the given logger (defaulting
// to the standard library logger when nil) describing whether
// tribal_mine_on_demand will be registered. The three possible outcomes are
// reported as distinct log lines so operators can tell apart:
//
//  1. "disabled (llm_enabled=false)" — operator has not opted in.
//  2. "disabled (daily_budget<=0)"  — operator opted in but budget is 0.
//  3. "enabled (...; LLM probe runs per-call)" — tool is registered; the
//     probe is deferred to invocation time and the log line makes this
//     explicit so the operator knows a wire-up-time probe failure is
//     no longer the same thing as "disabled".
//
// The earlier implementation conflated "not configured" with "probe failed"
// in one disabled branch. Since the probe no longer runs at wire-up, that
// branch is now impossible — and we say so explicitly in the enabled case.
func logMiningFactoryWireup(factory mcpserver.MiningServiceFactory, cfg config.TribalConfig) {
	logMiningFactoryWireupTo(log.Default(), factory, cfg)
}

// logMiningFactoryWireupTo is the testable form that writes to an injected
// logger. Production callers use logMiningFactoryWireup.
func logMiningFactoryWireupTo(logger *log.Logger, factory mcpserver.MiningServiceFactory, cfg config.TribalConfig) {
	if factory != nil {
		logger.Printf(
			"mcp: tribal_mine_on_demand enabled (daily_budget=%d, model=%s); LLM probe runs per-call",
			cfg.DailyBudget, cfg.Model,
		)
		return
	}
	// factory == nil must correspond to exactly one of the config gates in
	// buildMiningFactory. If a future refactor adds a third nil-return
	// branch, the panic surfaces the drift immediately rather than letting
	// operators see a silent no-op.
	switch {
	case !cfg.LLMEnabled:
		logger.Printf("mcp: tribal_mine_on_demand disabled (tribal.llm_enabled=false)")
	case cfg.DailyBudget <= 0:
		logger.Printf("mcp: tribal_mine_on_demand disabled (tribal.daily_budget=%d)", cfg.DailyBudget)
	default:
		panic("logMiningFactoryWireupTo: nil factory with llm_enabled=true and daily_budget>0 — buildMiningFactory invariant violated")
	}
}
