package tribal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo creates a temporary git repo and returns its path.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	return dir
}

// commitFileWithMsg writes content to a file and commits it with the given message.
func commitFileWithMsg(t *testing.T, dir, filename, content, message string) {
	t.Helper()
	fpath := filepath.Join(dir, filename)
	if err := os.WriteFile(fpath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run("add", filename)
	run("commit", "-m", message)
}

func TestCommitRationaleExtractor_BasicExtraction(t *testing.T) {
	dir := initGitRepo(t)
	commitFileWithMsg(t, dir, "main.go", "package main\n", "feat: add main entry point for the application")

	ext := &CommitRationaleExtractor{RepoDir: dir}
	fact, evidence, err := ext.ExtractForFile(context.Background(), "main.go", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fact == nil {
		t.Fatal("expected a fact, got nil")
	}

	// Verify fact fields
	if fact.SubjectID != 42 {
		t.Errorf("SubjectID = %d, want 42", fact.SubjectID)
	}
	if fact.Kind != "rationale" {
		t.Errorf("Kind = %q, want %q", fact.Kind, "rationale")
	}
	if fact.Body != "feat: add main entry point for the application" {
		t.Errorf("Body = %q, want commit message", fact.Body)
	}
	if fact.SourceQuote != "feat: add main entry point for the application" {
		t.Errorf("SourceQuote = %q, want first line", fact.SourceQuote)
	}
	if fact.Confidence != 1.0 {
		t.Errorf("Confidence = %f, want 1.0", fact.Confidence)
	}
	if fact.Model != "" {
		t.Errorf("Model = %q, want empty (NULL)", fact.Model)
	}
	if fact.StalenessHash == "" {
		t.Error("StalenessHash should not be empty")
	}
	if fact.Extractor != commitRationaleExtractorName {
		t.Errorf("Extractor = %q, want %q", fact.Extractor, commitRationaleExtractorName)
	}

	// Verify evidence
	if len(evidence) != 1 {
		t.Fatalf("evidence count = %d, want 1", len(evidence))
	}
	ev := evidence[0]
	if ev.SourceType != "commit_msg" {
		t.Errorf("SourceType = %q, want %q", ev.SourceType, "commit_msg")
	}
	if len(ev.SourceRef) != 40 {
		t.Errorf("SourceRef = %q, want 40-char SHA", ev.SourceRef)
	}
	if ev.Author != "Test" {
		t.Errorf("Author = %q, want %q", ev.Author, "Test")
	}
}

func TestCommitRationaleExtractor_MultiLineBody(t *testing.T) {
	dir := initGitRepo(t)
	msg := "feat: add authentication module\n\nThis adds JWT-based auth with refresh tokens.\nIncludes middleware for route protection."
	commitFileWithMsg(t, dir, "auth.go", "package auth\n", msg)

	ext := &CommitRationaleExtractor{RepoDir: dir}
	fact, _, err := ext.ExtractForFile(context.Background(), "auth.go", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fact == nil {
		t.Fatal("expected a fact, got nil")
	}

	if fact.SourceQuote != "feat: add authentication module" {
		t.Errorf("SourceQuote = %q, want first line only", fact.SourceQuote)
	}
	if !strings.Contains(fact.Body, "JWT-based auth") {
		t.Errorf("Body should contain full message body, got %q", fact.Body)
	}
}

func TestCommitRationaleExtractor_FiltersTrivialTypes(t *testing.T) {
	dir := initGitRepo(t)

	// First commit: non-trivial (will be older)
	commitFileWithMsg(t, dir, "util.go", "package util\n", "feat: add string utility helpers for parsing")

	// Subsequent trivial commits (more recent)
	commitFileWithMsg(t, dir, "util.go", "package util\n// v2\n", "chore: update formatting of util package")
	commitFileWithMsg(t, dir, "util.go", "package util\n// v3\n", "ci: add pipeline step for util tests")
	commitFileWithMsg(t, dir, "util.go", "package util\n// v4\n", "docs: document the string utility helpers")
	commitFileWithMsg(t, dir, "util.go", "package util\n// v5\n", "style: reformat util imports and spacing")

	ext := &CommitRationaleExtractor{RepoDir: dir}
	fact, _, err := ext.ExtractForFile(context.Background(), "util.go", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fact == nil {
		t.Fatal("expected a fact, got nil — should have fallen through to feat commit")
	}

	// Should skip all trivial types and find the feat commit
	if !strings.HasPrefix(fact.Body, "feat: add string utility") {
		t.Errorf("Body = %q, want the feat commit (not chore/ci/docs/style)", fact.Body)
	}
}

func TestCommitRationaleExtractor_FiltersShortMessages(t *testing.T) {
	dir := initGitRepo(t)

	// Short message (<=20 chars) — should be filtered
	commitFileWithMsg(t, dir, "tiny.go", "package tiny\n", "fix typo")

	ext := &CommitRationaleExtractor{RepoDir: dir}
	fact, _, err := ext.ExtractForFile(context.Background(), "tiny.go", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fact != nil {
		t.Errorf("expected nil fact for short message, got Body=%q", fact.Body)
	}
}

func TestCommitRationaleExtractor_MostRecentNonTrivialWins(t *testing.T) {
	dir := initGitRepo(t)

	commitFileWithMsg(t, dir, "svc.go", "package svc\n", "feat: initial service implementation with handlers")
	commitFileWithMsg(t, dir, "svc.go", "package svc\n// v2\n", "fix: correct error handling in service layer")

	ext := &CommitRationaleExtractor{RepoDir: dir}
	fact, _, err := ext.ExtractForFile(context.Background(), "svc.go", 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fact == nil {
		t.Fatal("expected a fact, got nil")
	}

	// Most recent non-trivial should win (the fix commit)
	if !strings.HasPrefix(fact.Body, "fix: correct error handling") {
		t.Errorf("Body = %q, want the most recent fix commit", fact.Body)
	}
}

func TestCommitRationaleExtractor_NoHistory(t *testing.T) {
	dir := initGitRepo(t)
	// Create a dummy first commit so git is valid
	commitFileWithMsg(t, dir, "dummy.go", "package dummy\n", "feat: initial commit for the dummy package")

	ext := &CommitRationaleExtractor{RepoDir: dir}
	// Query a file that doesn't exist in history
	fact, _, err := ext.ExtractForFile(context.Background(), "nonexistent.go", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fact != nil {
		t.Errorf("expected nil fact for nonexistent file, got %+v", fact)
	}
}

func TestCommitRationaleExtractor_StalenessHashIsSHA256(t *testing.T) {
	dir := initGitRepo(t)
	commitFileWithMsg(t, dir, "hash.go", "package hash\n", "refactor: restructure hash module for clarity")

	ext := &CommitRationaleExtractor{RepoDir: dir}
	fact, _, err := ext.ExtractForFile(context.Background(), "hash.go", 99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fact == nil {
		t.Fatal("expected a fact")
	}

	// SHA-256 hex digest is 64 chars
	if len(fact.StalenessHash) != 64 {
		t.Errorf("StalenessHash length = %d, want 64 (SHA-256 hex)", len(fact.StalenessHash))
	}

	// Verify it matches the expected hash of the message
	expected := sha256Hash(fact.Body)
	if fact.StalenessHash != expected {
		t.Errorf("StalenessHash = %q, want SHA256(%q) = %q", fact.StalenessHash, fact.Body, expected)
	}
}

func TestParseConventionalType(t *testing.T) {
	tests := []struct {
		subject string
		want    string
	}{
		{"feat: add feature", "feat"},
		{"fix: correct bug", "fix"},
		{"refactor: clean up code base", "refactor"},
		{"perf: optimize query performance", "perf"},
		{"chore: bump dependencies version", "chore"},
		{"ci: add github actions workflow", "ci"},
		{"docs: update readme documentation", "docs"},
		{"style: fix indentation issues", "style"},
		{"feat(auth): add login endpoint", "feat"},
		{"fix!: breaking change in the API", "fix"},
		{"no conventional format here", ""},
		{"", ""},
		{"a]b: weird", ""},
	}

	for _, tt := range tests {
		got := parseConventionalType(tt.subject)
		if got != tt.want {
			t.Errorf("parseConventionalType(%q) = %q, want %q", tt.subject, got, tt.want)
		}
	}
}

func TestIsNonTrivial(t *testing.T) {
	tests := []struct {
		name string
		e    CommitEntry
		want bool
	}{
		{
			name: "feat with long message",
			e:    CommitEntry{FullMsg: "feat: add a new feature to the system", CommitType: "feat"},
			want: true,
		},
		{
			name: "short message filtered",
			e:    CommitEntry{FullMsg: "fix typo", CommitType: "fix"},
			want: false,
		},
		{
			name: "exactly 20 chars filtered",
			e:    CommitEntry{FullMsg: "12345678901234567890", CommitType: ""},
			want: false,
		},
		{
			name: "21 chars passes length",
			e:    CommitEntry{FullMsg: "123456789012345678901", CommitType: ""},
			want: true,
		},
		{
			name: "chore type filtered",
			e:    CommitEntry{FullMsg: "chore: update all the dependencies now", CommitType: "chore"},
			want: false,
		},
		{
			name: "ci type filtered",
			e:    CommitEntry{FullMsg: "ci: add github actions workflow step", CommitType: "ci"},
			want: false,
		},
		{
			name: "docs type filtered",
			e:    CommitEntry{FullMsg: "docs: update the readme with examples", CommitType: "docs"},
			want: false,
		},
		{
			name: "style type filtered",
			e:    CommitEntry{FullMsg: "style: reformat all the source files", CommitType: "style"},
			want: false,
		},
		{
			name: "no conventional type long msg passes",
			e:    CommitEntry{FullMsg: "update the configuration for the project", CommitType: ""},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNonTrivial(&tt.e)
			if got != tt.want {
				t.Errorf("isNonTrivial(%q) = %v, want %v", tt.e.FullMsg, got, tt.want)
			}
		})
	}
}

func TestAllowedAndTrivialTypes(t *testing.T) {
	// Verify allowed types are recognized
	for _, typ := range []string{"feat", "fix", "refactor", "perf"} {
		if !allowedTypes[typ] {
			t.Errorf("%q should be in allowedTypes", typ)
		}
		if trivialTypes[typ] {
			t.Errorf("%q should not be in trivialTypes", typ)
		}
	}

	// Verify trivial types are recognized
	for _, typ := range []string{"chore", "ci", "docs", "style"} {
		if !trivialTypes[typ] {
			t.Errorf("%q should be in trivialTypes", typ)
		}
		if allowedTypes[typ] {
			t.Errorf("%q should not be in allowedTypes", typ)
		}
	}
}
