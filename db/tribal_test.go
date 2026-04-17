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
		// Phase 3: cluster key enables deterministic clustering of tribal
		// facts sharing a subject/kind.
		"cluster_key": false,
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

	// Verify tribal_feedback table exists with correct columns.
	rows4, err := db.DB().Query("PRAGMA table_info(tribal_feedback)")
	if err != nil {
		t.Fatalf("pragma tribal_feedback: %v", err)
	}
	defer rows4.Close()

	feedbackCols := map[string]bool{
		"id": false, "fact_id": false, "reason": false,
		"details": false, "reporter": false, "created_at": false,
	}
	for rows4.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt interface{}
		if err := rows4.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if _, ok := feedbackCols[name]; ok {
			feedbackCols[name] = true
		} else {
			t.Errorf("unexpected column in tribal_feedback: %s", name)
		}
	}
	for col, found := range feedbackCols {
		if !found {
			t.Errorf("missing column in tribal_feedback: %s", col)
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

// TestTribalPhase3Migration verifies the Phase 3 additive migration:
//   - tribal_facts gains cluster_key TEXT NOT NULL DEFAULT ” on pre-Phase-3 DBs.
//   - source_files gains last_pr_id_set BLOB and pr_miner_version TEXT DEFAULT ”.
//   - idx_tribal_facts_cluster exists on (subject_id, kind, cluster_key).
//   - The migration is idempotent — running CreateTribalSchema twice is a no-op.
//   - PRAGMA foreign_keys = ON is honored on the handle returned by OpenClaimsDB.
func TestTribalPhase3Migration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phase3.db")
	db, err := OpenClaimsDB(path)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Simulate a pre-Phase-3 core schema: create the tables without the
	// Phase 3 columns, matching the shape callers would see if they had
	// extracted against an older live_docs build.
	legacyCore := `
CREATE TABLE IF NOT EXISTS symbols (
    id              INTEGER PRIMARY KEY,
    repo            TEXT NOT NULL,
    import_path     TEXT NOT NULL,
    symbol_name     TEXT NOT NULL,
    language        TEXT NOT NULL,
    kind            TEXT NOT NULL,
    visibility      TEXT NOT NULL DEFAULT 'public'
                    CHECK(visibility IN ('public', 'internal', 'private', 're-exported', 'conditional')),
    display_name    TEXT,
    scip_symbol     TEXT,
    UNIQUE(repo, import_path, symbol_name)
);
CREATE TABLE IF NOT EXISTS source_files (
    id              INTEGER PRIMARY KEY,
    repo            TEXT NOT NULL,
    relative_path   TEXT NOT NULL,
    content_hash    TEXT NOT NULL,
    extractor_version TEXT NOT NULL,
    grammar_version TEXT,
    last_indexed    TEXT NOT NULL,
    deleted         INTEGER NOT NULL DEFAULT 0,
    UNIQUE(repo, relative_path)
);
CREATE TABLE IF NOT EXISTS tribal_facts (
    id                INTEGER PRIMARY KEY,
    subject_id        INTEGER NOT NULL REFERENCES symbols(id),
    kind              TEXT NOT NULL CHECK(kind IN ('ownership','rationale','invariant','quirk','todo','deprecation')),
    body              TEXT NOT NULL,
    source_quote      TEXT NOT NULL,
    confidence        REAL NOT NULL,
    corroboration     INTEGER NOT NULL DEFAULT 1,
    extractor         TEXT NOT NULL,
    extractor_version TEXT NOT NULL,
    model             TEXT,
    staleness_hash    TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stale','quarantined','superseded','deleted')),
    created_at        TEXT NOT NULL,
    last_verified     TEXT NOT NULL
);
`
	if _, err := db.DB().Exec(legacyCore); err != nil {
		t.Fatalf("seed legacy core schema: %v", err)
	}

	// Sanity check: the legacy DB must NOT yet have Phase 3 columns.
	if hasColumn(t, db, "tribal_facts", "cluster_key") {
		t.Fatal("legacy seed unexpectedly already has cluster_key")
	}
	if hasColumn(t, db, "source_files", "last_pr_id_set") {
		t.Fatal("legacy seed unexpectedly already has last_pr_id_set")
	}

	// Run the migration via CreateTribalSchema.
	if err := db.CreateTribalSchema(); err != nil {
		t.Fatalf("create tribal schema (migration): %v", err)
	}

	// Post-migration assertions: new columns exist with the expected types.
	assertColumnType(t, db, "tribal_facts", "cluster_key", "TEXT", true, "''")
	assertColumnType(t, db, "source_files", "last_pr_id_set", "BLOB", false, "")
	assertColumnType(t, db, "source_files", "pr_miner_version", "TEXT", false, "''")

	// Index exists and covers (subject_id, kind, cluster_key).
	assertIndexExists(t, db, "idx_tribal_facts_cluster")
	indexedCols := indexColumns(t, db, "idx_tribal_facts_cluster")
	wantCols := []string{"subject_id", "kind", "cluster_key"}
	if len(indexedCols) != len(wantCols) {
		t.Fatalf("idx_tribal_facts_cluster columns: got %v, want %v", indexedCols, wantCols)
	}
	for i, c := range wantCols {
		if indexedCols[i] != c {
			t.Errorf("idx_tribal_facts_cluster column %d: got %q, want %q", i, indexedCols[i], c)
		}
	}

	// Idempotency: a second run must succeed and produce identical schema.
	beforeMaster := snapshotSqliteMaster(t, db)
	if err := db.CreateTribalSchema(); err != nil {
		t.Fatalf("second CreateTribalSchema: %v", err)
	}
	afterMaster := snapshotSqliteMaster(t, db)
	if beforeMaster != afterMaster {
		t.Errorf("schema changed on second CreateTribalSchema run\nbefore:\n%s\nafter:\n%s", beforeMaster, afterMaster)
	}

	// PRAGMA foreign_keys must be ON on the open handle.
	var fk int
	if err := db.DB().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("query pragma foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys pragma: got %d, want 1", fk)
	}
}

