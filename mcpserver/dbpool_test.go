package mcpserver

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// createTestDBDir creates a temp directory with empty .claims.db files for the given repo names.
func createTestDBDir(t *testing.T, repos []string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range repos {
		path := filepath.Join(dir, name+claimsDBSuffix)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("create test db file %s: %v", path, err)
		}
	}
	return dir
}

func TestDBPool_Manifest(t *testing.T) {
	repos := []string{"api", "kubernetes", "website"}
	dir := createTestDBDir(t, repos)

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	got, err := pool.Manifest()
	if err != nil {
		t.Fatalf("Manifest() error: %v", err)
	}

	sort.Strings(got)
	sort.Strings(repos)

	if len(got) != len(repos) {
		t.Fatalf("Manifest() returned %d repos, want %d", len(got), len(repos))
	}
	for i, name := range repos {
		if got[i] != name {
			t.Errorf("Manifest()[%d] = %q, want %q", i, got[i], name)
		}
	}
}

func TestDBPool_ManifestEmptyDir(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	got, err := pool.Manifest()
	if err != nil {
		t.Fatalf("Manifest() error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Manifest() returned %d repos for empty dir, want 0", len(got))
	}
}

func TestDBPool_ManifestDoesNotOpenDBs(t *testing.T) {
	repos := []string{"alpha", "beta"}
	dir := createTestDBDir(t, repos)

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	_, err := pool.Manifest()
	if err != nil {
		t.Fatalf("Manifest() error: %v", err)
	}

	if pool.Len() != 0 {
		t.Errorf("Manifest() opened %d connections, want 0", pool.Len())
	}
}

func TestDBPool_OpenLazy(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	// Open creates the DB file lazily via modernc.org/sqlite.
	cdb, err := pool.Open("testproject")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	if cdb == nil {
		t.Fatal("Open() returned nil ClaimsDB")
	}
	if pool.Len() != 1 {
		t.Errorf("pool.Len() = %d after Open, want 1", pool.Len())
	}
}

func TestDBPool_OpenCached(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	cdb1, err := pool.Open("myrepo")
	if err != nil {
		t.Fatalf("first Open() error: %v", err)
	}

	cdb2, err := pool.Open("myrepo")
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}

	if cdb1 != cdb2 {
		t.Error("Open() returned different instances for the same repo, expected cached")
	}

	if pool.Len() != 1 {
		t.Errorf("pool.Len() = %d, want 1 (should not duplicate)", pool.Len())
	}
}

func TestDBPool_LRUEviction(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, 2) // max 2

	// Open 2 repos — fills capacity.
	_, err := pool.Open("repo-a")
	if err != nil {
		t.Fatalf("Open(repo-a): %v", err)
	}
	_, err = pool.Open("repo-b")
	if err != nil {
		t.Fatalf("Open(repo-b): %v", err)
	}

	if pool.Len() != 2 {
		t.Fatalf("pool.Len() = %d, want 2", pool.Len())
	}

	// Open a third — should evict repo-a (LRU).
	_, err = pool.Open("repo-c")
	if err != nil {
		t.Fatalf("Open(repo-c): %v", err)
	}

	if pool.Len() != 2 {
		t.Errorf("pool.Len() = %d after eviction, want 2", pool.Len())
	}

	// repo-a should no longer be cached; opening it again should create a new connection.
	pool.mu.Lock()
	_, aExists := pool.conns["repo-a"]
	_, bExists := pool.conns["repo-b"]
	_, cExists := pool.conns["repo-c"]
	pool.mu.Unlock()

	if aExists {
		t.Error("repo-a should have been evicted")
	}
	if !bExists {
		t.Error("repo-b should still be cached")
	}
	if !cExists {
		t.Error("repo-c should be cached")
	}

	pool.Close()
}

func TestDBPool_LRUPromotionPreventsEviction(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, 2)
	defer pool.Close()

	// Open A, then B.
	_, err := pool.Open("repo-a")
	if err != nil {
		t.Fatalf("Open(repo-a): %v", err)
	}
	_, err = pool.Open("repo-b")
	if err != nil {
		t.Fatalf("Open(repo-b): %v", err)
	}

	// Access A again to promote it — B is now LRU.
	_, err = pool.Open("repo-a")
	if err != nil {
		t.Fatalf("Open(repo-a) again: %v", err)
	}

	// Open C — should evict B (not A, since A was promoted).
	_, err = pool.Open("repo-c")
	if err != nil {
		t.Fatalf("Open(repo-c): %v", err)
	}

	pool.mu.Lock()
	_, aExists := pool.conns["repo-a"]
	_, bExists := pool.conns["repo-b"]
	pool.mu.Unlock()

	if !aExists {
		t.Error("repo-a should NOT have been evicted (was promoted)")
	}
	if bExists {
		t.Error("repo-b should have been evicted (was LRU)")
	}
}

func TestDBPool_SetMaxOpenConns(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	// Open a DB and verify it works (SetMaxOpenConns is called internally).
	cdb, err := pool.Open("conntest")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}

	// Verify the DB is functional by creating schema (exercises the connection).
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("CreateSchema() error: %v", err)
	}
}

func TestDBPool_Close(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, DefaultMaxOpenDBs)

	_, err := pool.Open("close-a")
	if err != nil {
		t.Fatalf("Open(close-a): %v", err)
	}
	_, err = pool.Open("close-b")
	if err != nil {
		t.Fatalf("Open(close-b): %v", err)
	}

	if err := pool.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	if pool.Len() != 0 {
		t.Errorf("pool.Len() = %d after Close, want 0", pool.Len())
	}
}

