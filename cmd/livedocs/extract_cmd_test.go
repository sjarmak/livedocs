package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/live-docs/live_docs/semantic"

	_ "modernc.org/sqlite"
)

// resetExtractFlags resets global flag state to avoid leaking between tests.
func resetExtractFlags() {
	extractRepo = ""
	extractOutput = ""
	extractTier2 = false
	extractTribal = ""
}

func TestExtractCommandRegistered(t *testing.T) {
	registered := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		registered[cmd.Name()] = true
	}
	if !registered["extract"] {
		t.Error("extract subcommand not registered on root command")
	}
}

func TestExtractCommandFlags(t *testing.T) {
	// Verify the command has the expected flags and description without
	// invoking Execute (which can leave sticky state in cobra).
	if extractCmd.Long == "" {
		t.Error("extract command missing long description")
	}
	if !strings.Contains(extractCmd.Long, "tree-sitter") {
		t.Error("extract description missing tree-sitter mention")
	}
	if extractCmd.Flags().Lookup("repo") == nil {
		t.Error("extract command missing --repo flag")
	}
	if extractCmd.Flags().Lookup("output") == nil {
		t.Error("extract command missing --output flag")
	}
}

func TestExtractCommandRequiresArgs(t *testing.T) {
	resetExtractFlags()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"extract"})
	err := rootCmd.Execute()
	if err == nil {
		t.Error("expected error when no args provided")
	}
}

func TestExtractCreatesDB(t *testing.T) {
	resetExtractFlags()

	// Create a temp directory with a simple Go file.
	repoDir := t.TempDir()
	goFile := filepath.Join(repoDir, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main

import "fmt"

// Hello prints a greeting.
func Hello() {
	fmt.Println("hello")
}

func main() {
	Hello()
}
`), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}

	// Also write go.mod so go/packages can load it.
	goMod := filepath.Join(repoDir, "go.mod")
	if err := os.WriteFile(goMod, []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "test.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"extract", "--repo", "test-repo", "--output", outputDB, repoDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("extract command failed: %v", err)
	}

	// Verify the DB file exists.
	if _, err := os.Stat(outputDB); os.IsNotExist(err) {
		t.Fatal("output DB file was not created")
	}

	out := buf.String()
	if !strings.Contains(out, "Extract Summary") {
		t.Error("output missing Extract Summary")
	}
	if !strings.Contains(out, "test-repo") {
		t.Error("output missing repo name")
	}
}

func TestExtractWithPythonFile(t *testing.T) {
	resetExtractFlags()

	repoDir := t.TempDir()

	// Write a Python file.
	pyFile := filepath.Join(repoDir, "hello.py")
	if err := os.WriteFile(pyFile, []byte(`
def greet(name):
    """Say hello."""
    print(f"Hello, {name}")

class Greeter:
    def __init__(self, name):
        self.name = name

    def greet(self):
        print(f"Hello, {self.name}")
`), 0644); err != nil {
		t.Fatalf("write py file: %v", err)
	}

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "pyrepo.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"extract", "--repo", "pyrepo", "--output", outputDB, repoDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("extract command failed: %v", err)
	}

	if _, err := os.Stat(outputDB); os.IsNotExist(err) {
		t.Fatal("output DB file was not created")
	}

	out := buf.String()
	if !strings.Contains(out, "Non-Go files extracted") {
		t.Error("output missing non-Go files count")
	}
}

func TestExtractDefaultOutput(t *testing.T) {
	resetExtractFlags()

	repoDir := t.TempDir()

	// Write a minimal Go file.
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// Run from a temp dir so the default output goes there.
	origDir, _ := os.Getwd()
	tmpOut := t.TempDir()
	if err := os.Chdir(tmpOut); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origDir)

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"extract", "--repo", "myrepo", repoDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("extract command failed: %v", err)
	}

	defaultPath := filepath.Join(tmpOut, "myrepo.claims.db")
	if _, err := os.Stat(defaultPath); os.IsNotExist(err) {
		t.Errorf("default output DB not created at %s", defaultPath)
	}
}

func TestExtractTier2FlagRegistered(t *testing.T) {
	f := extractCmd.Flags().Lookup("tier2")
	if f == nil {
		t.Fatal("extract command missing --tier2 flag")
	}
	if f.DefValue != "false" {
		t.Errorf("expected --tier2 default=false, got %s", f.DefValue)
	}
}

func TestExtractTier2MissingAPIKey(t *testing.T) {
	resetExtractFlags()

	// Ensure ANTHROPIC_API_KEY is not set.
	t.Setenv("ANTHROPIC_API_KEY", "")

	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "test.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"extract", "--tier2", "--repo", "test-repo", "--output", outputDB, repoDir})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when ANTHROPIC_API_KEY is missing")
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error should mention ANTHROPIC_API_KEY, got: %v", err)
	}
}

// mockLLM is a mock LLMClient for testing tier2 extraction without real API calls.
type mockLLM struct {
	response string
	err      error
}

func (m *mockLLM) Complete(_ context.Context, _, _ string) (string, error) {
	return m.response, m.err
}

func TestExtractTier2WithMock(t *testing.T) {
	resetExtractFlags()

	// Set API key so the check passes.
	t.Setenv("ANTHROPIC_API_KEY", "test-key-for-mock")

	// Override the LLM client factory to return our mock.
	origFactory := newLLMClient
	defer func() { newLLMClient = origFactory }()

	mock := &mockLLM{
		response: `[
			{"subject_name": "Hello", "purpose": "Prints a greeting message", "complexity": "simple", "stability": "stable"},
			{"subject_name": "main", "purpose": "Entry point", "usage_pattern": "Called at startup", "complexity": "trivial"}
		]`,
	}
	newLLMClient = func(apiKey string) (semantic.LLMClient, error) {
		return mock, nil
	}

	repoDir := t.TempDir()
	goFile := filepath.Join(repoDir, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main

import "fmt"

// Hello prints a greeting.
func Hello() {
	fmt.Println("hello")
}

func main() {
	Hello()
}
`), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	goMod := filepath.Join(repoDir, "go.mod")
	if err := os.WriteFile(goMod, []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "tier2.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"extract", "--tier2", "--repo", "tier2-repo", "--output", outputDB, repoDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("extract with --tier2 failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Semantic claims") {
		t.Error("output missing semantic claims info")
	}
	if !strings.Contains(out, "Extract Summary") {
		t.Error("output missing Extract Summary")
	}

	// Verify confidence gate: no semantic claims with confidence < 0.7 in DB.
	sqlDB, err := sql.Open("sqlite", outputDB)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	var lowConfCount int
	err = sqlDB.QueryRow("SELECT COUNT(*) FROM claims WHERE claim_tier = 'semantic' AND confidence < 0.7").Scan(&lowConfCount)
	if err != nil {
		t.Fatalf("query low confidence: %v", err)
	}
	if lowConfCount != 0 {
		t.Errorf("expected 0 low-confidence semantic claims, got %d", lowConfCount)
	}

	// Verify some semantic claims were stored (purpose claims have confidence 0.7).
	var semanticCount int
	err = sqlDB.QueryRow("SELECT COUNT(*) FROM claims WHERE claim_tier = 'semantic'").Scan(&semanticCount)
	if err != nil {
		t.Fatalf("query semantic count: %v", err)
	}
	if semanticCount == 0 {
		t.Error("expected some semantic claims to be stored")
	}
}

