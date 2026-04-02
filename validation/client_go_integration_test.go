// Package validation contains integration tests that validate the extraction
// pipeline against the real kubernetes/client-go staging module.
//
// These tests require the kubernetes corpus at ~/kubernetes/kubernetes/.
// They are skipped automatically when the corpus is not available.
package validation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/live-docs/live_docs/cache"
	"github.com/live-docs/live_docs/extractor"
	"github.com/live-docs/live_docs/extractor/goextractor"
	"github.com/live-docs/live_docs/extractor/lang"
	"github.com/live-docs/live_docs/extractor/treesitter"
	"github.com/live-docs/live_docs/versionnorm"
)

const (
	clientGoStagingDir = "staging/src/k8s.io/client-go"
	kubeRepoDir        = "kubernetes/kubernetes"
	clientGoRestDir    = "staging/src/k8s.io/client-go/rest"
	repo               = "kubernetes/kubernetes"
)

// kubeRoot returns the absolute path to the kubernetes repo, or skips the test.
func kubeRoot(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot determine home directory: %v", err)
	}
	root := filepath.Join(home, kubeRepoDir)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skipf("kubernetes corpus not found at %s", root)
	}
	return root
}

// clientGoDir returns the absolute path to client-go staging dir, or skips.
func clientGoDir(t *testing.T) string {
	t.Helper()
	root := kubeRoot(t)
	dir := filepath.Join(root, clientGoStagingDir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("client-go staging dir not found at %s", dir)
	}
	return dir
}

// collectGoFiles walks a directory and returns all .go file paths (non-vendor).
func collectGoFiles(t *testing.T, dir string, limit int) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable
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

// TestTreeSitterExtractor_ClientGo runs the tree-sitter extractor on a sample
// of client-go Go files and validates claim structure and performance.
func TestTreeSitterExtractor_ClientGo(t *testing.T) {
	cgDir := clientGoDir(t)

	// Collect up to 200 files for a representative sample.
	files := collectGoFiles(t, cgDir, 200)
	if len(files) == 0 {
		t.Fatal("no Go files found in client-go")
	}
	t.Logf("tree-sitter: processing %d Go files from client-go", len(files))

	registry := lang.NewRegistry()
	ext := treesitter.New(registry)

	var allClaims []extractor.Claim
	start := time.Now()

	for _, f := range files {
		claims, err := ext.Extract(context.Background(), f, "go")
		if err != nil {
			// Some files may fail (e.g., CGO). Log and continue.
			t.Logf("tree-sitter: skip %s: %v", filepath.Base(f), err)
			continue
		}
		allClaims = append(allClaims, claims...)
	}

	elapsed := time.Since(start)
	t.Logf("tree-sitter: %d claims from %d files in %s (%.1f files/sec)",
		len(allClaims), len(files), elapsed, float64(len(files))/elapsed.Seconds())

	// Validate: must have claims.
	if len(allClaims) == 0 {
		t.Fatal("tree-sitter produced zero claims")
	}

	// Check predicate distribution.
	predicateCounts := countByPredicate(allClaims)
	t.Logf("tree-sitter predicate distribution: %v", predicateCounts)

	// Tree-sitter must produce defines claims.
	if predicateCounts[extractor.PredicateDefines] == 0 {
		t.Error("tree-sitter: expected defines claims")
	}

	// Tree-sitter must NOT produce deep-only predicates.
	for _, pred := range []extractor.Predicate{
		extractor.PredicateHasKind,
		extractor.PredicateImplements,
		extractor.PredicateHasSignature,
		extractor.PredicateEncloses,
	} {
		if predicateCounts[pred] > 0 {
			t.Errorf("tree-sitter: should not emit deep-only predicate %q, got %d", pred, predicateCounts[pred])
		}
	}

	// All claims should be tree-sitter safe.
	if err := extractor.ValidateTreeSitterClaims(allClaims); err != nil {
		t.Errorf("tree-sitter: predicate boundary violation: %v", err)
	}
}

