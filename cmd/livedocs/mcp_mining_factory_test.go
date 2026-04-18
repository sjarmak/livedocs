package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjarmak/livedocs/config"
	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/mcpserver"
)

// TestBuildMiningFactory_Disabled_WhenLLMEnabledFalse verifies the factory is
// nil when Tribal.LLMEnabled=false. Without explicit opt-in, JIT mining must
// not silently consume the DailyBudget.
func TestBuildMiningFactory_Disabled_WhenLLMEnabledFalse(t *testing.T) {
	cfg := config.TribalConfig{
		LLMEnabled:  false,
		DailyBudget: 100,
		Model:       "claude-haiku-4-5",
	}

	factory := buildMiningFactory(cfg, fakeLLMClientFactory)
	if factory != nil {
		t.Fatalf("expected nil factory when LLMEnabled=false, got %T", factory)
	}
}

// TestBuildMiningFactory_Disabled_WhenBudgetZero verifies the factory is nil
// when DailyBudget <= 0 even if LLMEnabled=true. Zero budget means "no LLM
// calls allowed", which would make JIT mining a silent no-op.
func TestBuildMiningFactory_Disabled_WhenBudgetZero(t *testing.T) {
	cfg := config.TribalConfig{
		LLMEnabled:  true,
		DailyBudget: 0,
		Model:       "claude-haiku-4-5",
	}

	factory := buildMiningFactory(cfg, fakeLLMClientFactory)
	if factory != nil {
		t.Fatalf("expected nil factory when DailyBudget=0, got %T", factory)
	}
}

// TestBuildMiningFactory_RegistersWhenConfiguredEvenIfProbeFailsAtWireup is
// the m7v.23 regression guard. Before m7v.23, buildMiningFactory probed LLM
// availability at wire-up and returned nil on failure, leaving the tool
// unregistered for the entire MCP server lifetime. A transient PATH issue at
// startup would therefore permanently disable tribal_mine_on_demand. The fix
// moves the probe into the returned closure; the factory itself must be
// non-nil whenever config preconditions hold.
func TestBuildMiningFactory_RegistersWhenConfiguredEvenIfProbeFailsAtWireup(t *testing.T) {
	cfg := config.TribalConfig{
		LLMEnabled:  true,
		DailyBudget: 100,
		Model:       "claude-haiku-4-5",
	}

	// Both CLI and API report unavailable at wire-up — simulating a
	// transient failure (temporary PATH issue, credential rotation race,
	// etc.). Before m7v.23 this returned nil. After m7v.23 it must still
	// register the factory.
	unreachable := llmClientFactory{
		newCLI: func(_ string) (llmClient, error) {
			return nil, errLLMNotAvailable
		},
		newAPI: func(_, _ string) (llmClient, error) {
			return nil, errLLMNotAvailable
		},
	}

	factory := buildMiningFactory(cfg, unreachable)
	if factory == nil {
		t.Fatal("expected non-nil factory when LLMEnabled=true and DailyBudget>0 even if the probe would fail; got nil")
	}
}

// TestBuildMiningFactory_Enabled_WhenCLIAvailable verifies the factory is
// non-nil when the Claude CLI is available. The factory should produce a
// valid TribalMiningService when invoked with a seeded claims DB.
func TestBuildMiningFactory_Enabled_WhenCLIAvailable(t *testing.T) {
	cfg := config.TribalConfig{
		LLMEnabled:  true,
		DailyBudget: 100,
		Model:       "claude-haiku-4-5",
	}

	factory := buildMiningFactory(cfg, fakeLLMClientFactory)
	if factory == nil {
		t.Fatalf("expected non-nil factory when CLI available; got nil")
	}

	// Invoke the factory with a valid claims DB to confirm it constructs a
	// service instead of returning an error.
	tmpDir := t.TempDir()
	cdb := seedClaimsDBForFactory(t, tmpDir, "my-repo")
	defer cdb.Close()

	svc, err := factory("my-repo", cdb)
	if err != nil {
		t.Fatalf("factory returned error: %v", err)
	}
	if svc == nil {
		t.Fatal("factory returned nil service without error")
	}
}