func TestExtractTier2ConfidenceGate(t *testing.T) {
	resetExtractFlags()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	origFactory := newLLMClient
	defer func() { newLLMClient = origFactory }()

	// Mock returns claims that will get confidence values:
	// purpose=0.7, usage_pattern=0.6, complexity=0.6, stability=0.5
	// Only purpose (0.7) should survive the >= 0.7 threshold.
	mock := &mockLLM{
		response: `[
			{"subject_name": "Hello", "purpose": "Prints greeting", "usage_pattern": "Direct call", "complexity": "simple", "stability": "stable"}
		]`,
	}
	newLLMClient = func(apiKey string) (semantic.LLMClient, error) {
		return mock, nil
	}

	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte(`package main

// Hello prints a greeting.
func Hello() {}
func main() { Hello() }
`), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "gate.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"extract", "--tier2", "--repo", "gate-repo", "--output", outputDB, repoDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	// Verify: no claims below threshold.
	sqlDB, err := sql.Open("sqlite", outputDB)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	var lowCount int
	err = sqlDB.QueryRow("SELECT COUNT(*) FROM claims WHERE claim_tier = 'semantic' AND confidence < 0.7").Scan(&lowCount)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if lowCount != 0 {
		t.Errorf("confidence gate failed: found %d claims with confidence < 0.7", lowCount)
	}
}

func TestExtractAtomicReplace(t *testing.T) {
	resetExtractFlags()

	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "atomic.claims.db")

	// Place a sentinel file at the output path to verify it gets replaced (not removed first).
	if err := os.WriteFile(outputDB, []byte("sentinel-data"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"extract", "--repo", "atomic-repo", "--output", outputDB, repoDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("extract command failed: %v", err)
	}

	// Output DB should exist and be a valid SQLite file (not the sentinel).
	content, err := os.ReadFile(outputDB)
	if err != nil {
		t.Fatalf("read output db: %v", err)
	}
	if string(content) == "sentinel-data" {
		t.Error("output DB still contains sentinel data; atomic rename did not happen")
	}
	// SQLite files start with "SQLite format 3\000".
	if len(content) < 16 || string(content[:15]) != "SQLite format 3" {
		t.Error("output DB is not a valid SQLite file")
	}

	// No temp files should remain in the output directory.
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestExtractAtomicNoTempFileOnSuccess(t *testing.T) {
	resetExtractFlags()

	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "notmp.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"extract", "--repo", "notmp-repo", "--output", outputDB, repoDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("extract command failed: %v", err)
	}

	// Exactly one file should remain in the output directory (the DB itself).
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("expected exactly 1 file in output dir, got %d: %v", len(entries), names)
	}
}

