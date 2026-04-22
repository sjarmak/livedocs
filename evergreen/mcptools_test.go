package evergreen

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// --- Constructor contract guards -----------------------------------------

func TestNewStatusTool_NilStore(t *testing.T) {
	if _, err := NewStatusTool(nil, nil); err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestNewRefreshTool_NilDeps(t *testing.T) {
	s := testMcpStore(t)
	exec := &fakeExecutor{}
	lim := NewKeyedRateLimiter(RateLimiterConfig{})
	cases := []struct {
		name string
		fn   func() (*RefreshTool, error)
	}{
		{"nil store", func() (*RefreshTool, error) { return NewRefreshTool(nil, exec, lim, nil) }},
		{"nil executor", func() (*RefreshTool, error) { return NewRefreshTool(s, nil, lim, nil) }},
		{"nil limiter", func() (*RefreshTool, error) { return NewRefreshTool(s, exec, nil, nil) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.fn(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// --- StatusTool: list vs single ------------------------------------------

func TestStatusTool_EmptyStore(t *testing.T) {
	s := testMcpStore(t)
	tool, err := NewStatusTool(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Handle(context.Background(), StatusInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Documents) != 0 {
		t.Errorf("want empty, got %d", len(out.Documents))
	}
}

func TestStatusTool_SingleDoc(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "d1")
	if err := s.Save(ctx, doc); err != nil {
		t.Fatal(err)
	}
	tool, _ := NewStatusTool(s, nil)
	out, err := tool.Handle(ctx, StatusInput{DocID: "d1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Documents) != 1 || out.Documents[0].Document.ID != "d1" {
		t.Errorf("unexpected output: %+v", out)
	}
}

func TestStatusTool_MissingDocID_ErrNotFound(t *testing.T) {
	s := testMcpStore(t)
	tool, _ := NewStatusTool(s, nil)
	_, err := tool.Handle(context.Background(), StatusInput{DocID: "missing"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestStatusTool_ListMultiple(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		_ = s.Save(ctx, mkDoc(t, id))
	}
	tool, _ := NewStatusTool(s, nil)
	out, _ := tool.Handle(ctx, StatusInput{})
	if len(out.Documents) != 3 {
		t.Errorf("want 3, got %d", len(out.Documents))
	}
}

// StatusTool: doc-scoped Cold findings still fire with nil claims.
func TestStatusTool_AgeColdWithNilClaims(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)

	doc := mkDoc(t, "old")
	doc.MaxAgeDays = 1
	doc.LastRefreshedAt = now.Add(-5 * 24 * time.Hour)
	if err := s.Save(ctx, doc); err != nil {
		t.Fatal(err)
	}
	tool, _ := NewStatusTool(s, nil, StatusWithClock(func() time.Time { return now }))
	out, err := tool.Handle(ctx, StatusInput{DocID: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Documents) != 1 {
		t.Fatalf("want 1 doc, got %d", len(out.Documents))
	}
	fs := out.Documents[0].Findings
	if len(fs) != 1 || fs[0].Severity != ColdSeverity {
		t.Errorf("expected cold finding, got %+v", fs)
	}
}

// StatusTool: detector errors (via a faulty claims reader) propagate wrapped.
func TestStatusTool_DetectorErrorPropagates(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "d")
	_ = s.Save(ctx, doc)
	claims := &alwaysErrorClaims{err: errors.New("backend down")}
	tool, _ := NewStatusTool(s, claims)
	_, err := tool.Handle(ctx, StatusInput{DocID: "d"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- RefreshTool: validation ---------------------------------------------

func TestRefreshTool_MissingDocID(t *testing.T) {
	tool := mustRefreshTool(t, testMcpStore(t), &fakeExecutor{})
	_, err := tool.Handle(context.Background(), RefreshInput{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRefreshTool_DocNotFound(t *testing.T) {
	tool := mustRefreshTool(t, testMcpStore(t), &fakeExecutor{})
	_, err := tool.Handle(context.Background(), RefreshInput{DocID: "missing"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// --- RefreshTool: orphan guard -------------------------------------------

func TestRefreshTool_OrphanedBlocksRefresh(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "o")
	doc.Status = OrphanedStatus
	_ = s.Save(ctx, doc)

	exec := &fakeExecutor{}
	tool := mustRefreshTool(t, s, exec)
	_, err := tool.Handle(ctx, RefreshInput{DocID: "o"})
	if !errors.Is(err, ErrOrphaned) {
		t.Errorf("got %v, want ErrOrphaned", err)
	}
	if exec.calls != 0 {
		t.Errorf("executor called %d times despite orphan block", exec.calls)
	}
}

func TestRefreshTool_AcknowledgeOrphanProceeds(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "o")
	doc.Status = OrphanedStatus
	_ = s.Save(ctx, doc)

	exec := &fakeExecutor{
		result: RefreshResult{RenderedAnswer: "fresh", Backend: "deepsearch-mcp"},
	}
	tool := mustRefreshTool(t, s, exec)
	out, err := tool.Handle(ctx, RefreshInput{DocID: "o", AcknowledgeOrphan: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Document.Status != FreshStatus {
		t.Errorf("Status = %q, want Fresh", out.Document.Status)
	}
	if exec.calls != 1 {
		t.Errorf("executor calls = %d, want 1", exec.calls)
	}
}

// --- RefreshTool: rate limiting ------------------------------------------

func TestRefreshTool_RateLimited(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "d")
	_ = s.Save(ctx, doc)

	exec := &fakeExecutor{result: RefreshResult{RenderedAnswer: "v2"}}
	lim := &denyLimiter{}
	tool, err := NewRefreshTool(s, exec, lim, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Handle(ctx, RefreshInput{DocID: "d"})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("got %v, want ErrRateLimited", err)
	}
	if exec.calls != 0 {
		t.Errorf("executor called %d times despite rate-limit", exec.calls)
	}
}

// Rate limit is checked AFTER the orphan guard so repeated
// ack-required calls on an orphaned doc don't drain tokens.
func TestRefreshTool_OrphanCheckedBeforeRateLimit(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "o")
	doc.Status = OrphanedStatus
	_ = s.Save(ctx, doc)

	exec := &fakeExecutor{}
	lim := &recordingLimiter{}
	tool, _ := NewRefreshTool(s, exec, lim, nil)
	_, err := tool.Handle(ctx, RefreshInput{DocID: "o"})
	if !errors.Is(err, ErrOrphaned) {
		t.Fatalf("got %v, want ErrOrphaned", err)
	}
	if lim.calls != 0 {
		t.Errorf("limiter called %d times before orphan guard", lim.calls)
	}
}

// --- RefreshTool: executor failure ---------------------------------------

func TestRefreshTool_ExecutorError_PropagatesWithoutMutation(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "d")
	doc.RenderedAnswer = "original"
	_ = s.Save(ctx, doc)

	exec := &fakeExecutor{err: errors.New("upstream boom")}
	tool := mustRefreshTool(t, s, exec)
	_, err := tool.Handle(ctx, RefreshInput{DocID: "d"})
	if err == nil {
		t.Fatal("expected error")
	}
	// Store must be unchanged: no mutation on executor failure.
	got, _ := s.Get(ctx, "d")
	if got.RenderedAnswer != "original" {
		t.Errorf("store was mutated after executor failure: %q", got.RenderedAnswer)
	}
}

// --- RefreshTool: happy path ---------------------------------------------

func TestRefreshTool_HappyPath_UpdatesAllExpectedFields(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	fixedNow := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)

	doc := mkDoc(t, "d")
	doc.RenderedAnswer = "original"
	doc.Manifest = []ManifestEntry{{Repo: "old", Fuzzy: true}}
	doc.LastRefreshedAt = fixedNow.Add(-10 * 24 * time.Hour)
	_ = s.Save(ctx, doc)

	newManifest := []ManifestEntry{
		{Repo: "github.com/x/y", CommitSHA: "c", FilePath: "f.go", LineStart: 1, LineEnd: 2},
	}
	extID := "sg-v-42"
	exec := &fakeExecutor{
		result: RefreshResult{
			RenderedAnswer: "fresh answer",
			Manifest:       newManifest,
			Backend:        "deepsearch-mcp",
			ExternalID:     &extID,
		},
	}
	lim := NewKeyedRateLimiter(RateLimiterConfig{})
	tool, _ := NewRefreshTool(s, exec, lim, nil,
		RefreshWithClock(func() time.Time { return fixedNow }))

	out, err := tool.Handle(ctx, RefreshInput{DocID: "d"})
	if err != nil {
		t.Fatal(err)
	}
	// Verify the returned Document reflects the refresh.
	got := out.Document
	if got.RenderedAnswer != "fresh answer" {
		t.Errorf("RenderedAnswer = %q", got.RenderedAnswer)
	}
	if len(got.Manifest) != 1 || got.Manifest[0].Repo != "github.com/x/y" {
		t.Errorf("Manifest mismatch: %+v", got.Manifest)
	}
	if got.Backend != "deepsearch-mcp" {
		t.Errorf("Backend = %q", got.Backend)
	}
	if got.ExternalID == nil || *got.ExternalID != "sg-v-42" {
		t.Errorf("ExternalID = %v", got.ExternalID)
	}
	if !got.LastRefreshedAt.Equal(fixedNow) {
		t.Errorf("LastRefreshedAt = %v, want %v", got.LastRefreshedAt, fixedNow)
	}
	if got.Status != FreshStatus {
		t.Errorf("Status = %q, want Fresh", got.Status)
	}
	// Preserved fields must survive.
	if got.Query != doc.Query || got.MaxAgeDays != doc.MaxAgeDays {
		t.Errorf("preserved fields dropped")
	}

	// Verify the store persisted the same state.
	stored, _ := s.Get(ctx, "d")
	if stored.RenderedAnswer != "fresh answer" {
		t.Errorf("store not updated")
	}
}

// Executor returning Backend="" must not clobber the existing Backend.
func TestRefreshTool_EmptyBackend_PreservesExisting(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	doc := mkDoc(t, "d")
	doc.Backend = "prior-backend"
	_ = s.Save(ctx, doc)

	exec := &fakeExecutor{result: RefreshResult{RenderedAnswer: "v", Backend: ""}}
	tool := mustRefreshTool(t, s, exec)
	out, _ := tool.Handle(ctx, RefreshInput{DocID: "d"})
	if out.Document.Backend != "prior-backend" {
		t.Errorf("Backend = %q, want prior-backend preserved", out.Document.Backend)
	}
}

// Post-refresh detector runs and attaches findings to the output.
func TestRefreshTool_PostRefreshDetectorRuns(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	fixedNow := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)

	doc := mkDoc(t, "d")
	doc.MaxAgeDays = 1
	doc.LastRefreshedAt = fixedNow.Add(-10 * 24 * time.Hour)
	_ = s.Save(ctx, doc)

	// Executor returns result with LastRefreshedAt that will be set by the
	// tool to fixedNow. But we want findings from the NEW state — so we
	// configure the clock so that post-refresh the doc is fresh (no age
	// finding). An executor returning the same manifest and no drift
	// should yield zero findings.
	exec := &fakeExecutor{result: RefreshResult{RenderedAnswer: "v"}}
	tool := mustRefreshToolWithClock(t, s, exec, fixedNow)
	out, err := tool.Handle(ctx, RefreshInput{DocID: "d"})
	if err != nil {
		t.Fatal(err)
	}
	// Post-refresh, LastRefreshedAt == fixedNow, so age(0) <= MaxAge(1).
	// No findings expected.
	if len(out.Findings) != 0 {
		t.Errorf("expected 0 findings post-refresh, got %+v", out.Findings)
	}
}

// --- Concurrency sanity --------------------------------------------------

// The tools are plain structs and their Handle methods are reentrant in
// the sense that they delegate state to the store (which is concurrency-safe).
// This test just confirms no data-race flags during concurrent access.
func TestStatusTool_ConcurrentHandle(t *testing.T) {
	s := testMcpStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = s.Save(ctx, mkDoc(t, string(rune('a'+i))))
	}
	tool, _ := NewStatusTool(s, nil)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tool.Handle(ctx, StatusInput{}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Handle: %v", err)
	}
}

// --- Test helpers --------------------------------------------------------

func testMcpStore(t *testing.T) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp_test.db")
	s, err := OpenSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustRefreshTool(t *testing.T, store DocumentStore, exec RefreshExecutor) *RefreshTool {
	t.Helper()
	lim := NewKeyedRateLimiter(RateLimiterConfig{})
	tool, err := NewRefreshTool(store, exec, lim, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func mustRefreshToolWithClock(t *testing.T, store DocumentStore, exec RefreshExecutor, now time.Time) *RefreshTool {
	t.Helper()
	lim := NewKeyedRateLimiter(RateLimiterConfig{})
	tool, err := NewRefreshTool(store, exec, lim, nil,
		RefreshWithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

// fakeExecutor is a controllable RefreshExecutor for tests.
type fakeExecutor struct {
	result RefreshResult
	err    error
	calls  int
}

func (e *fakeExecutor) Refresh(_ context.Context, _ *Document) (RefreshResult, error) {
	e.calls++
	if e.err != nil {
		return RefreshResult{}, e.err
	}
	return e.result, nil
}
func (e *fakeExecutor) Name() string { return "fake" }

// denyLimiter rejects every call.
type denyLimiter struct{}

func (denyLimiter) Allow(_ context.Context, _ string) error { return ErrRateLimited }

// recordingLimiter tracks Allow invocations so tests can assert call order.
type recordingLimiter struct{ calls int }

func (l *recordingLimiter) Allow(_ context.Context, _ string) error {
	l.calls++
	return nil
}

// alwaysErrorClaims returns err from every method.
type alwaysErrorClaims struct{ err error }

func (c *alwaysErrorClaims) GetSymbol(_ context.Context, _ string, _ int64) (*SymbolState, error) {
	return nil, c.err
}
func (c *alwaysErrorClaims) ResolveSymbolByLocation(_ context.Context, _, _ string, _, _ int) (int64, error) {
	return 0, c.err
}
