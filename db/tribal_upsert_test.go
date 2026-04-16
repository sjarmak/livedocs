package db

import (
	"testing"
)

// upsertFact is a small factory for tests that want a fully-populated
// TribalFact with common defaults.
func upsertFact(subjectID int64, body, quote string) TribalFact {
	return TribalFact{
		SubjectID:        subjectID,
		Kind:             "rationale",
		Body:             body,
		SourceQuote:      quote,
		Confidence:       0.9,
		Corroboration:    1,
		Extractor:        "test_upsert",
		ExtractorVersion: "1.0",
		Model:            "",
		StalenessHash:    "stale",
		Status:           "active",
		CreatedAt:        "2025-01-01T00:00:00Z",
		LastVerified:     "2025-01-01T00:00:00Z",
	}
}

func upsertEvidence(sourceRef, contentHash string) TribalEvidence {
	return TribalEvidence{
		SourceType:  "pr_comment",
		SourceRef:   sourceRef,
		Author:      "alice",
		AuthoredAt:  "2025-01-01T00:00:00Z",
		ContentHash: contentHash,
	}
}

// TestUpsertTribalFactRejectsEmptyEvidence verifies the upsert API matches
// InsertTribalFact's contract for the evidence-required invariant.
func TestUpsertTribalFactRejectsEmptyEvidence(t *testing.T) {
	cdb := tribalDB(t)
	subjectID := insertTestSymbol(t, cdb, "Handler")
	_, _, err := cdb.UpsertTribalFact(upsertFact(subjectID, "body", "quote"), nil)
	if err == nil {
		t.Fatal("expected error on empty evidence")
	}
}

// TestUpsertTribalFactRejectsCallerSuppliedClusterKey guards the invariant
// that callers must not pre-compute ClusterKey.
func TestUpsertTribalFactRejectsCallerSuppliedClusterKey(t *testing.T) {
	cdb := tribalDB(t)
	subjectID := insertTestSymbol(t, cdb, "Handler")
	fact := upsertFact(subjectID, "body", "quote")
	fact.ClusterKey = "precomputed"
	_, _, err := cdb.UpsertTribalFact(fact, []TribalEvidence{upsertEvidence("s1", "h1")})
	if err == nil {
		t.Fatal("expected error on caller-supplied ClusterKey")
	}
}

// TestUpsertTribalFactFreshInsert covers the insert (non-merge) path.
func TestUpsertTribalFactFreshInsert(t *testing.T) {
	cdb := tribalDB(t)
	subjectID := insertTestSymbol(t, cdb, "Handler")
	factID, merged, err := cdb.UpsertTribalFact(
		upsertFact(subjectID, "must hold mutex", "quote A"),
		[]TribalEvidence{upsertEvidence("pr/1", "h1")},
	)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if merged {
		t.Fatal("fresh insert reported merged=true")
	}
	if factID <= 0 {
		t.Fatalf("unexpected factID %d", factID)
	}
}

// TestTribalCorroboration covers AC6: two facts with same
// (subject_id, kind, cluster_key) but different source_refs produce a
// single tribal_facts row with corroboration=2 and two evidence rows.
func TestTribalCorroboration(t *testing.T) {
	cdb := tribalDB(t)
	subjectID := insertTestSymbol(t, cdb, "Handler")

	// First fact — normalized form is identical to the second.
	_, merged1, err := cdb.UpsertTribalFact(
		upsertFact(subjectID, "@alice callers must hold the mutex", "quote 1"),
		[]TribalEvidence{upsertEvidence("pr/1", "h1")},
	)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if merged1 {
		t.Fatal("first upsert reported merged")
	}

	// Second fact — different source quote and evidence, but normalized
	// body is identical.
	_, merged2, err := cdb.UpsertTribalFact(
		upsertFact(subjectID, "Callers must hold the mutex.", "quote 2"),
		[]TribalEvidence{upsertEvidence("pr/2", "h2")},
	)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if !merged2 {
		t.Fatal("second upsert did not report merged")
	}

	// Verify exactly one tribal_facts row with corroboration=2.
	var factCount, corroboration int
	if err := cdb.DB().QueryRow(
		`SELECT COUNT(*), MAX(corroboration) FROM tribal_facts WHERE subject_id = ?`,
		subjectID,
	).Scan(&factCount, &corroboration); err != nil {
		t.Fatalf("query facts: %v", err)
	}
	if factCount != 1 {
		t.Errorf("factCount = %d, want 1", factCount)
	}
	if corroboration != 2 {
		t.Errorf("corroboration = %d, want 2", corroboration)
	}

	// Verify two evidence rows.
	var evidenceCount int
	if err := cdb.DB().QueryRow(
		`SELECT COUNT(*) FROM tribal_evidence`,
	).Scan(&evidenceCount); err != nil {
		t.Fatalf("query evidence: %v", err)
	}
	if evidenceCount != 2 {
		t.Errorf("evidenceCount = %d, want 2", evidenceCount)
	}
}