func TestExtractSensitiveContentFilter(t *testing.T) {
	resetExtractFlags()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	origFactory := newLLMClient
	defer func() { newLLMClient = origFactory }()

	// Mock returns a claim with sensitive content in purpose.
	mock := &mockLLM{
		response: `[
			{"subject_name": "Hello", "purpose": "Stores the database password securely"}
		]`,
	}
	newLLMClient = func(apiKey string) (semantic.LLMClient, error) {
		return mock, nil
	}

	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte(`package main

// Hello prints a greeting.
func Hello() {}
func main() { Hello() }
`), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "sensitive.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"extract", "--tier2", "--repo", "sensitive-repo", "--output", outputDB, repoDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	// The semantic claim about "password" should NOT be in the DB.
	// The extract command runs DeleteSensitiveClaims() after semantic generation.
	sqlDB, err := sql.Open("sqlite", outputDB)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	var sensitiveCount int
	err = sqlDB.QueryRow("SELECT COUNT(*) FROM claims WHERE object_text LIKE '%password%'").Scan(&sensitiveCount)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if sensitiveCount != 0 {
		t.Errorf("expected 0 claims with 'password' in object_text, got %d", sensitiveCount)
	}
}

func TestTribalLLMConfigNotSet(t *testing.T) {
	resetExtractFlags()

	// Create a temp repo with a Go file but no .livedocs.yaml with llm_enabled.
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// Initialize a git repo so deterministic extractors don't fail.
	initGitRepo(t, repoDir)

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "llm-config.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"extract", "--tribal=llm", "--repo", "llm-config-repo", "--output", outputDB, repoDir})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when llm_enabled is not set in .livedocs.yaml")
	}
	if !strings.Contains(err.Error(), ".livedocs.yaml") {
		t.Errorf("error should mention .livedocs.yaml, got: %v", err)
	}
	if !strings.Contains(err.Error(), "llm_enabled") {
		t.Errorf("error should mention llm_enabled, got: %v", err)
	}
}

func TestTribalLLMMissingAuth(t *testing.T) {
	resetExtractFlags()

	// Create a temp repo with .livedocs.yaml containing llm_enabled: true.
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".livedocs.yaml"), []byte("tribal:\n  llm_enabled: true\n"), 0644); err != nil {
		t.Fatalf("write .livedocs.yaml: %v", err)
	}

	// Initialize a git repo with a remote so git remote parsing works.
	initGitRepo(t, repoDir)
	cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/test-org/test-repo.git")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add remote: %v: %s", err, out)
	}

	// Ensure ANTHROPIC_API_KEY is not set. Override PATH to exclude claude
	// binary but keep git and other essentials available.
	t.Setenv("ANTHROPIC_API_KEY", "")

	// Create a minimal PATH with only git's directory (no claude binary).
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git not on PATH: %v", err)
	}
	t.Setenv("PATH", filepath.Dir(gitPath))

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "llm-auth.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"extract", "--tribal=llm", "--repo", "llm-auth-repo", "--output", outputDB, repoDir})
	err = rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when neither claude CLI nor ANTHROPIC_API_KEY is available")
	}
	if !strings.Contains(err.Error(), "claude") && !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error should mention auth methods, got: %v", err)
	}
}

func TestParseGitRemoteURL(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantOwner string
		wantName  string
		wantOK    bool
	}{
		{
			name:      "HTTPS with .git suffix",
			input:     "https://github.com/live-docs/live_docs.git",
			wantOwner: "live-docs",
			wantName:  "live_docs",
			wantOK:    true,
		},
		{
			name:      "HTTPS without .git suffix",
			input:     "https://github.com/kubernetes/kubernetes",
			wantOwner: "kubernetes",
			wantName:  "kubernetes",
			wantOK:    true,
		},
		{
			name:      "SSH format",
			input:     "git@github.com:live-docs/live_docs.git",
			wantOwner: "live-docs",
			wantName:  "live_docs",
			wantOK:    true,
		},
		{
			name:      "SSH without .git suffix",
			input:     "git@github.com:org/repo",
			wantOwner: "org",
			wantName:  "repo",
			wantOK:    true,
		},
		{
			name:   "empty string",
			input:  "",
			wantOK: false,
		},
		{
			name:   "bare hostname",
			input:  "github.com",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, name, ok := parseGitRemoteURL(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("parseGitRemoteURL(%q): ok=%v, want %v", tt.input, ok, tt.wantOK)
			}
			if ok {
				if owner != tt.wantOwner {
					t.Errorf("owner=%q, want %q", owner, tt.wantOwner)
				}
				if name != tt.wantName {
					t.Errorf("name=%q, want %q", name, tt.wantName)
				}
			}
		})
	}
}

// initGitRepo initializes a minimal git repo in the given directory
// so that tribal extractors (blame, rationale) don't fail on git commands.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "add", "."},
		{"git", "commit", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
