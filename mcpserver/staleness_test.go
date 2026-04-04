package mcpserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
)

// setupStalenessTestDB creates a temp claims DB with schema, a symbol, a claim,
// and a source_file record. Returns the DB, cleanup func, and the content hash used.
func setupStalenessTestDB(t *testing.T, repoName, importPath, relPath, content string) (*db.ClaimsDB, string) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.claims.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })

	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	contentHash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))

	// Insert a symbol, claim, and source_file record.
	symID, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       repoName,
		ImportPath: importPath,
		SymbolName: "TestFunc",
		Language:   "go",
		Kind:       "function",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}

	_, err = cdb.InsertClaim(db.Claim{
		SubjectID:        symID,
		Predicate:        "defines",
		ObjectText:       "a test function",
		SourceFile:       relPath,
		SourceLine:       1,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "test-extractor",
		ExtractorVersion: "1.0",
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}

	_, err = cdb.UpsertSourceFile(db.SourceFile{
		Repo:             repoName,
		RelativePath:     relPath,
		ContentHash:      contentHash,
		ExtractorVersion: "1.0",
		GrammarVersion:   "",
		LastIndexed:      db.Now(),
	})
	if err != nil {
		t.Fatalf("upsert source file: %v", err)
	}

	return cdb, contentHash
}

func TestStalenessChecker_CheckPackageStaleness_NoRepoRoot(t *testing.T) {
	t.Parallel()

	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", "pkg/main.go", "package main")

	sc := NewStalenessChecker(nil, nil)
	stale := sc.CheckPackageStaleness(context.Background(), cdb, "myrepo", "example.com/pkg")
	if len(stale) != 0 {
		t.Errorf("expected no stale files when repo root not configured, got %d", len(stale))
	}
}

func TestStalenessChecker_CheckPackageStaleness_NotStale(t *testing.T) {
	t.Parallel()

	originalContent := "package main\n"
	repoDir := t.TempDir()
	relPath := "pkg/main.go"

	// Write the file with same content as stored hash.
	absPath := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte(originalContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", relPath, originalContent)

	sc := NewStalenessChecker(map[string]string{"myrepo": repoDir}, nil)
	stale := sc.CheckPackageStaleness(context.Background(), cdb, "myrepo", "example.com/pkg")
	if len(stale) != 0 {
		t.Errorf("expected no stale files, got %d", len(stale))
	}
}

func TestStalenessChecker_CheckPackageStaleness_Stale(t *testing.T) {
	t.Parallel()

	originalContent := "package main\n"
	modifiedContent := "package main\n\nfunc NewFunc() {}\n"
	repoDir := t.TempDir()
	relPath := "pkg/main.go"

	// Write the modified file on disk.
	absPath := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte(modifiedContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// DB has the original content hash.
	cdb, storedHash := setupStalenessTestDB(t, "myrepo", "example.com/pkg", relPath, originalContent)

	sc := NewStalenessChecker(map[string]string{"myrepo": repoDir}, nil)
	stale := sc.CheckPackageStaleness(context.Background(), cdb, "myrepo", "example.com/pkg")

	if len(stale) != 1 {
		t.Fatalf("expected 1 stale file, got %d", len(stale))
	}
	if stale[0].RelativePath != relPath {
		t.Errorf("stale file path = %q, want %q", stale[0].RelativePath, relPath)
	}
	if stale[0].StoredHash != storedHash {
		t.Errorf("stored hash mismatch")
	}
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte(modifiedContent)))
	if stale[0].CurrentHash != expectedHash {
		t.Errorf("current hash = %q, want %q", stale[0].CurrentHash, expectedHash)
	}
}

func TestStalenessChecker_CheckPackageStaleness_FileDeleted(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	relPath := "pkg/main.go"
	// Don't create the file on disk — simulates deletion.

	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", relPath, "package main\n")

	sc := NewStalenessChecker(map[string]string{"myrepo": repoDir}, nil)
	stale := sc.CheckPackageStaleness(context.Background(), cdb, "myrepo", "example.com/pkg")

	// Deleted files are skipped, not reported as stale.
	if len(stale) != 0 {
		t.Errorf("expected no stale files for deleted file, got %d", len(stale))
	}

	// Verify MarkFileDeleted was called: the source_file record should be gone.
	files, err := cdb.GetSourceFilesByImportPath("example.com/pkg")
	if err != nil {
		t.Fatalf("GetSourceFilesByImportPath after delete: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 source files after MarkFileDeleted, got %d", len(files))
	}
}

