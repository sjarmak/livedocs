//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/live-docs/live_docs/pipeline"
	_ "modernc.org/sqlite"
)

// buildLivedocs compiles the livedocs binary into a temp directory and returns
// the path to the resulting executable. The binary is cleaned up when the test
// finishes.
func buildLivedocs(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "livedocs")

	projectRoot := projectRootDir(t)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/livedocs")
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build livedocs: %v\n%s", err, out)
	}
	return bin
}

// projectRootDir returns the absolute path to the live_docs project root.
func projectRootDir(t *testing.T) string {
	t.Helper()
	// The integration/ directory is one level below the project root.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// If we're in integration/, go up one level.
	if filepath.Base(wd) == "integration" {
		return filepath.Dir(wd)
	}
	// Otherwise assume we're at the project root.
	return wd
}

// createTempGitRepo creates a temporary directory with an initialized git
// repository and returns its path. The repo is configured with a test user
// for commits.
func createTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	commands := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git setup %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// gitCommitAll stages all files in the repo directory and creates a commit
// with the given message. Returns the commit SHA.
func gitCommitAll(t *testing.T, repoDir, message string) string {
	t.Helper()

	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	cmd = exec.Command("git", "commit", "-m", message)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// writeFile creates a file at the given path (relative to dir) with the given
// content, creating parent directories as needed.
func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	absPath := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

// diffReport mirrors the JSON output of `livedocs diff --format json`.
type diffReport struct {
	FromCommit     string   `json:"from_commit"`
	ToCommit       string   `json:"to_commit"`
	FilesChanged   int      `json:"files_changed"`
	FilesExtracted int      `json:"files_extracted"`
	FilesDeleted   int      `json:"files_deleted"`
	CacheHits      int      `json:"cache_hits"`
	ClaimsStored   int      `json:"claims_stored"`
	Duration       string   `json:"duration"`
	ChangedFiles   []string `json:"changed_files"`
	DeletedFiles   []string `json:"deleted_files"`
}

// queryClaimCountByFile opens a claims DB and counts claims whose source_file
// contains the given path substring. The Go deep extractor stores absolute paths,
// so we use LIKE to match both absolute and relative paths.
func queryClaimCountByFile(t *testing.T, dbPath, sourceFile string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db %s: %v", dbPath, err)
	}
	defer db.Close()

	var count int
	pattern := "%" + sourceFile
	err = db.QueryRow("SELECT COUNT(*) FROM claims WHERE source_file LIKE ?", pattern).Scan(&count)
	if err != nil {
		t.Fatalf("query claims for %s: %v", sourceFile, err)
	}
	return count
}

// queryTotalClaims returns the total number of claims in the DB.
func queryTotalClaims(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db %s: %v", dbPath, err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM claims").Scan(&count)
	if err != nil {
		t.Fatalf("query total claims: %v", err)
	}
	return count
}

// querySymbolNames returns all distinct symbol names in the DB.
func querySymbolNames(t *testing.T, dbPath string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db %s: %v", dbPath, err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT DISTINCT symbol_name FROM symbols ORDER BY symbol_name")
	if err != nil {
		t.Fatalf("query symbol names: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan symbol name: %v", err)
		}
		names = append(names, name)
	}
	return names
}

// Go source file templates for the test repo.
const goFileA = `package a

// Hello greets the caller.
func Hello() string {
	return "hello"
}

// Config holds settings.
type Config struct {
	Name string
	Port int
}
`

const goFileAModified = `package a

// HelloWorld greets the entire world.
func HelloWorld() string {
	return "hello world"
}

// Goodbye says farewell.
func Goodbye() string {
	return "goodbye"
}

// Config holds settings.
type Config struct {
	Name    string
	Port    int
	Verbose bool
}
`

const goFileB = `package b

// Process performs the main processing.
func Process(input string) (string, error) {
	if input == "" {
		return "", nil
	}
	return input + " processed", nil
}

// Result holds processing output.
type Result struct {
	Output string
	OK     bool
}
`

