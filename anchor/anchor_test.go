package anchor

import (
	"testing"
	"time"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/gitdiff"
)

func TestNewAnchor(t *testing.T) {
	a := NewAnchor(42, "pkg/server/handler.go", 10, 25)
	if a.ClaimID != 42 {
		t.Errorf("ClaimID = %d, want 42", a.ClaimID)
	}
	if a.File != "pkg/server/handler.go" {
		t.Errorf("File = %q, want %q", a.File, "pkg/server/handler.go")
	}
	if a.StartLine != 10 {
		t.Errorf("StartLine = %d, want 10", a.StartLine)
	}
	if a.EndLine != 25 {
		t.Errorf("EndLine = %d, want 25", a.EndLine)
	}
	if a.Status != StatusVerified {
		t.Errorf("Status = %q, want %q", a.Status, StatusVerified)
	}
}

func TestNewAnchor_WholeFile(t *testing.T) {
	a := NewAnchor(7, "main.go", 0, 0)
	if a.StartLine != 0 || a.EndLine != 0 {
		t.Errorf("expected whole-file anchor (0,0), got (%d,%d)", a.StartLine, a.EndLine)
	}
	if a.Status != StatusVerified {
		t.Errorf("Status = %q, want %q", a.Status, StatusVerified)
	}
}

func TestAnchor_IsWholeFile(t *testing.T) {
	whole := NewAnchor(1, "f.go", 0, 0)
	if !whole.IsWholeFile() {
		t.Error("expected IsWholeFile() == true for (0,0)")
	}
	partial := NewAnchor(2, "f.go", 5, 10)
	if partial.IsWholeFile() {
		t.Error("expected IsWholeFile() == false for (5,10)")
	}
}

func TestAnchor_Overlaps(t *testing.T) {
	tests := []struct {
		name    string
		anchor  Anchor
		start   int
		end     int
		overlap bool
	}{
		{"exact match", NewAnchor(1, "f.go", 10, 20), 10, 20, true},
		{"partial overlap start", NewAnchor(1, "f.go", 10, 20), 5, 15, true},
		{"partial overlap end", NewAnchor(1, "f.go", 10, 20), 15, 25, true},
		{"contained", NewAnchor(1, "f.go", 10, 20), 12, 18, true},
		{"surrounding", NewAnchor(1, "f.go", 10, 20), 5, 25, true},
		{"before", NewAnchor(1, "f.go", 10, 20), 1, 9, false},
		{"after", NewAnchor(1, "f.go", 10, 20), 21, 30, false},
		{"adjacent before", NewAnchor(1, "f.go", 10, 20), 1, 10, true},
		{"adjacent after", NewAnchor(1, "f.go", 10, 20), 20, 30, true},
		{"whole file always overlaps", NewAnchor(1, "f.go", 0, 0), 50, 60, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.anchor.Overlaps(tt.start, tt.end)
			if got != tt.overlap {
				t.Errorf("Overlaps(%d, %d) = %v, want %v", tt.start, tt.end, got, tt.overlap)
			}
		})
	}
}

func TestIndex_Add_And_ForFile(t *testing.T) {
	idx := NewIndex()
	a1 := NewAnchor(1, "a.go", 1, 10)
	a2 := NewAnchor(2, "a.go", 20, 30)
	a3 := NewAnchor(3, "b.go", 1, 5)

	idx.Add(a1)
	idx.Add(a2)
	idx.Add(a3)

	aAnchors := idx.ForFile("a.go")
	if len(aAnchors) != 2 {
		t.Fatalf("ForFile(a.go) returned %d anchors, want 2", len(aAnchors))
	}

	bAnchors := idx.ForFile("b.go")
	if len(bAnchors) != 1 {
		t.Fatalf("ForFile(b.go) returned %d anchors, want 1", len(bAnchors))
	}

	cAnchors := idx.ForFile("c.go")
	if len(cAnchors) != 0 {
		t.Fatalf("ForFile(c.go) returned %d anchors, want 0", len(cAnchors))
	}
}