// TestGoDeepExtractor_ClientGoRest runs the Go deep extractor on the client-go/rest
// package and validates richer claim output.
func TestGoDeepExtractor_ClientGoRest(t *testing.T) {
	root := kubeRoot(t)
	restDir := filepath.Join(root, clientGoRestDir)
	if _, err := os.Stat(restDir); os.IsNotExist(err) {
		t.Skipf("client-go/rest dir not found at %s", restDir)
	}

	ext := &goextractor.GoDeepExtractor{
		Repo:       repo,
		ModulePath: "k8s.io/client-go",
	}

	start := time.Now()
	claims, err := ext.Extract(context.Background(), restDir, "go")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("go-deep: Extract failed on client-go/rest: %v", err)
	}

	t.Logf("go-deep: %d claims from client-go/rest in %s", len(claims), elapsed)

	if len(claims) == 0 {
		t.Fatal("go-deep produced zero claims")
	}

	// Validate all claims.
	var invalidCount int
	for i, c := range claims {
		if err := c.Validate(); err != nil {
			if invalidCount < 5 {
				t.Logf("go-deep: claim %d invalid: %v (subject=%q pred=%q)", i, err, c.SubjectName, c.Predicate)
			}
			invalidCount++
		}
	}
	if invalidCount > 0 {
		t.Logf("go-deep: %d/%d claims failed validation", invalidCount, len(claims))
	}

	// Check predicate distribution.
	predicateCounts := countByPredicate(claims)
	t.Logf("go-deep predicate distribution: %v", predicateCounts)

	// Deep extractor MUST produce deep-only predicates.
	if predicateCounts[extractor.PredicateHasKind] == 0 {
		t.Error("go-deep: expected has_kind claims")
	}
	if predicateCounts[extractor.PredicateHasSignature] == 0 {
		t.Error("go-deep: expected has_signature claims")
	}
	if predicateCounts[extractor.PredicateEncloses] == 0 {
		t.Error("go-deep: expected encloses claims")
	}

	// Implements may or may not appear depending on interfaces in rest/.
	t.Logf("go-deep: implements claims = %d", predicateCounts[extractor.PredicateImplements])

	// Deep extractor should also produce the tree-sitter-safe predicates.
	if predicateCounts[extractor.PredicateDefines] == 0 {
		t.Error("go-deep: expected defines claims")
	}
	if predicateCounts[extractor.PredicateImports] == 0 {
		t.Error("go-deep: expected imports claims (rest/ imports many packages)")
	}
}

// TestDeepExtractorRicherThanTreeSitter compares the predicate sets from both
// extractors on the same package to confirm the deep extractor finds more.
func TestDeepExtractorRicherThanTreeSitter(t *testing.T) {
	root := kubeRoot(t)
	restDir := filepath.Join(root, clientGoRestDir)
	if _, err := os.Stat(restDir); os.IsNotExist(err) {
		t.Skipf("client-go/rest dir not found at %s", restDir)
	}

	// Run deep extractor.
	deepExt := &goextractor.GoDeepExtractor{
		Repo:       repo,
		ModulePath: "k8s.io/client-go",
	}
	deepClaims, err := deepExt.Extract(context.Background(), restDir, "go")
	if err != nil {
		t.Fatalf("go-deep: Extract failed: %v", err)
	}

	// Run tree-sitter on the same files.
	registry := lang.NewRegistry()
	tsExt := treesitter.New(registry)
	files := collectGoFiles(t, restDir, 0)

	var tsClaims []extractor.Claim
	for _, f := range files {
		claims, err := tsExt.Extract(context.Background(), f, "go")
		if err != nil {
			continue
		}
		tsClaims = append(tsClaims, claims...)
	}

	deepPreds := countByPredicate(deepClaims)
	tsPreds := countByPredicate(tsClaims)

	t.Logf("Comparison on client-go/rest:")
	t.Logf("  deep extractor: %d total claims, %d unique predicates", len(deepClaims), len(deepPreds))
	t.Logf("  tree-sitter:    %d total claims, %d unique predicates", len(tsClaims), len(tsPreds))
	t.Logf("  deep predicates: %v", deepPreds)
	t.Logf("  ts predicates:   %v", tsPreds)

	// Deep extractor must produce strictly more predicate types.
	if len(deepPreds) <= len(tsPreds) {
		t.Errorf("deep extractor should have more predicate types (%d) than tree-sitter (%d)",
			len(deepPreds), len(tsPreds))
	}

	// Deep extractor must have these predicates that tree-sitter cannot produce.
	for _, pred := range []extractor.Predicate{
		extractor.PredicateHasKind,
		extractor.PredicateHasSignature,
		extractor.PredicateEncloses,
	} {
		if deepPreds[pred] == 0 {
			t.Errorf("deep extractor missing deep-only predicate %q", pred)
		}
		if tsPreds[pred] > 0 {
			t.Errorf("tree-sitter should not have predicate %q", pred)
		}
	}
}