func TestDBPool_DefaultMaxOpen(t *testing.T) {
	pool := NewDBPool(t.TempDir(), 0) // 0 should default to 20
	defer pool.Close()

	if pool.maxOpen != DefaultMaxOpenDBs {
		t.Errorf("maxOpen = %d, want %d (default)", pool.maxOpen, DefaultMaxOpenDBs)
	}
}

func TestDBPool_InvalidationOnModifiedFile(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	repoName := "invalidation-test"

	// First open creates the DB.
	cdb1, err := pool.Open(repoName)
	if err != nil {
		t.Fatalf("first Open() error: %v", err)
	}
	if err := cdb1.CreateSchema(); err != nil {
		t.Fatalf("CreateSchema() error: %v", err)
	}

	// Second open without modification returns the same instance.
	cdb2, err := pool.Open(repoName)
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}
	if cdb1 != cdb2 {
		t.Error("expected same instance when file not modified")
	}

	// Touch the DB file with a future mtime to simulate extraction update.
	dbPath := pool.dbPath(repoName)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(dbPath, future, future); err != nil {
		t.Fatalf("Chtimes() error: %v", err)
	}

	// Third open should detect the newer mtime and return a fresh connection.
	cdb3, err := pool.Open(repoName)
	if err != nil {
		t.Fatalf("third Open() after touch error: %v", err)
	}
	if cdb3 == cdb1 {
		t.Error("expected new instance after file modification, got same pointer")
	}

	// Pool should still have exactly 1 entry for this repo.
	if pool.Len() != 1 {
		t.Errorf("pool.Len() = %d after invalidation, want 1", pool.Len())
	}
}

func TestDBPool_InvalidationPreservesPoolCount(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, 3)
	defer pool.Close()

	// Open 3 repos to fill the pool.
	for _, name := range []string{"repo-x", "repo-y", "repo-z"} {
		if _, err := pool.Open(name); err != nil {
			t.Fatalf("Open(%s): %v", name, err)
		}
	}
	if pool.Len() != 3 {
		t.Fatalf("pool.Len() = %d, want 3", pool.Len())
	}

	// Touch repo-y to invalidate it.
	dbPath := pool.dbPath("repo-y")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(dbPath, future, future); err != nil {
		t.Fatalf("Chtimes() error: %v", err)
	}

	// Reopen repo-y — invalidation replaces it; pool count stays 3, no eviction needed.
	if _, err := pool.Open("repo-y"); err != nil {
		t.Fatalf("Open(repo-y) after invalidation: %v", err)
	}
	if pool.Len() != 3 {
		t.Errorf("pool.Len() = %d after invalidation reopen, want 3", pool.Len())
	}

	// All three repos should still be present.
	pool.mu.Lock()
	for _, name := range []string{"repo-x", "repo-y", "repo-z"} {
		if _, ok := pool.conns[name]; !ok {
			t.Errorf("repo %s should still be in pool after invalidation of repo-y", name)
		}
	}
	pool.mu.Unlock()
}

func TestDBPool_LastAccess(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	// Before any Open, LastAccess returns zero time and false.
	_, ok := pool.LastAccess("unqueried")
	if ok {
		t.Error("LastAccess for unqueried repo should return false")
	}

	// Open a repo — should record access time.
	before := time.Now()
	_, err := pool.Open("accessed-repo")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	after := time.Now()

	accessTime, ok := pool.LastAccess("accessed-repo")
	if !ok {
		t.Fatal("LastAccess should return true after Open")
	}
	if accessTime.Before(before) || accessTime.After(after) {
		t.Errorf("LastAccess time %v not between %v and %v", accessTime, before, after)
	}

	// Second Open should update the access time.
	time.Sleep(1 * time.Millisecond)
	_, err = pool.Open("accessed-repo")
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}
	accessTime2, _ := pool.LastAccess("accessed-repo")
	if !accessTime2.After(accessTime) {
		t.Errorf("second access time %v should be after first %v", accessTime2, accessTime)
	}
}

func TestDBPool_LastAccessWithCustomClock(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	fixedTime := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	pool.nowFunc = func() time.Time { return fixedTime }

	_, err := pool.Open("clock-test")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}

	accessTime, ok := pool.LastAccess("clock-test")
	if !ok {
		t.Fatal("LastAccess should return true")
	}
	if !accessTime.Equal(fixedTime) {
		t.Errorf("access time = %v, want %v", accessTime, fixedTime)
	}
}

func TestDBPool_RepoExists(t *testing.T) {
	dir := createTestDBDir(t, []string{"alpha", "beta"})
	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	tests := []struct {
		name     string
		repo     string
		want     bool
		wantErr  bool
	}{
		{name: "existing repo", repo: "alpha", want: true},
		{name: "another existing repo", repo: "beta", want: true},
		{name: "non-existent repo", repo: "gamma", want: false},
		{name: "empty name", repo: "", wantErr: true},
		{name: "path traversal", repo: "../etc", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pool.RepoExists(tt.repo)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("RepoExists(%q) = %v, want %v", tt.repo, got, tt.want)
			}
		})
	}
}

func TestDBPool_NoInvalidationWhenStatFails(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	repoName := "stat-fail-test"

	// Open creates the DB.
	cdb1, err := pool.Open(repoName)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}

	// Remove the DB file to make stat fail.
	dbPath := pool.dbPath(repoName)
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}

	// Open should return the cached connection since stat fails.
	cdb2, err := pool.Open(repoName)
	if err != nil {
		t.Fatalf("Open() after remove error: %v", err)
	}
	if cdb1 != cdb2 {
		t.Error("expected same instance when stat fails (file removed)")
	}
}