// hasColumn reports whether table has a column with the given name.
func hasColumn(t *testing.T, db *ClaimsDB, table, column string) bool {
	t.Helper()
	rows, err := db.DB().Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan pragma row: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}

// assertColumnType verifies that (table, column) has the expected declared
// type, NOT NULL flag, and default expression. An empty wantDefault string
// means "do not assert on default".
func assertColumnType(t *testing.T, db *ClaimsDB, table, column, wantType string, wantNotNull bool, wantDefault string) {
	t.Helper()
	rows, err := db.DB().Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan pragma row: %v", err)
		}
		if name != column {
			continue
		}
		if typ != wantType {
			t.Errorf("%s.%s type: got %q, want %q", table, column, typ, wantType)
		}
		gotNotNull := notnull == 1
		if gotNotNull != wantNotNull {
			t.Errorf("%s.%s notnull: got %v, want %v", table, column, gotNotNull, wantNotNull)
		}
		if wantDefault != "" {
			gotDefault, _ := dflt.(string)
			if gotDefault != wantDefault {
				t.Errorf("%s.%s default: got %q, want %q", table, column, gotDefault, wantDefault)
			}
		}
		return
	}
	t.Errorf("%s.%s not found", table, column)
}

// assertIndexExists verifies the index is registered in sqlite_master.
func assertIndexExists(t *testing.T, db *ClaimsDB, name string) {
	t.Helper()
	var count int
	if err := db.DB().QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", name,
	).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master for %s: %v", name, err)
	}
	if count != 1 {
		t.Errorf("index %s: expected 1 row in sqlite_master, got %d", name, count)
	}
}

// indexColumns returns the ordered list of column names an index covers
// via PRAGMA index_info.
func indexColumns(t *testing.T, db *ClaimsDB, index string) []string {
	t.Helper()
	rows, err := db.DB().Query("PRAGMA index_info(" + index + ")")
	if err != nil {
		t.Fatalf("pragma index_info(%s): %v", index, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			t.Fatalf("scan pragma index_info row: %v", err)
		}
		cols = append(cols, name)
	}
	return cols
}

