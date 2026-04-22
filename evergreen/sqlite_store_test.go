package evergreen

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Compile-time: SQLiteStore implements DocumentStore.
var _ DocumentStore = (*SQLiteStore)(nil)

// testStore returns an opened, migrated store backed by a tempfile. The
// store's Close runs on cleanup. File-backed (not :memory:) to avoid the
// connection-pool-shared-cache gotcha with modernc.org/sqlite.
func testStore(t *testing.T, opts ...SQLiteOption) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evergreen_test.db")
	s, err := OpenSQLiteStore(context.Background(), path, opts...)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// mkDoc is a fixture helper with sensible defaults.
func mkDoc(t *testing.T, id string) *Document {
	t.Helper()
	if id == "" {
		id = NewDocumentID()
	}
	sym := int64(42)
	return &Document{
		ID:             id,
		Query:          "how does X work?",
		RenderedAnswer: "X works by...",
		Manifest: []ManifestEntry{
			{
				SymbolID:              &sym,
				Repo:                  "github.com/x/y",
				CommitSHA:             "abc",
				FilePath:              "f.go",
				ContentHashAtRender:   "h",
				SignatureHashAtRender: "s",
				LineStart:             10,
				LineEnd:               20,
			},
			{Repo: "github.com/x/y", CommitSHA: "abc", Fuzzy: true},
		},
		Status:          FreshStatus,
		RefreshPolicy:   AlertPolicy,
		MaxAgeDays:      30,
		CreatedAt:       time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC),
		LastRefreshedAt: time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC),
		Backend:         "deepsearch-mcp",
	}
}

// --- Constructor / open --------------------------------------------------

func TestNewSQLiteStore_NilDB(t *testing.T) {
	if _, err := NewSQLiteStore(nil); err == nil {
		t.Fatal("expected error for nil *sql.DB")
	}
}

func TestOpenSQLiteStore_MigrateIdempotent(t *testing.T) {
	s := testStore(t)
	// Migrate twice — second call must succeed with no drift.
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestWithMaxRevisions_IgnoresNonPositive(t *testing.T) {
	s := testStore(t, WithMaxRevisions(0))
	if s.maxRevisions != defaultMaxRevisions {
		t.Errorf("maxRevisions = %d, want default %d", s.maxRevisions, defaultMaxRevisions)
	}
	s2 := testStore(t, WithMaxRevisions(-1))
	if s2.maxRevisions != defaultMaxRevisions {
		t.Errorf("maxRevisions = %d, want default %d", s2.maxRevisions, defaultMaxRevisions)
	}
	s3 := testStore(t, WithMaxRevisions(3))
	if s3.maxRevisions != 3 {
		t.Errorf("maxRevisions = %d, want 3", s3.maxRevisions)
	}
}

// --- Save / Get roundtrip -------------------------------------------------

func TestSave_EmptyID(t *testing.T) {
	s := testStore(t)
	doc := mkDoc(t, "")
	doc.ID = ""
	if err := s.Save(context.Background(), doc); !errors.Is(err, ErrInvalidDocument) {
		t.Errorf("empty ID: got %v, want ErrInvalidDocument", err)
	}
}

func TestSave_NilDoc(t *testing.T) {
	s := testStore(t)
	if err := s.Save(context.Background(), nil); err == nil {
		t.Error("expected error for nil doc")
	}
}

func TestSaveGet_Roundtrip(t *testing.T) {
	s := testStore(t)
	orig := mkDoc(t, "doc-1")
	extID := "external-99"
	orig.ExternalID = &extID

	ctx := context.Background()
	if err := s.Save(ctx, orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get(ctx, "doc-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Normalize times for comparison (Parse strips monotonic, UTC applied).
	want := *orig
	want.CreatedAt = want.CreatedAt.UTC()
	want.LastRefreshedAt = want.LastRefreshedAt.UTC()

	if !reflect.DeepEqual(got, &want) {
		t.Errorf("roundtrip mismatch:\n got: %#v\nwant: %#v", got, &want)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := testStore(t)
	if _, err := s.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestSave_NilExternalIDPersistsAsNull(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "d")
	doc.ExternalID = nil
	if err := s.Save(ctx, doc); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "d")
	if err != nil {
		t.Fatal(err)
	}
	if got.ExternalID != nil {
		t.Errorf("expected nil ExternalID, got %q", *got.ExternalID)
	}
}

func TestSaveGet_EmptyManifest(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "d")
	doc.Manifest = nil
	if err := s.Save(ctx, doc); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "d")
	if err != nil {
		t.Fatal(err)
	}
	// JSON-encoded nil slice round-trips as nil (not []).
	if got.Manifest != nil && len(got.Manifest) != 0 {
		t.Errorf("expected nil/empty manifest, got %v", got.Manifest)
	}
}

// --- List ----------------------------------------------------------------

func TestList_Empty(t *testing.T) {
	s := testStore(t)
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %d", len(got))
	}
}