func TestIndex_BuildFromClaims(t *testing.T) {
	claims := []db.Claim{
		{ID: 1, SourceFile: "handler.go", SourceLine: 10},
		{ID: 2, SourceFile: "handler.go", SourceLine: 50},
		{ID: 3, SourceFile: "server.go", SourceLine: 0},
		{ID: 4, SourceFile: "util.go", SourceLine: 5},
	}

	idx := BuildFromClaims(claims, 20)

	// handler.go should have 2 anchors
	hAnchors := idx.ForFile("handler.go")
	if len(hAnchors) != 2 {
		t.Fatalf("handler.go: got %d anchors, want 2", len(hAnchors))
	}

	// First anchor: line 10 with radius 20 -> (1, 30) clamped to (1, 30)
	if hAnchors[0].StartLine != 1 || hAnchors[0].EndLine != 30 {
		t.Errorf("handler.go anchor 0: got (%d,%d), want (1,30)", hAnchors[0].StartLine, hAnchors[0].EndLine)
	}

	// server.go: line 0 means whole-file anchor
	sAnchors := idx.ForFile("server.go")
	if len(sAnchors) != 1 {
		t.Fatalf("server.go: got %d anchors, want 1", len(sAnchors))
	}
	if !sAnchors[0].IsWholeFile() {
		t.Error("server.go: expected whole-file anchor")
	}
}

func TestIndex_Invalidate_FileModified(t *testing.T) {
	idx := NewIndex()
	idx.Add(NewAnchor(1, "handler.go", 10, 30))
	idx.Add(NewAnchor(2, "handler.go", 50, 70))
	idx.Add(NewAnchor(3, "server.go", 1, 20))

	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "handler.go"},
	}

	stale := idx.Invalidate(changes)
	if len(stale) != 2 {
		t.Fatalf("Invalidate returned %d stale anchors, want 2", len(stale))
	}

	// Both handler.go anchors should be stale
	for _, s := range stale {
		if s.File != "handler.go" {
			t.Errorf("unexpected stale file: %s", s.File)
		}
		if s.Status != StatusStale {
			t.Errorf("expected StatusStale, got %q", s.Status)
		}
	}

	// server.go should remain verified
	sAnchors := idx.ForFile("server.go")
	if sAnchors[0].Status != StatusVerified {
		t.Errorf("server.go should remain verified, got %q", sAnchors[0].Status)
	}
}

func TestIndex_Invalidate_FileDeleted(t *testing.T) {
	idx := NewIndex()
	idx.Add(NewAnchor(1, "old.go", 10, 30))

	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusDeleted, Path: "old.go"},
	}

	stale := idx.Invalidate(changes)
	if len(stale) != 1 {
		t.Fatalf("Invalidate returned %d stale anchors, want 1", len(stale))
	}
	if stale[0].Status != StatusInvalid {
		t.Errorf("deleted file anchor should be StatusInvalid, got %q", stale[0].Status)
	}
}

func TestIndex_Invalidate_FileRenamed(t *testing.T) {
	idx := NewIndex()
	idx.Add(NewAnchor(1, "old_name.go", 10, 30))

	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusRenamed, Path: "new_name.go", OldPath: "old_name.go"},
	}

	stale := idx.Invalidate(changes)
	if len(stale) != 1 {
		t.Fatalf("Invalidate returned %d stale anchors, want 1", len(stale))
	}
	if stale[0].Status != StatusStale {
		t.Errorf("renamed file anchor should be StatusStale, got %q", stale[0].Status)
	}
}

func TestIndex_Invalidate_NoChanges(t *testing.T) {
	idx := NewIndex()
	idx.Add(NewAnchor(1, "stable.go", 10, 30))

	stale := idx.Invalidate(nil)
	if len(stale) != 0 {
		t.Fatalf("Invalidate(nil) returned %d stale anchors, want 0", len(stale))
	}
}

func TestIndex_Invalidate_UnrelatedChange(t *testing.T) {
	idx := NewIndex()
	idx.Add(NewAnchor(1, "handler.go", 10, 30))

	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "unrelated.go"},
	}

	stale := idx.Invalidate(changes)
	if len(stale) != 0 {
		t.Fatalf("expected no stale anchors for unrelated file change, got %d", len(stale))
	}
}

func TestIndex_QueryStale(t *testing.T) {
	idx := NewIndex()
	idx.Add(NewAnchor(1, "a.go", 1, 10))
	idx.Add(NewAnchor(2, "a.go", 20, 30))
	idx.Add(NewAnchor(3, "b.go", 1, 10))

	// Invalidate a.go
	idx.Invalidate([]gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "a.go"},
	})

	stale := idx.QueryStale()
	if len(stale) != 2 {
		t.Fatalf("QueryStale returned %d, want 2", len(stale))
	}
	for _, s := range stale {
		if s.File != "a.go" {
			t.Errorf("unexpected stale file: %s", s.File)
		}
	}
}

