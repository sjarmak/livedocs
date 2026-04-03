package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
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