func TestStalenessChecker_CheckPackageStaleness_CancelledContext(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	relPath := "pkg/main.go"

	// Create the file on disk with different content so it would be stale.
	absPath := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte("modified content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", relPath, "package main\n")

	// Pre-cancel the context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sc := NewStalenessChecker(map[string]string{"myrepo": repoDir}, nil)
	stale := sc.CheckPackageStaleness(ctx, cdb, "myrepo", "example.com/pkg")

	// Should return immediately without reading files.
	if len(stale) != 0 {
		t.Errorf("expected 0 stale files with cancelled context, got %d", len(stale))
	}
}

func TestStalenessChecker_RefreshStaleFiles_NilRegistry(t *testing.T) {
	t.Parallel()

	sc := NewStalenessChecker(map[string]string{"myrepo": "/tmp"}, nil)
	refreshed, errs := sc.RefreshStaleFiles(context.Background(), nil, []StaleFile{
		{RelativePath: "foo.go", RepoName: "myrepo"},
	})
	if refreshed != 0 {
		t.Errorf("expected 0 refreshed with nil registry, got %d", refreshed)
	}
	if len(errs) != 0 {
		t.Errorf("expected 0 errors with nil registry, got %d", len(errs))
	}
}

func TestStalenessChecker_HasRepoRoot(t *testing.T) {
	t.Parallel()

	sc := NewStalenessChecker(map[string]string{"myrepo": "/tmp/repo"}, nil)
	if !sc.HasRepoRoot("myrepo") {
		t.Error("expected HasRepoRoot=true for configured repo")
	}
	if sc.HasRepoRoot("other") {
		t.Error("expected HasRepoRoot=false for unconfigured repo")
	}
	if sc.RepoRoot("myrepo") != "/tmp/repo" {
		t.Errorf("RepoRoot = %q, want %q", sc.RepoRoot("myrepo"), "/tmp/repo")
	}
}

func TestStalenessWarning_NoStaleFiles(t *testing.T) {
	t.Parallel()
	msg := stalenessWarning(nil, 0, nil)
	if msg != "" {
		t.Errorf("expected empty warning for no stale files, got %q", msg)
	}
}

func TestStalenessWarning_AllRefreshed(t *testing.T) {
	t.Parallel()
	stale := []StaleFile{{RelativePath: "a.go"}, {RelativePath: "b.go"}}
	msg := stalenessWarning(stale, 2, nil)
	if msg == "" {
		t.Error("expected non-empty warning for refreshed files")
	}
	if !strContains(msg, "re-extracted") {
		t.Errorf("expected 're-extracted' in warning, got %q", msg)
	}
}

func TestStalenessWarning_PartialFailure(t *testing.T) {
	t.Parallel()
	stale := []StaleFile{{RelativePath: "a.go"}, {RelativePath: "b.go"}}
	errs := []error{fmt.Errorf("extract failed")}
	msg := stalenessWarning(stale, 1, errs)
	if !strContains(msg, "Warning") {
		t.Errorf("expected 'Warning' in message, got %q", msg)
	}
	if !strContains(msg, "extract failed") {
		t.Errorf("expected error text in message, got %q", msg)
	}
}

func strContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestGetSourceFilesByImportPath tests the new DB method.
func TestGetSourceFilesByImportPath(t *testing.T) {
	t.Parallel()

	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", "pkg/main.go", "package main\n")

	files, err := cdb.GetSourceFilesByImportPath("example.com/pkg")
	if err != nil {
		t.Fatalf("GetSourceFilesByImportPath: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 source file, got %d", len(files))
	}
	if files[0].RelativePath != "pkg/main.go" {
		t.Errorf("RelativePath = %q, want %q", files[0].RelativePath, "pkg/main.go")
	}
}

func TestGetSourceFilesByImportPath_NoMatch(t *testing.T) {
	t.Parallel()

	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", "pkg/main.go", "package main\n")

	files, err := cdb.GetSourceFilesByImportPath("example.com/nonexistent")
	if err != nil {
		t.Fatalf("GetSourceFilesByImportPath: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 source files, got %d", len(files))
	}
}

// Ensure the extractor import is used (needed for NewStalenessChecker signature).
var _ *extractor.Registry
