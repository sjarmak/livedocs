package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/live-docs/live_docs/db"
)

func TestVerifyClaimsCommandRegistered(t *testing.T) {
	registered := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		registered[cmd.Name()] = true
	}
	if !registered["verify-claims"] {
		t.Errorf("subcommand 'verify-claims' not registered on root command")
	}
}

// setupTestDB creates a temporary claims DB with schema and returns the path and cleanup func.
func setupTestDB(t *testing.T) (string, *db.ClaimsDB) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.claims.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return dbPath, cdb
}

// insertTestClaim inserts a symbol and claim into the DB and creates the source file.
func insertTestClaim(t *testing.T, cdb *db.ClaimsDB, repoDir, fileName, symbolName, predicate string, line int, lastVerified string) {
	t.Helper()

	symID, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       "test/repo",
		ImportPath: "test/pkg",
		SymbolName: symbolName,
		Language:   "go",
		Kind:       "func",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}

	_, err = cdb.InsertClaim(db.Claim{
		SubjectID:        symID,
		Predicate:        predicate,
		SourceFile:       fileName,
		SourceLine:       line,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "test-extractor",
		ExtractorVersion: "0.1.0",
		LastVerified:     lastVerified,
	})
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}

	// Create the source file if it doesn't exist.
	absFile := fileName
	if !filepath.IsAbs(fileName) {
		absFile = filepath.Join(repoDir, fileName)
	}
	if err := os.MkdirAll(filepath.Dir(absFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := os.Stat(absFile); os.IsNotExist(err) {
		content := "package main\n\nfunc " + symbolName + "() {}\n"
		if err := os.WriteFile(absFile, []byte(content), 0o644); err != nil {
			t.Fatalf("write source file: %v", err)
		}
	}
}

func TestVerifyClaimsBasicVerify_NoClaims(t *testing.T) {
	dbPath, cdb := setupTestDB(t)
	cdb.Close()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--db", dbPath, t.TempDir()})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "OK: no claims") {
		t.Errorf("expected 'OK: no claims' in output, got: %q", buf.String())
	}
}

func TestVerifyClaimsBasicVerify_AllMatch(t *testing.T) {
	repoDir := t.TempDir()
	dbPath, cdb := setupTestDB(t)

	// Use a future last_verified so source file mtime won't be newer.
	futureTime := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	insertTestClaim(t, cdb, repoDir, filepath.Join(repoDir, "main.go"), "Foo", "defines", 3, futureTime)
	cdb.Close()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--db", dbPath, repoDir})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "OK:") {
		t.Errorf("expected 'OK:' in output, got: %q", buf.String())
	}
}

func TestVerifyClaimsBasicVerify_DriftDetected(t *testing.T) {
	repoDir := t.TempDir()
	dbPath, cdb := setupTestDB(t)

	// Create claim pointing to a file that won't exist.
	symID, _ := cdb.UpsertSymbol(db.Symbol{
		Repo:       "test/repo",
		ImportPath: "test/pkg",
		SymbolName: "Missing",
		Language:   "go",
		Kind:       "func",
		Visibility: "public",
	})
	_, _ = cdb.InsertClaim(db.Claim{
		SubjectID:        symID,
		Predicate:        "defines",
		SourceFile:       filepath.Join(repoDir, "nonexistent.go"),
		SourceLine:       1,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "test-extractor",
		ExtractorVersion: "0.1.0",
		LastVerified:     time.Now().UTC().Format(time.RFC3339),
	})
	cdb.Close()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--db", dbPath, repoDir})
	err := rootCmd.Execute()
	out := buf.String()

	if err == nil {
		t.Fatalf("expected error for drift, got nil. output: %q", out)
	}
	if !strings.Contains(out, "DRIFT:") {
		t.Errorf("expected 'DRIFT:' in output, got: %q", out)
	}
	if !strings.Contains(out, "source file not found") {
		t.Errorf("expected 'source file not found' detail, got: %q", out)
	}
}

