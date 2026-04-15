package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEmpty(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	assertDefaults(t, cfg)
}

func TestParseEmptyBytes(t *testing.T) {
	cfg, err := Parse([]byte{})
	if err != nil {
		t.Fatalf("Parse([]byte{}): %v", err)
	}
	assertDefaults(t, cfg)
}

func TestParseEmptyYAML(t *testing.T) {
	cfg, err := Parse([]byte("---\n"))
	if err != nil {
		t.Fatalf("Parse empty YAML: %v", err)
	}
	assertDefaults(t, cfg)
}

func TestParsePartial(t *testing.T) {
	yaml := []byte("languages:\n  - go\n  - python\n")
	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse partial: %v", err)
	}
	if len(cfg.Languages) != 2 {
		t.Errorf("expected 2 languages, got %d", len(cfg.Languages))
	}
	if cfg.Languages[0] != "go" || cfg.Languages[1] != "python" {
		t.Errorf("unexpected languages: %v", cfg.Languages)
	}
	// Defaults should still be applied for unset fields.
	if cfg.ClaimsDB != DefaultClaimsDBPath {
		t.Errorf("ClaimsDB = %q, want %q", cfg.ClaimsDB, DefaultClaimsDBPath)
	}
	if cfg.CacheDB != DefaultCacheDBPath {
		t.Errorf("CacheDB = %q, want %q", cfg.CacheDB, DefaultCacheDBPath)
	}
}

func TestParseFull(t *testing.T) {
	yaml := []byte(`
languages:
  - go
include:
  - "cmd/**"
exclude:
  - "generated"
claims_db: "custom/claims.db"
cache_db: "custom/cache.db"
repo: "my-org/my-repo"
`)
	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse full: %v", err)
	}
	if cfg.ClaimsDB != "custom/claims.db" {
		t.Errorf("ClaimsDB = %q, want %q", cfg.ClaimsDB, "custom/claims.db")
	}
	if cfg.CacheDB != "custom/cache.db" {
		t.Errorf("CacheDB = %q, want %q", cfg.CacheDB, "custom/cache.db")
	}
	if cfg.Repo != "my-org/my-repo" {
		t.Errorf("Repo = %q, want %q", cfg.Repo, "my-org/my-repo")
	}
	// User exclude should be merged with defaults.
	hasGenerated := false
	hasGit := false
	for _, e := range cfg.Exclude {
		if e == "generated" {
			hasGenerated = true
		}
		if e == ".git" {
			hasGit = true
		}
	}
	if !hasGenerated {
		t.Error("missing user exclude 'generated'")
	}
	if !hasGit {
		t.Error("missing default exclude '.git'")
	}
}

func TestParseInvalidYAML(t *testing.T) {
	_, err := Parse([]byte("{{invalid"))
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadNonexistent(t *testing.T) {
	cfg, err := Load("/nonexistent/.livedocs.yaml")
	if err != nil {
		t.Fatalf("Load nonexistent: %v", err)
	}
	assertDefaults(t, cfg)
}

func TestLoadExistingFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".livedocs.yaml")
	if err := os.WriteFile(cfgPath, []byte("languages:\n  - typescript\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Languages) != 1 || cfg.Languages[0] != "typescript" {
		t.Errorf("unexpected languages: %v", cfg.Languages)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".livedocs.yaml")
	if err := os.WriteFile(cfgPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load empty file: %v", err)
	}
	assertDefaults(t, cfg)
}

func TestApplyDefaultsNoDuplicateExcludes(t *testing.T) {
	cfg := Config{
		Exclude: []string{".git", "vendor", "custom-dir"},
	}
	applied := cfg.ApplyDefaults()
	count := 0
	for _, e := range applied.Exclude {
		if e == ".git" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected .git once, got %d times", count)
	}
}

func TestDefaultYAML(t *testing.T) {
	out := DefaultYAML([]string{"go", "python"})
	if out == "" {
		t.Error("DefaultYAML returned empty string")
	}
	// Should contain the header comment.
	if len(out) < 10 {
		t.Error("DefaultYAML output too short")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	original := Config{
		Languages: []string{"go"},
		ClaimsDB:  "custom.db",
		Repo:      "test/repo",
	}
	data, err := Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	restored, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse after Marshal: %v", err)
	}
	if len(restored.Languages) != 1 || restored.Languages[0] != "go" {
		t.Errorf("languages mismatch: %v", restored.Languages)
	}
	if restored.ClaimsDB != "custom.db" {
		t.Errorf("ClaimsDB = %q, want %q", restored.ClaimsDB, "custom.db")
	}
}

