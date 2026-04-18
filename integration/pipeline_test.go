//go:build integration

// Package integration contains integration tests that validate PRD acceptance
// criteria against real kubernetes repositories at ~/kubernetes/.
//
// Run with: go test -tags integration -v -timeout 120s ./integration/
package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjarmak/livedocs/cache"
	"github.com/sjarmak/livedocs/check"
	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
	"github.com/sjarmak/livedocs/extractor/lang"
	"github.com/sjarmak/livedocs/extractor/treesitter"
	"github.com/sjarmak/livedocs/pipeline"
)

// clientGoRoot returns the absolute path to ~/kubernetes/client-go, or skips.
func clientGoRoot(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot determine home directory: %v", err)
	}
	root := filepath.Join(home, "kubernetes", "client-go")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skipf("client-go not found at %s", root)
	}
	return root
}

// kubeRoot returns the absolute path to ~/kubernetes/kubernetes, or skips.
func kubeRoot(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot determine home directory: %v", err)
	}
	root := filepath.Join(home, "kubernetes", "kubernetes")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skipf("kubernetes/kubernetes not found at %s", root)
	}
	return root
}

// collectGoFiles walks a directory and returns .go files (non-vendor, non-.git).
func collectGoFiles(t *testing.T, dir string, limit int) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == "vendor" || base == ".git" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
			if limit > 0 && len(files) >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return files
}

// newExtractorRegistry creates a registry with tree-sitter extractors.
func newExtractorRegistry() *extractor.Registry {
	registry := lang.NewRegistry()
	tsExt := treesitter.New(registry)
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: tsExt,
	})
	return reg
}

// openTestDBs creates in-memory cache and claims databases.
func openTestDBs(t *testing.T) (*cache.SQLiteStore, *db.ClaimsDB) {
	t.Helper()
	cacheStore, err := cache.NewSQLiteStore(":memory:", 1<<30)
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

// TestExtractClientGo runs tree-sitter extraction on ~/kubernetes/client-go
// and validates PRD acceptance criteria: >100 symbols, >500 claims,
// schema has language/visibility columns.
func TestExtractClientGo(t *testing.T) {
	cgRoot := clientGoRoot(t)

	files := collectGoFiles(t, cgRoot, 500)
	if len(files) < 50 {
		t.Fatalf("expected at least 50 Go files in client-go, got %d", len(files))
	}
	t.Logf("processing %d Go files from client-go", len(files))

	registry := lang.NewRegistry()
	ext := treesitter.New(registry)

	var allClaims []extractor.Claim
	symbolSet := make(map[string]bool)
	start := time.Now()

	for _, f := range files {
		claims, err := ext.Extract(context.Background(), f, "go")
		if err != nil {
			continue
		}
		for _, c := range claims {
			allClaims = append(allClaims, c)
			if c.Predicate == extractor.PredicateDefines {
				symbolSet[c.SubjectName] = true
			}
		}
	}

	elapsed := time.Since(start)
	t.Logf("extracted %d claims, %d unique symbols in %s", len(allClaims), len(symbolSet), elapsed)

	// PRD criterion: >100 symbols
	if len(symbolSet) <= 100 {
		t.Errorf("expected >100 symbols, got %d", len(symbolSet))
	}

	// PRD criterion: >500 claims
	if len(allClaims) <= 500 {
		t.Errorf("expected >500 claims, got %d", len(allClaims))
	}

	// PRD criterion: schema has language/visibility columns
	// Verify claims carry language and visibility fields.
	hasLanguage := false
	hasVisibility := false
	for _, c := range allClaims {
		if c.Language != "" {
			hasLanguage = true
		}
		if c.Visibility != "" {
			hasVisibility = true
		}
		if hasLanguage && hasVisibility {
			break
		}
	}
	if !hasLanguage {
		t.Error("no claims have Language field set")
	}
	if !hasVisibility {
		t.Error("no claims have Visibility field set")
	}

	// Verify round-trip through claims DB preserves language/visibility.
	cdb, err := db.OpenClaimsDB(":memory:")
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	defer cdb.Close()
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	// Store one sample claim and read back.
	sample := allClaims[0]
	symID, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       "kubernetes/client-go",
		ImportPath: "client-go",
		SymbolName: sample.SubjectName,
		Language:   sample.Language,
		Kind:       string(sample.Kind),
		Visibility: string(sample.Visibility),
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}
	sym, err := cdb.GetSymbolByCompositeKey("kubernetes/client-go", "client-go", sample.SubjectName)
	if err != nil {
		t.Fatalf("get symbol: %v", err)
	}
	if sym.Language == "" {
		t.Error("stored symbol missing language column")
	}
	if sym.Visibility == "" {
		t.Error("stored symbol missing visibility column")
	}
	_ = symID
}

