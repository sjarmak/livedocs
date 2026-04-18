package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sjarmak/livedocs/config"
	"github.com/sjarmak/livedocs/db"
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

// TestBuildMiningFactory_Disabled_WhenLLMUnavailable verifies the factory is
// nil when no LLM client is available (CLI absent, API key absent).
func TestBuildMiningFactory_Disabled_WhenLLMUnavailable(t *testing.T) {
	cfg := config.TribalConfig{
		LLMEnabled:  true,
		DailyBudget: 100,
		Model:       "claude-haiku-4-5",
	}

	// Neither CLI nor API client is available.
	noLLM := llmClientFactory{
		newCLI: func(_ string) (llmClient, error) {
			return nil, errLLMNotAvailable
		},
		newAPI: func(_, _ string) (llmClient, error) {
			return nil, errLLMNotAvailable
		},
	}

	factory := buildMiningFactory(cfg, noLLM)
	if factory != nil {
		t.Fatalf("expected nil factory when no LLM available, got %T", factory)
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

// TestBuildMiningFactory_Enabled_WhenAPIKeyFallback verifies that when the
// CLI is unavailable but an API key is set, the factory is non-nil.
func TestBuildMiningFactory_Enabled_WhenAPIKeyFallback(t *testing.T) {
	cfg := config.TribalConfig{
		LLMEnabled:  true,
		DailyBudget: 100,
		Model:       "claude-haiku-4-5",
	}

	// CLI unavailable, API key succeeds.
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
		t.Fatalf("expected non-nil factory when API key set; got nil")
	}
}

// TestBuildMiningFactory_Disabled_WhenAPIKeyMissingAndCLIAbsent asserts the
// factory is nil when CLI is absent and ANTHROPIC_API_KEY is also unset.
func TestBuildMiningFactory_Disabled_WhenAPIKeyMissingAndCLIAbsent(t *testing.T) {
	cfg := config.TribalConfig{
		LLMEnabled:  true,
		DailyBudget: 100,
		Model:       "claude-haiku-4-5",
	}

	// CLI unavailable, API requires non-empty key.
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

	// Ensure the env var is unset for the duration of the test.
	t.Setenv("ANTHROPIC_API_KEY", "")
	_ = os.Unsetenv("ANTHROPIC_API_KEY")

	factory := buildMiningFactory(cfg, apiOnly)
	if factory != nil {
		t.Fatalf("expected nil factory when CLI and API key both absent, got %T", factory)
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