func TestVerifyClaimsStalenessOutput(t *testing.T) {
	repoDir := t.TempDir()
	dbPath, cdb := setupTestDB(t)

	futureTime := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	insertTestClaim(t, cdb, repoDir, filepath.Join(repoDir, "fresh.go"), "Fresh", "defines", 3, futureTime)
	cdb.Close()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--staleness", "--db", dbPath, repoDir})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "FRESH:") {
		t.Errorf("expected 'FRESH:' in staleness output, got: %q", out)
	}
	if !strings.Contains(out, "verified") {
		t.Errorf("expected 'verified' in staleness output, got: %q", out)
	}
}

func TestVerifyClaimsStalenessStale(t *testing.T) {
	repoDir := t.TempDir()
	dbPath, cdb := setupTestDB(t)

	// Use a past time so the file mtime is newer.
	pastTime := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	insertTestClaim(t, cdb, repoDir, filepath.Join(repoDir, "old.go"), "Old", "defines", 3, pastTime)

	// Touch the file to ensure mtime is recent.
	os.WriteFile(filepath.Join(repoDir, "old.go"), []byte("package main\n\nfunc Old() {}\n// updated\n"), 0o644)

	cdb.Close()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--staleness", "--db", dbPath, repoDir})
	err := rootCmd.Execute()
	out := buf.String()

	if err == nil {
		t.Fatalf("expected error for stale claims, got nil. output: %q", out)
	}
	if !strings.Contains(out, "STALE:") {
		t.Errorf("expected 'STALE:' in output, got: %q", out)
	}
}

func TestVerifyClaimsCanary_Pass(t *testing.T) {
	repoDir := t.TempDir()
	dbPath, cdb := setupTestDB(t)

	futureTime := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	// Insert a few claims that all pass.
	for i := 0; i < 5; i++ {
		name := "Func" + string(rune('A'+i))
		file := filepath.Join(repoDir, name+".go")
		insertTestClaim(t, cdb, repoDir, file, name, "defines", 3, futureTime)
	}
	cdb.Close()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--canary", "--db", dbPath, repoDir})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Canary:") {
		t.Errorf("expected 'Canary:' in output, got: %q", out)
	}
	if !strings.Contains(out, "canary passed") {
		t.Errorf("expected 'canary passed' in output, got: %q", out)
	}
}

func TestVerifyClaimsCanary_Fail(t *testing.T) {
	repoDir := t.TempDir()
	dbPath, cdb := setupTestDB(t)

	// All claims point to nonexistent files -> 100% stale -> should fail.
	for i := 0; i < 5; i++ {
		name := "Gone" + string(rune('A'+i))
		symID, _ := cdb.UpsertSymbol(db.Symbol{
			Repo:       "test/repo",
			ImportPath: "test/pkg",
			SymbolName: name,
			Language:   "go",
			Kind:       "func",
			Visibility: "public",
		})
		_, _ = cdb.InsertClaim(db.Claim{
			SubjectID:        symID,
			Predicate:        "defines",
			SourceFile:       filepath.Join(repoDir, name+".go"),
			SourceLine:       1,
			Confidence:       1.0,
			ClaimTier:        "structural",
			Extractor:        "test-extractor",
			ExtractorVersion: "0.1.0",
			LastVerified:     time.Now().UTC().Format(time.RFC3339),
		})
	}
	cdb.Close()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--canary", "--db", dbPath, repoDir})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected canary failure, got nil")
	}
	if !strings.Contains(err.Error(), "canary failed") {
		t.Errorf("expected 'canary failed' in error, got: %v", err)
	}
}

func TestVerifyClaimsCheckExisting_NoReadmes(t *testing.T) {
	repoDir := t.TempDir()
	dbPath, cdb := setupTestDB(t)
	cdb.Close()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--check-existing", "--db", dbPath, repoDir})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "no README.md files found") {
		t.Errorf("expected no-readme message, got: %q", buf.String())
	}
}

func TestVerifyClaimsDriftOutputFormat(t *testing.T) {
	line := formatDrift("pkg/foo.go", 42, "defines", "MyFunc", "source file not found")
	expected := "DRIFT: pkg/foo.go:42: defines MyFunc — source file not found"
	// The em-dash in the format uses a Unicode em dash.
	if !strings.HasPrefix(line, "DRIFT: pkg/foo.go:42:") {
		t.Errorf("drift format wrong prefix: %q", line)
	}
	if line != expected {
		t.Errorf("drift format mismatch:\n  got:  %q\n  want: %q", line, expected)
	}
}
