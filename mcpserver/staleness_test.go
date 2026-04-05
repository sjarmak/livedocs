package mcpserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

// --- mock extractor for resilience tests ---

type mockExtractor struct {
	extractFn func(ctx context.Context, path, lang string) ([]extractor.Claim, error)
}

func (m *mockExtractor) Extract(ctx context.Context, path, lang string) ([]extractor.Claim, error) {
	return m.extractFn(ctx, path, lang)
}

func (m *mockExtractor) ExtractBytes(_ context.Context, _ []byte, _ string, _ string) ([]extractor.Claim, error) {
	return nil, extractor.ErrRequiresLocalFS
}

func (m *mockExtractor) Name() string    { return "mock-extractor" }
func (m *mockExtractor) Version() string { return "1.0" }

// makeRegistryWithMock creates a Registry with a mock extractor registered for .go files.
func makeRegistryWithMock(fn func(ctx context.Context, path, lang string) ([]extractor.Claim, error)) *extractor.Registry {
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:          "go",
		Extensions:        []string{".go"},
		TreeSitterGrammar: "tree-sitter-go",
		FastExtractor:     &mockExtractor{extractFn: fn},
	})
	return reg
}

func TestRefreshStaleFiles_ContextTimeout(t *testing.T) {
	t.Parallel()

	// Create a context that is already cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reg := makeRegistryWithMock(func(_ context.Context, _, _ string) ([]extractor.Claim, error) {
		t.Fatal("extractor should not be called after context cancellation")
		return nil, nil
	})

	sc := NewStalenessChecker(map[string]string{"myrepo": "/tmp"}, reg)
	staleFiles := []StaleFile{
		{RelativePath: "a.go", RepoName: "myrepo"},
		{RelativePath: "b.go", RepoName: "myrepo"},
		{RelativePath: "c.go", RepoName: "myrepo"},
	}

	refreshed, errs := sc.RefreshStaleFiles(ctx, nil, staleFiles)

	if refreshed != 0 {
		t.Errorf("expected 0 refreshed with cancelled context, got %d", refreshed)
	}
	if len(errs) == 0 {
		t.Fatal("expected at least one error for context cancellation")
	}
	if !strContains(errs[0].Error(), "context cancelled") {
		t.Errorf("expected context cancellation error, got %q", errs[0].Error())
	}
}

