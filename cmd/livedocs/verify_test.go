package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyCommandRegistered(t *testing.T) {
	registered := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		registered[cmd.Name()] = true
	}
	if !registered["verify"] {
		t.Error("subcommand \"verify\" not registered on root command")
	}
}

func TestVerifyCommand_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"verify", dir})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("verify on empty dir should succeed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "No AI context files found") {
		t.Errorf("expected 'No AI context files found' in output, got: %q", out)
	}
}

func TestVerifyCommand_SingleFile(t *testing.T) {
	dir := t.TempDir()
	// Create a CLAUDE.md that references a path that exists and one that doesn't.
	existing := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(existing, "main"), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeMD := filepath.Join(dir, "CLAUDE.md")
	content := "# Project\n\nSource is at `src/main`.\nOld code at `src/legacy/old.go`.\n"
	if err := os.WriteFile(claudeMD, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"verify", claudeMD})
	err := rootCmd.Execute()
	// Should fail because of stale reference.
	if err == nil {
		t.Fatal("verify should return error when stale references exist")
	}
	out := buf.String()
	if !strings.Contains(out, "src/legacy/old.go") {
		t.Errorf("output should mention stale path, got: %q", out)
	}
}

func TestVerifyCommand_JSONOutput(t *testing.T) {
	dir := t.TempDir()
	claudeMD := filepath.Join(dir, "CLAUDE.md")
	content := "# Project\n\nNothing special here.\n"
	if err := os.WriteFile(claudeMD, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"verify", "--json", claudeMD})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("verify --json failed: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, buf.String())
	}
	if _, ok := result["accuracy_percent"]; !ok {
		t.Error("JSON output missing 'accuracy_percent' field")
	}
	if _, ok := result["files"]; !ok {
		t.Error("JSON output missing 'files' field")
	}
}

func TestVerifyCommand_FormatSummary(t *testing.T) {
	dir := t.TempDir()
	claudeMD := filepath.Join(dir, "CLAUDE.md")
	content := "# Project\n\nNo paths here.\n"
	if err := os.WriteFile(claudeMD, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reset flags from prior tests.
	verifyJSON = false
	verifyFormat = "human"

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"verify", "--format", "summary", claudeMD})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("verify --format summary failed: %v", err)
	}
	out := buf.String()
	// Summary format should be a single concise line.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 3 {
		t.Errorf("summary format should be concise, got %d lines: %q", len(lines), out)
	}
}

func TestVerifyCommand_DirectoryMode(t *testing.T) {
	dir := t.TempDir()
	// Create a CLAUDE.md with only valid references.
	subdir := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "util.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeMD := filepath.Join(dir, "CLAUDE.md")
	content := "# Project\n\nUtilities at `pkg/util.go`.\n"
	if err := os.WriteFile(claudeMD, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reset flags from prior tests.
	verifyJSON = false
	verifyFormat = "human"

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"verify", dir})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("verify on dir with valid refs should succeed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "100%") {
		t.Errorf("expected 100%% accuracy, got: %q", out)
	}
}