// TestE2EIncrementalPipeline validates the full incremental pipeline:
// create a temp git repo, commit files, extract claims, modify files,
// run diff to show only changed packages, re-extract, verify cache hits,
// test deletion-aware reconciliation, and run verify-claims.
func TestE2EIncrementalPipeline(t *testing.T) {
	bin := buildLivedocs(t)
	repoDir := createTempGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.claims.db")

	t.Logf("livedocs binary: %s", bin)
	t.Logf("temp repo: %s", repoDir)
	t.Logf("claims db: %s", dbPath)

	// --- Phase A: Initial commit + extract ---
	t.Run("InitialExtract", func(t *testing.T) {
		// Create go.mod so go/packages can load the module.
		writeFile(t, repoDir, "go.mod", "module example.com/testrepo\n\ngo 1.21\n")
		writeFile(t, repoDir, "pkg/a/a.go", goFileA)

		commit1 := gitCommitAll(t, repoDir, "initial: add file A")
		t.Logf("commit1: %s", commit1)

		// Run livedocs extract.
		cmd := exec.Command(bin, "extract", repoDir, "--repo", "testrepo", "-o", dbPath)
		out, err := cmd.CombinedOutput()
		t.Logf("extract output:\n%s", out)
		if err != nil {
			t.Fatalf("livedocs extract failed: %v\n%s", err, out)
		}

		// Verify claims were extracted.
		total := queryTotalClaims(t, dbPath)
		t.Logf("total claims after initial extract: %d", total)
		if total == 0 {
			t.Fatal("expected claims after initial extraction, got 0")
		}

		// Verify symbols include Hello and Config.
		symbols := querySymbolNames(t, dbPath)
		t.Logf("symbols: %v", symbols)

		symbolSet := make(map[string]bool)
		for _, s := range symbols {
			symbolSet[s] = true
		}
		for _, want := range []string{"Hello", "Config"} {
			if !symbolSet[want] {
				t.Errorf("expected symbol %q in DB, not found (have: %v)", want, symbols)
			}
		}
	})

	// --- Phase B: Modify file A + add file B, run diff ---
	var commit1, commit2 string
	t.Run("DiffChangedPackages", func(t *testing.T) {
		// Get current HEAD as commit1 for the diff.
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = repoDir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git rev-parse: %v", err)
		}
		commit1 = strings.TrimSpace(string(out))

		// Modify file A and add file B.
		writeFile(t, repoDir, "pkg/a/a.go", goFileAModified)
		writeFile(t, repoDir, "pkg/b/b.go", goFileB)

		commit2 = gitCommitAll(t, repoDir, "modify a, add b")
		t.Logf("diff %s..%s", commit1[:8], commit2[:8])

		// Run livedocs diff in JSON mode.
		cmd = exec.Command(bin, "diff", commit1, commit2, repoDir, "--repo", "testrepo", "--format", "json")
		out, err = cmd.CombinedOutput()
		t.Logf("diff output:\n%s", out)
		if err != nil {
			t.Fatalf("livedocs diff failed: %v\n%s", err, out)
		}

		// Parse the JSON report.
		var report diffReport
		if err := json.Unmarshal(out, &report); err != nil {
			t.Fatalf("parse diff JSON: %v\nraw: %s", err, out)
		}

		t.Logf("diff report: changed=%d extracted=%d deleted=%d",
			report.FilesChanged, report.FilesExtracted, report.FilesDeleted)

		// Verify changed files include both a.go and b.go.
		changedSet := make(map[string]bool)
		for _, f := range report.ChangedFiles {
			changedSet[f] = true
		}
		if !changedSet["pkg/a/a.go"] {
			t.Errorf("expected pkg/a/a.go in changed files, got: %v", report.ChangedFiles)
		}
		if !changedSet["pkg/b/b.go"] {
			t.Errorf("expected pkg/b/b.go in changed files, got: %v", report.ChangedFiles)
		}

		// Verify go.mod is NOT in changed files (it was not modified).
		if changedSet["go.mod"] {
			t.Error("go.mod should not be in changed files (it was not modified)")
		}

		// Verify at least some files were reported as changed.
		if report.FilesChanged == 0 {
			t.Error("expected FilesChanged > 0")
		}
	})

	// --- Phase C: Cache hit verification ---
	t.Run("CacheHitVerification", func(t *testing.T) {
		if commit1 == "" || commit2 == "" {
			t.Skip("no commits from DiffChangedPackages")
		}

		// Run the same diff twice. The diff command uses in-memory cache per
		// run, so we measure wall-clock time instead. A second run on the same
		// commits should be roughly the same speed (both are cache-cold for
		// in-memory cache). To properly test cache hits, we use the pipeline
		// API directly below.
		//
		// First run (warm-up / baseline):
		start1 := time.Now()
		cmd := exec.Command(bin, "diff", commit1, commit2, repoDir, "--repo", "testrepo", "--format", "json")
		out1, err := cmd.CombinedOutput()
		elapsed1 := time.Since(start1)
		if err != nil {
			t.Fatalf("first diff run: %v\n%s", err, out1)
		}

		// Second run:
		start2 := time.Now()
		cmd = exec.Command(bin, "diff", commit1, commit2, repoDir, "--repo", "testrepo", "--format", "json")
		out2, err := cmd.CombinedOutput()
		elapsed2 := time.Since(start2)
		if err != nil {
			t.Fatalf("second diff run: %v\n%s", err, out2)
		}

		t.Logf("diff run 1: %s, run 2: %s", elapsed1, elapsed2)

		// Both runs should complete in reasonable time (< 60s each).
		if elapsed1 > 60*time.Second {
			t.Errorf("first diff run took %s, expected < 60s", elapsed1)
		}
		if elapsed2 > 60*time.Second {
			t.Errorf("second diff run took %s, expected < 60s", elapsed2)
		}

		// Now test cache hits via the pipeline Go API for a definitive check.
		t.Run("PipelineAPICacheHit", func(t *testing.T) {
			testPipelineCacheHit(t, repoDir, commit1, commit2)
		})
	})

	// --- Phase D: Deletion-aware reconciliation ---
	t.Run("DeletionReconciliation", func(t *testing.T) {
		if commit2 == "" {
			t.Skip("no commit2")
		}

		// Delete file A.
		if err := os.Remove(filepath.Join(repoDir, "pkg/a/a.go")); err != nil {
			t.Fatalf("remove a.go: %v", err)
		}

		commit3 := gitCommitAll(t, repoDir, "delete file A")
		t.Logf("commit3 (deletion): %s", commit3)

		// Run diff to detect deletion.
		cmd := exec.Command(bin, "diff", commit2, commit3, repoDir, "--repo", "testrepo", "--format", "json")
		out, err := cmd.CombinedOutput()
		t.Logf("deletion diff output:\n%s", out)
		if err != nil {
			t.Fatalf("deletion diff failed: %v\n%s", err, out)
		}

		var report diffReport
		if err := json.Unmarshal(out, &report); err != nil {
			t.Fatalf("parse deletion diff JSON: %v\nraw: %s", err, out)
		}

		// Verify pkg/a/a.go appears in deleted files.
		deletedSet := make(map[string]bool)
		for _, f := range report.DeletedFiles {
			deletedSet[f] = true
		}
		if !deletedSet["pkg/a/a.go"] {
			t.Errorf("expected pkg/a/a.go in deleted files, got: %v", report.DeletedFiles)
		}
		if report.FilesDeleted == 0 {
			t.Error("expected FilesDeleted > 0")
		}

		// Re-extract from current state (after deletion).
		// Remove old DB first since extract removes it anyway.
		dbPath2 := filepath.Join(t.TempDir(), "post-delete.claims.db")
		cmd = exec.Command(bin, "extract", repoDir, "--repo", "testrepo", "-o", dbPath2)
		out, err = cmd.CombinedOutput()
		t.Logf("post-deletion extract output:\n%s", out)
		if err != nil {
			t.Fatalf("post-deletion extract failed: %v\n%s", err, out)
		}

		// Query the new DB: no claims should reference pkg/a/a.go.
		claimsForA := queryClaimCountByFile(t, dbPath2, "pkg/a/a.go")
		t.Logf("claims for deleted pkg/a/a.go: %d", claimsForA)
		if claimsForA > 0 {
			t.Errorf("expected 0 claims for deleted file pkg/a/a.go, got %d", claimsForA)
		}

		// Verify that pkg/b/b.go still has claims.
		claimsForB := queryClaimCountByFile(t, dbPath2, "pkg/b/b.go")
		t.Logf("claims for pkg/b/b.go: %d", claimsForB)
		if claimsForB == 0 {
			t.Error("expected claims for pkg/b/b.go after deletion of A, got 0")
		}

		// Verify symbols from file B are still present.
		symbols := querySymbolNames(t, dbPath2)
		t.Logf("symbols after deletion: %v", symbols)
		symbolSet := make(map[string]bool)
		for _, s := range symbols {
			symbolSet[s] = true
		}
		if !symbolSet["Process"] {
			t.Error("expected symbol Process from file B to survive deletion of A")
		}
	})

	// --- Phase E: verify-claims ---
	t.Run("VerifyClaims", func(t *testing.T) {
		// Backdate all source files by 5 seconds so that last_verified
		// (stored during extraction) is strictly after every file's mtime.
		// This avoids sub-second timing races where mtime nanoseconds
		// exceed the second-precision last_verified timestamp.
		past := time.Now().Add(-5 * time.Second)
		filepath.Walk(repoDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			_ = os.Chtimes(path, past, past)
			return nil
		})

		// Extract fresh DB from current state for verify-claims.
		dbPath3 := filepath.Join(t.TempDir(), "verify.claims.db")
		cmd := exec.Command(bin, "extract", repoDir, "--repo", "testrepo", "-o", dbPath3)
		out, err := cmd.CombinedOutput()
		t.Logf("verify extract output:\n%s", out)
		if err != nil {
			t.Fatalf("extract for verify: %v\n%s", err, out)
		}

		// Run verify-claims.
		cmd = exec.Command(bin, "verify-claims", "--db", dbPath3, repoDir)
		out, err = cmd.CombinedOutput()
		t.Logf("verify-claims output:\n%s", out)

		// verify-claims should exit 0 (all claims consistent).
		if err != nil {
			t.Errorf("verify-claims failed (expected exit 0): %v\n%s", err, out)
		}
	})
}

