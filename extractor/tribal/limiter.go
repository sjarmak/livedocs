// Package tribal — limiter.go implements KeyedLimiter, a bounded-keyspace
// token-bucket rate limiter.
//
// Purpose and callers:
//   - Primary: mcpserver.TribalMineOnDemandHandler uses KeyedLimiter keyed by
//     the MCP client's SessionID() to cap the rate at which any one session
//     can trigger LLM-backed PR comment mining, preventing daily-budget DoS
//     from a single (potentially compromised) agent (live_docs-m7v.22).
//   - Secondary: TribalMiningService.MineFile may wrap its singleflight
//     dedup-key registration in a KeyedLimiter to bound the singleflight
//     key-space against adversarial callers that enumerate distinct
//     relPaths (live_docs-m7v.17 DoS follow-up).
//
// Design choices and non-goals:
//   - Token bucket is per-key via golang.org/x/time/rate.Limiter; orthogonal
//     to DailyBudget in the mining service (rate caps RPS; budget caps
//     daily cost). The limiter NEVER touches budget state.
//   - The limiter map is bounded by MaxKeys and evicts least-recently-used
//     entries. Without this bound the map itself becomes a DoS surface
//     (adversary sends N distinct synthetic session IDs → unbounded
//     allocations). See TestKeyedLimiter_LRUEvictionBoundsMap.
//   - Empty keys bucket under AnonymousID so callers without a stable
//     identity (stdio transport, legacy clients) still face a quota
//     without causing a panic or a silent bypass.
//   - KeyedLimiter is deliberately stateless across process restarts —
//     rate-limiting is a defense-in-depth throttle, not an authorization
//     boundary. A process restart resets all buckets; that is acceptable
//     because DailyBudget continues to cap lifetime cost at the service
//     layer regardless of restart state.
//
// Threat model addressed:
//   - Compromised/adversarial MCP client exhausting DailyBudget via a
//     rapid burst of tribal_mine_on_demand invocations: mitigated by the
//     per-session token bucket with small Burst and modest Rate.
//   - Adversary forging synthetic session IDs to outrun the LRU and
//     flood the limiter map: mitigated by MaxKeys LRU eviction (old
//     entries drop, attacker cannot grow the map beyond MaxKeys).
//   - Session spoofing of a legitimate user's ID: forgery of a session ID
//     at most shares a bucket with the real user (tighter, not looser
//     throttling) — no privilege escalation. DailyBudget accounting is
//     unaffected; session ID is a bucket key, never an authorization
//     principal.
package tribal

import (
	"container/list"
	"sync"

	"golang.org/x/time/rate"
)

// KeyedLimiterConfig configures a KeyedLimiter.
type KeyedLimiterConfig struct {
	// Rate is the token refill rate in tokens per second for each key.
	// Zero or negative values default to DefaultKeyedLimiterRate.
	Rate float64

	// Burst is the maximum number of tokens a single key may hold at
	// once (the initial bucket size). Zero or negative values default
	// to DefaultKeyedLimiterBurst.
	Burst int

	// MaxKeys is the LRU capacity for the limiter map. When a new key
	// would push the map over MaxKeys, the least-recently-used entry is
	// evicted. Zero or negative values default to
	// DefaultKeyedLimiterMaxKeys.
	//
	// SECURITY-SENSITIVE: sizing this below the expected concurrent
	// session count degrades fairness (active sessions may be swapped in
	// and out of the active map), but as of live_docs-m7v.24 it no longer
	// grants attackers a burst-refresh advantage: evicted limiters are
	// preserved in a same-size snapshot cache and restored on re-insertion,
	// so thrashing MaxKeys+1 distinct IDs cannot reset a victim's drained
	// bucket. Still prefer to set MaxKeys above the expected session
	// population to avoid churn between the two caches.
	MaxKeys int

	// AnonymousID is the bucket used for empty-string keys. All
	// requests with an empty key share this bucket. Defaults to
	// "anonymous".
	AnonymousID string
}

// Sensible defaults chosen to be restrictive enough that a compromised
// agent cannot exhaust a typical 100-call DailyBudget within a minute,
// while still allowing legitimate bursty exploration.
const (
	DefaultKeyedLimiterRate    = 1.0 // 1 token/sec refill
	DefaultKeyedLimiterBurst   = 5   // 5 calls in a burst
	DefaultKeyedLimiterMaxKeys = 256 // bounded limiter map
	DefaultAnonymousID         = "anonymous"
)

