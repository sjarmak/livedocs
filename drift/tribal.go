// Package drift — tribal facts drift detection.
//
// CheckTribal detects drift in tribal knowledge facts by comparing their
// evidence against the current state of the codebase. Unlike structural drift
// which deletes stale claims, tribal drift never deletes rows — it transitions
// facts to 'stale' or 'quarantined' status.
package drift

import (
	"container/list"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sjarmak/livedocs/db"
)

// LivenessChecker probes whether a fact's source reference (e.g. a PR
// comment URL) is still reachable and, if so, returns a stable content
// hash of the upstream payload. Implementations must be safe for
// concurrent use.
type LivenessChecker interface {
	// CheckSourceRefLive returns:
	//   live=true, hash=<sha256>, err=nil   when the source is reachable
	//   live=false, hash="", err=nil        when the source returned 404
	//   live=false, hash="", err=<non-nil>  on transport/runtime errors
	CheckSourceRefLive(ctx context.Context, sourceRef string) (live bool, contentHash string, err error)
}

// CheckTribal scans all active and stale tribal facts for drift.
//
// A fact transitions to 'quarantined' when its subject symbol no longer exists
// in the symbols table.
//
// A fact transitions from 'active' to 'stale' when any of its evidence rows'
// content_hash values differ from the fact's staleness_hash (indicating the
// underlying evidence has changed).
//
// Facts already in 'stale' status remain 'stale' (not deleted) when evidence
// changes again. Facts in 'superseded' or 'deleted' status are never touched.
//
// Returns the count of newly staled facts, newly quarantined facts, and any
// error encountered.
func CheckTribal(cdb *db.ClaimsDB) (staleCount int, quarantinedCount int, err error) {
	return CheckTribalWithLiveness(cdb, nil)
}

// CheckTribalWithLiveness is the liveness-aware variant of CheckTribal. When
// checker is non-nil, the pass invokes it for every pr_comment evidence row
// on an active fact. If the upstream comment is gone (live=false) or returns
// a hash that no longer matches the stored staleness_hash, the fact is
// flipped to 'stale'. Transport errors are treated as non-authoritative and
// do not trigger a transition.
func CheckTribalWithLiveness(cdb *db.ClaimsDB, checker LivenessChecker) (staleCount int, quarantinedCount int, err error) {
	// Fetch only facts that are eligible for drift detection.
	facts, err := cdb.GetTribalFactsByStatuses("active", "stale")
	if err != nil {
		return 0, 0, fmt.Errorf("check tribal drift: %w", err)
	}

	ctx := context.Background()

	for _, fact := range facts {
		// Check whether the subject symbol still exists.
		exists, serr := cdb.SymbolExistsByID(fact.SubjectID)
		if serr != nil {
			return staleCount, quarantinedCount, fmt.Errorf("check tribal drift: symbol %d: %w", fact.SubjectID, serr)
		}

		if !exists {
			// Symbol disappeared — quarantine the fact (regardless of current status).
			if fact.Status != "quarantined" {
				if uerr := cdb.UpdateFactStatus(fact.ID, "quarantined"); uerr != nil {
					return staleCount, quarantinedCount, fmt.Errorf("check tribal drift: quarantine fact %d: %w", fact.ID, uerr)
				}
				quarantinedCount++
			}
			continue
		}

		// Symbol exists — check evidence hashes for staleness.
		// Only transition active facts to stale; already-stale facts stay stale.
		if fact.Status == "active" {
			changed := evidenceHashChanged(fact)
			if !changed && checker != nil {
				changed = prCommentLivenessChanged(ctx, checker, fact)
			}
			if changed {
				if uerr := cdb.UpdateFactStatus(fact.ID, "stale"); uerr != nil {
					return staleCount, quarantinedCount, fmt.Errorf("check tribal drift: stale fact %d: %w", fact.ID, uerr)
				}
				staleCount++
			}
		}
	}

	return staleCount, quarantinedCount, nil
}

// evidenceHashChanged returns true if any evidence row's content_hash differs
// from the fact's staleness_hash, indicating the underlying evidence has been
// modified since the fact was last verified.
func evidenceHashChanged(fact db.TribalFact) bool {
	for _, ev := range fact.Evidence {
		if ev.ContentHash != fact.StalenessHash {
			return true
		}
	}
	return false
}

// prCommentLivenessChanged probes every pr_comment evidence row on the fact
// via the supplied LivenessChecker. Returns true if any probe reports the
// upstream comment is gone OR returns a hash that differs from the recorded
// staleness_hash. Transport errors are ignored (non-authoritative).
func prCommentLivenessChanged(ctx context.Context, checker LivenessChecker, fact db.TribalFact) bool {
	for _, ev := range fact.Evidence {
		if ev.SourceType != "pr_comment" || ev.SourceRef == "" {
			continue
		}
		live, hash, err := checker.CheckSourceRefLive(ctx, ev.SourceRef)
		if err != nil {
			continue
		}
		if !live {
			return true
		}
		if hash != "" && hash != fact.StalenessHash {
			return true
		}
	}
	return false
}

