package drift

import (
	"path/filepath"
	"testing"

	"github.com/live-docs/live_docs/db"
)

// setupTribalDB creates a temp claims DB with tribal schema and returns it.
func setupTribalDB(t *testing.T) *db.ClaimsDB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.claims.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create core schema: %v", err)
	}
	if err := cdb.CreateTribalSchema(); err != nil {
		t.Fatalf("create tribal schema: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	return cdb
}

// insertSymbol inserts a test symbol and returns its ID.
func insertSymbol(t *testing.T, cdb *db.ClaimsDB, name string) int64 {
	t.Helper()
	id, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       "test/repo",
		ImportPath: "test/pkg",
		SymbolName: name,
		Language:   "go",
		Kind:       "function",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("insert symbol %s: %v", name, err)
	}
	return id
}

// insertFact inserts a tribal fact with one evidence row using the given
// staleness hash and evidence content hash. Returns the fact ID.
func insertFact(t *testing.T, cdb *db.ClaimsDB, subjectID int64, status, stalenessHash, evidenceHash string) int64 {
	t.Helper()
	fact := db.TribalFact{
		SubjectID:        subjectID,
		Kind:             "rationale",
		Body:             "test body",
		SourceQuote:      "test quote",
		Confidence:       0.9,
		Corroboration:    1,
		Extractor:        "test",
		ExtractorVersion: "1.0",
		StalenessHash:    stalenessHash,
		Status:           status,
		CreatedAt:        "2026-01-01T00:00:00Z",
		LastVerified:     "2026-01-01T00:00:00Z",
	}
	evidence := []db.TribalEvidence{
		{
			SourceType:  "blame",
			SourceRef:   "abc123:file.go:10",
			ContentHash: evidenceHash,
		},
	}
	id, err := cdb.InsertTribalFact(fact, evidence)
	if err != nil {
		t.Fatalf("insert tribal fact: %v", err)
	}
	// If the desired status is not 'active' (the default), update it.
	if status != "active" {
		if err := cdb.UpdateFactStatus(id, status); err != nil {
			t.Fatalf("set fact status to %s: %v", status, err)
		}
	}
	return id
}

// countFacts returns the total number of rows in tribal_facts.
func countFacts(t *testing.T, cdb *db.ClaimsDB) int {
	t.Helper()
	var count int
	err := cdb.DB().QueryRow("SELECT COUNT(*) FROM tribal_facts").Scan(&count)
	if err != nil {
		t.Fatalf("count facts: %v", err)
	}
	return count
}

// factStatus returns the current status of a fact by ID.
func factStatus(t *testing.T, cdb *db.ClaimsDB, factID int64) string {
	t.Helper()
	var status string
	err := cdb.DB().QueryRow("SELECT status FROM tribal_facts WHERE id = ?", factID).Scan(&status)
	if err != nil {
		t.Fatalf("get fact status %d: %v", factID, err)
	}
	return status
}

func TestTribalDrift_EvidenceHashChanged_TransitionsToStale(t *testing.T) {
	cdb := setupTribalDB(t)

	symID := insertSymbol(t, cdb, "MyFunc")
	// Fact has staleness_hash "aaa", evidence has content_hash "bbb" (different).
	factID := insertFact(t, cdb, symID, "active", "aaa", "bbb")

	initialCount := countFacts(t, cdb)

	stale, quarantined, err := CheckTribal(cdb)
	if err != nil {
		t.Fatalf("CheckTribal: %v", err)
	}

	if stale != 1 {
		t.Errorf("expected staleCount=1, got %d", stale)
	}
	if quarantined != 0 {
		t.Errorf("expected quarantinedCount=0, got %d", quarantined)
	}
	if factStatus(t, cdb, factID) != "stale" {
		t.Errorf("expected fact status 'stale', got %q", factStatus(t, cdb, factID))
	}

	// Row count unchanged.
	if countFacts(t, cdb) != initialCount {
		t.Errorf("row count changed: was %d, now %d", initialCount, countFacts(t, cdb))
	}
}

func TestTribalDrift_SymbolDeleted_TransitionsToQuarantined(t *testing.T) {
	cdb := setupTribalDB(t)

	symID := insertSymbol(t, cdb, "DoStuff")
	factID := insertFact(t, cdb, symID, "active", "aaa", "aaa")

	// Delete the symbol to simulate it disappearing from the codebase.
	_, err := cdb.DB().Exec("DELETE FROM symbols WHERE id = ?", symID)
	if err != nil {
		t.Fatalf("delete symbol: %v", err)
	}

	initialCount := countFacts(t, cdb)

	stale, quarantined, err := CheckTribal(cdb)
	if err != nil {
		t.Fatalf("CheckTribal: %v", err)
	}

	if stale != 0 {
		t.Errorf("expected staleCount=0, got %d", stale)
	}
	if quarantined != 1 {
		t.Errorf("expected quarantinedCount=1, got %d", quarantined)
	}
	if factStatus(t, cdb, factID) != "quarantined" {
		t.Errorf("expected fact status 'quarantined', got %q", factStatus(t, cdb, factID))
	}

	// Row count unchanged.
	if countFacts(t, cdb) != initialCount {
		t.Errorf("row count changed: was %d, now %d", initialCount, countFacts(t, cdb))
	}
}

