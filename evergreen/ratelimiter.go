package evergreen

import (
	"context"
	"fmt"

	"github.com/sjarmak/livedocs/extractor/tribal"
)

// SessionIDResolver extracts an MCP session identifier from ctx. Empty
// strings are bucketed under the limiter's anonymous ID so untagged
// callers still face a quota. A resolver that always returns "" is legal
// and causes all requests to share the anonymous bucket.
type SessionIDResolver func(ctx context.Context) string

// RateLimiterConfig configures a KeyedRateLimiter. Zero-valued fields
// fall through to sensible defaults documented on each field.
type RateLimiterConfig struct {
	// PerDocRefreshesPerHour caps refreshes for a single (session, doc)
	// pair. Default 6 per hour ≈ 1 per 10 minutes, which aligns with the
	// PRD Phase 1 guardrail "1 refresh/doc/10min per session".
	PerDocRefreshesPerHour float64

	// PerDocBurst is the initial token count for the (session, doc) bucket.
	// Default 1 so an idle document can be refreshed immediately, but a
	// second refresh must wait for the bucket to refill.
	PerDocBurst int

	// PerSessionRefreshesPerHour caps total refreshes from a single
	// session across all documents. Default 10.
	PerSessionRefreshesPerHour float64

	// PerSessionBurst is the initial token count for the per-session
	// bucket. Default 3; lets a session fire a small burst but not
	// exhaust the hourly allowance at once.
	PerSessionBurst int

	// MaxSessions is the LRU capacity for both the per-session and
	// per-(session, doc) keyed limiter maps. Default 256. Sized low
	// enough that the memory ceiling is predictable, high enough that a
	// normal multi-user install will not see eviction-induced thrash.
	MaxSessions int

	// Resolver extracts the MCP session ID from ctx. Required; nil is
	// treated as "resolve to empty string" so callers without a resolver
	// (tests, stdio transports) still function under the anonymous bucket.
	Resolver SessionIDResolver
}

// Package-level defaults used when a config field is zero.
const (
	defaultPerDocPerHour      = 6.0  // 6/hr ≈ one per 10 minutes
	defaultPerDocBurst        = 1
	defaultPerSessionPerHour  = 10.0
	defaultPerSessionBurst    = 3
	defaultRateLimiterMaxKeys = 256
)

// KeyedRateLimiter implements RateLimiter using two composed token buckets:
// one keyed by (session, doc) to cap per-document refresh frequency, and
// one keyed by session to cap the overall session budget. Both buckets
// must allow for a request to succeed.
//
// Threat model: defense-in-depth against a compromised or runaway MCP
// client invoking evergreen_refresh in a loop. Not an authorization
// boundary; session IDs are bucket keys, never principals. See the
// tribal package's KeyedLimiter doc for the broader design.
//
// When used with an adapter backend that has its own upstream rate
// limiting (e.g. sourcegraph's HasActiveRefresh plus deepsearch_quota),
// configure this as a loose secondary cap. When used with the OSS
// deepsearch-MCP executor, this is the only gate.
type KeyedRateLimiter struct {
	perDoc     *tribal.KeyedLimiter
	perSession *tribal.KeyedLimiter
	resolve    SessionIDResolver
}

// NewKeyedRateLimiter builds a KeyedRateLimiter from cfg, applying
// defaults to zero fields.
func NewKeyedRateLimiter(cfg RateLimiterConfig) *KeyedRateLimiter {
	if cfg.PerDocRefreshesPerHour <= 0 {
		cfg.PerDocRefreshesPerHour = defaultPerDocPerHour
	}
	if cfg.PerDocBurst <= 0 {
		cfg.PerDocBurst = defaultPerDocBurst
	}
	if cfg.PerSessionRefreshesPerHour <= 0 {
		cfg.PerSessionRefreshesPerHour = defaultPerSessionPerHour
	}
	if cfg.PerSessionBurst <= 0 {
		cfg.PerSessionBurst = defaultPerSessionBurst
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = defaultRateLimiterMaxKeys
	}
	resolver := cfg.Resolver
	if resolver == nil {
		resolver = func(context.Context) string { return "" }
	}

	perHourToPerSec := func(perHour float64) float64 { return perHour / 3600.0 }

	return &KeyedRateLimiter{
		perDoc: tribal.NewKeyedLimiter(tribal.KeyedLimiterConfig{
			Rate:    perHourToPerSec(cfg.PerDocRefreshesPerHour),
			Burst:   cfg.PerDocBurst,
			MaxKeys: cfg.MaxSessions,
		}),
		perSession: tribal.NewKeyedLimiter(tribal.KeyedLimiterConfig{
			Rate:    perHourToPerSec(cfg.PerSessionRefreshesPerHour),
			Burst:   cfg.PerSessionBurst,
			MaxKeys: cfg.MaxSessions,
		}),
		resolve: resolver,
	}
}

// Allow implements RateLimiter. The key is the document ID; session ID is
// resolved from ctx. Returns nil when both the per-session and per-(session,
// doc) buckets allow the request, or a wrapped ErrRateLimited otherwise.
//
// Denial reasons are distinguishable by error message but both satisfy
// errors.Is(err, ErrRateLimited). The session cap is checked first so an
// exhausted session rejects early without consuming a per-doc token.
// Callers that want to distinguish session-vs-doc denial for telemetry
// should inspect the wrapped error's message; the sentinel identity is
// the supported boundary.
func (l *KeyedRateLimiter) Allow(ctx context.Context, docID string) error {
	sessionID := l.resolve(ctx)
	if !l.perSession.Allow(sessionID) {
		return fmt.Errorf("%w: session cap exhausted", ErrRateLimited)
	}
	// Compose the per-(session, doc) key. NUL separator prevents collision
	// with any plausible session-id or doc-id contents.
	perDocKey := sessionID + "\x00" + docID
	if !l.perDoc.Allow(perDocKey) {
		return fmt.Errorf("%w: per-document cap exhausted", ErrRateLimited)
	}
	return nil
}

// Close releases underlying resources. Currently a no-op; exposed so
// callers can use defer-close and any future resource additions do not
// break call sites.
func (l *KeyedRateLimiter) Close() error {
	// Best-effort: the tribal.KeyedLimiter.Close is itself a no-op today.
	_ = l.perDoc.Close()
	_ = l.perSession.Close()
	return nil
}
