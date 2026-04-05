package pipeline

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/live-docs/live_docs/cache"
	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
	"github.com/live-docs/live_docs/gitdiff"
)

// stubExtractor is a test extractor that returns predictable claims.
type stubExtractor struct {
	name    string
	version string
	claims  map[string][]extractor.Claim // path -> claims
}

func (s *stubExtractor) Name() string    { return s.name }
func (s *stubExtractor) Version() string { return s.version }
func (s *stubExtractor) ExtractBytes(_ context.Context, src []byte, relPath string, lang string) ([]extractor.Claim, error) {
	return s.Extract(nil, relPath, lang)
}

func (s *stubExtractor) Extract(_ context.Context, path string, lang string) ([]extractor.Claim, error) {
	if claims, ok := s.claims[path]; ok {
		return claims, nil
	}
	// Default: produce one defines claim per file.
	return []extractor.Claim{
		{
			SubjectRepo:       "test/repo",
			SubjectImportPath: "test/pkg",
			SubjectName:       filepath.Base(path),
			Language:          lang,
			Kind:              extractor.KindFunc,
			Visibility:        extractor.VisibilityPublic,
			Predicate:         extractor.PredicateDefines,
			SourceFile:        path,
			SourceLine:        1,
			Confidence:        1.0,
			ClaimTier:         extractor.TierStructural,
			Extractor:         s.name,
			ExtractorVersion:  s.version,
			LastVerified:      time.Now(),
		},
	}, nil
}

// setupTestRepo creates a temp git repo with initial files and returns
// the repo dir and first commit SHA.
func setupTestRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()

	gitRun(t, dir, "git", "init")
	gitRun(t, dir, "git", "checkout", "-b", "main")

	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	gitRun(t, dir, "git", "add", ".")
	gitRun(t, dir, "git", "commit", "-m", "initial")

	return dir, getHEAD(t, dir)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %v failed: %v\n%s", args, err, out)
	}
}

func getHEAD(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return string(out[:len(out)-1])
}

func openTestDBs(t *testing.T) (*cache.SQLiteStore, *db.ClaimsDB) {
	t.Helper()

	cacheStore, err := cache.NewSQLiteStore(":memory:", 1<<30) // 1GB cap
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	t.Cleanup(func() { cacheStore.Close() })

	claimsDB, err := db.OpenClaimsDB(":memory:")
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	if err := claimsDB.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { claimsDB.Close() })

	return cacheStore, claimsDB
}

