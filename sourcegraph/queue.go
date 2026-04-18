// Package sourcegraph — queue.go implements the enrichment queue that bridges
// the watch loop to the enricher. It receives file paths via a buffered channel,
// applies debounce to coalesce rapid events, resolves paths to symbol IDs, and
// dispatches enrichment runs with an in-flight guard.
package sourcegraph

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sjarmak/livedocs/db"
)

// defaultBufferSize is the channel buffer capacity for incoming path batches.
const defaultBufferSize = 100

// defaultDebounceDuration is the quiet period after the last event before
// the queue processes a batch.
const defaultDebounceDuration = 5 * time.Second

// QueueConfig holds configuration for the enrichment queue.
type QueueConfig struct {
	// BufferSize is the channel buffer capacity. Zero uses defaultBufferSize.
	BufferSize int
	// DebounceDuration is the quiet period after the last event before processing.
	// Zero uses defaultDebounceDuration.
	DebounceDuration time.Duration
	// Repo is the repository identifier used for path resolution.
	Repo string
	// StatusFile is the path to write .livedocs-status.json. Empty disables status writes.
	StatusFile string
}

// QueueStatus is the JSON structure written to the status file.
type QueueStatus struct {
	QueueDepth           int    `json:"queue_depth"`
	LastEnrichmentTime   string `json:"last_enrichment_time"`
	SymbolsEnrichedTotal int64  `json:"symbols_enriched_total"`
}

// EnrichmentQueue bridges file change events from the watch loop to the
// enricher. It coalesces rapid events via debounce and prevents concurrent
// enrichment runs with an in-flight guard.
type EnrichmentQueue struct {
	config   QueueConfig
	enricher *Enricher
	claimsDB *db.ClaimsDB

	pathCh chan []string // buffered channel for incoming path batches

	inFlight int32 // atomic: 1 while enricher is running

	statusMu             sync.Mutex
	symbolsEnrichedTotal int64
	lastEnrichmentTime   time.Time
}

// NewEnrichmentQueue creates an EnrichmentQueue with the given configuration.
func NewEnrichmentQueue(cfg QueueConfig, enricher *Enricher, claimsDB *db.ClaimsDB) *EnrichmentQueue {
	bufSize := cfg.BufferSize
	if bufSize <= 0 {
		bufSize = defaultBufferSize
	}
	if cfg.DebounceDuration <= 0 {
		cfg.DebounceDuration = defaultDebounceDuration
	}
	return &EnrichmentQueue{
		config:   cfg,
		enricher: enricher,
		claimsDB: claimsDB,
		pathCh:   make(chan []string, bufSize),
	}
}

// Send enqueues a batch of changed file paths for enrichment. It is
// non-blocking: if the channel buffer is full, it logs a warning and drops
// the batch. Returns true if enqueued, false if dropped.
func (q *EnrichmentQueue) Send(paths []string) bool {
	select {
	case q.pathCh <- paths:
		return true
	default:
		log.Printf("enrichment-queue: channel full (capacity %d), dropping batch of %d paths",
			cap(q.pathCh), len(paths))
		return false
	}
}

// Start launches the debounce loop goroutine. It returns immediately.
// The loop runs until ctx is cancelled.
func (q *EnrichmentQueue) Start(ctx context.Context) {
	go q.loop(ctx)
}

// loop is the main debounce goroutine. It accumulates paths from the channel,
// waits for a quiet period, then dispatches enrichment.
func (q *EnrichmentQueue) loop(ctx context.Context) {
	accumulated := make(map[string]struct{})
	timer := time.NewTimer(q.config.DebounceDuration)
	timer.Stop() // start stopped; we arm it on first receive

	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return

		case paths := <-q.pathCh:
			for _, p := range paths {
				accumulated[p] = struct{}{}
			}
			// Reset debounce timer on every receive.
			if !timer.Stop() {
				// Drain the timer channel if it already fired.
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(q.config.DebounceDuration)

		case <-timer.C:
			if len(accumulated) == 0 {
				continue
			}
			// Snapshot and clear the accumulated set.
			paths := make([]string, 0, len(accumulated))
			for p := range accumulated {
				paths = append(paths, p)
			}
			accumulated = make(map[string]struct{})

			q.processBatch(ctx, paths)
		}
	}
}

