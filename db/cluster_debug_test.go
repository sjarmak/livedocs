package db

import (
	"bytes"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// freshClusterDebugPair creates an on-disk main ClaimsDB + sidecar
// ClusterDebugDB pair, attaches the sidecar, and returns both. The
// ClaimsDB is freshly initialised with both core and tribal schemas.
func freshClusterDebugPair(t *testing.T) (*ClaimsDB, *ClusterDebugDB, string) {
	t.Helper()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "claims.db")
	main, err := OpenClaimsDB(mainPath)
	if err != nil {
		t.Fatalf("open main: %v", err)
	}
	if err := main.CreateSchema(); err != nil {
		t.Fatalf("create main schema: %v", err)
	}
	if err := main.CreateTribalSchema(); err != nil {
		t.Fatalf("create tribal schema: %v", err)
	}
	cd, err := OpenClusterDebugDB(mainPath)
	if err != nil {
		t.Fatalf("open cluster debug: %v", err)
	}
	if err := cd.Attach(main); err != nil {
		t.Fatalf("attach: %v", err)
	}
	main.EnableClusterDebug(cd)
	t.Cleanup(func() {
		main.DisableClusterDebug()
		_ = cd.Detach(main)
		_ = cd.Close()
		_ = main.Close()
	})
	return main, cd, mainPath
}

