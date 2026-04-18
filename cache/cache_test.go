package cache_test

import (
	"testing"
	"time"

	"github.com/sjarmak/livedocs/cache"
)

// helper creates an in-memory SQLite store with the given size cap.
func newTestStore(t *testing.T, capBytes int64) cache.Store {
	t.Helper()
	s, err := cache.NewSQLiteStore(":memory:", capBytes)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func makeEntry(repo, path, hash, extV, gramV string, size int64) cache.Entry {
	return cache.Entry{
		Repo:             repo,
		RelativePath:     path,
		ContentHash:      hash,
		ExtractorVersion: extV,
		GrammarVersion:   gramV,
		LastIndexed:      time.Now(),
		SizeBytes:        size,
		Deleted:          false,
	}
}

// --- Hit / Miss ---

func TestHit_ExactMatch(t *testing.T) {
	s := newTestStore(t, 1<<30)
	e := makeEntry("r", "a.go", "abc123", "v1", "g1", 100)
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Hit("r", "a.go", "abc123", "v1", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected hit, got miss")
	}
}

func TestHit_ContentHashMismatch(t *testing.T) {
	s := newTestStore(t, 1<<30)
	e := makeEntry("r", "a.go", "abc123", "v1", "g1", 100)
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Hit("r", "a.go", "DIFFERENT", "v1", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected miss on content hash mismatch")
	}
}

func TestHit_ExtractorVersionMismatch(t *testing.T) {
	s := newTestStore(t, 1<<30)
	e := makeEntry("r", "a.go", "abc123", "v1", "g1", 100)
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Hit("r", "a.go", "abc123", "v2", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected miss on extractor version mismatch")
	}
}

func TestHit_GrammarVersionMismatch(t *testing.T) {
	s := newTestStore(t, 1<<30)
	e := makeEntry("r", "a.go", "abc123", "v1", "g1", 100)
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Hit("r", "a.go", "abc123", "v1", "g2")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected miss on grammar version mismatch")
	}
}

func TestHit_DeletedEntryIsMiss(t *testing.T) {
	s := newTestStore(t, 1<<30)
	e := makeEntry("r", "a.go", "abc123", "v1", "g1", 100)
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeleted("r", "a.go"); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Hit("r", "a.go", "abc123", "v1", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected miss for deleted entry")
	}
}

func TestHit_NoEntry(t *testing.T) {
	s := newTestStore(t, 1<<30)
	ok, err := s.Hit("r", "nonexistent.go", "abc", "v1", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected miss for nonexistent entry")
	}
}

// --- Put / Upsert ---

func TestPut_Upsert(t *testing.T) {
	s := newTestStore(t, 1<<30)
	e1 := makeEntry("r", "a.go", "hash1", "v1", "g1", 100)
	if err := s.Put(e1); err != nil {
		t.Fatal(err)
	}
	// Update same file with new hash
	e2 := makeEntry("r", "a.go", "hash2", "v2", "g1", 200)
	if err := s.Put(e2); err != nil {
		t.Fatal(err)
	}
	// Old key should miss
	ok, _ := s.Hit("r", "a.go", "hash1", "v1", "g1")
	if ok {
		t.Error("old entry should not hit after upsert")
	}
	// New key should hit
	ok, _ = s.Hit("r", "a.go", "hash2", "v2", "g1")
	if !ok {
		t.Error("new entry should hit after upsert")
	}
}

func TestPut_ClearsDeletedFlag(t *testing.T) {
	s := newTestStore(t, 1<<30)
	e := makeEntry("r", "a.go", "abc", "v1", "g1", 100)
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeleted("r", "a.go"); err != nil {
		t.Fatal(err)
	}
	// Re-put should clear deleted flag
	e2 := makeEntry("r", "a.go", "abc", "v1", "g1", 100)
	if err := s.Put(e2); err != nil {
		t.Fatal(err)
	}
	ok, _ := s.Hit("r", "a.go", "abc", "v1", "g1")
	if !ok {
		t.Error("re-put should clear deleted flag")
	}
}

// --- TotalSize ---