// lruEntry pairs a key with its rate limiter and list position.
type lruEntry struct {
	key     string
	limiter *rate.Limiter
}

// KeyedLimiter is a bounded-keyspace token-bucket rate limiter. It is
// safe for concurrent use.
//
// The zero value is NOT usable; construct via NewKeyedLimiter. Calling
// methods on a zero-value KeyedLimiter will panic on the first insert.
//
// Thrash-reset defense (live_docs-m7v.24): when a key is evicted from the
// active LRU its *rate.Limiter is moved into a same-size snapshot LRU
// instead of being discarded. On re-insertion the snapshot entry is
// restored, so bucket state (tokens + last-refill timestamp) survives
// eviction. An adversary cycling MaxKeys+1 distinct IDs therefore cannot
// grant a victim a fresh burst by triggering eviction. Total resident
// limiters are bounded at 2 * MaxKeys (active + snapshot).
type KeyedLimiter struct {
	mu      sync.Mutex
	rate    rate.Limit
	burst   int
	maxKeys int
	anonID  string
	byKey   map[string]*list.Element // key -> *list.Element whose Value is *lruEntry
	lru     *list.List               // front = most-recently-used, back = LRU

	// Snapshot cache: preserves evicted limiters so re-inserting the same
	// key restores (tokens, last-refill) rather than granting a fresh
	// burst. Same-size LRU as the active map so it cannot become a DoS
	// surface beyond O(MaxKeys).
	snapByKey map[string]*list.Element // key -> *list.Element whose Value is *lruEntry
	snapLRU   *list.List               // front = most-recently-inserted, back = LRU
}

// NewKeyedLimiter constructs a KeyedLimiter with the given config, applying
// defaults to zero/negative fields.
func NewKeyedLimiter(cfg KeyedLimiterConfig) *KeyedLimiter {
	if cfg.Rate <= 0 {
		cfg.Rate = DefaultKeyedLimiterRate
	}
	if cfg.Burst <= 0 {
		cfg.Burst = DefaultKeyedLimiterBurst
	}
	if cfg.MaxKeys <= 0 {
		cfg.MaxKeys = DefaultKeyedLimiterMaxKeys
	}
	if cfg.AnonymousID == "" {
		cfg.AnonymousID = DefaultAnonymousID
	}
	return &KeyedLimiter{
		rate:      rate.Limit(cfg.Rate),
		burst:     cfg.Burst,
		maxKeys:   cfg.MaxKeys,
		anonID:    cfg.AnonymousID,
		byKey:     make(map[string]*list.Element, cfg.MaxKeys),
		lru:       list.New(),
		snapByKey: make(map[string]*list.Element, cfg.MaxKeys),
		snapLRU:   list.New(),
	}
}

// Allow reports whether a single token is available for the given key
// RIGHT NOW and consumes it if so. Empty keys are bucketed under the
// anonymous ID so untagged callers still face a quota. Each successful
// Allow touches the key's LRU position so active sessions survive
// eviction as long as they are used.
//
// Allow never blocks: callers that want to wait should use rate.Limiter
// directly via Limiter(key).
func (l *KeyedLimiter) Allow(key string) bool {
	bucketKey := l.bucketKey(key)
	lim := l.limiterForKey(bucketKey)
	return lim.Allow()
}

// Limiter returns the underlying *rate.Limiter for the given key,
// promoting it to most-recently-used. Use this when you need the
// Wait/Reserve variants. Callers MUST NOT retain the returned pointer
// after eviction — prefer calling this per-request.
func (l *KeyedLimiter) Limiter(key string) *rate.Limiter {
	return l.limiterForKey(l.bucketKey(key))
}

// Size reports the current number of tracked keys. Primarily used by tests
// to assert bounded behavior.
func (l *KeyedLimiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lru.Len()
}

// hasKey reports whether a key currently has a tracked limiter. Used only
// by tests; not exported.
func (l *KeyedLimiter) hasKey(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.byKey[l.bucketKey(key)]
	return ok
}