func TestRefreshStaleFiles_Debounce(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	relPath := "pkg/debounce.go"

	// Create the file on disk.
	absPath := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	extractCount := 0
	reg := makeRegistryWithMock(func(_ context.Context, _, _ string) ([]extractor.Claim, error) {
		extractCount++
		return []extractor.Claim{
			{
				SubjectName: "Foo",
				Kind:        "function",
				Visibility:  "public",
				Predicate:   "defines",
				ObjectText:  "a function",
				SourceFile:  relPath,
				SourceLine:  1,
				Confidence:  1.0,
				ClaimTier:   "structural",
				Extractor:   "mock-extractor",
			},
		}, nil
	})

	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", relPath, "old content\n")

	clock := newMockClock()
	sc := NewStalenessChecker(map[string]string{"myrepo": repoDir}, reg)
	sc.clockFn = clock.Now

	staleFiles := []StaleFile{
		{RelativePath: relPath, RepoName: "myrepo"},
	}

	// First call — file should be re-extracted.
	refreshed, errs := sc.RefreshStaleFiles(context.Background(), cdb, staleFiles)
	if refreshed != 1 {
		t.Fatalf("first call: expected 1 refreshed, got %d (errs: %v)", refreshed, errs)
	}
	if extractCount != 1 {
		t.Fatalf("first call: expected extractCount=1, got %d", extractCount)
	}

	// Modify the file again so it would appear stale.
	if err := os.WriteFile(absPath, []byte("package pkg\n\nfunc New() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Advance clock by 3s (within 5s debounce window).
	clock.Advance(3 * time.Second)

	// Second call — should be skipped due to debounce.
	refreshed2, _ := sc.RefreshStaleFiles(context.Background(), cdb, staleFiles)
	if refreshed2 != 0 {
		t.Errorf("second call (within debounce): expected 0 refreshed, got %d", refreshed2)
	}
	if extractCount != 1 {
		t.Errorf("second call: expected extractCount still 1, got %d", extractCount)
	}

	// Advance clock past the debounce window (total 3+3 = 6s > 5s).
	clock.Advance(3 * time.Second)

	// Third call — debounce expired, should re-extract.
	refreshed3, errs3 := sc.RefreshStaleFiles(context.Background(), cdb, staleFiles)
	if refreshed3 != 1 {
		t.Errorf("third call (after debounce): expected 1 refreshed, got %d (errs: %v)", refreshed3, errs3)
	}
	if extractCount != 2 {
		t.Errorf("third call: expected extractCount=2, got %d", extractCount)
	}
}

func TestRefreshStaleFiles_PanicRecovery(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	relPath := "pkg/panic.go"

	// Create the file on disk so reExtractFile can read it.
	absPath := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := makeRegistryWithMock(func(_ context.Context, _, _ string) ([]extractor.Claim, error) {
		panic("simulated tree-sitter crash")
	})

	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", relPath, "package pkg\n")

	sc := NewStalenessChecker(map[string]string{"myrepo": repoDir}, reg)
	staleFiles := []StaleFile{
		{RelativePath: relPath, RepoName: "myrepo"},
	}

	refreshed, errs := sc.RefreshStaleFiles(context.Background(), cdb, staleFiles)

	if refreshed != 0 {
		t.Errorf("expected 0 refreshed after panic, got %d", refreshed)
	}
	if len(errs) == 0 {
		t.Fatal("expected at least one error for panic recovery")
	}
	if !strContains(errs[0].Error(), "panic during re-extraction") {
		t.Errorf("expected panic error, got %q", errs[0].Error())
	}
	if !strContains(errs[0].Error(), "simulated tree-sitter crash") {
		t.Errorf("expected panic message in error, got %q", errs[0].Error())
	}
}

func TestRefreshStaleFiles_SQLiteBusyContinues(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	relPath1 := "pkg/busy.go"
	relPath2 := "pkg/ok.go"

	// Create both files on disk.
	for _, rp := range []string{relPath1, relPath2} {
		absPath := filepath.Join(repoDir, rp)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absPath, []byte("package pkg\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	callCount := 0
	reg := makeRegistryWithMock(func(_ context.Context, path, _ string) ([]extractor.Claim, error) {
		callCount++
		if strContains(path, "busy.go") {
			return nil, fmt.Errorf("SQLITE_BUSY (5): database is locked")
		}
		return []extractor.Claim{}, nil
	})

	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", relPath1, "package pkg\n")

	sc := NewStalenessChecker(map[string]string{"myrepo": repoDir}, reg)
	staleFiles := []StaleFile{
		{RelativePath: relPath1, RepoName: "myrepo"},
		{RelativePath: relPath2, RepoName: "myrepo"},
	}

	refreshed, errs := sc.RefreshStaleFiles(context.Background(), cdb, staleFiles)

	// The second file should still be processed even though the first got SQLITE_BUSY.
	if callCount < 2 {
		t.Errorf("expected extractor called for both files, got %d calls", callCount)
	}
	// SQLITE_BUSY error should be collected as a warning.
	if len(errs) == 0 {
		t.Fatal("expected at least one warning error for SQLITE_BUSY")
	}
	foundBusy := false
	for _, err := range errs {
		if strContains(err.Error(), "SQLITE_BUSY") {
			foundBusy = true
		}
	}
	if !foundBusy {
		t.Errorf("expected SQLITE_BUSY warning in errors, got %v", errs)
	}
	// The second file (ok.go) might or might not succeed depending on the DB state,
	// but processing should not have been stopped by the SQLITE_BUSY error.
	_ = refreshed
}

func TestIsSQLiteBusy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"regular error", fmt.Errorf("something else"), false},
		{"SQLITE_BUSY", fmt.Errorf("SQLITE_BUSY (5)"), true},
		{"database is locked", fmt.Errorf("database is locked"), true},
		{"wrapped SQLITE_BUSY", fmt.Errorf("re-extract foo.go: SQLITE_BUSY (5): %w", fmt.Errorf("inner")), true},
		{"wrapped database locked", fmt.Errorf("tx: database is locked"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isSQLiteBusy(tt.err)
			if got != tt.want {
				t.Errorf("isSQLiteBusy(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// --- staleness cache tests ---

// mockClock provides a controllable time source for testing.
type mockClock struct {
	mu  sync.Mutex
	now time.Time
}

func newMockClock() *mockClock {
	return &mockClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *mockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mockClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestStalenessCache_HitWithinTTL(t *testing.T) {
	t.Parallel()

	originalContent := "package main\n"
	modifiedContent := "package main\n\nfunc Added() {}\n"
	repoDir := t.TempDir()
	relPath := "pkg/main.go"

	// Write modified file on disk so it would be stale.
	absPath := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte(modifiedContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", relPath, originalContent)

	clock := newMockClock()
	sc := NewStalenessChecker(map[string]string{"myrepo": repoDir}, nil)
	// Override the cache's clock.
	sc.cache.nowFunc = clock.Now

	// Read the current file content to know what hash the first call will produce.
	origContent, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatal(err)
	}

	// First call — should read files and detect staleness.
	stale1 := sc.CheckPackageStaleness(context.Background(), cdb, "myrepo", "example.com/pkg")
	if len(stale1) != 1 {
		t.Fatalf("first call: expected 1 stale file, got %d", len(stale1))
	}

	// Advance clock by 5s (within 10s TTL).
	clock.Advance(5 * time.Second)

	// Modify the file again so if cache is bypassed, we'd get different results.
	if err := os.WriteFile(absPath, []byte("package main\n\nfunc Different() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Second call — should return cached result, not re-read files.
	stale2 := sc.CheckPackageStaleness(context.Background(), cdb, "myrepo", "example.com/pkg")
	if len(stale2) != 1 {
		t.Fatalf("second call: expected 1 stale file (cached), got %d", len(stale2))
	}

	// The cached result should have the same hash as the first call's result,
	// NOT the hash of the newly modified file — proving cache was used.
	expectedHash := fmt.Sprintf("%x", sha256.Sum256(origContent))
	if stale2[0].CurrentHash != expectedHash {
		t.Errorf("cached result has wrong hash: got %q, want %q (cache was bypassed)", stale2[0].CurrentHash, expectedHash)
	}
}

func TestStalenessCache_ExpiredAfterTTL(t *testing.T) {
	t.Parallel()

	originalContent := "package main\n"
	modifiedContent := "package main\n\nfunc Added() {}\n"
	repoDir := t.TempDir()
	relPath := "pkg/main.go"

	absPath := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte(modifiedContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", relPath, originalContent)

	clock := newMockClock()
	sc := NewStalenessChecker(map[string]string{"myrepo": repoDir}, nil)
	sc.cache.nowFunc = clock.Now

	// First call — populates cache.
	stale1 := sc.CheckPackageStaleness(context.Background(), cdb, "myrepo", "example.com/pkg")
	if len(stale1) != 1 {
		t.Fatalf("first call: expected 1 stale file, got %d", len(stale1))
	}
	firstHash := stale1[0].CurrentHash

	// Advance clock past TTL (11s > 10s).
	clock.Advance(11 * time.Second)

	// Write different content so re-check produces a different hash.
	newContent := "package main\n\nfunc Refreshed() {}\n"
	if err := os.WriteFile(absPath, []byte(newContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Second call — cache expired, should re-read files.
	stale2 := sc.CheckPackageStaleness(context.Background(), cdb, "myrepo", "example.com/pkg")
	if len(stale2) != 1 {
		t.Fatalf("second call after TTL: expected 1 stale file, got %d", len(stale2))
	}

	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte(newContent)))
	if stale2[0].CurrentHash != expectedHash {
		t.Errorf("after TTL expiry: got hash %q, want %q (files were not re-checked)", stale2[0].CurrentHash, expectedHash)
	}
	if stale2[0].CurrentHash == firstHash {
		t.Error("after TTL expiry: hash unchanged — cache was not invalidated")
	}
}

func TestStalenessCache_LRUEviction(t *testing.T) {
	t.Parallel()

	clock := newMockClock()
	cache := newStalenessCache(clock.Now)

	// Fill cache to capacity.
	for i := 0; i < staleCacheMaxEntries; i++ {
		key := fmt.Sprintf("repo:pkg/%d", i)
		cache.put(key, nil)
	}

	if len(cache.entries) != staleCacheMaxEntries {
		t.Fatalf("expected %d entries, got %d", staleCacheMaxEntries, len(cache.entries))
	}

	// Add one more — should evict the LRU entry (repo:pkg/0).
	cache.put("repo:pkg/new", []StaleFile{{RelativePath: "new.go"}})

	if len(cache.entries) != staleCacheMaxEntries {
		t.Fatalf("after eviction: expected %d entries, got %d", staleCacheMaxEntries, len(cache.entries))
	}

	// The oldest entry should be evicted.
	if _, ok := cache.get("repo:pkg/0"); ok {
		t.Error("expected repo:pkg/0 to be evicted, but it was found")
	}

	// The new entry should exist.
	if _, ok := cache.get("repo:pkg/new"); !ok {
		t.Error("expected repo:pkg/new to exist after insertion")
	}
}

func TestReExtractFile_ZeroClaimGuard(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	relPath := "pkg/guarded.go"

	// Create the file on disk.
	absPath := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set up DB with an existing claim for this file.
	cdb, _ := setupStalenessTestDB(t, "myrepo", "example.com/pkg", relPath, "package pkg\n")

	// Verify there is at least one existing claim.
	existingClaims, err := cdb.GetClaimsByFile(relPath)
	if err != nil {
		t.Fatalf("GetClaimsByFile: %v", err)
	}
	if len(existingClaims) == 0 {
		t.Fatal("expected existing claims in DB before test")
	}

	// Mock extractor that returns 0 claims.
	reg := makeRegistryWithMock(func(_ context.Context, _, _ string) ([]extractor.Claim, error) {
		return []extractor.Claim{}, nil
	})

	sc := NewStalenessChecker(map[string]string{"myrepo": repoDir}, reg)
	staleFiles := []StaleFile{
		{RelativePath: relPath, RepoName: "myrepo"},
	}

	refreshed, errs := sc.RefreshStaleFiles(context.Background(), cdb, staleFiles)

	// Should have failed with zero-claim guard error.
	if refreshed != 0 {
		t.Errorf("expected 0 refreshed, got %d", refreshed)
	}
	if len(errs) == 0 {
		t.Fatal("expected at least one error for zero-claim guard")
	}
	foundGuard := false
	for _, e := range errs {
		if strContains(e.Error(), "refusing to overwrite") {
			foundGuard = true
		}
	}
	if !foundGuard {
		t.Errorf("expected 'refusing to overwrite' error, got %v", errs)
	}

	// Verify existing claims are preserved.
	afterClaims, err := cdb.GetClaimsByFile(relPath)
	if err != nil {
		t.Fatalf("GetClaimsByFile after guard: %v", err)
	}
	if len(afterClaims) != len(existingClaims) {
		t.Errorf("expected %d claims preserved, got %d", len(existingClaims), len(afterClaims))
	}
}

// Ensure the extractor import is used (needed for NewStalenessChecker signature).
var _ *extractor.Registry