func TestTribalDrift_StaleFactStaysStale(t *testing.T) {
	cdb := setupTribalDB(t)

	symID := insertSymbol(t, cdb, "Handler")
	// Fact is already 'stale', evidence hash differs from staleness_hash.
	factID := insertFact(t, cdb, symID, "stale", "aaa", "ccc")

	stale, quarantined, err := CheckTribal(cdb)
	if err != nil {
		t.Fatalf("CheckTribal: %v", err)
	}

	// Should NOT count as a new stale transition.
	if stale != 0 {
		t.Errorf("expected staleCount=0, got %d", stale)
	}
	if quarantined != 0 {
		t.Errorf("expected quarantinedCount=0, got %d", quarantined)
	}
	if factStatus(t, cdb, factID) != "stale" {
		t.Errorf("expected fact status 'stale', got %q", factStatus(t, cdb, factID))
	}
}

func TestTribalDrift_SupersededAndDeletedNotTouched(t *testing.T) {
	cdb := setupTribalDB(t)

	symID := insertSymbol(t, cdb, "Legacy")

	// Create facts in 'superseded' and 'deleted' states with mismatched hashes.
	supID := insertFact(t, cdb, symID, "superseded", "aaa", "zzz")
	delID := insertFact(t, cdb, symID, "deleted", "aaa", "zzz")

	stale, quarantined, err := CheckTribal(cdb)
	if err != nil {
		t.Fatalf("CheckTribal: %v", err)
	}

	if stale != 0 {
		t.Errorf("expected staleCount=0, got %d", stale)
	}
	if quarantined != 0 {
		t.Errorf("expected quarantinedCount=0, got %d", quarantined)
	}

	// Statuses unchanged.
	if factStatus(t, cdb, supID) != "superseded" {
		t.Errorf("superseded fact changed to %q", factStatus(t, cdb, supID))
	}
	if factStatus(t, cdb, delID) != "deleted" {
		t.Errorf("deleted fact changed to %q", factStatus(t, cdb, delID))
	}
}

func TestTribalDrift_ActiveFactWithMatchingHash_NoTransition(t *testing.T) {
	cdb := setupTribalDB(t)

	symID := insertSymbol(t, cdb, "Stable")
	factID := insertFact(t, cdb, symID, "active", "aaa", "aaa")

	stale, quarantined, err := CheckTribal(cdb)
	if err != nil {
		t.Fatalf("CheckTribal: %v", err)
	}

	if stale != 0 {
		t.Errorf("expected staleCount=0, got %d", stale)
	}
	if quarantined != 0 {
		t.Errorf("expected quarantinedCount=0, got %d", quarantined)
	}
	if factStatus(t, cdb, factID) != "active" {
		t.Errorf("expected fact status 'active', got %q", factStatus(t, cdb, factID))
	}
}

func TestTribalDrift_StaleFactWithDeletedSymbol_Quarantined(t *testing.T) {
	cdb := setupTribalDB(t)

	symID := insertSymbol(t, cdb, "Obsolete")
	factID := insertFact(t, cdb, symID, "stale", "aaa", "bbb")

	// Delete the symbol.
	_, err := cdb.DB().Exec("DELETE FROM symbols WHERE id = ?", symID)
	if err != nil {
		t.Fatalf("delete symbol: %v", err)
	}

	stale, quarantined, err := CheckTribal(cdb)
	if err != nil {
		t.Fatalf("CheckTribal: %v", err)
	}

	if stale != 0 {
		t.Errorf("expected staleCount=0, got %d", stale)
	}
	if quarantined != 1 {
		t.Errorf("expected quarantinedCount=1, got %d", quarantined)
	}
	if factStatus(t, cdb, factID) != "quarantined" {
		t.Errorf("expected fact status 'quarantined', got %q", factStatus(t, cdb, factID))
	}
}

func TestTribalDrift_EmptyDB_NoCrash(t *testing.T) {
	cdb := setupTribalDB(t)

	stale, quarantined, err := CheckTribal(cdb)
	if err != nil {
		t.Fatalf("CheckTribal on empty DB: %v", err)
	}
	if stale != 0 || quarantined != 0 {
		t.Errorf("expected 0/0, got stale=%d quarantined=%d", stale, quarantined)
	}
}