func TestList_OrderByLastRefreshedDesc(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)

	a := mkDoc(t, "a")
	a.LastRefreshedAt = now.Add(-48 * time.Hour)
	b := mkDoc(t, "b")
	b.LastRefreshedAt = now
	c := mkDoc(t, "c")
	c.LastRefreshedAt = now.Add(-24 * time.Hour)
	for _, d := range []*Document{a, b, c} {
		if err := s.Save(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := []string{got[0].ID, got[1].ID, got[2].ID}
	wantIDs := []string{"b", "c", "a"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("order = %v, want %v", gotIDs, wantIDs)
	}
}

// --- Delete --------------------------------------------------------------

func TestDelete_Existing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Save(ctx, mkDoc(t, "d")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "d"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete: got %v, want ErrNotFound", err)
	}
}

func TestDelete_Missing(t *testing.T) {
	s := testStore(t)
	if err := s.Delete(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// Delete must cascade revision history — otherwise revisions leak orphans.
func TestDelete_CascadesRevisions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "d")
	for i := 0; i < 3; i++ {
		doc.RenderedAnswer = "rev " + string(rune('A'+i))
		if err := s.Save(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	// Two revisions now exist (first save wrote no revision).
	if err := s.Delete(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM deep_search_document_revisions WHERE document_id = ?`,
		"d",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("revisions after Delete = %d, want 0 (cascade)", count)
	}
}

// --- UpdateStatus --------------------------------------------------------

func TestUpdateStatus_Existing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "d")
	if err := s.Save(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateStatus(ctx, "d", OrphanedStatus); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "d")
	if got.Status != OrphanedStatus {
		t.Errorf("Status = %q, want %q", got.Status, OrphanedStatus)
	}
}

func TestUpdateStatus_Missing(t *testing.T) {
	s := testStore(t)
	if err := s.UpdateStatus(context.Background(), "missing", FreshStatus); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// --- Revision history ----------------------------------------------------

// Overwriting an existing document captures the previous revision.
func TestSave_CapturesPreviousRevision(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "d")
	doc.RenderedAnswer = "v1"
	if err := s.Save(ctx, doc); err != nil {
		t.Fatal(err)
	}
	doc.RenderedAnswer = "v2"
	if err := s.Save(ctx, doc); err != nil {
		t.Fatal(err)
	}

	var ans string
	if err := s.db.QueryRowContext(ctx,
		`SELECT rendered_answer FROM deep_search_document_revisions
		 WHERE document_id = ? AND revision_num = 1`, "d",
	).Scan(&ans); err != nil {
		t.Fatalf("query revision: %v", err)
	}
	if ans != "v1" {
		t.Errorf("revision 1 answer = %q, want %q", ans, "v1")
	}
}

// With maxRevisions=3, the 4th overwrite prunes the oldest revision,
// keeping exactly 3 entries.
func TestSave_PrunesBeyondCap(t *testing.T) {
	s := testStore(t, WithMaxRevisions(3))
	ctx := context.Background()
	doc := mkDoc(t, "d")

	// 1 save = 0 revisions.
	// N overwrites = N revisions until cap, then prune to cap.
	for i := 1; i <= 5; i++ {
		doc.RenderedAnswer = "v" + string(rune('0'+i))
		if err := s.Save(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	// 4 overwrites happened after the first save. Cap is 3.
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM deep_search_document_revisions WHERE document_id = ?`,
		"d",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("revisions after cap prune = %d, want 3", count)
	}

	// Confirm oldest revisions were dropped (revision_num 1 gone, 4 kept).
	var minRev, maxRev int
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(revision_num), MAX(revision_num)
		 FROM deep_search_document_revisions WHERE document_id = ?`,
		"d",
	).Scan(&minRev, &maxRev); err != nil {
		t.Fatal(err)
	}
	if minRev != 2 || maxRev != 4 {
		t.Errorf("revision range = [%d, %d], want [2, 4]", minRev, maxRev)
	}
}

// Idempotent saves (identical content, rewritten) still increment revisions;
// documented behavior so user can count "number of times Save was invoked".
func TestSave_IdempotentSavesBumpRevisions(t *testing.T) {
	s := testStore(t, WithMaxRevisions(10))
	ctx := context.Background()
	doc := mkDoc(t, "d")
	for i := 0; i < 3; i++ {
		if err := s.Save(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	_ = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM deep_search_document_revisions WHERE document_id = ?`,
		"d",
	).Scan(&count)
	// First save: no revision; second: rev 1; third: rev 2.
	if count != 2 {
		t.Errorf("revisions = %d, want 2", count)
	}
}

// --- Concurrency ---------------------------------------------------------

// Safe for concurrent use: many goroutines saving distinct documents
// converge without data-race or duplicate-key errors. Same-ID contention
// is also safe by construction: _txlock=immediate serializes writers at
// BeginTx via SQLite's write lock, so captureRevision always sees the
// committed previous row. We don't need an explicit same-ID test.
func TestSQLiteStore_ConcurrentSavesDistinctIDs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			doc := mkDoc(t, "")
			doc.ID = NewDocumentID()
			doc.Query = "q-" + string(rune('a'+i%26))
			if err := s.Save(ctx, doc); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent save: %v", err)
	}
	got, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Errorf("saved %d docs, stored %d", n, len(got))
	}
}

