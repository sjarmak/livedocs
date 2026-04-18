package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sjarmak/livedocs/gitdiff"
)

// Compile-time check that LocalFileSource satisfies FileSource.
var _ FileSource = (*LocalFileSource)(nil)

func TestLocalFileSource_ReadFile(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello world")
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	src := NewLocalFileSource(dir)
	got, err := src.ReadFile(context.Background(), "ignored", "ignored", "test.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("ReadFile = %q, want %q", got, content)
	}
}

func TestLocalFileSource_ReadFile_NotFound(t *testing.T) {
	dir := t.TempDir()
	src := NewLocalFileSource(dir)

	_, err := src.ReadFile(context.Background(), "", "", "missing.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLocalFileSource_ListFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	src := NewLocalFileSource(dir)
	matches, err := src.ListFiles(context.Background(), "", "", "*.go")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("ListFiles returned %d files, want 2: %v", len(matches), matches)
	}
	for _, m := range matches {
		if ext := filepath.Ext(m); ext != ".go" {
			t.Errorf("unexpected file %s in results", m)
		}
	}
}

func TestLocalFileSource_DiffBetween(t *testing.T) {
	dir := t.TempDir()

	// Initialize a git repo.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "checkout", "-b", "main")

	// First commit: add a file.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "hello.txt")
	run("git", "commit", "-m", "initial")

	// Get first commit hash.
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	firstCommit := string(out[:len(out)-1]) // trim newline

	// Second commit: modify the file and add another.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "world.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "hello.txt", "world.txt")
	run("git", "commit", "-m", "second")

	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	secondCommit := string(out[:len(out)-1])

	// Test DiffBetween.
	src := NewLocalFileSource(dir)
	changes, err := src.DiffBetween(context.Background(), "ignored", firstCommit, secondCommit)
	if err != nil {
		t.Fatalf("DiffBetween: %v", err)
	}

	if len(changes) != 2 {
		t.Fatalf("DiffBetween returned %d changes, want 2: %v", len(changes), changes)
	}

	// Build a map for easier assertions.
	byPath := make(map[string]gitdiff.ChangeStatus)
	for _, c := range changes {
		byPath[c.Path] = c.Status
	}

	if status, ok := byPath["hello.txt"]; !ok || status != gitdiff.StatusModified {
		t.Errorf("hello.txt: got status %q, want %q", status, gitdiff.StatusModified)
	}
	if status, ok := byPath["world.txt"]; !ok || status != gitdiff.StatusAdded {
		t.Errorf("world.txt: got status %q, want %q", status, gitdiff.StatusAdded)
	}
}
