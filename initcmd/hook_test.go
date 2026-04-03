package initcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPostCommitHook_NewHook(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)

	installed, err := InstallPostCommitHook(dir)
	if err != nil {
		t.Fatalf("InstallPostCommitHook: %v", err)
	}
	if !installed {
		t.Error("expected installed = true for new hook")
	}

	hookPath := filepath.Join(dir, ".git", "hooks", "post-commit")
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}

	s := string(content)
	if !strings.HasPrefix(s, "#!/bin/sh\n") {
		t.Error("expected shebang line")
	}
	if !strings.Contains(s, "livedocs extract") {
		t.Error("expected livedocs extract command in hook")
	}
	if !strings.Contains(s, hookMarkerBegin) {
		t.Error("expected begin marker in hook")
	}
	if !strings.Contains(s, hookMarkerEnd) {
		t.Error("expected end marker in hook")
	}

	// Check file is executable.
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("stat hook: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Errorf("hook file is not executable: %v", info.Mode())
	}
}

func TestInstallPostCommitHook_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	os.MkdirAll(hooksDir, 0755)

	existing := "#!/bin/sh\necho 'existing hook'\n"
	os.WriteFile(filepath.Join(hooksDir, "post-commit"), []byte(existing), 0755)

	installed, err := InstallPostCommitHook(dir)
	if err != nil {
		t.Fatalf("InstallPostCommitHook: %v", err)
	}
	if !installed {
		t.Error("expected installed = true when appending")
	}

	content, err := os.ReadFile(filepath.Join(hooksDir, "post-commit"))
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}

	s := string(content)
	// Original content preserved.
	if !strings.Contains(s, "echo 'existing hook'") {
		t.Error("original hook content was lost")
	}
	// Livedocs section appended.
	if !strings.Contains(s, "livedocs extract") {
		t.Error("livedocs extract not appended")
	}
	// Should not duplicate shebang.
	if strings.Count(s, "#!/bin/sh") != 1 {
		t.Error("shebang was duplicated")
	}
}

func TestInstallPostCommitHook_Idempotent(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)

	// First install.
	installed1, err := InstallPostCommitHook(dir)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !installed1 {
		t.Error("first install should return true")
	}

	// Second install — should be no-op.
	installed2, err := InstallPostCommitHook(dir)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if installed2 {
		t.Error("second install should return false (already installed)")
	}

	// Content should not be duplicated.
	content, _ := os.ReadFile(filepath.Join(dir, ".git", "hooks", "post-commit"))
	if strings.Count(string(content), hookMarkerBegin) != 1 {
		t.Error("hook marker was duplicated")
	}
}

func TestInstallPostCommitHook_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	// No .git directory.

	_, err := InstallPostCommitHook(dir)
	if err == nil {
		t.Error("expected error for non-git directory")
	}
}

func TestInstallPostCommitHook_ExistingWithoutTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	os.MkdirAll(hooksDir, 0755)

	// Existing hook without trailing newline.
	existing := "#!/bin/sh\necho 'no newline'"
	os.WriteFile(filepath.Join(hooksDir, "post-commit"), []byte(existing), 0755)

	installed, err := InstallPostCommitHook(dir)
	if err != nil {
		t.Fatalf("InstallPostCommitHook: %v", err)
	}
	if !installed {
		t.Error("expected installed = true")
	}

	content, _ := os.ReadFile(filepath.Join(hooksDir, "post-commit"))
	s := string(content)
	// Marker should be on its own line, not glued to previous content.
	if strings.Contains(s, "no newline"+hookMarkerBegin) {
		t.Error("marker was glued to existing content without newline separator")
	}
}