// NewSQLiteStore must enable PRAGMA foreign_keys so Delete cascades to
// revisions regardless of what the caller's DSN set. Without this, a caller
// opening plain `sql.Open("sqlite", path)` would see silent orphan rows.
func TestNewSQLiteStore_EnablesForeignKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "external.db")
	// Open without the _pragma=foreign_keys%3d1 DSN parameter. Default is OFF.
	rawDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()

	// Confirm the raw handle has foreign_keys OFF at the connection level
	// to validate the precondition of the test.
	var fk int
	if err := rawDB.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 0 {
		t.Skipf("expected default foreign_keys=0, got %d (driver-dependent)", fk)
	}

	s, err := NewSQLiteStore(rawDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	doc := mkDoc(t, "d")
	for i := 0; i < 3; i++ {
		doc.RenderedAnswer = "v" + string(rune('0'+i))
		if err := s.Save(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := rawDB.QueryRow(
		`SELECT COUNT(*) FROM deep_search_document_revisions WHERE document_id = ?`,
		"d",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("orphaned revisions after Delete = %d, want 0 (foreign_keys not enabled)", count)
	}
}


func TestClose_NoOpForExternalDB(t *testing.T) {
	// Build a store from an externally-opened DB. Close must not release it.
	path := filepath.Join(t.TempDir(), "ext.db")
	ext, err := OpenSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// Steal its *sql.DB and wrap separately.
	raw := ext.db
	wrap, err := NewSQLiteStore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := wrap.Close(); err != nil {
		t.Errorf("unexpected error from non-owning Close: %v", err)
	}
	// The external DB must still be usable.
	if err := ext.Migrate(context.Background()); err != nil {
		t.Errorf("external DB broken after non-owning Close: %v", err)
	}
	_ = ext.Close()
}
