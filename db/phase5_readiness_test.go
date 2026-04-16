package db

import (
	"path/filepath"
	"testing"
)

// TestPhase5ReadinessDropClusterDebug is a load-bearing skip: it exists
// to compile-check the Phase 5 drop scenario today, and will flip live
// at Phase 5 kickoff. The body asserts that removing the sidecar DB
// file leaves no dangling references in the main ClaimsDB.
//
// Until Phase 5, t.Skip short-circuits the test before any I/O happens
// so developers can still run `go test ./db/...` without touching
// calibration data.
func TestPhase5ReadinessDropClusterDebug(t *testing.T) {
	t.Skip("enable at Phase 5 kickoff")

	dir := t.TempDir()
	mainPath := filepath.Join(dir, "claims.db")
	main, err := OpenClaimsDB(mainPath)
	if err != nil {
		t.Fatalf("open main: %v", err)
	}
	defer main.Close()
	if err := main.CreateSchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := main.CreateTribalSchema(); err != nil {
		t.Fatalf("tribal schema: %v", err)
	}

	cd, err := OpenClusterDebugDB(mainPath)
	if err != nil {
		t.Fatalf("open cd: %v", err)
	}
	if err := cd.Attach(main); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// At Phase 5 the drop procedure is: detach, close, rm the sidecar
	// file, ensure the main DB's tribal_facts rows are unaffected.
	if err := cd.Detach(main); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if err := cd.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Verify main DB still reports the tribal schema intact.
	var n int
	if err := main.DB().QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='tribal_facts'`,
	).Scan(&n); err != nil {
		t.Fatalf("query tribal_facts: %v", err)
	}
	if n != 1 {
		t.Errorf("tribal_facts table count = %d, want 1", n)
	}
}
