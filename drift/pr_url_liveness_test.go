package drift

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sjarmak/livedocs/db"
)

// fakeLivenessChecker implements LivenessChecker via per-key lookup.
type fakeLivenessChecker struct {
	results map[string]fakeLivenessResult
	calls   int
}

type fakeLivenessResult struct {
	live bool
	hash string
	err  error
}

func (f *fakeLivenessChecker) CheckSourceRefLive(_ context.Context, sourceRef string) (bool, string, error) {
	f.calls++
	r, ok := f.results[sourceRef]
	if !ok {
		return true, "", nil
	}
	return r.live, r.hash, r.err
}

// insertFactWithPRComment inserts a tribal fact with one pr_comment evidence
// row whose ContentHash matches the fact's StalenessHash (so evidenceHashChanged
// alone would NOT flip it stale). Returns the fact ID.
func insertFactWithPRComment(t *testing.T, cdb *db.ClaimsDB, subjectID int64, stalenessHash, sourceRef string) int64 {
	t.Helper()
	fact := db.TribalFact{
		SubjectID:        subjectID,
		Kind:             "rationale",
		Body:             "test body",
		SourceQuote:      "test quote",
		Confidence:       0.9,
		Corroboration:    1,
		Extractor:        "pr_comment_miner",
		ExtractorVersion: "0.1.0",
		Model:            "claude-haiku",
		StalenessHash:    stalenessHash,
		Status:           "active",
		CreatedAt:        "2026-01-01T00:00:00Z",
		LastVerified:     "2026-01-01T00:00:00Z",
	}
	evidence := []db.TribalEvidence{
		{
			SourceType:  "pr_comment",
			SourceRef:   sourceRef,
			ContentHash: stalenessHash, // matches so hash-based drift won't trigger
		},
	}
	id, err := cdb.InsertTribalFact(fact, evidence)
	if err != nil {
		t.Fatalf("insert tribal fact: %v", err)
	}
	return id
}

func TestPRURLLivenessStale_HashChanged(t *testing.T) {
	cdb := setupTribalDB(t)

	symID := insertSymbol(t, cdb, "LiveFunc")
	ref := "https://github.com/org/repo/pull/1#r1"
	factID := insertFactWithPRComment(t, cdb, symID, "aaa", ref)

	checker := &fakeLivenessChecker{
		results: map[string]fakeLivenessResult{
			ref: {live: true, hash: "bbb"}, // hash != staleness_hash
		},
	}

	stale, quarantined, err := CheckTribalWithLiveness(cdb, checker)
	if err != nil {
		t.Fatalf("CheckTribalWithLiveness: %v", err)
	}
	if stale != 1 {
		t.Errorf("staleCount = %d, want 1", stale)
	}
	if quarantined != 0 {
		t.Errorf("quarantinedCount = %d, want 0", quarantined)
	}
	if factStatus(t, cdb, factID) != "stale" {
		t.Errorf("fact status = %q, want stale", factStatus(t, cdb, factID))
	}
}

func TestPRURLLivenessStale_NotFound(t *testing.T) {
	cdb := setupTribalDB(t)

	symID := insertSymbol(t, cdb, "GoneFunc")
	ref := "https://github.com/org/repo/pull/2#r2"
	factID := insertFactWithPRComment(t, cdb, symID, "aaa", ref)

	checker := &fakeLivenessChecker{
		results: map[string]fakeLivenessResult{
			ref: {live: false, hash: ""},
		},
	}

	stale, _, err := CheckTribalWithLiveness(cdb, checker)
	if err != nil {
		t.Fatalf("CheckTribalWithLiveness: %v", err)
	}
	if stale != 1 {
		t.Errorf("staleCount = %d, want 1", stale)
	}
	if factStatus(t, cdb, factID) != "stale" {
		t.Errorf("fact status = %q, want stale", factStatus(t, cdb, factID))
	}
}

