package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjarmak/livedocs/db"
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
	if !strings.Contains(out, "DRIFT [HIGH]:") {
		t.Errorf("expected 'DRIFT [HIGH]:' in output, got: %q", out)
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
	expected := "DRIFT [LOW]: pkg/foo.go:42: defines MyFunc — source file not found"
	if !strings.HasPrefix(line, "DRIFT [LOW]: pkg/foo.go:42:") {
		t.Errorf("drift format wrong prefix: %q", line)
	}
	if line != expected {
		t.Errorf("drift format mismatch:\n  got:  %q\n  want: %q", line, expected)
	}
}

func TestVerifyClaimsDriftSeverityFormat(t *testing.T) {
	tests := []struct {
		name     string
		severity DriftSeverity
		want     string
	}{
		{"HIGH", SeverityHigh, "DRIFT [HIGH]: f.go:1: defines Foo — detail"},
		{"MEDIUM", SeverityMedium, "DRIFT [MEDIUM]: f.go:1: defines Foo — detail"},
		{"LOW", SeverityLow, "DRIFT [LOW]: f.go:1: defines Foo — detail"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDriftWithSeverity(tt.severity, "f.go", 1, "defines", "Foo", "detail")
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVerifyClaimsCheckExisting_HighSeverity_FileDeleted(t *testing.T) {
	repoDir := t.TempDir()
	dbPath, cdb := setupTestDB(t)

	// Insert a symbol with a "defines" claim pointing to a file that will be deleted.
	symID, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       "test/repo",
		ImportPath: "test/pkg",
		SymbolName: "DeletedFunc",
		Language:   "go",
		Kind:       "func",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}
	_, err = cdb.InsertClaim(db.Claim{
		SubjectID:        symID,
		Predicate:        "defines",
		SourceFile:       filepath.Join(repoDir, "deleted.go"),
		SourceLine:       10,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "test-extractor",
		ExtractorVersion: "0.1.0",
		LastVerified:     time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}
	cdb.Close()

	// Create a README that references the symbol (using backtick to ensure extraction).
	readmeContent := "# My Package\n\nUses `DeletedFunc` for processing.\n"
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte(readmeContent), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	// Note: we do NOT create deleted.go — it's deleted.

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--check-existing", "--db", dbPath, repoDir})
	err = rootCmd.Execute()
	out := buf.String()

	if err == nil {
		t.Fatalf("expected error for drift, got nil. output: %q", out)
	}
	if !strings.Contains(out, "DRIFT [HIGH]:") {
		t.Errorf("expected 'DRIFT [HIGH]:' in output, got: %q", out)
	}
	if !strings.Contains(out, "source file") && !strings.Contains(out, "no longer exists") {
		t.Errorf("expected file deletion detail in output, got: %q", out)
	}
}

func TestVerifyClaimsCheckExisting_HighSeverity_SymbolNotInDB(t *testing.T) {
	repoDir := t.TempDir()
	dbPath, cdb := setupTestDB(t)
	cdb.Close()

	// Create a README that references a symbol not in the DB at all.
	readmeContent := "# My Package\n\nUses `NonExistentSymbol` for processing.\n"
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte(readmeContent), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--check-existing", "--db", dbPath, repoDir})
	err := rootCmd.Execute()
	out := buf.String()

	if err == nil {
		t.Fatalf("expected error for drift, got nil. output: %q", out)
	}
	if !strings.Contains(out, "DRIFT [HIGH]:") {
		t.Errorf("expected 'DRIFT [HIGH]:' in output, got: %q", out)
	}
	if !strings.Contains(out, "not found in claims DB") {
		t.Errorf("expected 'not found in claims DB' detail, got: %q", out)
	}
}

func TestVerifyClaimsCheckExisting_ExitCodeNonZeroOnDrift(t *testing.T) {
	repoDir := t.TempDir()
	dbPath, cdb := setupTestDB(t)
	cdb.Close()

	// README references unknown symbol -> drift -> non-zero exit.
	readmeContent := "# Package\n\n`UnknownFunc` does things.\n"
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte(readmeContent), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--check-existing", "--db", dbPath, repoDir})
	err := rootCmd.Execute()

	if err == nil {
		t.Fatalf("expected non-nil error (non-zero exit) when drift found, got nil")
	}
}

func TestVerifyClaimsCheckExisting_ExitCodeZeroNoDrift(t *testing.T) {
	repoDir := t.TempDir()
	dbPath, cdb := setupTestDB(t)

	// Insert symbol and create its source file so no drift.
	futureTime := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	insertTestClaim(t, cdb, repoDir, filepath.Join(repoDir, "main.go"), "GoodFunc", "defines", 3, futureTime)
	cdb.Close()

	// README references the symbol that exists in the DB with valid source.
	readmeContent := "# Package\n\n`GoodFunc` does things.\n"
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte(readmeContent), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"verify-claims", "--check-existing", "--db", dbPath, repoDir})
	err := rootCmd.Execute()

	if err != nil {
		t.Fatalf("expected nil error (zero exit) when no drift, got: %v. output: %q", err, buf.String())
	}
}

func TestVerifyClaimsBasicVerify_HighSeverityFileDeleted(t *testing.T) {
	repoDir := t.TempDir()
	_, cdb := setupTestDB(t)

	// Claim pointing to nonexistent file should produce HIGH severity.
	symID, _ := cdb.UpsertSymbol(db.Symbol{
		Repo:       "test/repo",
		ImportPath: "test/pkg",
		SymbolName: "Gone",
		Language:   "go",
		Kind:       "func",
		Visibility: "public",
	})
	_, _ = cdb.InsertClaim(db.Claim{
		SubjectID:        symID,
		Predicate:        "defines",
		SourceFile:       filepath.Join(repoDir, "gone.go"),
		SourceLine:       1,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "test-extractor",
		ExtractorVersion: "0.1.0",
		LastVerified:     time.Now().UTC().Format(time.RFC3339),
	})

	// Call runBasicVerify directly to avoid cobra flag state leakage between tests.
	buf := new(bytes.Buffer)
	err := runBasicVerify(cdb, repoDir, buf)
	cdb.Close()
	out := buf.String()

	if err == nil {
		t.Fatalf("expected error for drift, got nil")
	}
	if !strings.Contains(out, "DRIFT [HIGH]:") {
		t.Errorf("expected 'DRIFT [HIGH]:' for deleted file, got: %q", out)
	}
}
