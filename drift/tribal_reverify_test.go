package drift

import (
	"context"
	"testing"
	"time"

	"github.com/live-docs/live_docs/db"
)

// insertLLMFact inserts an LLM-extracted tribal fact (model != "") with the
// given lastVerified timestamp. Returns the fact ID.
func insertLLMFact(t *testing.T, cdb *db.ClaimsDB, subjectID int64, lastVerified string, confidence float64) int64 {
	t.Helper()
	fact := db.TribalFact{
		SubjectID:        subjectID,
		Kind:             "rationale",
		Body:             "test llm body",
		SourceQuote:      "test quote",
		Confidence:       confidence,
		Corroboration:    1,
		Extractor:        "pr_comment_miner",
		ExtractorVersion: "0.1.0",
		Model:            "claude-haiku-4-5-20251001",
		StalenessHash:    "abc",
		Status:           "active",
		CreatedAt:        lastVerified,
		LastVerified:     lastVerified,
	}
	evidence := []db.TribalEvidence{
		{
			SourceType:  "pr_comment",
			SourceRef:   "https://github.com/test/repo/pull/1#comment",
			ContentHash: "abc",
		},
	}
	id, err := cdb.InsertTribalFact(fact, evidence)
	if err != nil {
		t.Fatalf("insert llm fact: %v", err)
	}
	return id
}

// factConfidence returns the current confidence of a fact by ID.
func factConfidence(t *testing.T, cdb *db.ClaimsDB, factID int64) float64 {
	t.Helper()
	var conf float64
	err := cdb.DB().QueryRow("SELECT confidence FROM tribal_facts WHERE id = ?", factID).Scan(&conf)
	if err != nil {
		t.Fatalf("get fact confidence %d: %v", factID, err)
	}
	return conf
}

// factLastVerified returns the current last_verified of a fact by ID.
func factLastVerified(t *testing.T, cdb *db.ClaimsDB, factID int64) string {
	t.Helper()
	var lv string
	err := cdb.DB().QueryRow("SELECT last_verified FROM tribal_facts WHERE id = ?", factID).Scan(&lv)
	if err != nil {
		t.Fatalf("get fact last_verified %d: %v", factID, err)
	}
	return lv
}

// mockVerifier is a test SemanticVerifier that returns a fixed verdict sequence.
type mockVerifier struct {
	verdicts []ReverifyVerdict
	calls    int
}

func (m *mockVerifier) VerifyFact(ctx context.Context, fact db.TribalFact) (ReverifyVerdict, error) {
	if m.calls >= len(m.verdicts) {
		return VerdictAccept, nil
	}
	v := m.verdicts[m.calls]
	m.calls++
	return v, nil
}

func TestTribalReverify_AcceptVerdict(t *testing.T) {
	cdb := setupTribalDB(t)
	symID := insertSymbol(t, cdb, "AcceptFunc")
	oldTime := "2026-01-01T00:00:00Z"
	factID := insertLLMFact(t, cdb, symID, oldTime, 0.9)

	verifier := &mockVerifier{verdicts: []ReverifyVerdict{VerdictAccept}}
	now := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	result, err := ReverifyTribal(cdb, ReverifyOptions{
		SampleSize: 10,
		MaxAge:     30 * 24 * time.Hour,
		NowFn:      func() time.Time { return now },
		Verifier:   verifier,
		Budget:     100,
	})
	if err != nil {
		t.Fatalf("ReverifyTribal: %v", err)
	}

	if result.Accepted != 1 {
		t.Errorf("expected accepted=1, got %d", result.Accepted)
	}
	if result.Downgraded != 0 {
		t.Errorf("expected downgraded=0, got %d", result.Downgraded)
	}
	if result.Rejected != 0 {
		t.Errorf("expected rejected=0, got %d", result.Rejected)
	}

	// Status should still be active.
	if s := factStatus(t, cdb, factID); s != "active" {
		t.Errorf("expected status 'active', got %q", s)
	}
	// last_verified should be updated.
	lv := factLastVerified(t, cdb, factID)
	if lv == oldTime {
		t.Errorf("expected last_verified to be updated from %s", oldTime)
	}
}

