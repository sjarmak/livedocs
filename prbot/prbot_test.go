package prbot

import (
	"strings"
	"testing"

	"github.com/live-docs/live_docs/anchor"
	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/gitdiff"
)

func TestAnalyze_NoChanges(t *testing.T) {
	claims := []db.Claim{
		{ID: 1, SourceFile: "pkg/foo.go", SourceLine: 10, Predicate: "defines", ObjectText: "Foo struct"},
	}
	report := Analyze(nil, claims, 3)

	if len(report.Invalidated) != 0 {
		t.Fatalf("expected 0 invalidated, got %d", len(report.Invalidated))
	}
	if report.Summary.Total != 1 {
		t.Fatalf("expected 1 total anchor, got %d", report.Summary.Total)
	}
	if report.Summary.Verified != 1 {
		t.Fatalf("expected 1 verified, got %d", report.Summary.Verified)
	}
}

func TestAnalyze_NoClaims(t *testing.T) {
	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "pkg/foo.go"},
	}
	report := Analyze(changes, nil, 3)

	if len(report.Invalidated) != 0 {
		t.Fatalf("expected 0 invalidated, got %d", len(report.Invalidated))
	}
}

func TestAnalyze_ModifiedFileInvalidatesClaim(t *testing.T) {
	claims := []db.Claim{
		{ID: 1, SourceFile: "pkg/foo.go", SourceLine: 10, Predicate: "defines", ObjectText: "Foo struct"},
		{ID: 2, SourceFile: "pkg/bar.go", SourceLine: 20, Predicate: "exports", ObjectText: "Bar function"},
	}
	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "pkg/foo.go"},
	}

	report := Analyze(changes, claims, 3)

	if len(report.Invalidated) != 1 {
		t.Fatalf("expected 1 invalidated, got %d", len(report.Invalidated))
	}
	ic := report.Invalidated[0]
	if ic.Claim.ID != 1 {
		t.Errorf("expected claim ID 1, got %d", ic.Claim.ID)
	}
	if ic.Anchor.Status != anchor.StatusStale {
		t.Errorf("expected stale status, got %s", ic.Anchor.Status)
	}
	if ic.Change.Path != "pkg/foo.go" {
		t.Errorf("expected change path pkg/foo.go, got %s", ic.Change.Path)
	}
}

func TestAnalyze_DeletedFileInvalidatesClaim(t *testing.T) {
	claims := []db.Claim{
		{ID: 1, SourceFile: "pkg/foo.go", SourceLine: 10, Predicate: "defines", ObjectText: "Foo struct"},
	}
	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusDeleted, Path: "pkg/foo.go"},
	}

	report := Analyze(changes, claims, 3)

	if len(report.Invalidated) != 1 {
		t.Fatalf("expected 1 invalidated, got %d", len(report.Invalidated))
	}
	if report.Invalidated[0].Anchor.Status != anchor.StatusInvalid {
		t.Errorf("expected invalid status, got %s", report.Invalidated[0].Anchor.Status)
	}
}

func TestAnalyze_RenamedFileInvalidatesOldAndNewPaths(t *testing.T) {
	claims := []db.Claim{
		{ID: 1, SourceFile: "pkg/old.go", SourceLine: 5, Predicate: "defines", ObjectText: "OldFunc"},
		{ID: 2, SourceFile: "pkg/new.go", SourceLine: 5, Predicate: "defines", ObjectText: "NewFunc"},
	}
	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusRenamed, Path: "pkg/new.go", OldPath: "pkg/old.go"},
	}

	report := Analyze(changes, claims, 3)

	if len(report.Invalidated) != 2 {
		t.Fatalf("expected 2 invalidated, got %d", len(report.Invalidated))
	}
	for _, ic := range report.Invalidated {
		if ic.Anchor.Status != anchor.StatusStale {
			t.Errorf("expected stale status for claim %d, got %s", ic.Claim.ID, ic.Anchor.Status)
		}
	}
}

func TestAnalyze_WholeFileAnchor(t *testing.T) {
	// SourceLine 0 -> whole-file anchor
	claims := []db.Claim{
		{ID: 1, SourceFile: "README.md", SourceLine: 0, Predicate: "has_doc", ObjectText: "Package overview"},
	}
	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "README.md"},
	}

	report := Analyze(changes, claims, 3)

	if len(report.Invalidated) != 1 {
		t.Fatalf("expected 1 invalidated, got %d", len(report.Invalidated))
	}
}

func TestAnalyze_UnrelatedChangeNoImpact(t *testing.T) {
	claims := []db.Claim{
		{ID: 1, SourceFile: "pkg/foo.go", SourceLine: 10, Predicate: "defines", ObjectText: "Foo struct"},
	}
	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "pkg/unrelated.go"},
	}

	report := Analyze(changes, claims, 3)

	if len(report.Invalidated) != 0 {
		t.Fatalf("expected 0 invalidated, got %d", len(report.Invalidated))
	}
}