// TestVersionNormalization_RealGoMod uses the actual kubernetes go.mod to verify
// staging path resolution for client-go.
func TestVersionNormalization_RealGoMod(t *testing.T) {
	root := kubeRoot(t)

	// Run go mod edit -json to get structured replace directives.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "mod", "edit", "-json")
	cmd.Dir = root
	jsonData, err := cmd.Output()
	if err != nil {
		t.Fatalf("go mod edit -json failed: %v", err)
	}

	sm, err := versionnorm.BuildStagingMap(jsonData)
	if err != nil {
		t.Fatalf("BuildStagingMap failed: %v", err)
	}

	t.Logf("Staging map has %d entries", len(sm))

	// client-go must be in the staging map.
	clientGoLocal, ok := sm["k8s.io/client-go"]
	if !ok {
		t.Fatal("k8s.io/client-go not found in staging map")
	}
	if clientGoLocal != "./staging/src/k8s.io/client-go" {
		t.Errorf("client-go staging path = %q, want %q", clientGoLocal, "./staging/src/k8s.io/client-go")
	}

	// Build normalizer and test resolution.
	normalizer := versionnorm.NewNormalizer(sm)

	// IsStagingModule tests.
	if !normalizer.IsStagingModule("k8s.io/client-go") {
		t.Error("k8s.io/client-go should be a staging module")
	}
	if !normalizer.IsStagingModule("k8s.io/client-go/rest") {
		t.Error("k8s.io/client-go/rest should be a staging module (subpackage)")
	}
	if normalizer.IsStagingModule("github.com/google/go-cmp") {
		t.Error("github.com/google/go-cmp should NOT be a staging module")
	}

	// ResolveStagingPath tests.
	canonical, ok := normalizer.ResolveStagingPath("./staging/src/k8s.io/client-go")
	if !ok {
		t.Fatal("ResolveStagingPath should resolve client-go staging path")
	}
	if canonical != "k8s.io/client-go" {
		t.Errorf("ResolveStagingPath = %q, want %q", canonical, "k8s.io/client-go")
	}

	canonical, ok = normalizer.ResolveStagingPath("./staging/src/k8s.io/client-go/rest")
	if !ok {
		t.Fatal("ResolveStagingPath should resolve client-go/rest subpackage")
	}
	if canonical != "k8s.io/client-go/rest" {
		t.Errorf("ResolveStagingPath = %q, want %q", canonical, "k8s.io/client-go/rest")
	}

	// CanonicalImportPath strips versions.
	cip := normalizer.CanonicalImportPath("k8s.io/client-go@v0.0.0-20260324094416-91061ea648b7")
	if cip != "k8s.io/client-go" {
		t.Errorf("CanonicalImportPath = %q, want %q", cip, "k8s.io/client-go")
	}

	cip = normalizer.CanonicalImportPath("k8s.io/client-go@v0.28.0/kubernetes")
	if cip != "k8s.io/client-go/kubernetes" {
		t.Errorf("CanonicalImportPath = %q, want %q", cip, "k8s.io/client-go/kubernetes")
	}

	// Verify that a significant number of kubernetes staging modules are in the map.
	if len(sm) < 20 {
		t.Errorf("expected at least 20 staging modules, got %d", len(sm))
	}

	t.Logf("Sample staging modules:")
	count := 0
	for mod, path := range sm {
		if count >= 10 {
			break
		}
		t.Logf("  %s -> %s", mod, path)
		count++
	}
}