func TestTribalDefaults(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	if cfg.Tribal.LLMEnabled {
		t.Error("Tribal.LLMEnabled should default to false")
	}
	if cfg.Tribal.DailyBudget != DefaultTribalDailyBudget {
		t.Errorf("Tribal.DailyBudget = %d, want %d", cfg.Tribal.DailyBudget, DefaultTribalDailyBudget)
	}
	if cfg.Tribal.Model != DefaultTribalModel {
		t.Errorf("Tribal.Model = %q, want %q", cfg.Tribal.Model, DefaultTribalModel)
	}
	if len(cfg.Tribal.AllowedRepos) != 0 {
		t.Errorf("Tribal.AllowedRepos = %v, want empty", cfg.Tribal.AllowedRepos)
	}
	if cfg.Tribal.MaxFilesPerRun != DefaultTribalMaxFilesPerRun {
		t.Errorf("Tribal.MaxFilesPerRun = %d, want %d", cfg.Tribal.MaxFilesPerRun, DefaultTribalMaxFilesPerRun)
	}
	if cfg.Tribal.CriticBudgetPercent != DefaultTribalCriticBudgetPercent {
		t.Errorf("Tribal.CriticBudgetPercent = %d, want %d", cfg.Tribal.CriticBudgetPercent, DefaultTribalCriticBudgetPercent)
	}
	if cfg.Tribal.ClusterDebugEnabled {
		t.Error("Tribal.ClusterDebugEnabled should default to false")
	}
}

func TestTribalPhase3Fields(t *testing.T) {
	t.Run("defaults when unset", func(t *testing.T) {
		cfg, err := Parse([]byte("tribal:\n  llm_enabled: true\n"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if cfg.Tribal.MaxFilesPerRun != DefaultTribalMaxFilesPerRun {
			t.Errorf("Tribal.MaxFilesPerRun = %d, want %d",
				cfg.Tribal.MaxFilesPerRun, DefaultTribalMaxFilesPerRun)
		}
		if cfg.Tribal.CriticBudgetPercent != DefaultTribalCriticBudgetPercent {
			t.Errorf("Tribal.CriticBudgetPercent = %d, want %d",
				cfg.Tribal.CriticBudgetPercent, DefaultTribalCriticBudgetPercent)
		}
		if cfg.Tribal.ClusterDebugEnabled {
			t.Error("Tribal.ClusterDebugEnabled should default to false")
		}
	})

	t.Run("overrides when set", func(t *testing.T) {
		yamlData := []byte(`tribal:
  max_files_per_run: 25
  critic_budget_percent: 35
  cluster_debug_enabled: true
`)
		cfg, err := Parse(yamlData)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if cfg.Tribal.MaxFilesPerRun != 25 {
			t.Errorf("Tribal.MaxFilesPerRun = %d, want 25", cfg.Tribal.MaxFilesPerRun)
		}
		if cfg.Tribal.CriticBudgetPercent != 35 {
			t.Errorf("Tribal.CriticBudgetPercent = %d, want 35", cfg.Tribal.CriticBudgetPercent)
		}
		if !cfg.Tribal.ClusterDebugEnabled {
			t.Error("Tribal.ClusterDebugEnabled should be true")
		}

		// Round-trip: marshal and re-parse should preserve overrides.
		data, err := Marshal(cfg)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		restored, err := Parse(data)
		if err != nil {
			t.Fatalf("Parse after Marshal: %v", err)
		}
		if restored.Tribal.MaxFilesPerRun != 25 {
			t.Errorf("round-trip MaxFilesPerRun = %d, want 25", restored.Tribal.MaxFilesPerRun)
		}
		if restored.Tribal.CriticBudgetPercent != 35 {
			t.Errorf("round-trip CriticBudgetPercent = %d, want 35", restored.Tribal.CriticBudgetPercent)
		}
		if !restored.Tribal.ClusterDebugEnabled {
			t.Error("round-trip ClusterDebugEnabled lost")
		}
	})

	t.Run("invalid negative max_files_per_run", func(t *testing.T) {
		yamlData := []byte("tribal:\n  max_files_per_run: -5\n")
		_, err := Parse(yamlData)
		if err == nil {
			t.Fatal("expected error for negative max_files_per_run, got nil")
		}
	})

	t.Run("init template documents phase 3 fields", func(t *testing.T) {
		out := DefaultYAML([]string{"go"})
		wantSubstrings := []string{
			"max_files_per_run",
			"critic_budget_percent",
			"cluster_debug_enabled",
		}
		for _, s := range wantSubstrings {
			if !strings.Contains(out, s) {
				t.Errorf("DefaultYAML output missing %q; got:\n%s", s, out)
			}
		}
	})
}

func TestTribalOptIn(t *testing.T) {
	yamlData := []byte("tribal:\n  llm_enabled: true\n")
	cfg, err := Parse(yamlData)
	if err != nil {
		t.Fatalf("Parse tribal opt-in: %v", err)
	}
	if !cfg.Tribal.LLMEnabled {
		t.Error("Tribal.LLMEnabled should be true after explicit opt-in")
	}
	// Defaults should still apply for unset fields.
	if cfg.Tribal.DailyBudget != DefaultTribalDailyBudget {
		t.Errorf("Tribal.DailyBudget = %d, want %d", cfg.Tribal.DailyBudget, DefaultTribalDailyBudget)
	}
	if cfg.Tribal.Model != DefaultTribalModel {
		t.Errorf("Tribal.Model = %q, want %q", cfg.Tribal.Model, DefaultTribalModel)
	}

	// Round-trip: marshal and re-parse should preserve LLMEnabled.
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	restored, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse after Marshal: %v", err)
	}
	if !restored.Tribal.LLMEnabled {
		t.Error("Tribal.LLMEnabled lost after round-trip")
	}
}

func TestTribalAllowedRepos(t *testing.T) {
	yamlData := []byte(`tribal:
  llm_enabled: true
  allowed_repos:
    - "kubernetes/kubernetes"
    - "live-docs/live_docs"
  daily_budget: 50
  model: "claude-sonnet-4-20250514"
`)
	cfg, err := Parse(yamlData)
	if err != nil {
		t.Fatalf("Parse tribal allowed repos: %v", err)
	}
	if !cfg.Tribal.LLMEnabled {
		t.Error("Tribal.LLMEnabled should be true")
	}
	if len(cfg.Tribal.AllowedRepos) != 2 {
		t.Fatalf("Tribal.AllowedRepos has %d entries, want 2", len(cfg.Tribal.AllowedRepos))
	}
	if cfg.Tribal.AllowedRepos[0] != "kubernetes/kubernetes" {
		t.Errorf("AllowedRepos[0] = %q, want %q", cfg.Tribal.AllowedRepos[0], "kubernetes/kubernetes")
	}
	if cfg.Tribal.AllowedRepos[1] != "live-docs/live_docs" {
		t.Errorf("AllowedRepos[1] = %q, want %q", cfg.Tribal.AllowedRepos[1], "live-docs/live_docs")
	}
	if cfg.Tribal.DailyBudget != 50 {
		t.Errorf("Tribal.DailyBudget = %d, want 50", cfg.Tribal.DailyBudget)
	}
	if cfg.Tribal.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Tribal.Model = %q, want %q", cfg.Tribal.Model, "claude-sonnet-4-20250514")
	}
}

