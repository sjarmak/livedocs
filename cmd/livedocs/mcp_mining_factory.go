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
// tribal JIT mining is fully configured for production use:
//
//   - cfg.LLMEnabled must be true (explicit opt-in; deterministic extractors
//     run regardless, but LLM-classified PR comment mining is Phase 2 and
//     must not be silently enabled).
//   - cfg.DailyBudget must be > 0 (zero or negative would allow either
//     unbounded calls or silent no-ops depending on the consuming code path;
//     both are dangerous in a long-running MCP server).
//   - At least one LLM client path must succeed at wire-up time — either the
//     Claude CLI on PATH (preferred, uses OAuth) or ANTHROPIC_API_KEY in the
//     environment.
//
// When any precondition fails, the returned factory is nil. A nil factory
// causes mcpserver.Server to skip registration of the tribal_mine_on_demand
// tool entirely, which is the safe behavior: the tool will simply not appear
// in the MCP tool list for agents to call.
//
// The returned factory re-resolves the LLM client on every invocation so that
// credential rotation (e.g. refreshed CLI OAuth tokens) is picked up without
// restarting the server. It derives RepoOwner / RepoName from the per-repo
// claims DB's extraction_meta.RepoRoot via `git remote get-url origin`;
// callers that extract repos without a git remote simply will not have
// tribal_mine_on_demand work for that repo, but other repos remain usable.
func buildMiningFactory(cfg config.TribalConfig, llmFactory llmClientFactory) mcpserver.MiningServiceFactory {
	if !cfg.LLMEnabled {
		return nil
	}
	if cfg.DailyBudget <= 0 {
		return nil
	}

	// Probe LLM availability once at wire-up time. If neither path works we
	// return nil so the tool is never registered — better than registering a
	// tool that will fail every call.
	if _, err := resolveLLMForMining(cfg.Model, llmFactory); err != nil {
		return nil
	}

	model := cfg.Model
	budget := cfg.DailyBudget

	return func(repo string, cdb *db.ClaimsDB) (*tribal.TribalMiningService, error) {
		client, err := resolveLLMForMining(model, llmFactory)
		if err != nil {
			return nil, fmt.Errorf("mining factory: resolve llm client: %w", err)
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
// Anthropic API, matching extract_cmd's precedence exactly. It is used both
// as a wire-up-time probe (buildMiningFactory checks for at least one success)
// and on every factory invocation (so refreshed credentials are picked up).
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

// logMiningFactoryWireup writes a single line to stderr describing whether
// tribal_mine_on_demand will be registered. Kept in a helper so mcp.go stays
// small.
func logMiningFactoryWireup(factory mcpserver.MiningServiceFactory, cfg config.TribalConfig) {
	if factory != nil {
		log.Printf("mcp: tribal_mine_on_demand enabled (daily_budget=%d, model=%s)",
			cfg.DailyBudget, cfg.Model)
		return
	}
	switch {
	case !cfg.LLMEnabled:
		log.Printf("mcp: tribal_mine_on_demand disabled (tribal.llm_enabled=false)")
	case cfg.DailyBudget <= 0:
		log.Printf("mcp: tribal_mine_on_demand disabled (tribal.daily_budget=%d)", cfg.DailyBudget)
	default:
		log.Printf("mcp: tribal_mine_on_demand disabled (no LLM client: claude CLI missing and ANTHROPIC_API_KEY unset)")
	}
}