// TestCacheHitMiss validates that the content-hash cache correctly reports
// hits and misses for client-go files.
func TestCacheHitMiss(t *testing.T) {
	cgDir := clientGoDir(t)

	// Pick 5 Go files for the test.
	files := collectGoFiles(t, cgDir, 5)
	if len(files) < 3 {
		t.Skipf("need at least 3 Go files, got %d", len(files))
	}

	// Create in-memory cache store.
	store, err := cache.NewSQLiteStore(":memory:", 100*1024*1024) // 100 MB cap
	if err != nil {
		t.Fatalf("creating cache store: %v", err)
	}
	defer store.Close()

	extractorVersion := "0.1.0"
	grammarVersion := "0.12.2"

	// Phase 1: All files should be cache misses.
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		hash := sha256Hex(content)
		relPath, _ := filepath.Rel(cgDir, f)

		hit, err := store.Hit(repo, relPath, hash, extractorVersion, grammarVersion)
		if err != nil {
			t.Fatalf("cache.Hit failed: %v", err)
		}
		if hit {
			t.Errorf("expected cache miss for %s on first check", relPath)
		}

		// Simulate extraction by putting the entry.
		err = store.Put(cache.Entry{
			Repo:             repo,
			RelativePath:     relPath,
			ContentHash:      hash,
			ExtractorVersion: extractorVersion,
			GrammarVersion:   grammarVersion,
			LastIndexed:      time.Now().UTC(),
			SizeBytes:        int64(len(content)),
		})
		if err != nil {
			t.Fatalf("cache.Put failed: %v", err)
		}
	}

	// Phase 2: Same files, same content = cache hits.
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		hash := sha256Hex(content)
		relPath, _ := filepath.Rel(cgDir, f)

		hit, err := store.Hit(repo, relPath, hash, extractorVersion, grammarVersion)
		if err != nil {
			t.Fatalf("cache.Hit failed: %v", err)
		}
		if !hit {
			t.Errorf("expected cache hit for %s on second check", relPath)
		}
	}

	// Phase 3: Different extractor version = cache miss.
	for _, f := range files[:1] {
		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		hash := sha256Hex(content)
		relPath, _ := filepath.Rel(cgDir, f)

		hit, err := store.Hit(repo, relPath, hash, "0.2.0", grammarVersion)
		if err != nil {
			t.Fatalf("cache.Hit failed: %v", err)
		}
		if hit {
			t.Errorf("expected cache miss for %s with different extractor version", relPath)
		}
	}

	// Phase 4: Different content hash = cache miss.
	for _, f := range files[:1] {
		relPath, _ := filepath.Rel(cgDir, f)

		hit, err := store.Hit(repo, relPath, "deadbeef", extractorVersion, grammarVersion)
		if err != nil {
			t.Fatalf("cache.Hit failed: %v", err)
		}
		if hit {
			t.Errorf("expected cache miss for %s with different content hash", relPath)
		}
	}

	// Verify total size.
	totalSize, err := store.TotalSize()
	if err != nil {
		t.Fatalf("TotalSize failed: %v", err)
	}
	if totalSize == 0 {
		t.Error("expected non-zero total cache size after puts")
	}
	t.Logf("cache total size: %d bytes for %d files", totalSize, len(files))
}

// TestCacheReconciliation tests that the cache correctly identifies changed files
// when the file set changes.
func TestCacheReconciliation(t *testing.T) {
	cgDir := clientGoDir(t)

	files := collectGoFiles(t, cgDir, 10)
	if len(files) < 5 {
		t.Skipf("need at least 5 Go files, got %d", len(files))
	}

	store, err := cache.NewSQLiteStore(":memory:", 100*1024*1024)
	if err != nil {
		t.Fatalf("creating cache store: %v", err)
	}
	defer store.Close()

	// Populate cache with all files.
	fileHashes := make(map[string]string)
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		relPath, _ := filepath.Rel(cgDir, f)
		hash := sha256Hex(content)
		fileHashes[relPath] = hash

		err = store.Put(cache.Entry{
			Repo:             repo,
			RelativePath:     relPath,
			ContentHash:      hash,
			ExtractorVersion: "0.1.0",
			GrammarVersion:   "0.12.2",
			LastIndexed:      time.Now().UTC(),
			SizeBytes:        int64(len(content)),
		})
		if err != nil {
			t.Fatalf("cache.Put failed: %v", err)
		}
	}

	// Reconcile with unchanged file set = 0 changes.
	changed, err := store.Reconcile(repo, fileHashes)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("expected 0 changed files with same hashes, got %d", len(changed))
	}

	// Reconcile with one modified file hash.
	modifiedHashes := make(map[string]string)
	for k, v := range fileHashes {
		modifiedHashes[k] = v
	}
	for k := range modifiedHashes {
		modifiedHashes[k] = "modified-hash"
		break
	}

	changed, err = store.Reconcile(repo, modifiedHashes)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if len(changed) != 1 {
		t.Errorf("expected 1 changed file after modifying hash, got %d", len(changed))
	}

	// Reconcile with a removed file = should tombstone it.
	reducedHashes := make(map[string]string)
	first := true
	for k, v := range fileHashes {
		if first {
			first = false
			continue
		}
		reducedHashes[k] = v
	}

	changed, err = store.Reconcile(repo, reducedHashes)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("expected 0 changed files (removed file is tombstoned, not changed), got %d", len(changed))
	}
}

// --- helpers ---

func countByPredicate(claims []extractor.Claim) map[extractor.Predicate]int {
	m := make(map[extractor.Predicate]int)
	for _, c := range claims {
		m[c.Predicate]++
	}
	return m
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