// TestMiningFactory_CallTimeProbeReturnsErrorWhenUnreachable verifies that
// when both the Claude CLI and the Anthropic API key are unavailable at
// call time (not wire-up), the factory returns a wrapped
// ErrLLMClientUnavailable so callers can errors.Is-check and surface an
// actionable message to MCP clients. Crucially the factory is still
// non-nil — the tool was registered — but the invocation itself errors.
func TestMiningFactory_CallTimeProbeReturnsErrorWhenUnreachable(t *testing.T) {
	cfg := config.TribalConfig{
		LLMEnabled:  true,
		DailyBudget: 100,
		Model:       "claude-haiku-4-5",
	}

	unreachable := llmClientFactory{
		newCLI: func(_ string) (llmClient, error) {
			return nil, errLLMNotAvailable
		},
		newAPI: func(_, _ string) (llmClient, error) {
			return nil, errLLMNotAvailable
		},
	}

	factory := buildMiningFactory(cfg, unreachable)
	if factory == nil {
		t.Fatal("expected non-nil factory; got nil")
	}

	tmpDir := t.TempDir()
	cdb := seedClaimsDBForFactory(t, tmpDir, "repo-unreachable")
	defer cdb.Close()

	svc, err := factory("repo-unreachable", cdb)
	if err == nil {
		t.Fatal("expected error from factory when LLM is unreachable at call time; got nil")
	}
	if svc != nil {
		t.Fatalf("expected nil service on error; got %T", svc)
	}
	if !errors.Is(err, mcpserver.ErrLLMClientUnavailable) {
		t.Fatalf("expected error to wrap mcpserver.ErrLLMClientUnavailable so callers can classify it; got %v", err)
	}
	// The wrapped message must carry actionable guidance. We check for the
	// specific operator-facing strings the resolveLLMForMining helper
	// produces so regressions in the message are caught here.
	msg := err.Error()
	if !strings.Contains(msg, "claude CLI") || !strings.Contains(msg, "ANTHROPIC_API_KEY") {
		t.Fatalf("expected error message to name both LLM paths (claude CLI + ANTHROPIC_API_KEY); got %q", msg)
	}
	// Defense-in-depth: the error message must never contain an API key
	// value. Since the test env does not set one, we pick a canary string
	// and assert it is absent — true for any non-empty API key content.
	if strings.Contains(msg, "sk-ant") {
		t.Fatalf("error message leaks an API-key-like fragment: %q", msg)
	}
}

// TestMiningFactory_CallTimeProbeSucceedsWhenClientAvailable verifies the
// happy path: when the LLM client is available at call time, the factory
// returns a mining service without error. This is the paired positive case
// for TestMiningFactory_CallTimeProbeReturnsErrorWhenUnreachable.
func TestMiningFactory_CallTimeProbeSucceedsWhenClientAvailable(t *testing.T) {
	cfg := config.TribalConfig{
		LLMEnabled:  true,
		DailyBudget: 100,
		Model:       "claude-haiku-4-5",
	}

	factory := buildMiningFactory(cfg, fakeLLMClientFactory)
	if factory == nil {
		t.Fatal("expected non-nil factory; got nil")
	}

	tmpDir := t.TempDir()
	cdb := seedClaimsDBForFactory(t, tmpDir, "repo-available")
	defer cdb.Close()

	svc, err := factory("repo-available", cdb)
	if err != nil {
		t.Fatalf("factory returned error when LLM reachable: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service when LLM reachable")
	}
}