func TestTribalCustomBudgetPreserved(t *testing.T) {
	yamlData := []byte("tribal:\n  daily_budget: 200\n")
	cfg, err := Parse(yamlData)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Tribal.DailyBudget != 200 {
		t.Errorf("Tribal.DailyBudget = %d, want 200 (should not be overwritten by default)", cfg.Tribal.DailyBudget)
	}
}

func assertDefaults(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.ClaimsDB != DefaultClaimsDBPath {
		t.Errorf("ClaimsDB = %q, want %q", cfg.ClaimsDB, DefaultClaimsDBPath)
	}
	if cfg.CacheDB != DefaultCacheDBPath {
		t.Errorf("CacheDB = %q, want %q", cfg.CacheDB, DefaultCacheDBPath)
	}
	if len(cfg.Languages) != 0 {
		t.Errorf("Languages = %v, want empty", cfg.Languages)
	}
	if len(cfg.Exclude) != len(defaultExclude) {
		t.Errorf("Exclude has %d entries, want %d", len(cfg.Exclude), len(defaultExclude))
	}
	// Tribal defaults should also be applied.
	if cfg.Tribal.LLMEnabled {
		t.Error("Tribal.LLMEnabled should default to false")
	}
	if cfg.Tribal.DailyBudget != DefaultTribalDailyBudget {
		t.Errorf("Tribal.DailyBudget = %d, want %d", cfg.Tribal.DailyBudget, DefaultTribalDailyBudget)
	}
	if cfg.Tribal.Model != DefaultTribalModel {
		t.Errorf("Tribal.Model = %q, want %q", cfg.Tribal.Model, DefaultTribalModel)
	}
	if cfg.Tribal.MaxFilesPerRun != DefaultTribalMaxFilesPerRun {
		t.Errorf("Tribal.MaxFilesPerRun = %d, want %d", cfg.Tribal.MaxFilesPerRun, DefaultTribalMaxFilesPerRun)
	}
	if cfg.Tribal.CriticBudgetPercent != DefaultTribalCriticBudgetPercent {
		t.Errorf("Tribal.CriticBudgetPercent = %d, want %d", cfg.Tribal.CriticBudgetPercent, DefaultTribalCriticBudgetPercent)
	}
	if cfg.Tribal.ClusterDebugEnabled {
		t.Error("Tribal.ClusterDebugEnabled should default to false")
	}
}