func TestPRURLLivenessStale_Unchanged(t *testing.T) {
	cdb := setupTribalDB(t)

	symID := insertSymbol(t, cdb, "StillFunc")
	ref := "https://github.com/org/repo/pull/3#r3"
	factID := insertFactWithPRComment(t, cdb, symID, "aaa", ref)

	checker := &fakeLivenessChecker{
		results: map[string]fakeLivenessResult{
			ref: {live: true, hash: "aaa"}, // matches
		},
	}

	stale, _, err := CheckTribalWithLiveness(cdb, checker)
	if err != nil {
		t.Fatalf("CheckTribalWithLiveness: %v", err)
	}
	if stale != 0 {
		t.Errorf("staleCount = %d, want 0", stale)
	}
	if factStatus(t, cdb, factID) != "active" {
		t.Errorf("fact status = %q, want active", factStatus(t, cdb, factID))
	}
}

func TestPRURLLivenessStale_TransportErrorIsNonAuthoritative(t *testing.T) {
	cdb := setupTribalDB(t)

	symID := insertSymbol(t, cdb, "NetFlakyFunc")
	ref := "https://github.com/org/repo/pull/4#r4"
	factID := insertFactWithPRComment(t, cdb, symID, "aaa", ref)

	checker := &fakeLivenessChecker{
		results: map[string]fakeLivenessResult{
			ref: {live: false, hash: "", err: errors.New("network down")},
		},
	}

	stale, _, err := CheckTribalWithLiveness(cdb, checker)
	if err != nil {
		t.Fatalf("CheckTribalWithLiveness: %v", err)
	}
	if stale != 0 {
		t.Errorf("staleCount = %d, want 0 (transport errors should not flip status)", stale)
	}
	if factStatus(t, cdb, factID) != "active" {
		t.Errorf("fact status = %q, want active", factStatus(t, cdb, factID))
	}
}

// --- LivenessCache tests ---

func TestLivenessCache_TTL24h(t *testing.T) {
	now := time.Now()
	nowFn := func() time.Time { return now }
	var calls int
	runner := func(_ context.Context, ref string) ([]byte, bool, error) {
		calls++
		return []byte("body-" + ref), true, nil
	}
	cache := NewLivenessCache(nowFn, runner, 0)

	// First call: miss, runner invoked.
	live1, hash1, err := cache.CheckSourceRefLive(context.Background(), "url1")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !live1 {
		t.Error("first call: expected live")
	}
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte("body-url1")))
	if hash1 != expectedHash {
		t.Errorf("hash1 = %q, want %q", hash1, expectedHash)
	}
	if calls != 1 {
		t.Errorf("runner calls = %d, want 1", calls)
	}

	// Second call within TTL: hit, runner NOT invoked.
	live2, hash2, err := cache.CheckSourceRefLive(context.Background(), "url1")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !live2 || hash2 != hash1 {
		t.Errorf("second call cache mismatch")
	}
	if calls != 1 {
		t.Errorf("runner calls after hit = %d, want 1", calls)
	}

	// Advance past TTL.
	now = now.Add(livenessCacheTTL + time.Second)
	live3, _, err := cache.CheckSourceRefLive(context.Background(), "url1")
	if err != nil {
		t.Fatalf("third call: %v", err)
	}
	if !live3 {
		t.Error("third call: expected live")
	}
	if calls != 2 {
		t.Errorf("runner calls after TTL expiry = %d, want 2", calls)
	}
}

func TestLivenessCache_BudgetFailOpen(t *testing.T) {
	nowFn := func() time.Time { return time.Now() }
	runner := func(_ context.Context, _ string) ([]byte, bool, error) {
		return []byte("body"), true, nil
	}
	cache := NewLivenessCache(nowFn, runner, 1)

	// First call: within budget.
	if _, _, err := cache.CheckSourceRefLive(context.Background(), "url1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Second call (different URL): out of budget, should fail-open.
	live, hash, err := cache.CheckSourceRefLive(context.Background(), "url2")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !live {
		t.Error("fail-open expected live=true")
	}
	if hash != "" {
		t.Errorf("fail-open hash = %q, want empty", hash)
	}
}