// snapshotSqliteMaster returns a deterministic dump of the non-transient
// sqlite_master rows, used to prove the migration is idempotent.
func snapshotSqliteMaster(t *testing.T, db *ClaimsDB) string {
	t.Helper()
	rows, err := db.DB().Query(`
		SELECT type, name, tbl_name, IFNULL(sql, '')
		FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%'
		ORDER BY type, name
	`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()
	var out string
	for rows.Next() {
		var typ, name, tbl, sqlText string
		if err := rows.Scan(&typ, &name, &tbl, &sqlText); err != nil {
			t.Fatalf("scan sqlite_master row: %v", err)
		}
		out += typ + "|" + name + "|" + tbl + "|" + sqlText + "\n"
	}
	return out
}

func TestInsertTribalFeedback(t *testing.T) {
	cdb := tribalDB(t)
	subjectID := insertTestSymbol(t, cdb, "FeedbackTarget")

	// Insert a fact to attach feedback to.
	factID, err := cdb.InsertTribalFact(TribalFact{
		SubjectID:        subjectID,
		Kind:             "invariant",
		Body:             "test fact",
		SourceQuote:      "test quote",
		Confidence:       0.9,
		Corroboration:    1,
		Extractor:        "test",
		ExtractorVersion: "v1",
		StalenessHash:    "hash",
		Status:           "active",
		CreatedAt:        "2025-01-01T00:00:00Z",
		LastVerified:     "2025-01-01T00:00:00Z",
	}, []TribalEvidence{{
		SourceType:  "commit_msg",
		SourceRef:   "ref",
		ContentHash: "hash",
	}})
	if err != nil {
		t.Fatalf("insert fact: %v", err)
	}

	// Insert feedback.
	fbID, err := cdb.InsertTribalFeedback(TribalFeedback{
		FactID:    factID,
		Reason:    "wrong",
		Details:   "this is incorrect",
		Reporter:  "test-user",
		CreatedAt: "2025-02-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("insert feedback: %v", err)
	}
	if fbID == 0 {
		t.Error("expected non-zero feedback ID")
	}

	// Query feedback.
	fbs, err := cdb.GetTribalFeedbackByFact(factID)
	if err != nil {
		t.Fatalf("get feedback: %v", err)
	}
	if len(fbs) != 1 {
		t.Fatalf("expected 1 feedback row, got %d", len(fbs))
	}
	if fbs[0].Reason != "wrong" {
		t.Errorf("expected reason 'wrong', got %q", fbs[0].Reason)
	}
	if fbs[0].Details != "this is incorrect" {
		t.Errorf("expected details 'this is incorrect', got %q", fbs[0].Details)
	}
	if fbs[0].Reporter != "test-user" {
		t.Errorf("expected reporter 'test-user', got %q", fbs[0].Reporter)
	}
}

func TestInsertTribalFeedback_InvalidReason(t *testing.T) {
	cdb := tribalDB(t)

	_, err := cdb.InsertTribalFeedback(TribalFeedback{
		FactID:    1,
		Reason:    "invalid",
		Reporter:  "test",
		CreatedAt: "2025-02-01T00:00:00Z",
	})
	if err == nil {
		t.Fatal("expected error for invalid reason")
	}
}

func TestInsertTribalFeedback_MissingReporter(t *testing.T) {
	cdb := tribalDB(t)

	_, err := cdb.InsertTribalFeedback(TribalFeedback{
		FactID:    1,
		Reason:    "wrong",
		Reporter:  "",
		CreatedAt: "2025-02-01T00:00:00Z",
	})
	if err == nil {
		t.Fatal("expected error for missing reporter")
	}
}

func TestGetS4GateStats(t *testing.T) {
	cdb := tribalDB(t)
	subjectID := insertTestSymbol(t, cdb, "S4Target")

	// Insert two facts.
	factID1, err := cdb.InsertTribalFact(TribalFact{
		SubjectID: subjectID, Kind: "invariant", Body: "fact 1", SourceQuote: "q1",
		Confidence: 0.9, Corroboration: 1, Extractor: "test", ExtractorVersion: "v1",
		StalenessHash: "h1", Status: "active", CreatedAt: "2025-01-01T00:00:00Z", LastVerified: "2025-01-01T00:00:00Z",
	}, []TribalEvidence{{SourceType: "commit_msg", SourceRef: "r1", ContentHash: "h1"}})
	if err != nil {
		t.Fatalf("insert fact 1: %v", err)
	}

	factID2, err := cdb.InsertTribalFact(TribalFact{
		SubjectID: subjectID, Kind: "rationale", Body: "fact 2", SourceQuote: "q2",
		Confidence: 0.8, Corroboration: 1, Extractor: "test", ExtractorVersion: "v1",
		StalenessHash: "h2", Status: "active", CreatedAt: "2025-01-01T00:00:00Z", LastVerified: "2025-01-01T00:00:00Z",
	}, []TribalEvidence{{SourceType: "commit_msg", SourceRef: "r2", ContentHash: "h2"}})
	if err != nil {
		t.Fatalf("insert fact 2: %v", err)
	}

	// Add feedback.
	_, err = cdb.InsertTribalFeedback(TribalFeedback{FactID: factID1, Reason: "wrong", Reporter: "u1", CreatedAt: "2025-02-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("insert feedback: %v", err)
	}
	_, err = cdb.InsertTribalFeedback(TribalFeedback{FactID: factID1, Reason: "stale", Reporter: "u2", CreatedAt: "2025-02-02T00:00:00Z"})
	if err != nil {
		t.Fatalf("insert feedback: %v", err)
	}

	// Add a correction.
	_, err = cdb.InsertTribalCorrection(TribalCorrection{FactID: factID2, Action: "delete", Reason: "obsolete", Actor: "admin", CreatedAt: "2025-02-03T00:00:00Z"})
	if err != nil {
		t.Fatalf("insert correction: %v", err)
	}

	stats, err := cdb.GetS4GateStats()
	if err != nil {
		t.Fatalf("get S4 gate stats: %v", err)
	}

	if stats.TotalFeedback != 2 {
		t.Errorf("expected 2 total feedback, got %d", stats.TotalFeedback)
	}
	if stats.WrongReports != 1 {
		t.Errorf("expected 1 wrong report, got %d", stats.WrongReports)
	}
	if stats.StaleReports != 1 {
		t.Errorf("expected 1 stale report, got %d", stats.StaleReports)
	}
	if stats.TotalCorrections != 1 {
		t.Errorf("expected 1 total correction, got %d", stats.TotalCorrections)
	}
	if stats.DeleteCorrections != 1 {
		t.Errorf("expected 1 delete correction, got %d", stats.DeleteCorrections)
	}
	if stats.TotalLabeledFacts != 2 {
		t.Errorf("expected 2 labeled facts, got %d", stats.TotalLabeledFacts)
	}
	// Hallucination rate: (1 wrong + 1 delete) / 2 = 1.0
	if stats.HallucinationRate != 1.0 {
		t.Errorf("expected hallucination rate 1.0, got %f", stats.HallucinationRate)
	}
}