func TestTribalReverify_DowngradeVerdict(t *testing.T) {
	cdb := setupTribalDB(t)
	symID := insertSymbol(t, cdb, "DowngradeFunc")
	factID := insertLLMFact(t, cdb, symID, "2026-01-01T00:00:00Z", 0.9)

	verifier := &mockVerifier{verdicts: []ReverifyVerdict{VerdictDowngrade}}
	now := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	result, err := ReverifyTribal(cdb, ReverifyOptions{
		SampleSize: 10,
		MaxAge:     30 * 24 * time.Hour,
		NowFn:      func() time.Time { return now },
		Verifier:   verifier,
		Budget:     100,
	})
	if err != nil {
		t.Fatalf("ReverifyTribal: %v", err)
	}

	if result.Downgraded != 1 {
		t.Errorf("expected downgraded=1, got %d", result.Downgraded)
	}

	// Status still active, confidence reduced.
	if s := factStatus(t, cdb, factID); s != "active" {
		t.Errorf("expected status 'active', got %q", s)
	}
	conf := factConfidence(t, cdb, factID)
	expected := 0.9 * 0.6
	if conf < expected-0.001 || conf > expected+0.001 {
		t.Errorf("expected confidence ~%.2f, got %.4f", expected, conf)
	}
}

func TestTribalReverify_RejectVerdict(t *testing.T) {
	cdb := setupTribalDB(t)
	symID := insertSymbol(t, cdb, "RejectFunc")
	factID := insertLLMFact(t, cdb, symID, "2026-01-01T00:00:00Z", 0.9)

	verifier := &mockVerifier{verdicts: []ReverifyVerdict{VerdictReject}}
	now := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	result, err := ReverifyTribal(cdb, ReverifyOptions{
		SampleSize: 10,
		MaxAge:     30 * 24 * time.Hour,
		NowFn:      func() time.Time { return now },
		Verifier:   verifier,
		Budget:     100,
	})
	if err != nil {
		t.Fatalf("ReverifyTribal: %v", err)
	}

	if result.Rejected != 1 {
		t.Errorf("expected rejected=1, got %d", result.Rejected)
	}

	// Status should be stale.
	if s := factStatus(t, cdb, factID); s != "stale" {
		t.Errorf("expected status 'stale', got %q", s)
	}
}

func TestTribalReverify_BudgetExhaustion(t *testing.T) {
	cdb := setupTribalDB(t)
	symID := insertSymbol(t, cdb, "BudgetFunc")
	// Insert 5 old LLM facts.
	for i := 0; i < 5; i++ {
		insertLLMFact(t, cdb, symID, "2026-01-01T00:00:00Z", 0.9)
	}

	verifier := &mockVerifier{verdicts: []ReverifyVerdict{
		VerdictAccept, VerdictAccept, VerdictAccept, VerdictAccept, VerdictAccept,
	}}
	now := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	result, err := ReverifyTribal(cdb, ReverifyOptions{
		SampleSize: 10,
		MaxAge:     30 * 24 * time.Hour,
		NowFn:      func() time.Time { return now },
		Verifier:   verifier,
		Budget:     2, // Only allow 2 calls.
	})
	if err != nil {
		t.Fatalf("ReverifyTribal: %v", err)
	}

	// Should have processed exactly 2 facts (budget limit).
	total := result.Accepted + result.Downgraded + result.Rejected
	if total != 2 {
		t.Errorf("expected 2 facts processed (budget=2), got %d", total)
	}
	if result.BudgetExhausted != true {
		t.Errorf("expected BudgetExhausted=true")
	}
}

func TestTribalReverify_SampleBounds(t *testing.T) {
	cdb := setupTribalDB(t)
	symID := insertSymbol(t, cdb, "SampleFunc")
	// Insert 10 old LLM facts.
	for i := 0; i < 10; i++ {
		insertLLMFact(t, cdb, symID, "2026-01-01T00:00:00Z", 0.9)
	}

	verifier := &mockVerifier{verdicts: make([]ReverifyVerdict, 10)} // all accept
	now := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	result, err := ReverifyTribal(cdb, ReverifyOptions{
		SampleSize: 3, // sample only 3.
		MaxAge:     30 * 24 * time.Hour,
		NowFn:      func() time.Time { return now },
		Verifier:   verifier,
		Budget:     100,
	})
	if err != nil {
		t.Fatalf("ReverifyTribal: %v", err)
	}

	total := result.Accepted + result.Downgraded + result.Rejected
	if total != 3 {
		t.Errorf("expected 3 facts processed (sample=3), got %d", total)
	}
}