// snapshotSize reports the current number of entries in the snapshot
// cache. Used by tests to assert the snapshot cache stays bounded.
func (l *KeyedLimiter) snapshotSize() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapLRU.Len()
}

// Close releases any resources. Currently a no-op (no goroutines) —
// retained so callers can use the standard defer-close idiom and so
// future additions (e.g. a metrics emitter) don't require changing
// every call site.
func (l *KeyedLimiter) Close() error {
	return nil
}

// bucketKey normalizes an incoming key: empty keys map to the anonymous
// bucket so callers without a stable identity still face a quota.
//
// The anonymous bucket key is prefixed with a NUL byte so no client-supplied
// session ID can collide with (and starve) it — MCP session identifiers are
// identifier-style strings that cannot contain NUL.
func (l *KeyedLimiter) bucketKey(key string) string {
	if key == "" {
		return "\x00anon:" + l.anonID
	}
	return key
}

// limiterForKey returns the *rate.Limiter for bucketKey, inserting a new
// one if needed and evicting the LRU entry when at capacity. The returned
// limiter is also promoted to MRU on every call.
//
// Eviction semantics (live_docs-m7v.24): an entry evicted from the active
// LRU is moved into the snapshot LRU rather than discarded. On re-insertion
// of the same key, the snapshot entry is restored so (tokens, last-refill
// timestamp) carry over, preventing a thrash-reset burst refresh. The
// snapshot LRU is itself bounded at maxKeys so the combined resident set
// stays O(maxKeys).
func (l *KeyedLimiter) limiterForKey(bucketKey string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Already active — MRU promote and return.
	if el, ok := l.byKey[bucketKey]; ok {
		l.lru.MoveToFront(el)
		return el.Value.(*lruEntry).limiter
	}

	// Pop the snapshot for this key BEFORE we evict anyone from the active
	// LRU, so that making room here cannot itself push our own snapshot out
	// of a full snapshot LRU. popSnapshotLocked returns nil for keys with no
	// saved state, in which case a fresh limiter is allocated below.
	ent := l.popSnapshotLocked(bucketKey)

	// Make room in the active LRU, moving any evicted entries into the
	// snapshot LRU so their bucket state survives (live_docs-m7v.24).
	for l.lru.Len() >= l.maxKeys {
		back := l.lru.Back()
		if back == nil {
			break
		}
		l.lru.Remove(back)
		evicted := back.Value.(*lruEntry)
		delete(l.byKey, evicted.key)
		l.storeSnapshotLocked(evicted)
	}

	if ent == nil {
		ent = &lruEntry{
			key:     bucketKey,
			limiter: rate.NewLimiter(l.rate, l.burst),
		}
	}
	el := l.lru.PushFront(ent)
	l.byKey[bucketKey] = el
	return ent.limiter
}

// storeSnapshotLocked places an evicted active entry into the snapshot LRU
// so its *rate.Limiter can be restored if the same key returns. Caller
// must hold l.mu.
//
// Invariant: any given key is in at most one of {active LRU, snapshot LRU}
// at a time. Insertion pops the snapshot before pushing to active; eviction
// pushes from active to snapshot. Therefore the key being stored here is
// guaranteed not to already be present in the snapshot map.
//
// If the snapshot LRU is already at maxKeys, the oldest snapshot entry is
// dropped — this is the only place we truly discard a limiter.
func (l *KeyedLimiter) storeSnapshotLocked(ent *lruEntry) {
	for l.snapLRU.Len() >= l.maxKeys {
		back := l.snapLRU.Back()
		if back == nil {
			break
		}
		l.snapLRU.Remove(back)
		delete(l.snapByKey, back.Value.(*lruEntry).key)
	}
	el := l.snapLRU.PushFront(ent)
	l.snapByKey[ent.key] = el
}

// popSnapshotLocked returns and removes the snapshot entry for bucketKey,
// or nil if no snapshot exists. Caller must hold l.mu.
func (l *KeyedLimiter) popSnapshotLocked(bucketKey string) *lruEntry {
	el, ok := l.snapByKey[bucketKey]
	if !ok {
		return nil
	}
	l.snapLRU.Remove(el)
	delete(l.snapByKey, bucketKey)
	return el.Value.(*lruEntry)
}