// testPipelineCacheHit uses the pipeline Go API to definitively verify that
// a second extraction of unchanged files results in cache hits.
func testPipelineCacheHit(t *testing.T, repoDir, fromCommit, toCommit string) {
	t.Helper()

	cacheStore, claimsDB := openTestDBs(t)
	reg := newExtractorRegistry()

	p := pipeline.New(pipeline.Config{
		Repo:     "testrepo",
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: reg,
	})

	// First run: populates cache.
	result1, err := p.Run(context.Background(), fromCommit, toCommit)
	if err != nil {
		t.Fatalf("pipeline first run: %v", err)
	}
	t.Logf("pipeline run 1: extracted=%d cached=%d claims=%d",
		result1.FilesExtracted, result1.CacheHits, result1.ClaimsStored)

	// Second run: same commits, should hit cache.
	start := time.Now()
	result2, err := p.Run(context.Background(), fromCommit, toCommit)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("pipeline second run: %v", err)
	}

	t.Logf("pipeline run 2: extracted=%d cached=%d duration=%s",
		result2.FilesExtracted, result2.CacheHits, elapsed)

	// On the second run, previously extracted files should be cache hits.
	if result1.FilesExtracted > 0 && result2.CacheHits == 0 {
		t.Error("expected cache hits on second run, got 0")
	}
	if result2.FilesExtracted > 0 {
		t.Errorf("expected 0 files extracted on second run (all cached), got %d", result2.FilesExtracted)
	}

	// Second run should complete faster than 5 seconds.
	if elapsed > 5*time.Second {
		t.Errorf("second run took %s, expected < 5s for cached results", elapsed)
	}
}
