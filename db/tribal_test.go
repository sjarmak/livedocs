package db

import (
	"path/filepath"
	"testing"
)

// tribalDB creates a fresh DB with both core and tribal schemas.
func tribalDB(t *testing.T) *ClaimsDB {
	t.Helper()
	db := tempDB(t) // creates core schema
	if err := db.CreateTribalSchema(); err != nil {
		t.Fatalf("create tribal schema: %v", err)
	}
	return db
}

// insertTestSymbol inserts a symbol and returns its ID.
func insertTestSymbol(t *testing.T, db *ClaimsDB, name string) int64 {
	t.Helper()
	id, err := db.UpsertSymbol(Symbol{
		Repo:       "test/repo",
		ImportPath: "test/pkg",
		SymbolName: name,
		Language:   "go",
		Kind:       "function",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("insert test symbol %s: %v", name, err)
	}
	return id
}

func TestTribalSchema(t *testing.T) {
	db := tribalDB(t)

	// Verify tribal_facts table exists with correct columns.
	rows, err := db.DB().Query("PRAGMA table_info(tribal_facts)")
	if err != nil {
		t.Fatalf("pragma tribal_facts: %v", err)
	}
	defer rows.Close()

	expectedCols := map[string]bool{
		"id": false, "subject_id": false, "kind": false, "body": false,
		"source_quote": false, "confidence": false, "corroboration": false,
		"extractor": false, "extractor_version": false, "model": false,
		"staleness_hash": false, "status": false, "created_at": false,
		"last_verified": false,
	}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if _, ok := expectedCols[name]; ok {
			expectedCols[name] = true
		} else {
			t.Errorf("unexpected column in tribal_facts: %s", name)
		}
	}
	for col, found := range expectedCols {
		if !found {
			t.Errorf("missing column in tribal_facts: %s", col)
		}
	}

	// Verify tribal_evidence table exists with correct columns.
	rows2, err := db.DB().Query("PRAGMA table_info(tribal_evidence)")
	if err != nil {
		t.Fatalf("pragma tribal_evidence: %v", err)
	}
	defer rows2.Close()

	evidenceCols := map[string]bool{
		"id": false, "fact_id": false, "source_type": false,
		"source_ref": false, "author": false, "authored_at": false,
		"content_hash": false,
	}
	for rows2.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt interface{}
		if err := rows2.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if _, ok := evidenceCols[name]; ok {
			evidenceCols[name] = true
		} else {
			t.Errorf("unexpected column in tribal_evidence: %s", name)
		}
	}
	for col, found := range evidenceCols {
		if !found {
			t.Errorf("missing column in tribal_evidence: %s", col)
		}
	}

	// Verify tribal_corrections table exists with correct columns.
	rows3, err := db.DB().Query("PRAGMA table_info(tribal_corrections)")
	if err != nil {
		t.Fatalf("pragma tribal_corrections: %v", err)
	}
	defer rows3.Close()

	correctionCols := map[string]bool{
		"id": false, "fact_id": false, "action": false,
		"new_body": false, "reason": false, "actor": false,
		"created_at": false,
	}
	for rows3.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt interface{}
		if err := rows3.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if _, ok := correctionCols[name]; ok {
			correctionCols[name] = true
		} else {
			t.Errorf("unexpected column in tribal_corrections: %s", name)
		}
	}
	for col, found := range correctionCols {
		if !found {
			t.Errorf("missing column in tribal_corrections: %s", col)
		}
	}

	// Idempotency: calling CreateTribalSchema again should not error.
	if err := db.CreateTribalSchema(); err != nil {
		t.Fatalf("create tribal schema twice: %v", err)
	}
}

func TestTribalEvidenceRequired(t *testing.T) {
	db := tribalDB(t)
	subjectID := insertTestSymbol(t, db, "MyFunc")

	fact := TribalFact{
		SubjectID:        subjectID,
		Kind:             "ownership",
		Body:             "owned by team-x",
		SourceQuote:      "// OWNER: team-x",
		Confidence:       0.9,
		Corroboration:    1,
		Extractor:        "test",
		ExtractorVersion: "1.0",
		StalenessHash:    "abc123",
		Status:           "active",
		CreatedAt:        "2025-01-01T00:00:00Z",
		LastVerified:     "2025-01-01T00:00:00Z",
	}

	// Insert with empty evidence should fail.
	_, err := db.InsertTribalFact(fact, nil)
	if err == nil {
		t.Fatal("expected error when inserting fact without evidence")
	}

	// Verify no fact was inserted (rollback).
	var count int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM tribal_facts").Scan(&count); err != nil {
		t.Fatalf("count tribal_facts: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 tribal_facts after rollback, got %d", count)
	}

	// Also verify with empty slice.
	_, err = db.InsertTribalFact(fact, []TribalEvidence{})
	if err == nil {
		t.Fatal("expected error when inserting fact with empty evidence slice")
	}
}