// TestClusterDebugAttachDetach covers the basic lifecycle.
func TestClusterDebugAttachDetach(t *testing.T) {
	main, cd, mainPath := freshClusterDebugPair(t)
	wantPath := mainPath + ".cluster-debug.db"
	if cd.Path() != wantPath {
		t.Errorf("Path() = %q, want %q", cd.Path(), wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("stat sidecar file: %v", err)
	}
	// Detach and re-attach to verify Attach is idempotent-ish.
	if err := cd.Detach(main); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if err := cd.Attach(main); err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	// Calling Attach twice is tolerated as a no-op.
	if err := cd.Attach(main); err != nil {
		t.Fatalf("double-attach: %v", err)
	}
}

// TestClusterDebugExpiryEnforcement covers AC11: refuse to attach if any
// row is past expiry unless ExtendExpiryDays > 0, in which case a boot
// warning is logged.
func TestClusterDebugExpiryEnforcement(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "claims.db")

	// Create the sidecar and manually insert a row with expires_at in the past.
	cd1, err := OpenClusterDebugDB(mainPath)
	if err != nil {
		t.Fatalf("open cd1: %v", err)
	}
	if _, err := cd1.db.Exec(
		`INSERT INTO cluster_debug (fact_id, cluster_key, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		1, "k", 100, 100,
	); err != nil {
		t.Fatalf("insert past-expiry row: %v", err)
	}
	cd1.Close()

	// Re-opening with default options should fail.
	_, err = OpenClusterDebugDB(mainPath)
	if err == nil {
		t.Fatal("expected error reopening with past-expiry rows")
	}
	if !strings.Contains(err.Error(), "past expiry") {
		t.Errorf("error = %v, want substring 'past expiry'", err)
	}

	// Re-opening with ExtendExpiryDays > 0 should succeed AND log a warning.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cd2, err := OpenClusterDebugDBWithOpts(mainPath, ClusterDebugOpts{ExtendExpiryDays: 30})
	if err != nil {
		t.Fatalf("open with extend: %v", err)
	}
	defer cd2.Close()
	if !strings.Contains(buf.String(), "expiry extended") {
		t.Errorf("expected log warning about expiry extension, got: %s", buf.String())
	}
}

// TestClusterDebugSizeCeiling covers AC12: UpsertTribalFact refuses to
// insert into the sidecar when row count exceeds the per-handle cap.
func TestClusterDebugSizeCeiling(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "claims.db")
	main, err := OpenClaimsDB(mainPath)
	if err != nil {
		t.Fatalf("open main: %v", err)
	}
	if err := main.CreateSchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := main.CreateTribalSchema(); err != nil {
		t.Fatalf("tribal schema: %v", err)
	}
	defer main.Close()

	// MaxRows=1 so the second upsert hits the ceiling.
	cd, err := OpenClusterDebugDBWithOpts(mainPath, ClusterDebugOpts{MaxRows: 1})
	if err != nil {
		t.Fatalf("open cd: %v", err)
	}
	defer cd.Close()
	if err := cd.Attach(main); err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer cd.Detach(main)
	main.EnableClusterDebug(cd)
	defer main.DisableClusterDebug()

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	subjectID := insertTestSymbol(t, main, "Ceiling")
	if _, _, err := main.UpsertTribalFact(upsertFact(subjectID, "body one", "q"), []TribalEvidence{upsertEvidence("pr/1", "h1")}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Row cap of 1 reached; a second upsert must not insert another row.
	if _, _, err := main.UpsertTribalFact(upsertFact(subjectID, "body two", "q"), []TribalEvidence{upsertEvidence("pr/2", "h2")}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	// Only one row should live in the sidecar.
	n, err := cd.RowCount()
	if err != nil {
		t.Fatalf("row count: %v", err)
	}
	if n != 1 {
		t.Errorf("row count = %d, want 1", n)
	}
	if !strings.Contains(buf.String(), "row cap reached") {
		t.Errorf("expected row-cap warning in log, got: %s", buf.String())
	}
}

// TestClusterDebugDanglingSweep covers AC13 sweep: rows referencing a
// deleted fact should have their fact_id / nearest_body_match_id nulled.
func TestClusterDebugDanglingSweep(t *testing.T) {
	main, cd, _ := freshClusterDebugPair(t)

	subjectID := insertTestSymbol(t, main, "Sweep")
	factID, _, err := main.UpsertTribalFact(
		upsertFact(subjectID, "body one", "q1"),
		[]TribalEvidence{upsertEvidence("pr/1", "h1")},
	)
	if err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	// Insert a second fact so the first has a nearest-match candidate.
	factID2, _, err := main.UpsertTribalFact(
		upsertFact(subjectID, "body two", "q2"),
		[]TribalEvidence{upsertEvidence("pr/2", "h2")},
	)
	if err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	// Manually write a sidecar row that references both fact IDs so we
	// can delete one and watch the sweep clean up.
	if _, err := cd.db.Exec(
		`INSERT INTO cluster_debug (fact_id, cluster_key, nearest_body_match_id, body_token_jaccard) VALUES (?, ?, ?, ?)`,
		factID2, "manual-key", factID, 0.5,
	); err != nil {
		t.Fatalf("insert sweep row: %v", err)
	}

	// Delete the first fact directly from tribal_facts (bypassing any
	// corrections layer) to simulate a Phase 5 drop.
	if _, err := main.DB().Exec(`DELETE FROM tribal_evidence WHERE fact_id = ?`, factID); err != nil {
		t.Fatalf("delete evidence: %v", err)
	}
	if _, err := main.DB().Exec(`DELETE FROM tribal_facts WHERE id = ?`, factID); err != nil {
		t.Fatalf("delete fact: %v", err)
	}

	if err := cd.SweepDanglingFactIDs(main); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	rows, err := cd.db.Query(`SELECT fact_id, nearest_body_match_id FROM cluster_debug`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var factIDRow, nearest sql.NullInt64
		if err := rows.Scan(&factIDRow, &nearest); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++
		// Only rows that referenced the deleted fact should have nulls.
		if nearest.Valid && nearest.Int64 == factID {
			t.Errorf("nearest_body_match_id still references deleted fact %d", factID)
		}
	}
	if seen == 0 {
		t.Fatal("expected at least one cluster_debug row post-sweep")
	}
}

// TestClusterDebugSchema verifies the on-disk schema matches the PRD.
func TestClusterDebugSchema(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "claims.db")
	cd, err := OpenClusterDebugDB(mainPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer cd.Close()

	rows, err := cd.db.Query(`PRAGMA table_info(cluster_debug)`)
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	defer rows.Close()
	expected := map[string]bool{
		"fact_id":               false,
		"cluster_key":           false,
		"nearest_body_match_id": false,
		"body_token_jaccard":    false,
		"expires_at":            false,
		"created_at":            false,
	}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := expected[name]; ok {
			expected[name] = true
		} else {
			t.Errorf("unexpected column: %s", name)
		}
	}
	for name, seen := range expected {
		if !seen {
			t.Errorf("missing column: %s", name)
		}
	}
}

// TestClusterDebugForeignKeysPragma guards AC16.
func TestClusterDebugForeignKeysPragma(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "claims.db")
	cd, err := OpenClusterDebugDB(mainPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer cd.Close()
	var fk int
	if err := cd.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("query pragma: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

// TestClusterDebugWritesOnUpsert verifies the happy path: a successful
// upsert inserts a calibration row into the sidecar.
func TestClusterDebugWritesOnUpsert(t *testing.T) {
	main, cd, _ := freshClusterDebugPair(t)
	subjectID := insertTestSymbol(t, main, "Writes")

	if _, _, err := main.UpsertTribalFact(
		upsertFact(subjectID, "first body", "q1"),
		[]TribalEvidence{upsertEvidence("pr/1", "h1")},
	); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	if _, _, err := main.UpsertTribalFact(
		upsertFact(subjectID, "second body", "q2"),
		[]TribalEvidence{upsertEvidence("pr/2", "h2")},
	); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	n, err := cd.RowCount()
	if err != nil {
		t.Fatalf("row count: %v", err)
	}
	if n != 2 {
		t.Errorf("row count = %d, want 2", n)
	}
	pairs, err := cd.ListDebugPairs(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pairs) != 2 {
		t.Errorf("pairs = %d, want 2", len(pairs))
	}
}