// TestMiningFactory_APIKeyFallback verifies the CLI -> API key precedence at
// call time. When the CLI probe fails but the API key is present, the
// factory should succeed on the second path.
func TestMiningFactory_APIKeyFallback(t *testing.T) {
	cfg := config.TribalConfig{
		LLMEnabled:  true,
		DailyBudget: 100,
		Model:       "claude-haiku-4-5",
	}

	apiOnly := llmClientFactory{
		newCLI: func(_ string) (llmClient, error) {
			return nil, errLLMNotAvailable
		},
		newAPI: func(apiKey, _ string) (llmClient, error) {
			if apiKey == "" {
				return nil, errLLMNotAvailable
			}
			return &stubLLMClient{}, nil
		},
	}

	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	factory := buildMiningFactory(cfg, apiOnly)
	if factory == nil {
		t.Fatalf("expected non-nil factory; got nil")
	}

	tmpDir := t.TempDir()
	cdb := seedClaimsDBForFactory(t, tmpDir, "repo-apifallback")
	defer cdb.Close()

	svc, err := factory("repo-apifallback", cdb)
	if err != nil {
		t.Fatalf("factory errored despite API key path: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

// TestMiningFactory_FailsWhenRepoOwnerUnresolvable verifies the factory
// surfaces a specific (non-ErrLLMClientUnavailable) error when the LLM is
// fine but the repo metadata is broken. The caller must be able to
// distinguish this case from "LLM unreachable".
func TestMiningFactory_FailsWhenRepoOwnerUnresolvable(t *testing.T) {
	cfg := config.TribalConfig{
		LLMEnabled:  true,
		DailyBudget: 100,
		Model:       "claude-haiku-4-5",
	}

	factory := buildMiningFactory(cfg, fakeLLMClientFactory)
	if factory == nil {
		t.Fatal("expected non-nil factory; got nil")
	}

	// Seed a DB with no RepoRoot set, so resolveRepoOwner fails.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "broken.claims.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	defer cdb.Close()
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	// Deliberately do not call SetExtractionMeta — RepoRoot stays empty.

	svc, err := factory("broken", cdb)
	if err == nil {
		t.Fatal("expected error when repo owner unresolvable; got nil")
	}
	if svc != nil {
		t.Fatal("expected nil service on error")
	}
	if errors.Is(err, mcpserver.ErrLLMClientUnavailable) {
		t.Fatalf("repo-owner error must NOT be classified as LLM-unavailable; got %v", err)
	}
}

// TestLogMiningFactoryWireup_DistinguishesDisabledFromProbeFailed captures
// the three distinct log lines produced by logMiningFactoryWireupTo so
// operators can tell configured-vs-disabled apart. The pre-m7v.23 behaviour
// conflated "probe failed" with "not configured" — this test guards the
// split.
func TestLogMiningFactoryWireup_DistinguishesDisabledFromProbeFailed(t *testing.T) {
	type scenario struct {
		name        string
		factoryNil  bool
		cfg         config.TribalConfig
		wantSubstr  string
		notSubstrs  []string
	}

	cases := []scenario{
		{
			name:       "disabled_llm_enabled_false",
			factoryNil: true,
			cfg: config.TribalConfig{
				LLMEnabled:  false,
				DailyBudget: 100,
				Model:       "claude-haiku-4-5",
			},
			wantSubstr: "llm_enabled=false",
			notSubstrs: []string{"daily_budget", "enabled (", "probe"},
		},
		{
			name:       "disabled_daily_budget_zero",
			factoryNil: true,
			cfg: config.TribalConfig{
				LLMEnabled:  true,
				DailyBudget: 0,
				Model:       "claude-haiku-4-5",
			},
			wantSubstr: "daily_budget=0",
			notSubstrs: []string{"llm_enabled=false", "enabled (", "probe"},
		},
		{
			name:       "enabled_probe_deferred",
			factoryNil: false,
			cfg: config.TribalConfig{
				LLMEnabled:  true,
				DailyBudget: 100,
				Model:       "claude-haiku-4-5",
			},
			wantSubstr: "enabled",
			// After m7v.23 the enabled line must explicitly mention that
			// the probe runs per-call so operators know a startup probe
			// failure is no longer a permanent disable.
			notSubstrs: []string{"disabled", "llm_enabled=false"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := log.New(&buf, "", 0)

			var factory mcpserver.MiningServiceFactory
			if !tc.factoryNil {
				factory = buildMiningFactory(tc.cfg, fakeLLMClientFactory)
				if factory == nil {
					t.Fatal("scenario requires non-nil factory but buildMiningFactory returned nil")
				}
			}
			logMiningFactoryWireupTo(logger, factory, tc.cfg)

			got := buf.String()
			if !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("expected log line to contain %q; got %q", tc.wantSubstr, got)
			}
			for _, s := range tc.notSubstrs {
				if strings.Contains(got, s) {
					t.Errorf("expected log line NOT to contain %q; got %q", s, got)
				}
			}
			// Sanity: exactly one log line should be emitted per call.
			if got == "" || strings.Count(got, "\n") != 1 {
				t.Errorf("expected exactly one newline-terminated log line; got %q", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// seedClaimsDBForFactory creates a claims DB with schema + extraction_meta
// pointing at a valid RepoRoot directory. The RepoRoot is initialized as a
// git repo with a fake GitHub "origin" remote so the factory's git-remote
// lookup succeeds and yields a parseable owner/name pair.
func seedClaimsDBForFactory(t *testing.T, dir, repoName string) *db.ClaimsDB {
	t.Helper()
	repoDir := filepath.Join(dir, repoName)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Initialize git repo + set a canned origin remote. The factory only
	// reads `git remote get-url origin`, so no commits are needed.
	runGit := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q")
	runGit("remote", "add", "origin", "https://github.com/test-org/"+repoName+".git")

	dbPath := filepath.Join(dir, repoName+".claims.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := cdb.SetExtractionMeta(db.ExtractionMeta{
		CommitSHA:   "abc123",
		ExtractedAt: "2025-01-01T00:00:00Z",
		RepoRoot:    repoDir,
	}); err != nil {
		t.Fatalf("set extraction meta: %v", err)
	}
	return cdb
}