func TestTotalSize(t *testing.T) {
	s := newTestStore(t, 1<<30)
	if err := s.Put(makeEntry("r", "a.go", "h1", "v1", "g1", 100)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(makeEntry("r", "b.go", "h2", "v1", "g1", 250)); err != nil {
		t.Fatal(err)
	}
	total, err := s.TotalSize()
	if err != nil {
		t.Fatal(err)
	}
	if total != 350 {
		t.Errorf("expected 350, got %d", total)
	}
}

func TestTotalSize_ExcludesDeleted(t *testing.T) {
	s := newTestStore(t, 1<<30)
	if err := s.Put(makeEntry("r", "a.go", "h1", "v1", "g1", 100)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(makeEntry("r", "b.go", "h2", "v1", "g1", 250)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeleted("r", "a.go"); err != nil {
		t.Fatal(err)
	}
	total, err := s.TotalSize()
	if err != nil {
		t.Fatal(err)
	}
	if total != 250 {
		t.Errorf("expected 250, got %d", total)
	}
}

// --- Eviction ---

func TestEvict_UnderCapNoOp(t *testing.T) {
	s := newTestStore(t, 1000)
	if err := s.Put(makeEntry("r", "a.go", "h1", "v1", "g1", 100)); err != nil {
		t.Fatal(err)
	}
	evicted, err := s.Evict()
	if err != nil {
		t.Fatal(err)
	}
	if evicted != 0 {
		t.Errorf("expected 0 evicted, got %d", evicted)
	}
}

func TestEvict_OverCapEvictsLRU(t *testing.T) {
	// Cap at 300 bytes. Insert 4 entries of 100 each = 400 total.
	s := newTestStore(t, 300)
	now := time.Now()
	entries := []cache.Entry{
		{Repo: "r", RelativePath: "oldest.go", ContentHash: "h1", ExtractorVersion: "v1", GrammarVersion: "g1", LastIndexed: now.Add(-4 * time.Hour), SizeBytes: 100},
		{Repo: "r", RelativePath: "old.go", ContentHash: "h2", ExtractorVersion: "v1", GrammarVersion: "g1", LastIndexed: now.Add(-3 * time.Hour), SizeBytes: 100},
		{Repo: "r", RelativePath: "recent.go", ContentHash: "h3", ExtractorVersion: "v1", GrammarVersion: "g1", LastIndexed: now.Add(-2 * time.Hour), SizeBytes: 100},
		{Repo: "r", RelativePath: "newest.go", ContentHash: "h4", ExtractorVersion: "v1", GrammarVersion: "g1", LastIndexed: now.Add(-1 * time.Hour), SizeBytes: 100},
	}
	for _, e := range entries {
		if err := s.Put(e); err != nil {
			t.Fatal(err)
		}
	}
	evicted, err := s.Evict()
	if err != nil {
		t.Fatal(err)
	}
	if evicted < 1 {
		t.Errorf("expected at least 1 eviction, got %d", evicted)
	}
	total, _ := s.TotalSize()
	if total > 300 {
		t.Errorf("total size %d exceeds cap 300", total)
	}
	// Oldest should be gone
	ok, _ := s.Hit("r", "oldest.go", "h1", "v1", "g1")
	if ok {
		t.Error("oldest entry should have been evicted")
	}
	// Newest should remain
	ok, _ = s.Hit("r", "newest.go", "h4", "v1", "g1")
	if !ok {
		t.Error("newest entry should still be present")
	}
}

func TestEvict_TombstonedFirst(t *testing.T) {
	// Cap at 200. Insert 3 entries of 100 = 300. Tombstone the newest.
	// Evict should remove the tombstoned entry first, even though it's newest.
	s := newTestStore(t, 200)
	now := time.Now()
	entries := []cache.Entry{
		{Repo: "r", RelativePath: "old.go", ContentHash: "h1", ExtractorVersion: "v1", GrammarVersion: "g1", LastIndexed: now.Add(-2 * time.Hour), SizeBytes: 100},
		{Repo: "r", RelativePath: "mid.go", ContentHash: "h2", ExtractorVersion: "v1", GrammarVersion: "g1", LastIndexed: now.Add(-1 * time.Hour), SizeBytes: 100},
		{Repo: "r", RelativePath: "new.go", ContentHash: "h3", ExtractorVersion: "v1", GrammarVersion: "g1", LastIndexed: now, SizeBytes: 100},
	}
	for _, e := range entries {
		if err := s.Put(e); err != nil {
			t.Fatal(err)
		}
	}
	// Tombstone the newest entry
	if err := s.MarkDeleted("r", "new.go"); err != nil {
		t.Fatal(err)
	}
	evicted, err := s.Evict()
	if err != nil {
		t.Fatal(err)
	}
	if evicted < 1 {
		t.Fatalf("expected at least 1 eviction, got %d", evicted)
	}
	total, _ := s.TotalSize()
	if total > 200 {
		t.Errorf("total size %d exceeds cap 200", total)
	}
	// old.go should still be around (it's LRU but tombstoned entry was evicted first)
	ok, _ := s.Hit("r", "old.go", "h1", "v1", "g1")
	if !ok {
		t.Error("old.go should remain — tombstoned entry should have been evicted first")
	}
}

// --- Reconcile ---

func TestReconcile_DetectsChanged(t *testing.T) {
	s := newTestStore(t, 1<<30)
	if err := s.Put(makeEntry("r", "a.go", "hash_old", "v1", "g1", 100)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(makeEntry("r", "b.go", "hash_same", "v1", "g1", 100)); err != nil {
		t.Fatal(err)
	}
	current := map[string]string{
		"a.go": "hash_new",  // changed
		"b.go": "hash_same", // unchanged
		"c.go": "hash_c",    // new file
	}
	changed, err := s.Reconcile("r", current)
	if err != nil {
		t.Fatal(err)
	}
	// a.go changed, c.go is new — both should appear
	changedSet := make(map[string]bool)
	for _, p := range changed {
		changedSet[p] = true
	}
	if !changedSet["a.go"] {
		t.Error("a.go should be reported as changed")
	}
	if !changedSet["c.go"] {
		t.Error("c.go should be reported as changed (new file)")
	}
	if changedSet["b.go"] {
		t.Error("b.go should NOT be reported as changed")
	}
}

func TestReconcile_TombstonesDeletedFiles(t *testing.T) {
	s := newTestStore(t, 1<<30)
	if err := s.Put(makeEntry("r", "a.go", "h1", "v1", "g1", 100)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(makeEntry("r", "deleted.go", "h2", "v1", "g1", 100)); err != nil {
		t.Fatal(err)
	}
	// deleted.go is not in currentFiles → should be tombstoned
	current := map[string]string{
		"a.go": "h1",
	}
	_, err := s.Reconcile("r", current)
	if err != nil {
		t.Fatal(err)
	}
	// deleted.go should now be a miss
	ok, _ := s.Hit("r", "deleted.go", "h2", "v1", "g1")
	if ok {
		t.Error("deleted.go should be tombstoned after reconcile")
	}
}

// --- MarkDeleted ---

func TestMarkDeleted_Idempotent(t *testing.T) {
	s := newTestStore(t, 1<<30)
	if err := s.Put(makeEntry("r", "a.go", "h", "v1", "g1", 100)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeleted("r", "a.go"); err != nil {
		t.Fatal(err)
	}
	// Second call should not error
	if err := s.MarkDeleted("r", "a.go"); err != nil {
		t.Errorf("second MarkDeleted should not error: %v", err)
	}
}

func TestMarkDeleted_NonexistentIsNoOp(t *testing.T) {
	s := newTestStore(t, 1<<30)
	// Should not error on nonexistent entry
	if err := s.MarkDeleted("r", "nonexistent.go"); err != nil {
		t.Errorf("MarkDeleted on nonexistent should not error: %v", err)
	}
}

// --- Busy timeout ---

func TestBusyTimeoutSet(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/timeout_test.db"
	s, err := cache.NewSQLiteStore(path, 1<<30)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	var timeout int
	err = s.DB().QueryRow("PRAGMA busy_timeout").Scan(&timeout)
	if err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("expected busy_timeout=5000, got %d", timeout)
	}
}

// --- File-backed store ---

func TestNewSQLiteStore_FileBacked(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.db"
	s, err := cache.NewSQLiteStore(path, 1<<30)
	if err != nil {
		t.Fatalf("NewSQLiteStore with file: %v", err)
	}
	defer s.Close()

	e := makeEntry("r", "a.go", "h1", "v1", "g1", 50)
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Hit("r", "a.go", "h1", "v1", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected hit in file-backed store")
	}
}

// --- Evict phase 2 (LRU without tombstones) ---

func TestEvict_Phase2LRU(t *testing.T) {
	// Cap at 100. Insert 3 non-deleted entries of 50 = 150. No tombstones.
	s := newTestStore(t, 100)
	now := time.Now()
	entries := []cache.Entry{
		{Repo: "r", RelativePath: "a.go", ContentHash: "h1", ExtractorVersion: "v1", GrammarVersion: "g1", LastIndexed: now.Add(-3 * time.Hour), SizeBytes: 50},
		{Repo: "r", RelativePath: "b.go", ContentHash: "h2", ExtractorVersion: "v1", GrammarVersion: "g1", LastIndexed: now.Add(-2 * time.Hour), SizeBytes: 50},
		{Repo: "r", RelativePath: "c.go", ContentHash: "h3", ExtractorVersion: "v1", GrammarVersion: "g1", LastIndexed: now.Add(-1 * time.Hour), SizeBytes: 50},
	}
	for _, e := range entries {
		if err := s.Put(e); err != nil {
			t.Fatal(err)
		}
	}
	evicted, err := s.Evict()
	if err != nil {
		t.Fatal(err)
	}
	if evicted != 1 {
		t.Errorf("expected 1 eviction, got %d", evicted)
	}
	// Oldest (a.go) should be gone
	ok, _ := s.Hit("r", "a.go", "h1", "v1", "g1")
	if ok {
		t.Error("oldest entry should be evicted in phase 2")
	}
}

// --- Reconcile with empty cache ---

func TestReconcile_EmptyCache(t *testing.T) {
	s := newTestStore(t, 1<<30)
	current := map[string]string{
		"new.go": "hash1",
	}
	changed, err := s.Reconcile("r", current)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "new.go" {
		t.Errorf("expected [new.go], got %v", changed)
	}
}

// --- Reconcile with empty currentFiles ---

func TestReconcile_AllDeleted(t *testing.T) {
	s := newTestStore(t, 1<<30)
	if err := s.Put(makeEntry("r", "a.go", "h1", "v1", "g1", 100)); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Reconcile("r", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Errorf("expected no changed files, got %v", changed)
	}
	// a.go should be tombstoned
	ok, _ := s.Hit("r", "a.go", "h1", "v1", "g1")
	if ok {
		t.Error("a.go should be tombstoned after reconcile with empty currentFiles")
	}
}