func TestTribalSchemaBackwardCompatible(t *testing.T) {
	// Create a DB with only the core claims schema (no tribal tables).
	path := filepath.Join(t.TempDir(), "pre-tribal.db")
	db, err := OpenClaimsDB(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create core schema: %v", err)
	}

	// Verify tribal tables do not exist yet.
	var count int
	err = db.DB().QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='tribal_facts'",
	).Scan(&count)
	if err != nil {
		t.Fatalf("check tribal_facts existence: %v", err)
	}
	if count != 0 {
		t.Fatal("tribal_facts should not exist before CreateTribalSchema")
	}

	// Now add tribal schema on top — should succeed without error.
	if err := db.CreateTribalSchema(); err != nil {
		t.Fatalf("create tribal schema on pre-tribal db: %v", err)
	}

	// Verify all three tables now exist.
	for _, table := range []string{"tribal_facts", "tribal_evidence", "tribal_corrections"} {
		err = db.DB().QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&count)
		if err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s should exist after CreateTribalSchema", table)
		}
	}
}

func TestInsertTribalFact_RoundTrip(t *testing.T) {
	db := tribalDB(t)
	subjectID := insertTestSymbol(t, db, "Handler")

	fact := TribalFact{
		SubjectID:        subjectID,
		Kind:             "rationale",
		Body:             "uses mutex because of legacy race condition",
		SourceQuote:      "// Added mutex in PR #42 to fix data race",
		Confidence:       0.85,
		Corroboration:    2,
		Extractor:        "blame-analyzer",
		ExtractorVersion: "0.1.0",
		Model:            "claude-3",
		StalenessHash:    "hash123",
		Status:           "active",
		CreatedAt:        "2025-06-01T10:00:00Z",
		LastVerified:     "2025-06-01T10:00:00Z",
	}
	evidence := []TribalEvidence{
		{
			SourceType:  "blame",
			SourceRef:   "abc123:main.go:42",
			Author:      "alice",
			AuthoredAt:  "2024-01-15T09:00:00Z",
			ContentHash: "ev_hash_1",
		},
		{
			SourceType:  "commit_msg",
			SourceRef:   "abc123",
			Author:      "alice",
			AuthoredAt:  "2024-01-15T09:00:00Z",
			ContentHash: "ev_hash_2",
		},
	}

	factID, err := db.InsertTribalFact(fact, evidence)
	if err != nil {
		t.Fatalf("insert tribal fact: %v", err)
	}
	if factID == 0 {
		t.Fatal("expected non-zero fact ID")
	}

	// Query back by subject.
	facts, err := db.GetTribalFactsBySubject(subjectID)
	if err != nil {
		t.Fatalf("get facts by subject: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}

	got := facts[0]
	if got.ID != factID {
		t.Errorf("fact ID: got %d, want %d", got.ID, factID)
	}
	if got.SubjectID != subjectID {
		t.Errorf("subject_id: got %d, want %d", got.SubjectID, subjectID)
	}
	if got.Kind != "rationale" {
		t.Errorf("kind: got %q, want %q", got.Kind, "rationale")
	}
	if got.Body != fact.Body {
		t.Errorf("body mismatch")
	}
	if got.SourceQuote != fact.SourceQuote {
		t.Errorf("source_quote mismatch")
	}
	if got.Confidence != 0.85 {
		t.Errorf("confidence: got %f, want 0.85", got.Confidence)
	}
	if got.Corroboration != 2 {
		t.Errorf("corroboration: got %d, want 2", got.Corroboration)
	}
	if got.Model != "claude-3" {
		t.Errorf("model: got %q, want %q", got.Model, "claude-3")
	}
	if got.Status != "active" {
		t.Errorf("status: got %q, want %q", got.Status, "active")
	}

	// Verify evidence was populated.
	if len(got.Evidence) != 2 {
		t.Fatalf("expected 2 evidence rows, got %d", len(got.Evidence))
	}
	if got.Evidence[0].SourceType != "blame" {
		t.Errorf("evidence[0] source_type: got %q, want %q", got.Evidence[0].SourceType, "blame")
	}
	if got.Evidence[0].Author != "alice" {
		t.Errorf("evidence[0] author: got %q, want %q", got.Evidence[0].Author, "alice")
	}
	if got.Evidence[1].SourceType != "commit_msg" {
		t.Errorf("evidence[1] source_type: got %q, want %q", got.Evidence[1].SourceType, "commit_msg")
	}
}

func TestUpdateFactStatus(t *testing.T) {
	db := tribalDB(t)
	subjectID := insertTestSymbol(t, db, "Config")

	fact := TribalFact{
		SubjectID:        subjectID,
		Kind:             "quirk",
		Body:             "config must be loaded before logger",
		SourceQuote:      "// WARNING: order matters here",
		Confidence:       0.95,
		Corroboration:    1,
		Extractor:        "test",
		ExtractorVersion: "1.0",
		StalenessHash:    "xyz",
		Status:           "active",
		CreatedAt:        "2025-01-01T00:00:00Z",
		LastVerified:     "2025-01-01T00:00:00Z",
	}
	evidence := []TribalEvidence{{
		SourceType:  "inline_marker",
		SourceRef:   "config.go:10",
		ContentHash: "hash1",
	}}

	factID, err := db.InsertTribalFact(fact, evidence)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Transition through all valid statuses.
	for _, status := range []string{"stale", "quarantined", "superseded", "deleted", "active"} {
		if err := db.UpdateFactStatus(factID, status); err != nil {
			t.Fatalf("update to %q: %v", status, err)
		}
		facts, err := db.GetTribalFactsBySubject(subjectID)
		if err != nil {
			t.Fatalf("get after update to %q: %v", status, err)
		}
		if len(facts) != 1 || facts[0].Status != status {
			t.Errorf("after update: got status %q, want %q", facts[0].Status, status)
		}
	}

	// Invalid status should error.
	if err := db.UpdateFactStatus(factID, "bogus"); err == nil {
		t.Error("expected error for invalid status")
	}

	// Non-existent fact should error.
	if err := db.UpdateFactStatus(999999, "active"); err == nil {
		t.Error("expected error for non-existent fact")
	}
}

func TestGetTribalFactsBySubject_MultipleFacts(t *testing.T) {
	db := tribalDB(t)
	subjectID := insertTestSymbol(t, db, "Server")

	// Insert three facts for the same subject with different kinds.
	kinds := []string{"ownership", "rationale", "invariant"}
	for i, kind := range kinds {
		fact := TribalFact{
			SubjectID:        subjectID,
			Kind:             kind,
			Body:             "fact " + kind,
			SourceQuote:      "quote " + kind,
			Confidence:       0.8,
			Corroboration:    1,
			Extractor:        "test",
			ExtractorVersion: "1.0",
			StalenessHash:    kind,
			Status:           "active",
			CreatedAt:        "2025-01-01T00:00:00Z",
			LastVerified:     "2025-01-01T00:00:00Z",
		}
		evidence := []TribalEvidence{{
			SourceType:  "blame",
			SourceRef:   "file.go:" + string(rune('1'+i)),
			ContentHash: "h" + kind,
		}}
		if _, err := db.InsertTribalFact(fact, evidence); err != nil {
			t.Fatalf("insert fact %s: %v", kind, err)
		}
	}

	facts, err := db.GetTribalFactsBySubject(subjectID)
	if err != nil {
		t.Fatalf("get facts: %v", err)
	}
	if len(facts) != 3 {
		t.Fatalf("expected 3 facts, got %d", len(facts))
	}

	// Each fact should have exactly 1 evidence row.
	for _, f := range facts {
		if len(f.Evidence) != 1 {
			t.Errorf("fact %d: expected 1 evidence, got %d", f.ID, len(f.Evidence))
		}
	}

	// Verify we got the right kinds back.
	gotKinds := map[string]bool{}
	for _, f := range facts {
		gotKinds[f.Kind] = true
	}
	for _, k := range kinds {
		if !gotKinds[k] {
			t.Errorf("missing kind %q in results", k)
		}
	}
}

func TestGetTribalFactsByKind(t *testing.T) {
	db := tribalDB(t)
	sub1 := insertTestSymbol(t, db, "Func1")
	sub2 := insertTestSymbol(t, db, "Func2")

	// Insert one ownership fact for each subject.
	for _, subID := range []int64{sub1, sub2} {
		fact := TribalFact{
			SubjectID:        subID,
			Kind:             "ownership",
			Body:             "owned by team-a",
			SourceQuote:      "CODEOWNERS",
			Confidence:       1.0,
			Corroboration:    1,
			Extractor:        "test",
			ExtractorVersion: "1.0",
			StalenessHash:    "own",
			Status:           "active",
			CreatedAt:        "2025-01-01T00:00:00Z",
			LastVerified:     "2025-01-01T00:00:00Z",
		}
		evidence := []TribalEvidence{{
			SourceType:  "codeowners",
			SourceRef:   "CODEOWNERS:1",
			ContentHash: "co_hash",
		}}
		if _, err := db.InsertTribalFact(fact, evidence); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// Insert one rationale fact.
	fact := TribalFact{
		SubjectID:        sub1,
		Kind:             "rationale",
		Body:             "designed for extensibility",
		SourceQuote:      "// extensible by design",
		Confidence:       0.7,
		Corroboration:    1,
		Extractor:        "test",
		ExtractorVersion: "1.0",
		StalenessHash:    "rat",
		Status:           "active",
		CreatedAt:        "2025-01-01T00:00:00Z",
		LastVerified:     "2025-01-01T00:00:00Z",
	}
	evidence := []TribalEvidence{{
		SourceType:  "pr_comment",
		SourceRef:   "PR#100",
		ContentHash: "pr_hash",
	}}
	if _, err := db.InsertTribalFact(fact, evidence); err != nil {
		t.Fatalf("insert rationale: %v", err)
	}

	// Query by kind=ownership should return 2.
	ownership, err := db.GetTribalFactsByKind("ownership")
	if err != nil {
		t.Fatalf("get by kind ownership: %v", err)
	}
	if len(ownership) != 2 {
		t.Errorf("expected 2 ownership facts, got %d", len(ownership))
	}

	// Query by kind=rationale should return 1.
	rationale, err := db.GetTribalFactsByKind("rationale")
	if err != nil {
		t.Fatalf("get by kind rationale: %v", err)
	}
	if len(rationale) != 1 {
		t.Errorf("expected 1 rationale fact, got %d", len(rationale))
	}
}

func TestInsertTribalCorrection_RoundTrip(t *testing.T) {
	db := tribalDB(t)
	subjectID := insertTestSymbol(t, db, "CorrectedFunc")

	// Insert a fact to correct.
	fact := TribalFact{
		SubjectID:        subjectID,
		Kind:             "rationale",
		Body:             "original rationale",
		SourceQuote:      "// original quote",
		Confidence:       0.9,
		Corroboration:    1,
		Extractor:        "test",
		ExtractorVersion: "1.0",
		StalenessHash:    "orig_hash",
		Status:           "active",
		CreatedAt:        "2025-01-01T00:00:00Z",
		LastVerified:     "2025-01-01T00:00:00Z",
	}
	evidence := []TribalEvidence{{
		SourceType:  "blame",
		SourceRef:   "file.go:10",
		ContentHash: "ev_hash",
	}}
	factID, err := db.InsertTribalFact(fact, evidence)
	if err != nil {
		t.Fatalf("insert fact: %v", err)
	}

	// Insert a correction.
	correctionID, err := db.InsertTribalCorrection(TribalCorrection{
		FactID:    factID,
		Action:    "correct",
		NewBody:   "updated rationale",
		Reason:    "original was inaccurate",
		Actor:     "alice",
		CreatedAt: "2025-02-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("insert correction: %v", err)
	}
	if correctionID <= 0 {
		t.Fatalf("expected positive correction ID, got %d", correctionID)
	}

	// Verify correction was stored.
	var gotAction, gotNewBody, gotReason, gotActor string
	err = db.DB().QueryRow(
		"SELECT action, new_body, reason, actor FROM tribal_corrections WHERE id = ?",
		correctionID,
	).Scan(&gotAction, &gotNewBody, &gotReason, &gotActor)
	if err != nil {
		t.Fatalf("query correction: %v", err)
	}
	if gotAction != "correct" {
		t.Errorf("action: got %q, want %q", gotAction, "correct")
	}
	if gotNewBody != "updated rationale" {
		t.Errorf("new_body: got %q, want %q", gotNewBody, "updated rationale")
	}
	if gotReason != "original was inaccurate" {
		t.Errorf("reason: got %q, want %q", gotReason, "original was inaccurate")
	}
	if gotActor != "alice" {
		t.Errorf("actor: got %q, want %q", gotActor, "alice")
	}
}

func TestInsertTribalCorrection_InvalidAction(t *testing.T) {
	db := tribalDB(t)

	_, err := db.InsertTribalCorrection(TribalCorrection{
		FactID:    1,
		Action:    "invalid",
		Reason:    "test",
		Actor:     "bob",
		CreatedAt: "2025-01-01T00:00:00Z",
	})
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
}

func TestInsertTribalCorrection_MissingFields(t *testing.T) {
	db := tribalDB(t)

	// Missing reason.
	_, err := db.InsertTribalCorrection(TribalCorrection{
		FactID:    1,
		Action:    "correct",
		Actor:     "bob",
		CreatedAt: "2025-01-01T00:00:00Z",
	})
	if err == nil {
		t.Fatal("expected error for missing reason")
	}

	// Missing actor.
	_, err = db.InsertTribalCorrection(TribalCorrection{
		FactID:    1,
		Action:    "correct",
		Reason:    "test",
		CreatedAt: "2025-01-01T00:00:00Z",
	})
	if err == nil {
		t.Fatal("expected error for missing actor")
	}
}
