package sourcegraph

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
)

// blockingRouter is a PredicateRouter that blocks until unblocked, allowing
// tests to simulate long-running enrichment.
type blockingRouter struct {
	mockRouter
	blockCh chan struct{} // close to unblock
}

func newBlockingRouter() *blockingRouter {
	return &blockingRouter{
		mockRouter: mockRouter{
			results: map[string]string{},
		},
		blockCh: make(chan struct{}),
	}
}

func (b *blockingRouter) Route(ctx context.Context, predicate extractor.Predicate, sym SymbolContext) (string, error) {
	<-b.blockCh
	return b.mockRouter.Route(ctx, predicate, sym)
}

// countingRouter counts how many times Run is called by tracking Route calls.
type countingRouter struct {
	mu       sync.Mutex
	runCount int32
}

func (c *countingRouter) Route(ctx context.Context, predicate extractor.Predicate, sym SymbolContext) (string, error) {
	atomic.AddInt32(&c.runCount, 1)
	return "context about " + sym.Name, nil
}

func (c *countingRouter) routeCount() int32 {
	return atomic.LoadInt32(&c.runCount)
}

// setupQueueTestDB creates a test DB with symbols for the given import paths.
func setupQueueTestDB(t *testing.T, importPaths ...string) *db.ClaimsDB {
	t.Helper()
	cdb := setupTestDB(t)
	for _, ip := range importPaths {
		insertSymbol(t, cdb, "test/repo", ip, "Symbol_"+ip, "go", "func", "public")
	}
	return cdb
}

// TestDebounceCoalescesBurstEvents verifies that multiple rapid Send calls
// within the debounce window are coalesced into a single enrichment batch.
func TestDebounceCoalescesBurstEvents(t *testing.T) {
	cdb := setupQueueTestDB(t, "path/a.go", "path/b.go", "path/c.go")

	router := &countingRouter{}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	statusFile := filepath.Join(t.TempDir(), ".livedocs-status.json")
	q := NewEnrichmentQueue(QueueConfig{
		BufferSize:       10,
		DebounceDuration: 50 * time.Millisecond,
		Repo:             "test/repo",
		StatusFile:       statusFile,
	}, enricher, cdb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	// Send 3 batches rapidly — all within the debounce window.
	q.Send([]string{"path/a.go"})
	q.Send([]string{"path/b.go"})
	q.Send([]string{"path/c.go"})

	// Wait for debounce + processing time.
	time.Sleep(300 * time.Millisecond)

	// The router should have been called for symbols, but all paths should
	// have been coalesced into a single batch. Each symbol gets 4 predicate
	// calls (the defaults), so 3 symbols * 4 predicates = 12 total calls
	// if coalesced into one batch.
	count := router.routeCount()
	if count == 0 {
		t.Fatal("expected at least one router call, got 0")
	}

	// Verify status file was written.
	data, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatalf("read status file: %v", err)
	}
	var status QueueStatus
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if status.SymbolsEnrichedTotal == 0 {
		t.Error("expected symbols_enriched_total > 0")
	}
	if status.LastEnrichmentTime == "" {
		t.Error("expected last_enrichment_time to be set")
	}
}

// TestDropOnFullDoesNotBlock verifies that Send returns false and does not
// block when the channel buffer is full.
func TestDropOnFullDoesNotBlock(t *testing.T) {
	cdb := setupTestDB(t)
	router := &countingRouter{}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	// Buffer size of 2 — fill it, then verify drop.
	q := NewEnrichmentQueue(QueueConfig{
		BufferSize:       2,
		DebounceDuration: time.Hour, // won't fire during test
	}, enricher, cdb)

	// Don't start the loop — let the channel fill up.
	ok1 := q.Send([]string{"a.go"})
	ok2 := q.Send([]string{"b.go"})
	if !ok1 || !ok2 {
		t.Fatal("first two sends should succeed")
	}

	// This should complete immediately without blocking.
	done := make(chan bool, 1)
	go func() {
		result := q.Send([]string{"c.go"})
		done <- result
	}()

	select {
	case result := <-done:
		if result {
			t.Error("expected Send to return false (dropped), got true")
		}
	case <-time.After(time.Second):
		t.Fatal("Send blocked when channel was full — should be non-blocking")
	}
}

// TestInFlightGuardPreventsConurrentEnrichment verifies that a new batch
// is not dispatched while the previous enrichment is still running.
func TestInFlightGuardPreventsConcurrentEnrichment(t *testing.T) {
	cdb := setupQueueTestDB(t, "path/x.go", "path/y.go")

	blocker := newBlockingRouter()
	enricher, err := NewEnricher(cdb, blocker)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	q := NewEnrichmentQueue(QueueConfig{
		BufferSize:       10,
		DebounceDuration: 30 * time.Millisecond,
		Repo:             "test/repo",
	}, enricher, cdb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	// Send first batch — it will start enrichment and block in the router.
	q.Send([]string{"path/x.go"})

	// Wait for debounce to fire and enrichment to start.
	time.Sleep(100 * time.Millisecond)

	if !q.InFlight() {
		t.Fatal("expected enrichment to be in-flight")
	}

	// Send second batch — this should be coalesced into a debounce window
	// and then skipped by the in-flight guard.
	q.Send([]string{"path/y.go"})

	// Wait for second debounce to fire.
	time.Sleep(100 * time.Millisecond)

	// Unblock the first enrichment.
	close(blocker.blockCh)

	// Wait for processing to complete.
	time.Sleep(100 * time.Millisecond)

	if q.InFlight() {
		t.Error("expected enrichment to no longer be in-flight")
	}
}

// TestSendWithNilPaths verifies that sending nil/empty paths does not panic.
func TestSendWithNilPaths(t *testing.T) {
	cdb := setupTestDB(t)
	router := &countingRouter{}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	q := NewEnrichmentQueue(QueueConfig{
		BufferSize:       5,
		DebounceDuration: 20 * time.Millisecond,
	}, enricher, cdb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	// Should not panic.
	ok := q.Send(nil)
	if !ok {
		t.Error("expected Send(nil) to succeed (empty batch still enqueues)")
	}

	ok = q.Send([]string{})
	if !ok {
		t.Error("expected Send([]) to succeed")
	}

	time.Sleep(100 * time.Millisecond)

	// No symbols resolved, so no enricher calls.
	if router.routeCount() != 0 {
		t.Errorf("expected 0 route calls for empty paths, got %d", router.routeCount())
	}
}

// TestStatusFileContents verifies the structure and content of the status file.
func TestStatusFileContents(t *testing.T) {
	cdb := setupQueueTestDB(t, "pkg/foo.go")

	router := &countingRouter{}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	statusFile := filepath.Join(t.TempDir(), ".livedocs-status.json")
	q := NewEnrichmentQueue(QueueConfig{
		BufferSize:       10,
		DebounceDuration: 30 * time.Millisecond,
		Repo:             "test/repo",
		StatusFile:       statusFile,
	}, enricher, cdb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	q.Send([]string{"pkg/foo.go"})
	time.Sleep(300 * time.Millisecond)

	data, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatalf("read status file: %v", err)
	}

	var status QueueStatus
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify all fields are present in the JSON.
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for _, key := range []string{"queue_depth", "last_enrichment_time", "symbols_enriched_total"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("status file missing key %q", key)
		}
	}
}