func TestAnalyze_MultipleClaimsSameFile(t *testing.T) {
	claims := []db.Claim{
		{ID: 1, SourceFile: "pkg/foo.go", SourceLine: 10, Predicate: "defines", ObjectText: "Foo struct"},
		{ID: 2, SourceFile: "pkg/foo.go", SourceLine: 50, Predicate: "exports", ObjectText: "Bar method"},
		{ID: 3, SourceFile: "pkg/foo.go", SourceLine: 90, Predicate: "has_doc", ObjectText: "Package doc"},
	}
	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "pkg/foo.go"},
	}

	report := Analyze(changes, claims, 3)

	if len(report.Invalidated) != 3 {
		t.Fatalf("expected 3 invalidated, got %d", len(report.Invalidated))
	}
}

func TestAnalyze_SortedOutput(t *testing.T) {
	claims := []db.Claim{
		{ID: 1, SourceFile: "z/last.go", SourceLine: 10, Predicate: "defines", ObjectText: "Last"},
		{ID: 2, SourceFile: "a/first.go", SourceLine: 20, Predicate: "defines", ObjectText: "First"},
		{ID: 3, SourceFile: "a/first.go", SourceLine: 5, Predicate: "defines", ObjectText: "Earlier"},
	}
	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "z/last.go"},
		{Status: gitdiff.StatusModified, Path: "a/first.go"},
	}

	report := Analyze(changes, claims, 3)

	if len(report.Invalidated) != 3 {
		t.Fatalf("expected 3 invalidated, got %d", len(report.Invalidated))
	}
	// Should be sorted: a/first.go:5, a/first.go:20, z/last.go:10
	if report.Invalidated[0].Claim.SourceFile != "a/first.go" || report.Invalidated[0].Claim.SourceLine != 5 {
		t.Errorf("first item should be a/first.go:5, got %s:%d",
			report.Invalidated[0].Claim.SourceFile, report.Invalidated[0].Claim.SourceLine)
	}
	if report.Invalidated[2].Claim.SourceFile != "z/last.go" {
		t.Errorf("last item should be z/last.go, got %s", report.Invalidated[2].Claim.SourceFile)
	}
}

func TestFormatComment_NoInvalidated(t *testing.T) {
	report := Report{
		Summary: anchor.Summary{Total: 5, Verified: 5},
	}

	body := FormatComment(report)

	if !strings.Contains(body, "No Impact Detected") {
		t.Error("expected 'No Impact Detected' in clean comment")
	}
	if !strings.Contains(body, "5 anchored claims") {
		t.Errorf("expected claim count in body, got: %s", body)
	}
}

func TestFormatComment_WithInvalidated(t *testing.T) {
	report := Report{
		Invalidated: []InvalidatedClaim{
			{
				Anchor: anchor.Anchor{
					ClaimID: 1, File: "pkg/foo.go",
					StartLine: 7, EndLine: 13,
					Status: anchor.StatusStale,
				},
				Claim: db.Claim{
					ID: 1, SourceFile: "pkg/foo.go", SourceLine: 10,
					Predicate: "defines", ObjectText: "Foo struct handles configuration",
				},
				Change: gitdiff.FileChange{Status: gitdiff.StatusModified, Path: "pkg/foo.go"},
			},
		},
		Summary: anchor.Summary{Total: 3, Verified: 2, Stale: 1},
	}

	body := FormatComment(report)

	if !strings.Contains(body, "Documentation Impact") {
		t.Error("expected 'Documentation Impact' header")
	}
	if !strings.Contains(body, "**1** documentation claim") {
		t.Error("expected '1 documentation claim' in body")
	}
	if !strings.Contains(body, "`pkg/foo.go`") {
		t.Error("expected file path in body")
	}
	if !strings.Contains(body, "defines") {
		t.Error("expected predicate in body")
	}
	if !strings.Contains(body, "non-blocking") {
		t.Error("expected advisory note in body")
	}
}

func TestFormatComment_LongObjectTextTruncated(t *testing.T) {
	longText := strings.Repeat("x", 100)
	report := Report{
		Invalidated: []InvalidatedClaim{
			{
				Anchor: anchor.Anchor{ClaimID: 1, File: "f.go", Status: anchor.StatusStale},
				Claim:  db.Claim{ID: 1, SourceFile: "f.go", SourceLine: 1, Predicate: "defines", ObjectText: longText},
				Change: gitdiff.FileChange{Status: gitdiff.StatusModified, Path: "f.go"},
			},
		},
		Summary: anchor.Summary{Total: 1, Stale: 1},
	}

	body := FormatComment(report)

	if strings.Contains(body, longText) {
		t.Error("expected long text to be truncated")
	}
	if !strings.Contains(body, "...") {
		t.Error("expected truncation ellipsis")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is too long", 10, "this is..."},
	}
	for _, tt := range tests {
		got := truncate(tt.input, tt.max)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.want)
		}
	}
}

// mockPoster implements CommentPoster for testing.
type mockPoster struct {
	posted []string
}

func (m *mockPoster) PostComment(owner, repo string, prNumber int, body string) error {
	m.posted = append(m.posted, body)
	return nil
}

func TestCommentPosterInterface(t *testing.T) {
	poster := &mockPoster{}

	report := Report{
		Summary: anchor.Summary{Total: 1, Verified: 1},
	}
	body := FormatComment(report)
	err := poster.PostComment("org", "repo", 42, body)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(poster.posted) != 1 {
		t.Fatalf("expected 1 posted comment, got %d", len(poster.posted))
	}
	if !strings.Contains(poster.posted[0], "No Impact Detected") {
		t.Error("expected clean comment in posted body")
	}
}