// --- Liveness cache ---

const (
	// livenessCacheTTL is how long a liveness probe result stays fresh.
	// The spec requires a 24h cache window.
	livenessCacheTTL = 24 * time.Hour
	// livenessCacheMaxEntries bounds the cache to prevent unbounded growth.
	livenessCacheMaxEntries = 10000
)

type livenessCacheEntry struct {
	key       string
	live      bool
	hash      string
	checkedAt time.Time
}

// LivenessCache is a bounded LRU cache for liveness probe results with a
// 24h TTL, patterned after mcpserver/staleness.go. Safe for concurrent use.
type LivenessCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // front = most recently used
	nowFn   func() time.Time

	// Budget and delegate are optional: when Budget > 0 the cache
	// stops issuing new CheckSourceRefLive probes after `Budget`
	// distinct cache misses to bound gh api quota consumption.
	Budget   int
	callsMu  sync.Mutex
	callsMax int // atomic upper bound of total calls
	calls    int
	runner   LivenessRunner
}

// LivenessRunner is the low-level probe invoked on cache miss. The
// signature mirrors CommandRunner from extractor/tribal so tests can
// inject fakes without a dependency cycle. It is invoked with the raw
// sourceRef (e.g. a PR comment URL) and must return the HTTP payload and
// a boolean indicating whether the resource was found.
type LivenessRunner func(ctx context.Context, sourceRef string) (body []byte, found bool, err error)

// NewLivenessCache constructs an empty LivenessCache. The nowFn and
// runner are injectable for test isolation. Budget bounds how many cache
// misses (real probes) may be performed in the cache's lifetime; pass 0
// to disable budgeting.
func NewLivenessCache(nowFn func() time.Time, runner LivenessRunner, budget int) *LivenessCache {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &LivenessCache{
		entries: make(map[string]*list.Element),
		order:   list.New(),
		nowFn:   nowFn,
		runner:  runner,
		Budget:  budget,
	}
}

// CheckSourceRefLive implements LivenessChecker. On hit, returns the
// cached entry without touching the runner. On miss, invokes the runner,
// caches the result, and returns it. Budget exhaustion returns
// (true, "", nil) — we fail-open so the drift pass does not incorrectly
// stale facts because we ran out of quota.
func (c *LivenessCache) CheckSourceRefLive(ctx context.Context, sourceRef string) (bool, string, error) {
	if sourceRef == "" {
		return false, "", fmt.Errorf("empty sourceRef")
	}

	// Check cache.
	c.mu.Lock()
	if elem, ok := c.entries[sourceRef]; ok {
		entry := elem.Value.(*livenessCacheEntry)
		if c.nowFn().Sub(entry.checkedAt) < livenessCacheTTL {
			c.order.MoveToFront(elem)
			live, hash := entry.live, entry.hash
			c.mu.Unlock()
			return live, hash, nil
		}
		// Expired — drop.
		c.order.Remove(elem)
		delete(c.entries, sourceRef)
	}
	c.mu.Unlock()

	// Budget check.
	c.callsMu.Lock()
	if c.Budget > 0 && c.calls >= c.Budget {
		c.callsMu.Unlock()
		// Fail-open: pretend live & unchanged so we don't wrongly stale.
		return true, "", nil
	}
	c.calls++
	c.callsMu.Unlock()

	// Miss: invoke runner.
	if c.runner == nil {
		return true, "", nil
	}
	body, found, err := c.runner(ctx, sourceRef)
	if err != nil {
		// Transport error — do not cache, propagate.
		return false, "", err
	}
	if !found {
		c.put(sourceRef, &livenessCacheEntry{
			key:       sourceRef,
			live:      false,
			hash:      "",
			checkedAt: c.nowFn(),
		})
		return false, "", nil
	}
	sum := sha256.Sum256(body)
	hash := fmt.Sprintf("%x", sum)
	c.put(sourceRef, &livenessCacheEntry{
		key:       sourceRef,
		live:      true,
		hash:      hash,
		checkedAt: c.nowFn(),
	})
	return true, hash, nil
}

func (c *LivenessCache) put(key string, entry *livenessCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[key]; ok {
		elem.Value = entry
		c.order.MoveToFront(elem)
		return
	}
	if c.order.Len() >= livenessCacheMaxEntries {
		back := c.order.Back()
		if back != nil {
			evicted := c.order.Remove(back).(*livenessCacheEntry)
			delete(c.entries, evicted.key)
		}
	}
	elem := c.order.PushFront(entry)
	c.entries[key] = elem
}

// IsNotFoundErr is a small helper used by GhLivenessRunner implementations
// to detect 404s emitted by `gh api` (which returns non-zero exit status
// with a stderr containing "HTTP 404").
func IsNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 404") || strings.Contains(msg, "Not Found")
}
