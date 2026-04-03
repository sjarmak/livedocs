//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestManifestWorkflow validates the full manifest lifecycle:
// 1. livedocs check --update-manifest generates .livedocs/manifest
// 2. livedocs check --manifest completes in <2s
func TestManifestWorkflow(t *testing.T) {
	bin := buildLivedocs(t)

	// Create a temporary git repo with some Go files and a README.
	tmpDir := t.TempDir()

	// Initialize git repo.
	gitCmd(t, tmpDir, "init")
	gitCmd(t, tmpDir, "config", "user.email", "test@test.com")
	gitCmd(t, tmpDir, "config", "user.name", "Test User")

	// Create directory structure with source files and docs.
	writeFile(t, tmpDir, "pkg/auth/auth.go", `package auth

// Authenticate validates user credentials.
func Authenticate(user, pass string) bool {
	return user != "" && pass != ""
}
`)
	writeFile(t, tmpDir, "pkg/auth/token.go", `package auth

// GenerateToken creates a new auth token.
func GenerateToken(userID string) string {
	return "tok_" + userID
}
`)

	// Write a README alongside the Go files.
	writeFile(t, tmpDir, "pkg/auth/README.md", `# Auth Package

This package provides authentication utilities.

## Functions

- Authenticate: validates credentials
- GenerateToken: creates tokens
`)

	// Write a top-level README.
	writeFile(t, tmpDir, "README.md", `# Test Project

A test project for manifest workflow validation.
`)

	// Write docs.
	writeFile(t, tmpDir, "docs/api.md", `# API Reference

Documentation for the API.
`)

	// Commit everything so git diff works.
	gitCmd(t, tmpDir, "add", "-A")
	gitCmd(t, tmpDir, "commit", "-m", "initial commit")

	// Step 1: Generate manifest.
	cmd := exec.Command(bin, "check", "--update-manifest", tmpDir)
	out, err := cmd.CombinedOutput()
	t.Logf("update-manifest output:\n%s", string(out))
	if err != nil {
		t.Fatalf("livedocs check --update-manifest failed: %v", err)
	}

	// Verify manifest file was created.
	manifestPath := filepath.Join(tmpDir, ".livedocs", "manifest")
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("manifest file not found at %s: %v", manifestPath, err)
	}
	if info.Size() == 0 {
		t.Fatal("manifest file is empty")
	}
	t.Logf("manifest created: %s (%d bytes)", manifestPath, info.Size())

	// Make a second commit so git diff HEAD~1 works.
	writeFile(t, tmpDir, "pkg/auth/auth.go", `package auth

// Authenticate validates user credentials against the store.
func Authenticate(user, pass string) bool {
	return user != "" && pass != ""
}

// IsAdmin checks if a user has admin privileges.
func IsAdmin(user string) bool {
	return user == "admin"
}
`)
	gitCmd(t, tmpDir, "add", "-A")
	gitCmd(t, tmpDir, "commit", "-m", "add IsAdmin function")

	// Step 2: Run manifest check and verify speed.
	start := time.Now()
	cmd = exec.Command(bin, "check", "--manifest", tmpDir)
	out, err = cmd.CombinedOutput()
	elapsed := time.Since(start)

	t.Logf("manifest check output:\n%s", string(out))
	t.Logf("manifest check completed in %s", elapsed)

	// The command may return exit code 1 if drift is detected (that's OK).
	// We only care about timing and that it ran without crashing.
	if elapsed > 2*time.Second {
		t.Errorf("manifest check took %s, expected <2s", elapsed)
	}

	// Verify the command produced output (didn't silently fail).
	if len(out) == 0 {
		t.Error("manifest check produced no output")
	}
}

// gitCmd executes a git command in the given directory.
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}