// processBatch resolves file paths to symbol IDs and runs the enricher.
// It enforces the in-flight guard: if a previous run is still active,
// the batch is skipped.
func (q *EnrichmentQueue) processBatch(ctx context.Context, paths []string) {
	// In-flight guard: skip if previous enrichment is still running.
	if !atomic.CompareAndSwapInt32(&q.inFlight, 0, 1) {
		log.Printf("enrichment-queue: skipping batch of %d paths — previous enrichment still running", len(paths))
		return
	}
	defer atomic.StoreInt32(&q.inFlight, 0)

	// Resolve file paths to symbol IDs.
	symbolIDs := q.resolveSymbolIDs(paths)
	if len(symbolIDs) == 0 {
		log.Printf("enrichment-queue: no symbols found for %d paths, skipping", len(paths))
		q.writeStatus(len(q.pathCh))
		return
	}

	summary, err := q.enricher.Run(ctx, EnrichOpts{
		SymbolIDs: symbolIDs,
	})
	if err != nil {
		log.Printf("enrichment-queue: enricher error: %v", err)
	}

	q.statusMu.Lock()
	q.symbolsEnrichedTotal += int64(summary.SymbolsEnriched)
	q.lastEnrichmentTime = time.Now()
	q.statusMu.Unlock()

	q.writeStatus(len(q.pathCh))
}

// resolveSymbolIDs maps file paths to symbol IDs via ClaimsDB.
func (q *EnrichmentQueue) resolveSymbolIDs(paths []string) []int64 {
	seen := make(map[int64]struct{})
	var ids []int64

	for _, path := range paths {
		symbols, err := q.claimsDB.ListSymbolsByImportPath(path)
		if err != nil {
			log.Printf("enrichment-queue: resolve %s: %v", path, err)
			continue
		}
		for _, sym := range symbols {
			if _, ok := seen[sym.ID]; !ok {
				seen[sym.ID] = struct{}{}
				ids = append(ids, sym.ID)
			}
		}
	}
	return ids
}

// writeStatus writes the current queue status to the configured status file.
func (q *EnrichmentQueue) writeStatus(queueDepth int) {
	if q.config.StatusFile == "" {
		return
	}

	q.statusMu.Lock()
	status := QueueStatus{
		QueueDepth:           queueDepth,
		SymbolsEnrichedTotal: q.symbolsEnrichedTotal,
	}
	if !q.lastEnrichmentTime.IsZero() {
		status.LastEnrichmentTime = q.lastEnrichmentTime.Format(time.RFC3339)
	}
	q.statusMu.Unlock()

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		log.Printf("enrichment-queue: marshal status: %v", err)
		return
	}
	if err := os.WriteFile(q.config.StatusFile, data, 0644); err != nil {
		log.Printf("enrichment-queue: write status file: %v", err)
	}
}

// Status returns the current queue status. Exported for testing and monitoring.
func (q *EnrichmentQueue) Status() QueueStatus {
	q.statusMu.Lock()
	defer q.statusMu.Unlock()

	status := QueueStatus{
		QueueDepth:           len(q.pathCh),
		SymbolsEnrichedTotal: q.symbolsEnrichedTotal,
	}
	if !q.lastEnrichmentTime.IsZero() {
		status.LastEnrichmentTime = q.lastEnrichmentTime.Format(time.RFC3339)
	}
	return status
}

// Depth returns the current number of pending batches in the channel.
func (q *EnrichmentQueue) Depth() int {
	return len(q.pathCh)
}

// InFlight reports whether an enrichment run is currently in progress.
func (q *EnrichmentQueue) InFlight() bool {
	return atomic.LoadInt32(&q.inFlight) == 1
}

// formatPaths is a debug helper that joins paths for log messages.
func formatPaths(paths []string, max int) string {
	if len(paths) <= max {
		return fmt.Sprintf("%v", paths)
	}
	return fmt.Sprintf("%v... (%d total)", paths[:max], len(paths))
}
