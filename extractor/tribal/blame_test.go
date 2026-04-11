package tribal

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// blameSetupRepo creates a temporary git repo and returns its path.
func blameSetupRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "alice@example.com"},
		{"git", "config", "user.name", "Alice"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v\n%s", args, err, out)
		}
	}

	return dir
}

// blameCommitFile writes content to a file and commits it as the given author.
func blameCommitFile(t *testing.T, repoDir, filePath, content, authorName, authorEmail string) {
	t.Helper()

	fullPath := filepath.Join(repoDir, filePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cmds := [][]string{
		{"git", "add", filePath},
		{"git", "-c", fmt.Sprintf("user.name=%s", authorName),
			"-c", fmt.Sprintf("user.email=%s", authorEmail),
			"commit", "-m", fmt.Sprintf("add %s", filePath)},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit %v: %v\n%s", args, err, out)
		}
	}
}

func TestBlameExtractor_SingleAuthor(t *testing.T) {
	repo := blameSetupRepo(t)
	content := "line1\nline2\nline3\nline4\nline5\n"
	blameCommitFile(t, repo, "main.go", content, "Alice", "alice@example.com")

	ext := &BlameExtractor{}
	symbols := []SymbolRange{
		{SymbolID: 1, StartLine: 1, EndLine: 5},
	}

	facts, err := ext.ExtractForFile(context.Background(), repo, "main.go", symbols)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}

	fact := facts[0]
	if fact.SubjectID != 1 {
		t.Errorf("SubjectID = %d, want 1", fact.SubjectID)
	}
	if fact.Kind != "ownership" {
		t.Errorf("Kind = %q, want %q", fact.Kind, "ownership")
	}
	if fact.Confidence != 1.0 {
		t.Errorf("Confidence = %f, want 1.0", fact.Confidence)
	}
	if fact.Model != "" {
		t.Errorf("Model = %q, want empty (NULL)", fact.Model)
	}
	if fact.Extractor != extractorName {
		t.Errorf("Extractor = %q, want %q", fact.Extractor, extractorName)
	}
	if !strings.Contains(fact.Body, "alice@example.com") {
		t.Errorf("Body should contain author email, got: %s", fact.Body)
	}
	if !strings.Contains(fact.Body, "100%") {
		t.Errorf("Body should contain 100%% for single author, got: %s", fact.Body)
	}
	if fact.StalenessHash == "" {
		t.Error("StalenessHash should not be empty")
	}
	if fact.Status != "active" {
		t.Errorf("Status = %q, want %q", fact.Status, "active")
	}

	// Check evidence
	if len(fact.Evidence) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(fact.Evidence))
	}
	ev := fact.Evidence[0]
	if ev.SourceType != "blame" {
		t.Errorf("SourceType = %q, want %q", ev.SourceType, "blame")
	}
	if len(ev.SourceRef) < 7 {
		t.Errorf("SourceRef should be a commit SHA, got: %q", ev.SourceRef)
	}
	if !strings.Contains(ev.Author, "alice@example.com") {
		t.Errorf("Author = %q, want to contain alice@example.com", ev.Author)
	}
}