func TestTribalReverify_SkipsDeterministicFacts(t *testing.T) {
	cdb := setupTribalDB(t)
	symID := insertSymbol(t, cdb, "DetFunc")
	// Insert a deterministic fact (model="").
	insertFact(t, cdb, symID, "active", "abc", "abc")
	// Insert an LLM fact that's old enough.
	llmID := insertLLMFact(t, cdb, symID, "2026-01-01T00:00:00Z", 0.9)

	verifier := &mockVerifier{verdicts: []ReverifyVerdict{VerdictReject}}
	now := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	result, err := ReverifyTribal(cdb, ReverifyOptions{
		SampleSize: 10,
		MaxAge:     30 * 24 * time.Hour,
		NowFn:      func() time.Time { return now },
		Verifier:   verifier,
		Budget:     100,
	})
	if err != nil {
		t.Fatalf("ReverifyTribal: %v", err)
	}

	// Only the LLM fact should have been processed.
	if result.Rejected != 1 {
		t.Errorf("expected rejected=1, got %d", result.Rejected)
	}
	if verifier.calls != 1 {
		t.Errorf("expected 1 verifier call, got %d", verifier.calls)
	}
	// The LLM fact should be stale now.
	if s := factStatus(t, cdb, llmID); s != "stale" {
		t.Errorf("expected LLM fact status 'stale', got %q", s)
	}
}

func TestTribalReverify_SkipsRecentFacts(t *testing.T) {
	cdb := setupTribalDB(t)
	symID := insertSymbol(t, cdb, "RecentFunc")
	// Insert a fact verified just 5 days ago.
	recentTime := "2026-04-10T00:00:00Z"
	insertLLMFact(t, cdb, symID, recentTime, 0.9)

	verifier := &mockVerifier{verdicts: []ReverifyVerdict{VerdictAccept}}
	now := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	result, err := ReverifyTribal(cdb, ReverifyOptions{
		SampleSize: 10,
		MaxAge:     30 * 24 * time.Hour, // 30 days — fact is only 5 days old.
		NowFn:      func() time.Time { return now },
		Verifier:   verifier,
		Budget:     100,
	})
	if err != nil {
		t.Fatalf("ReverifyTribal: %v", err)
	}

	total := result.Accepted + result.Downgraded + result.Rejected
	if total != 0 {
		t.Errorf("expected 0 facts processed (all too recent), got %d", total)
	}
	if verifier.calls != 0 {
		t.Errorf("expected 0 verifier calls, got %d", verifier.calls)
	}
}

func TestTribalReverify_AllThreeVerdicts(t *testing.T) {
	cdb := setupTribalDB(t)
	symID := insertSymbol(t, cdb, "MixedFunc")
	f1 := insertLLMFact(t, cdb, symID, "2026-01-01T00:00:00Z", 0.9)
	f2 := insertLLMFact(t, cdb, symID, "2026-01-02T00:00:00Z", 0.8)
	f3 := insertLLMFact(t, cdb, symID, "2026-01-03T00:00:00Z", 0.7)

	verifier := &mockVerifier{verdicts: []ReverifyVerdict{
		VerdictAccept, VerdictDowngrade, VerdictReject,
	}}
	now := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)

	result, err := ReverifyTribal(cdb, ReverifyOptions{
		SampleSize: 10,
		MaxAge:     30 * 24 * time.Hour,
		NowFn:      func() time.Time { return now },
		Verifier:   verifier,
		Budget:     100,
	})
	if err != nil {
		t.Fatalf("ReverifyTribal: %v", err)
	}

	if result.Accepted != 1 {
		t.Errorf("accepted: want 1, got %d", result.Accepted)
	}
	if result.Downgraded != 1 {
		t.Errorf("downgraded: want 1, got %d", result.Downgraded)
	}
	if result.Rejected != 1 {
		t.Errorf("rejected: want 1, got %d", result.Rejected)
	}

	// f1 accepted: still active.
	if s := factStatus(t, cdb, f1); s != "active" {
		t.Errorf("f1: want active, got %q", s)
	}
	// f2 downgraded: active but lower confidence.
	if s := factStatus(t, cdb, f2); s != "active" {
		t.Errorf("f2: want active, got %q", s)
	}
	conf := factConfidence(t, cdb, f2)
	if conf < 0.47 || conf > 0.49 {
		t.Errorf("f2 confidence: want ~0.48, got %.4f", conf)
	}
	// f3 rejected: stale.
	if s := factStatus(t, cdb, f3); s != "stale" {
		t.Errorf("f3: want stale, got %q", s)
	}
}