func TestIndex_QueryByStatus(t *testing.T) {
	idx := NewIndex()
	idx.Add(NewAnchor(1, "a.go", 1, 10))
	idx.Add(NewAnchor(2, "deleted.go", 1, 10))
	idx.Add(NewAnchor(3, "b.go", 1, 10))

	idx.Invalidate([]gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "a.go"},
		{Status: gitdiff.StatusDeleted, Path: "deleted.go"},
	})

	invalid := idx.QueryByStatus(StatusInvalid)
	if len(invalid) != 1 {
		t.Fatalf("QueryByStatus(Invalid) returned %d, want 1", len(invalid))
	}
	if invalid[0].File != "deleted.go" {
		t.Errorf("expected deleted.go, got %s", invalid[0].File)
	}

	verified := idx.QueryByStatus(StatusVerified)
	if len(verified) != 1 {
		t.Fatalf("QueryByStatus(Verified) returned %d, want 1", len(verified))
	}
}

func TestIndex_StaleClaimIDs(t *testing.T) {
	idx := NewIndex()
	idx.Add(NewAnchor(10, "a.go", 1, 10))
	idx.Add(NewAnchor(20, "a.go", 20, 30))
	idx.Add(NewAnchor(30, "b.go", 1, 10))

	idx.Invalidate([]gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "a.go"},
	})

	ids := idx.StaleClaimIDs()
	if len(ids) != 2 {
		t.Fatalf("StaleClaimIDs returned %d, want 2", len(ids))
	}
	// Should contain 10 and 20
	idSet := map[int64]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	if !idSet[10] || !idSet[20] {
		t.Errorf("expected claim IDs {10, 20}, got %v", ids)
	}
}

func TestIndex_MarkVerified(t *testing.T) {
	idx := NewIndex()
	idx.Add(NewAnchor(1, "a.go", 1, 10))

	idx.Invalidate([]gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "a.go"},
	})

	stale := idx.QueryStale()
	if len(stale) != 1 {
		t.Fatal("expected 1 stale before MarkVerified")
	}

	idx.MarkVerified(1, time.Now())

	stale = idx.QueryStale()
	if len(stale) != 0 {
		t.Fatal("expected 0 stale after MarkVerified")
	}
}

func TestIndex_Summary(t *testing.T) {
	idx := NewIndex()
	idx.Add(NewAnchor(1, "a.go", 1, 10))
	idx.Add(NewAnchor(2, "b.go", 1, 10))
	idx.Add(NewAnchor(3, "c.go", 1, 10))

	idx.Invalidate([]gitdiff.FileChange{
		{Status: gitdiff.StatusModified, Path: "a.go"},
		{Status: gitdiff.StatusDeleted, Path: "b.go"},
	})

	s := idx.Summary()
	if s.Total != 3 {
		t.Errorf("Total = %d, want 3", s.Total)
	}
	if s.Verified != 1 {
		t.Errorf("Verified = %d, want 1", s.Verified)
	}
	if s.Stale != 1 {
		t.Errorf("Stale = %d, want 1", s.Stale)
	}
	if s.Invalid != 1 {
		t.Errorf("Invalid = %d, want 1", s.Invalid)
	}
}

func TestBuildFromClaims_ZeroLine_IsWholeFile(t *testing.T) {
	claims := []db.Claim{
		{ID: 1, SourceFile: "main.go", SourceLine: 0},
	}
	idx := BuildFromClaims(claims, 10)
	anchors := idx.ForFile("main.go")
	if len(anchors) != 1 {
		t.Fatal("expected 1 anchor")
	}
	if !anchors[0].IsWholeFile() {
		t.Error("SourceLine 0 should produce whole-file anchor")
	}
}

func TestIndex_Invalidate_AddedFile(t *testing.T) {
	idx := NewIndex()
	idx.Add(NewAnchor(1, "existing.go", 1, 10))

	changes := []gitdiff.FileChange{
		{Status: gitdiff.StatusAdded, Path: "new_file.go"},
	}

	// Added files have no existing anchors, so nothing should be stale
	stale := idx.Invalidate(changes)
	if len(stale) != 0 {
		t.Fatalf("expected no stale anchors for newly added file, got %d", len(stale))
	}
}