// TestTribalCorroborationIdempotent covers AC7: re-inserting identical
// (subject, kind, cluster_key, source_ref) produces no new evidence row
// and leaves corroboration unchanged.
func TestTribalCorroborationIdempotent(t *testing.T) {
	cdb := tribalDB(t)
	subjectID := insertTestSymbol(t, cdb, "Handler")
	ev := upsertEvidence("pr/42", "h42")
	if _, _, err := cdb.UpsertTribalFact(upsertFact(subjectID, "body", "quote"), []TribalEvidence{ev}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Same source_ref again — should be a no-op on evidence.
	if _, merged, err := cdb.UpsertTribalFact(upsertFact(subjectID, "body", "different quote"), []TribalEvidence{ev}); err != nil {
		t.Fatalf("second upsert: %v", err)
	} else if !merged {
		t.Fatal("expected merged=true on second upsert")
	}
	var evidenceCount, corroboration int
	if err := cdb.DB().QueryRow(`SELECT COUNT(*), MAX(corroboration) FROM tribal_evidence, tribal_facts WHERE tribal_facts.subject_id = ?`, subjectID).Scan(&evidenceCount, &corroboration); err != nil {
		t.Fatalf("query: %v", err)
	}
	if evidenceCount != 1 {
		t.Errorf("evidenceCount = %d, want 1", evidenceCount)
	}
	if corroboration != 1 {
		t.Errorf("corroboration = %d, want 1", corroboration)
	}
}

// TestTribalCorroborationQuoteStability covers AC8: merging preserves the
// original fact's body / source_quote / confidence / model and only
// records the later quote on the new evidence row.
func TestTribalCorroborationQuoteStability(t *testing.T) {
	cdb := tribalDB(t)
	subjectID := insertTestSymbol(t, cdb, "Handler")

	originalBody := "callers must hold the mutex"
	originalQuote := "ORIGINAL QUOTE"
	first := upsertFact(subjectID, originalBody, originalQuote)
	first.Model = "original-model"
	first.Confidence = 0.77

	_, _, err := cdb.UpsertTribalFact(first, []TribalEvidence{upsertEvidence("pr/a", "ha")})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Second write hits the same cluster (body differs only in
	// cosmetic noise) with a completely different quote + model +
	// confidence.
	second := upsertFact(subjectID, "Callers MUST hold the mutex.", "NEW QUOTE")
	second.Model = "overwrite-model"
	second.Confidence = 0.42
	_, merged, err := cdb.UpsertTribalFact(second, []TribalEvidence{upsertEvidence("pr/b", "hb")})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if !merged {
		t.Fatal("second upsert did not report merged")
	}

	// The persisted fact must retain the original body/quote/model/confidence.
	var gotBody, gotQuote, gotModel string
	var gotConfidence float64
	err = cdb.DB().QueryRow(
		`SELECT body, source_quote, COALESCE(model,''), confidence FROM tribal_facts WHERE subject_id = ?`,
		subjectID,
	).Scan(&gotBody, &gotQuote, &gotModel, &gotConfidence)
	if err != nil {
		t.Fatalf("query fact: %v", err)
	}
	if gotBody != originalBody {
		t.Errorf("body = %q, want %q", gotBody, originalBody)
	}
	if gotQuote != originalQuote {
		t.Errorf("source_quote = %q, want %q", gotQuote, originalQuote)
	}
	if gotModel != "original-model" {
		t.Errorf("model = %q, want original-model", gotModel)
	}
	if gotConfidence != 0.77 {
		t.Errorf("confidence = %f, want 0.77", gotConfidence)
	}

	// The new evidence row should exist.
	var evCount int
	if err := cdb.DB().QueryRow(`SELECT COUNT(*) FROM tribal_evidence`).Scan(&evCount); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if evCount != 2 {
		t.Errorf("evidence count = %d, want 2", evCount)
	}
}

// TestTribalCorroborationDifferentKindsDoNotMerge verifies that two facts
// with identical bodies but different kinds stay in separate rows.
func TestTribalCorroborationDifferentKindsDoNotMerge(t *testing.T) {
	cdb := tribalDB(t)
	subjectID := insertTestSymbol(t, cdb, "Handler")

	rationale := upsertFact(subjectID, "body", "q1")
	rationale.Kind = "rationale"
	if _, _, err := cdb.UpsertTribalFact(rationale, []TribalEvidence{upsertEvidence("pr/a", "ha")}); err != nil {
		t.Fatalf("rationale upsert: %v", err)
	}
	invariant := upsertFact(subjectID, "body", "q2")
	invariant.Kind = "invariant"
	if _, _, err := cdb.UpsertTribalFact(invariant, []TribalEvidence{upsertEvidence("pr/b", "hb")}); err != nil {
		t.Fatalf("invariant upsert: %v", err)
	}
	var count int
	if err := cdb.DB().QueryRow(`SELECT COUNT(*) FROM tribal_facts`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("fact count = %d, want 2", count)
	}
}

// TestTribalCorroborationDistinctBodiesDoNotMerge verifies that two facts
// with semantically different bodies (different cluster keys) stay in
// separate rows.
func TestTribalCorroborationDistinctBodiesDoNotMerge(t *testing.T) {
	cdb := tribalDB(t)
	subjectID := insertTestSymbol(t, cdb, "Handler")
	if _, _, err := cdb.UpsertTribalFact(upsertFact(subjectID, "must hold mutex", "q1"), []TribalEvidence{upsertEvidence("pr/a", "ha")}); err != nil {
		t.Fatalf("a: %v", err)
	}
	if _, _, err := cdb.UpsertTribalFact(upsertFact(subjectID, "must release mutex", "q2"), []TribalEvidence{upsertEvidence("pr/b", "hb")}); err != nil {
		t.Fatalf("b: %v", err)
	}
	var count int
	if err := cdb.DB().QueryRow(`SELECT COUNT(*) FROM tribal_facts`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("fact count = %d, want 2", count)
	}
}