func TestRun_NewFiles(t *testing.T) {
	// Setup: repo with one file, then add a second file.
	repoDir, sha1 := setupTestRepo(t, map[string]string{
		"existing.go": "package existing",
	})

	// Add a new file and commit.
	if err := os.WriteFile(filepath.Join(repoDir, "new.go"), []byte("package new"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoDir, "git", "add", ".")
	gitRun(t, repoDir, "git", "commit", "-m", "add new.go")
	sha2 := getHEAD(t, repoDir)

	cacheStore, claimsDB := openTestDBs(t)

	stub := &stubExtractor{name: "test-ext", version: "0.1.0"}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	p := New(Config{
		Repo:     "test/repo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	result, err := p.Run(context.Background(), sha1, sha2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.FilesChanged != 1 {
		t.Errorf("FilesChanged: got %d, want 1", result.FilesChanged)
	}
	if result.FilesExtracted != 1 {
		t.Errorf("FilesExtracted: got %d, want 1", result.FilesExtracted)
	}
	if result.CacheHits != 0 {
		t.Errorf("CacheHits: got %d, want 0", result.CacheHits)
	}
	if result.ClaimsStored < 1 {
		t.Errorf("ClaimsStored: got %d, want >= 1", result.ClaimsStored)
	}
}

func TestRun_CacheHit(t *testing.T) {
	repoDir, sha1 := setupTestRepo(t, map[string]string{
		"a.go": "package a",
	})

	// Modify a.go and commit.
	if err := os.WriteFile(filepath.Join(repoDir, "a.go"), []byte("package a // v2"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoDir, "git", "add", ".")
	gitRun(t, repoDir, "git", "commit", "-m", "modify a.go")
	sha2 := getHEAD(t, repoDir)

	cacheStore, claimsDB := openTestDBs(t)

	// Pre-populate cache with the hash of the NEW content.
	newContent := []byte("package a // v2")
	hash := fmt.Sprintf("%x", sha256.Sum256(newContent))
	if err := cacheStore.Put(cache.Entry{
		Repo:             "test/repo",
		RelativePath:     "a.go",
		ContentHash:      hash,
		ExtractorVersion: "0.1.0",
		GrammarVersion:   "",
		LastIndexed:      time.Now(),
		SizeBytes:        int64(len(newContent)),
	}); err != nil {
		t.Fatalf("cache put: %v", err)
	}

	stub := &stubExtractor{name: "test-ext", version: "0.1.0"}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	p := New(Config{
		Repo:     "test/repo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	result, err := p.Run(context.Background(), sha1, sha2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.CacheHits != 1 {
		t.Errorf("CacheHits: got %d, want 1", result.CacheHits)
	}
	if result.FilesExtracted != 0 {
		t.Errorf("FilesExtracted: got %d, want 0 (cache hit)", result.FilesExtracted)
	}
}

func TestRun_DeletedFiles(t *testing.T) {
	repoDir, sha1 := setupTestRepo(t, map[string]string{
		"keep.go":   "package keep",
		"remove.go": "package remove",
	})

	// Pre-populate cache so we can verify tombstoning.
	cacheStore, claimsDB := openTestDBs(t)
	removeContent := []byte("package remove")
	removeHash := fmt.Sprintf("%x", sha256.Sum256(removeContent))
	if err := cacheStore.Put(cache.Entry{
		Repo:             "test/repo",
		RelativePath:     "remove.go",
		ContentHash:      removeHash,
		ExtractorVersion: "0.1.0",
		LastIndexed:      time.Now(),
		SizeBytes:        int64(len(removeContent)),
	}); err != nil {
		t.Fatalf("cache put: %v", err)
	}

	// Delete remove.go and commit.
	gitRun(t, repoDir, "git", "rm", "remove.go")
	gitRun(t, repoDir, "git", "commit", "-m", "delete remove.go")
	sha2 := getHEAD(t, repoDir)

	stub := &stubExtractor{name: "test-ext", version: "0.1.0"}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	p := New(Config{
		Repo:     "test/repo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	result, err := p.Run(context.Background(), sha1, sha2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.FilesDeleted != 1 {
		t.Errorf("FilesDeleted: got %d, want 1", result.FilesDeleted)
	}

	// Verify cache entry is tombstoned.
	hit, err := cacheStore.Hit("test/repo", "remove.go", removeHash, "0.1.0", "")
	if err != nil {
		t.Fatalf("cache hit check: %v", err)
	}
	if hit {
		t.Error("expected cache entry to be tombstoned, but got hit")
	}
}

func TestRun_UnsupportedExtension(t *testing.T) {
	// Files with unsupported extensions should be skipped, not cause errors.
	repoDir, sha1 := setupTestRepo(t, map[string]string{
		"readme.txt": "hello",
	})

	if err := os.WriteFile(filepath.Join(repoDir, "notes.md"), []byte("# notes"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoDir, "git", "add", ".")
	gitRun(t, repoDir, "git", "commit", "-m", "add markdown")
	sha2 := getHEAD(t, repoDir)

	cacheStore, claimsDB := openTestDBs(t)

	// Registry with only Go support.
	stub := &stubExtractor{name: "test-ext", version: "0.1.0"}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	p := New(Config{
		Repo:     "test/repo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	result, err := p.Run(context.Background(), sha1, sha2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.FilesSkipped != 1 {
		t.Errorf("FilesSkipped: got %d, want 1", result.FilesSkipped)
	}
}

func TestRun_FullCycle(t *testing.T) {
	// Full cycle: add -> modify -> delete across two pipeline runs.
	repoDir, sha1 := setupTestRepo(t, map[string]string{
		"a.go": "package a",
		"b.go": "package b",
	})

	cacheStore, claimsDB := openTestDBs(t)
	stub := &stubExtractor{name: "test-ext", version: "0.1.0"}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	p := New(Config{
		Repo:     "test/repo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	// Run 1: modify a.go, add c.go.
	if err := os.WriteFile(filepath.Join(repoDir, "a.go"), []byte("package a // v2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "c.go"), []byte("package c"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoDir, "git", "add", ".")
	gitRun(t, repoDir, "git", "commit", "-m", "run1")
	sha2 := getHEAD(t, repoDir)

	r1, err := p.Run(context.Background(), sha1, sha2)
	if err != nil {
		t.Fatalf("Run1: %v", err)
	}
	if r1.FilesChanged != 2 {
		t.Errorf("Run1 FilesChanged: got %d, want 2", r1.FilesChanged)
	}
	if r1.FilesExtracted != 2 {
		t.Errorf("Run1 FilesExtracted: got %d, want 2", r1.FilesExtracted)
	}

	// Run 2: delete b.go.
	gitRun(t, repoDir, "git", "rm", "b.go")
	gitRun(t, repoDir, "git", "commit", "-m", "run2")
	sha3 := getHEAD(t, repoDir)

	r2, err := p.Run(context.Background(), sha2, sha3)
	if err != nil {
		t.Fatalf("Run2: %v", err)
	}
	if r2.FilesDeleted != 1 {
		t.Errorf("Run2 FilesDeleted: got %d, want 1", r2.FilesDeleted)
	}

	// Run 3: no changes should be a no-op.
	r3, err := p.Run(context.Background(), sha3, sha3)
	if err != nil {
		t.Fatalf("Run3: %v", err)
	}
	if r3.FilesChanged != 0 || r3.FilesExtracted != 0 || r3.FilesDeleted != 0 {
		t.Errorf("Run3: expected all zeros, got %+v", r3)
	}
}

func TestRun_ContextCancellation(t *testing.T) {
	repoDir, sha1 := setupTestRepo(t, map[string]string{
		"a.go": "package a",
	})

	if err := os.WriteFile(filepath.Join(repoDir, "b.go"), []byte("package b"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoDir, "git", "add", ".")
	gitRun(t, repoDir, "git", "commit", "-m", "add b")
	sha2 := getHEAD(t, repoDir)

	cacheStore, claimsDB := openTestDBs(t)
	stub := &stubExtractor{name: "test-ext", version: "0.1.0"}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	p := New(Config{
		Repo:     "test/repo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := p.Run(ctx, sha1, sha2)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRun_RenamedFile(t *testing.T) {
	repoDir, sha1 := setupTestRepo(t, map[string]string{
		"old.go": "package old",
	})

	// Rename old.go -> new.go
	gitRun(t, repoDir, "git", "mv", "old.go", "new.go")
	gitRun(t, repoDir, "git", "commit", "-m", "rename")
	sha2 := getHEAD(t, repoDir)

	cacheStore, claimsDB := openTestDBs(t)
	stub := &stubExtractor{name: "test-ext", version: "0.1.0"}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	// Pre-populate cache for old.go.
	content := []byte("package old")
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	if err := cacheStore.Put(cache.Entry{
		Repo:             "test/repo",
		RelativePath:     "old.go",
		ContentHash:      hash,
		ExtractorVersion: "0.1.0",
		LastIndexed:      time.Now(),
		SizeBytes:        int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}

	p := New(Config{
		Repo:     "test/repo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	result, err := p.Run(context.Background(), sha1, sha2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Rename: old.go should be tombstoned, new.go should be extracted.
	if result.FilesDeleted != 1 {
		t.Errorf("FilesDeleted: got %d, want 1 (old.go tombstoned)", result.FilesDeleted)
	}
	if result.FilesChanged != 1 {
		t.Errorf("FilesChanged: got %d, want 1 (new.go)", result.FilesChanged)
	}

	// Verify old.go is tombstoned.
	hit, err := cacheStore.Hit("test/repo", "old.go", hash, "0.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Error("old.go should be tombstoned")
	}
}

func TestGeneratedFilesSkipped(t *testing.T) {
	// Generated files should be skipped by the pipeline — zero claims produced.
	repoDir, sha1 := setupTestRepo(t, map[string]string{
		"main.go": "package main",
	})

	// Add generated files and a normal file.
	generatedFiles := map[string]string{
		"types_generated.go":       "package gen",
		"zz_generated.deepcopy.go": "package gen",
		"api.pb.go":                "package gen",
		"real.go":                  "package real",
	}
	for name, content := range generatedFiles {
		p := filepath.Join(repoDir, name)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repoDir, "git", "add", ".")
	gitRun(t, repoDir, "git", "commit", "-m", "add generated and real files")
	sha2 := getHEAD(t, repoDir)

	cacheStore, claimsDB := openTestDBs(t)
	stub := &stubExtractor{name: "test-ext", version: "0.1.0"}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	p := New(Config{
		Repo:     "test/repo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	result, err := p.Run(context.Background(), sha1, sha2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 3 generated files should be skipped, 1 real file extracted.
	if result.FilesSkipped != 3 {
		t.Errorf("FilesSkipped: got %d, want 3 (generated files)", result.FilesSkipped)
	}
	if result.FilesExtracted != 1 {
		t.Errorf("FilesExtracted: got %d, want 1 (real.go only)", result.FilesExtracted)
	}
	if result.ClaimsStored < 1 {
		t.Errorf("ClaimsStored: got %d, want >= 1", result.ClaimsStored)
	}
}

func TestRun_ChangedPathsPopulated(t *testing.T) {
	// Verify that ChangedPaths contains the relative paths of changed files.
	repoDir, sha1 := setupTestRepo(t, map[string]string{
		"a.go": "package a",
	})

	// Add two new files, modify existing one.
	if err := os.WriteFile(filepath.Join(repoDir, "a.go"), []byte("package a // v2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "b.go"), []byte("package b"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "c.go"), []byte("package c"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoDir, "git", "add", ".")
	gitRun(t, repoDir, "git", "commit", "-m", "add and modify files")
	sha2 := getHEAD(t, repoDir)

	cacheStore, claimsDB := openTestDBs(t)
	stub := &stubExtractor{name: "test-ext", version: "0.1.0"}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	p := New(Config{
		Repo:     "test/repo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	result, err := p.Run(context.Background(), sha1, sha2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(result.ChangedPaths) != 3 {
		t.Errorf("ChangedPaths length: got %d, want 3", len(result.ChangedPaths))
	}

	// Build a set for easy lookup.
	pathSet := make(map[string]bool, len(result.ChangedPaths))
	for _, p := range result.ChangedPaths {
		pathSet[p] = true
	}

	for _, want := range []string{"a.go", "b.go", "c.go"} {
		if !pathSet[want] {
			t.Errorf("ChangedPaths missing %q; got %v", want, result.ChangedPaths)
		}
	}
}

func TestRun_ChangedPathsExcludesDeleted(t *testing.T) {
	// Deleted files should NOT appear in ChangedPaths.
	repoDir, sha1 := setupTestRepo(t, map[string]string{
		"keep.go":   "package keep",
		"remove.go": "package remove",
	})

	// Modify keep.go and delete remove.go.
	if err := os.WriteFile(filepath.Join(repoDir, "keep.go"), []byte("package keep // v2"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoDir, "git", "rm", "remove.go")
	gitRun(t, repoDir, "git", "add", ".")
	gitRun(t, repoDir, "git", "commit", "-m", "modify and delete")
	sha2 := getHEAD(t, repoDir)

	cacheStore, claimsDB := openTestDBs(t)
	stub := &stubExtractor{name: "test-ext", version: "0.1.0"}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	p := New(Config{
		Repo:     "test/repo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	result, err := p.Run(context.Background(), sha1, sha2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(result.ChangedPaths) != 1 {
		t.Errorf("ChangedPaths length: got %d, want 1", len(result.ChangedPaths))
	}
	if len(result.ChangedPaths) > 0 && result.ChangedPaths[0] != "keep.go" {
		t.Errorf("ChangedPaths[0]: got %q, want %q", result.ChangedPaths[0], "keep.go")
	}
	if result.FilesDeleted != 1 {
		t.Errorf("FilesDeleted: got %d, want 1", result.FilesDeleted)
	}
}

func TestRun_FileReadError(t *testing.T) {
	// Create repo where a file exists in git but is missing from disk
	// at pipeline run time (simulating race condition).
	repoDir, sha1 := setupTestRepo(t, map[string]string{
		"a.go": "package a",
	})

	if err := os.WriteFile(filepath.Join(repoDir, "b.go"), []byte("package b"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoDir, "git", "add", ".")
	gitRun(t, repoDir, "git", "commit", "-m", "add b")
	sha2 := getHEAD(t, repoDir)

	// Remove b.go from disk (but it's still in the diff).
	os.Remove(filepath.Join(repoDir, "b.go"))

	cacheStore, claimsDB := openTestDBs(t)
	stub := &stubExtractor{name: "test-ext", version: "0.1.0"}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	p := New(Config{
		Repo:     "test/repo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	result, err := p.Run(context.Background(), sha1, sha2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should record the file read error but not fail the run.
	if len(result.Errors) != 1 {
		t.Errorf("expected 1 error, got %d: %+v", len(result.Errors), result.Errors)
	}
}