func TestBlameExtractor_MultipleAuthors(t *testing.T) {
	repo := blameSetupRepo(t)

	// Alice writes first 3 lines.
	content1 := "line1\nline2\nline3\n"
	blameCommitFile(t, repo, "main.go", content1, "Alice", "alice@example.com")

	// Bob appends 2 more lines.
	content2 := "line1\nline2\nline3\nline4\nline5\n"
	fullPath := filepath.Join(repo, "main.go")
	if err := os.WriteFile(fullPath, []byte(content2), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmds := [][]string{
		{"git", "add", "main.go"},
		{"git", "-c", "user.name=Bob", "-c", "user.email=bob@example.com",
			"commit", "-m", "bob adds lines"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	ext := &BlameExtractor{}
	symbols := []SymbolRange{
		{SymbolID: 42, StartLine: 1, EndLine: 5},
	}

	facts, err := ext.ExtractForFile(context.Background(), repo, "main.go", symbols)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}

	fact := facts[0]
	// Alice has 3 lines (60%), Bob has 2 lines (40%).
	if !strings.Contains(fact.Body, "Alice") {
		t.Errorf("Body should mention Alice, got: %s", fact.Body)
	}
	if !strings.Contains(fact.Body, "Bob") {
		t.Errorf("Body should mention Bob, got: %s", fact.Body)
	}

	// Evidence should have 2 entries (one per author).
	if len(fact.Evidence) != 2 {
		t.Fatalf("expected 2 evidence rows, got %d", len(fact.Evidence))
	}

	// First evidence should be Alice (top contributor).
	if !strings.Contains(fact.Evidence[0].Author, "alice@example.com") {
		t.Errorf("first evidence should be Alice, got: %s", fact.Evidence[0].Author)
	}
}

func TestBlameExtractor_MultipleSymbols(t *testing.T) {
	repo := blameSetupRepo(t)

	content := "aaa\nbbb\nccc\nddd\neee\nfff\n"
	blameCommitFile(t, repo, "main.go", content, "Alice", "alice@example.com")

	ext := &BlameExtractor{}
	symbols := []SymbolRange{
		{SymbolID: 1, StartLine: 1, EndLine: 3},
		{SymbolID: 2, StartLine: 4, EndLine: 6},
	}

	facts, err := ext.ExtractForFile(context.Background(), repo, "main.go", symbols)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}

	if facts[0].SubjectID != 1 {
		t.Errorf("first fact SubjectID = %d, want 1", facts[0].SubjectID)
	}
	if facts[1].SubjectID != 2 {
		t.Errorf("second fact SubjectID = %d, want 2", facts[1].SubjectID)
	}
}

func TestBlameExtractor_FileNotInGit(t *testing.T) {
	repo := blameSetupRepo(t)

	// Create a file but don't commit it.
	fullPath := filepath.Join(repo, "untracked.go")
	if err := os.WriteFile(fullPath, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ext := &BlameExtractor{}
	symbols := []SymbolRange{
		{SymbolID: 1, StartLine: 1, EndLine: 1},
	}

	facts, err := ext.ExtractForFile(context.Background(), repo, "untracked.go", symbols)
	if err != nil {
		t.Fatalf("expected no error for untracked file, got: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("expected 0 facts for untracked file, got %d", len(facts))
	}
}

func TestBlameExtractor_NonexistentFile(t *testing.T) {
	repo := blameSetupRepo(t)

	ext := &BlameExtractor{}
	symbols := []SymbolRange{
		{SymbolID: 1, StartLine: 1, EndLine: 1},
	}

	facts, err := ext.ExtractForFile(context.Background(), repo, "nope.go", symbols)
	if err != nil {
		t.Fatalf("expected no error for nonexistent file, got: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("expected 0 facts for nonexistent file, got %d", len(facts))
	}
}

func TestBlameExtractor_EmptySymbols(t *testing.T) {
	ext := &BlameExtractor{}
	facts, err := ext.ExtractForFile(context.Background(), "/tmp", "foo.go", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if facts != nil {
		t.Errorf("expected nil facts for empty symbols, got %v", facts)
	}
}

func TestBlameExtractor_SymbolRangeOutOfBounds(t *testing.T) {
	repo := blameSetupRepo(t)
	blameCommitFile(t, repo, "main.go", "line1\nline2\n", "Alice", "alice@example.com")

	ext := &BlameExtractor{}
	symbols := []SymbolRange{
		{SymbolID: 1, StartLine: 100, EndLine: 200},
	}

	facts, err := ext.ExtractForFile(context.Background(), repo, "main.go", symbols)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("expected 0 facts for out-of-bounds range, got %d", len(facts))
	}
}

func TestBlameExtractor_StalenessHashDeterministic(t *testing.T) {
	repo := blameSetupRepo(t)
	blameCommitFile(t, repo, "main.go", "aaa\nbbb\nccc\n", "Alice", "alice@example.com")

	ext := &BlameExtractor{}
	symbols := []SymbolRange{
		{SymbolID: 1, StartLine: 1, EndLine: 3},
	}

	facts1, err := ext.ExtractForFile(context.Background(), repo, "main.go", symbols)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}

	facts2, err := ext.ExtractForFile(context.Background(), repo, "main.go", symbols)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}

	if len(facts1) != 1 || len(facts2) != 1 {
		t.Fatal("expected 1 fact from each run")
	}

	if facts1[0].StalenessHash != facts2[0].StalenessHash {
		t.Errorf("staleness hash not deterministic: %q vs %q",
			facts1[0].StalenessHash, facts2[0].StalenessHash)
	}
}

func TestBlameExtractor_StalenessHashChangesOnNewCommit(t *testing.T) {
	repo := blameSetupRepo(t)
	blameCommitFile(t, repo, "main.go", "aaa\nbbb\nccc\n", "Alice", "alice@example.com")

	ext := &BlameExtractor{}
	symbols := []SymbolRange{
		{SymbolID: 1, StartLine: 1, EndLine: 3},
	}

	facts1, err := ext.ExtractForFile(context.Background(), repo, "main.go", symbols)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}

	// Bob modifies line 2.
	fullPath := filepath.Join(repo, "main.go")
	if err := os.WriteFile(fullPath, []byte("aaa\nBBB\nccc\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmds := [][]string{
		{"git", "add", "main.go"},
		{"git", "-c", "user.name=Bob", "-c", "user.email=bob@example.com",
			"commit", "-m", "bob edits"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	facts2, err := ext.ExtractForFile(context.Background(), repo, "main.go", symbols)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}

	if len(facts1) != 1 || len(facts2) != 1 {
		t.Fatal("expected 1 fact from each run")
	}

	if facts1[0].StalenessHash == facts2[0].StalenessHash {
		t.Error("staleness hash should change after a new commit modifies blamed lines")
	}
}

func TestParsePorcelainBlame(t *testing.T) {
	// Minimal porcelain output for 2 lines from same commit.
	porcelain := "abc1234567890123456789012345678901234567 1 1 2\n" +
		"author Alice\n" +
		"author-mail <alice@example.com>\n" +
		"author-time 1700000000\n" +
		"author-tz +0000\n" +
		"committer Alice\n" +
		"committer-mail <alice@example.com>\n" +
		"committer-time 1700000000\n" +
		"committer-tz +0000\n" +
		"summary initial\n" +
		"filename main.go\n" +
		"\tline1 content\n" +
		"abc1234567890123456789012345678901234567 2 2\n" +
		"\tline2 content\n"

	lines, err := parsePorcelainBlame([]byte(porcelain))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	if lines[0].LineNumber != 1 {
		t.Errorf("line[0].LineNumber = %d, want 1", lines[0].LineNumber)
	}
	if lines[1].LineNumber != 2 {
		t.Errorf("line[1].LineNumber = %d, want 2", lines[1].LineNumber)
	}
	if lines[0].AuthorName != "Alice" {
		t.Errorf("line[0].AuthorName = %q, want Alice", lines[0].AuthorName)
	}
	if lines[0].AuthorMail != "alice@example.com" {
		t.Errorf("line[0].AuthorMail = %q, want alice@example.com", lines[0].AuthorMail)
	}
	// Second line should also have author info (cached from first occurrence).
	if lines[1].AuthorName != "Alice" {
		t.Errorf("line[1].AuthorName = %q, want Alice (cached)", lines[1].AuthorName)
	}
}

func TestComputeStalenessHash(t *testing.T) {
	lines := []blameLine{
		{CommitSHA: "abc123", LineNumber: 1},
		{CommitSHA: "abc123", LineNumber: 2},
	}

	h := computeStalenessHash(lines)

	// Verify it matches manual SHA256.
	expected := sha256.New()
	fmt.Fprintf(expected, "%s:%d\n", "abc123", 1)
	fmt.Fprintf(expected, "%s:%d\n", "abc123", 2)
	want := fmt.Sprintf("%x", expected.Sum(nil))

	if h != want {
		t.Errorf("hash = %q, want %q", h, want)
	}
}

func TestAggregateAuthors_Sorting(t *testing.T) {
	lines := []blameLine{
		{AuthorName: "Bob", AuthorMail: "bob@x.com", AuthorTime: "2024-01-01T00:00:00Z", CommitSHA: "bbb"},
		{AuthorName: "Alice", AuthorMail: "alice@x.com", AuthorTime: "2024-01-02T00:00:00Z", CommitSHA: "aaa"},
		{AuthorName: "Alice", AuthorMail: "alice@x.com", AuthorTime: "2024-01-03T00:00:00Z", CommitSHA: "aaa2"},
	}

	stats := aggregateAuthors(lines)
	if len(stats) != 2 {
		t.Fatalf("expected 2 authors, got %d", len(stats))
	}

	// Alice has 2 lines, Bob has 1.
	if stats[0].Email != "alice@x.com" {
		t.Errorf("top author = %q, want alice@x.com", stats[0].Email)
	}
	if stats[0].Lines != 2 {
		t.Errorf("alice lines = %d, want 2", stats[0].Lines)
	}
	if stats[1].Email != "bob@x.com" {
		t.Errorf("second author = %q, want bob@x.com", stats[1].Email)
	}
}

func TestFormatOwnershipBody(t *testing.T) {
	stats := []authorStats{
		{Name: "Alice", Email: "alice@x.com", Lines: 8},
		{Name: "Bob", Email: "bob@x.com", Lines: 2},
	}

	body := formatOwnershipBody(stats, 10)
	if !strings.HasPrefix(body, "Ownership: ") {
		t.Errorf("body should start with 'Ownership: ', got: %s", body)
	}
	if !strings.Contains(body, "80%") {
		t.Errorf("body should contain 80%% for Alice, got: %s", body)
	}
	if !strings.Contains(body, "20%") {
		t.Errorf("body should contain 20%% for Bob, got: %s", body)
	}
}
