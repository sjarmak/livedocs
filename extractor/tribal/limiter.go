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
type KeyedLimiter struct {
	mu      sync.Mutex
	rate    rate.Limit
	burst   int
	maxKeys int
	anonID  string
	byKey   map[string]*list.Element // key -> *list.Element whose Value is *lruEntry
	lru     *list.List               // front = most-recently-used, back = LRU
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
		rate:    rate.Limit(cfg.Rate),
		burst:   cfg.Burst,
		maxKeys: cfg.MaxKeys,
		anonID:  cfg.AnonymousID,
		byKey:   make(map[string]*list.Element, cfg.MaxKeys),
		lru:     list.New(),
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

// Close releases any resources. Currently a no-op (no goroutines) —
// retained so callers can use the standard defer-close idiom and so
// future additions (e.g. a metrics emitter) don't require changing
// every call site.
func (l *KeyedLimiter) Close() error {
	return nil
}

// bucketKey normalizes an incoming key: empty keys map to the anonymous
// bucket so callers without a stable identity still face a quota.
func (l *KeyedLimiter) bucketKey(key string) string {
	if key == "" {
		return l.anonID
	}
	return key
}

// limiterForKey returns the *rate.Limiter for bucketKey, inserting a new
// one if needed and evicting the LRU entry when at capacity. The returned
// limiter is also promoted to MRU on every call.
func (l *KeyedLimiter) limiterForKey(bucketKey string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	if el, ok := l.byKey[bucketKey]; ok {
		l.lru.MoveToFront(el)
		return el.Value.(*lruEntry).limiter
	}

	// Evict LRU entries until we have room for a new key.
	for l.lru.Len() >= l.maxKeys {
		back := l.lru.Back()
		if back == nil {
			break
		}
		l.lru.Remove(back)
		delete(l.byKey, back.Value.(*lruEntry).key)
	}

	ent := &lruEntry{
		key:     bucketKey,
		limiter: rate.NewLimiter(l.rate, l.burst),
	}
	el := l.lru.PushFront(ent)
	l.byKey[bucketKey] = el
	return ent.limiter
}