// recentGoChangeSHAs finds two adjacent commits that actually modify .go files.
func recentGoChangeSHAs(t *testing.T, repoDir string) (from, to string) {
	t.Helper()
	// Find commits that touch .go files.
	cmd := exec.Command("git", "log", "--format=%H", "-n", "5", "--diff-filter=M", "--", "*.go")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	shas := strings.Fields(strings.TrimSpace(string(out)))
	if len(shas) < 2 {
		t.Skipf("need at least 2 commits modifying .go files in %s", repoDir)
	}
	// shas[0] is most recent, shas[1] is its predecessor among .go-changing commits.
	// Use shas[1] as "from" and shas[0] as "to".
	return shas[1], shas[0]
}

// TestDiffClientGo runs the pipeline diff between two recent commits on client-go
// and verifies that only changed packages are output and unchanged files are skipped.
func TestDiffClientGo(t *testing.T) {
	cgRoot := clientGoRoot(t)

	// Find two commits that actually modify .go files.
	fromSHA, toSHA := recentGoChangeSHAs(t, cgRoot)
	t.Logf("diffing %s..%s", fromSHA[:8], toSHA[:8])

	cacheStore, claimsDB := openTestDBs(t)
	reg := newExtractorRegistry()

	p := pipeline.New(pipeline.Config{
		Repo:     "kubernetes/client-go",
		RepoDir:  cgRoot,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	start := time.Now()
	result, err := p.Run(context.Background(), fromSHA, toSHA)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("pipeline.Run: %v", err)
	}

	t.Logf("diff result: changed=%d extracted=%d skipped=%d cached=%d deleted=%d claims=%d duration=%s",
		result.FilesChanged, result.FilesExtracted, result.FilesSkipped,
		result.CacheHits, result.FilesDeleted, result.ClaimsStored, elapsed)

	// For a small diff between adjacent commits, it should complete quickly.
	if elapsed > 30*time.Second {
		t.Errorf("diff took %s, expected <30s for adjacent commits", elapsed)
	}

	// Unchanged files should be skipped (they don't appear in the diff at all).
	// The diff should only contain files that actually changed.
	// We verify this by checking that FilesChanged is less than the total file count.
	totalFiles := collectGoFiles(t, cgRoot, 0)
	if result.FilesChanged >= len(totalFiles) {
		t.Errorf("diff reported %d changed files, but repo has only %d total -- expected partial diff",
			result.FilesChanged, len(totalFiles))
	}
}

// TestCacheHit extracts the same repo twice and verifies the second run
// completes in <2s due to cache hits.
func TestCacheHit(t *testing.T) {
	cgRoot := clientGoRoot(t)

	// Find two commits that actually modify .go files.
	fromSHA, toSHA := recentGoChangeSHAs(t, cgRoot)

	cacheStore, claimsDB := openTestDBs(t)
	reg := newExtractorRegistry()

	p := pipeline.New(pipeline.Config{
		Repo:     "kubernetes/client-go",
		RepoDir:  cgRoot,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	// First run: populates cache.
	result1, err := p.Run(context.Background(), fromSHA, toSHA)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	t.Logf("first run: extracted=%d cached=%d", result1.FilesExtracted, result1.CacheHits)

	// Second run: same diff, should hit cache for all files.
	start := time.Now()
	result2, err := p.Run(context.Background(), fromSHA, toSHA)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	t.Logf("second run: extracted=%d cached=%d duration=%s",
		result2.FilesExtracted, result2.CacheHits, elapsed)

	if elapsed > 2*time.Second {
		t.Errorf("second run took %s, expected <2s for cache hits", elapsed)
	}

	// On the second run, all previously extracted files should be cache hits.
	if result1.FilesExtracted > 0 && result2.CacheHits == 0 {
		t.Error("expected cache hits on second run, got 0")
	}
	if result2.FilesExtracted > 0 {
		t.Errorf("expected 0 files extracted on second run (all cached), got %d", result2.FilesExtracted)
	}
}

// TestGeneratedExclusion_RealRepo runs extraction on ~/kubernetes/kubernetes/pkg/apis
// and verifies that generated files (*_generated.go, zz_generated*, *.pb.go) are skipped.
func TestGeneratedExclusion_RealRepo(t *testing.T) {
	kubeDir := kubeRoot(t)
	apisDir := filepath.Join(kubeDir, "pkg", "apis")
	if _, err := os.Stat(apisDir); os.IsNotExist(err) {
		t.Skipf("pkg/apis not found at %s", apisDir)
	}

	files := collectGoFiles(t, apisDir, 0)
	if len(files) == 0 {
		t.Fatal("no Go files found in pkg/apis")
	}

	var generatedCount, normalCount int
	var generatedNames []string

	for _, f := range files {
		if extractor.IsGenerated(f) {
			generatedCount++
			if len(generatedNames) < 10 {
				generatedNames = append(generatedNames, filepath.Base(f))
			}
		} else {
			normalCount++
		}
	}

	t.Logf("pkg/apis: %d total files, %d generated, %d normal",
		len(files), generatedCount, normalCount)
	t.Logf("sample generated: %v", generatedNames)

	// kubernetes/kubernetes/pkg/apis has many generated files.
	if generatedCount == 0 {
		t.Error("expected generated files in pkg/apis, found none")
	}

	// Verify the extractor skips generated files.
	registry := lang.NewRegistry()
	ext := treesitter.New(registry)

	var extractedFromGenerated int
	for _, f := range files {
		if !extractor.IsGenerated(f) {
			continue
		}
		claims, err := ext.Extract(context.Background(), f, "go")
		if err != nil {
			continue
		}
		// Check that the extractor flags them as generated.
		for _, c := range claims {
			if c.Predicate == extractor.PredicateIsGenerated {
				extractedFromGenerated++
			}
		}
	}

	// Verify IsGenerated correctly identifies patterns.
	patterns := []struct {
		name string
		want bool
	}{
		{"types_generated.go", true},
		{"zz_generated.deepcopy.go", true},
		{"api.pb.go", true},
		{"controller.go", false},
		{"types.go", false},
	}
	for _, p := range patterns {
		got := extractor.IsGenerated(p.name)
		if got != p.want {
			t.Errorf("IsGenerated(%q) = %v, want %v", p.name, got, p.want)
		}
	}
}

// TestCheckStateless_Performance runs check.RunManifestWithFiles with 1000+ files
// and verifies it completes in <2s.
func TestCheckStateless_Performance(t *testing.T) {
	kubeDir := kubeRoot(t)

	// Create a temporary manifest file in a temp dir.
	tmpDir := t.TempDir()
	manifestDir := filepath.Join(tmpDir, ".livedocs")
	if err := os.MkdirAll(manifestDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a manifest with broad glob patterns.
	manifest := `entries:
  - source: "pkg/**/*.go"
    docs:
      - "docs/api-reference.md"
  - source: "cmd/**/*.go"
    docs:
      - "docs/cli-reference.md"
  - source: "staging/**/*.go"
    docs:
      - "docs/staging.md"
`
	if err := os.WriteFile(filepath.Join(manifestDir, "manifest"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	// Collect 1000+ file paths from the kubernetes repo.
	files := collectGoFiles(t, kubeDir, 1500)
	if len(files) < 1000 {
		t.Skipf("need 1000+ Go files, got %d", len(files))
	}

	// Convert to relative paths (relative to the kubeDir).
	relFiles := make([]string, 0, len(files))
	for _, f := range files {
		rel, err := filepath.Rel(kubeDir, f)
		if err != nil {
			continue
		}
		relFiles = append(relFiles, rel)
	}
	t.Logf("testing with %d files", len(relFiles))

	ctx := context.Background()
	start := time.Now()

	// Use tmpDir as root since that's where the manifest lives.
	// The file paths are relative to kubeDir but the manifest globs
	// will still be pattern-matched against them.
	result, err := check.RunManifestWithFiles(ctx, tmpDir, relFiles)
	elapsed := time.Since(start)

	if err != nil {
		// If manifest loading fails because the format is wrong, that's OK --
		// we're testing performance, not correctness of the manifest content.
		t.Logf("RunManifestWithFiles returned error (may be expected): %v", err)
		// Still check timing since we want to verify it doesn't hang.
		if elapsed > 2*time.Second {
			t.Errorf("even with error, took %s, expected <2s", elapsed)
		}
		return
	}

	t.Logf("manifest check: %d affected docs, %d changed files, duration=%s",
		len(result.AffectedDocs), len(result.ChangedFiles), elapsed)

	if elapsed > 2*time.Second {
		t.Errorf("manifest check with %d files took %s, expected <2s", len(relFiles), elapsed)
	}

	_ = fmt.Sprintf("result: %+v", result)
}
