package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetExtractFlags resets global flag state to avoid leaking between tests.
func resetExtractFlags() {
	extractRepo = ""
	extractOutput = ""
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
