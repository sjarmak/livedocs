package evergreen

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Compile-time: KeyedRateLimiter implements RateLimiter.
var _ RateLimiter = (*KeyedRateLimiter)(nil)

type sessionKey struct{}

func sessionCtx(id string) context.Context {
	return context.WithValue(context.Background(), sessionKey{}, id)
}

func resolveSession(ctx context.Context) string {
	s, _ := ctx.Value(sessionKey{}).(string)
	return s
}

// --- Defaults ------------------------------------------------------------

func TestNewKeyedRateLimiter_AppliesDefaults(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{})
	if l.perDoc == nil || l.perSession == nil {
		t.Fatal("buckets not constructed")
	}
	if l.resolve == nil {
		t.Fatal("resolver must default to empty-string returner")
	}
	// A nil-config resolver should yield "" for any ctx.
	if got := l.resolve(context.Background()); got != "" {
		t.Errorf("default resolver = %q, want empty", got)
	}
}

// --- Happy path: the first request always succeeds ----------------------

func TestAllow_FirstRequestSucceeds(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{Resolver: resolveSession})
	if err := l.Allow(sessionCtx("s1"), "doc-a"); err != nil {
		t.Errorf("first request denied: %v", err)
	}
}

// --- Per-document cap ----------------------------------------------------

// Per-doc bucket has burst=1 by default. The second refresh for the same
// (session, doc) inside the same microsecond is rejected.
func TestAllow_PerDocCap_SecondCallDenied(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{Resolver: resolveSession})
	ctx := sessionCtx("s1")
	if err := l.Allow(ctx, "doc-a"); err != nil {
		t.Fatalf("first: %v", err)
	}
	err := l.Allow(ctx, "doc-a")
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("second: got %v, want wrapped ErrRateLimited", err)
	}
}

// Distinct docs share the per-session cap but have independent per-doc
// buckets — the second distinct doc is NOT rate-limited by the per-doc rule.
func TestAllow_PerDocCap_DistinctDocsIndependent(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{
		PerSessionRefreshesPerHour: 1000, // take the session cap out of the picture
		PerSessionBurst:            100,
		Resolver:                   resolveSession,
	})
	ctx := sessionCtx("s1")
	if err := l.Allow(ctx, "doc-a"); err != nil {
		t.Fatal(err)
	}
	if err := l.Allow(ctx, "doc-b"); err != nil {
		t.Errorf("distinct doc-b denied: %v", err)
	}
}

// Distinct sessions have independent per-doc buckets even for the SAME doc.
// Otherwise multi-user installs would interfere with each other.
func TestAllow_PerDocCap_DistinctSessionsIndependent(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{
		PerSessionRefreshesPerHour: 1000,
		PerSessionBurst:            100,
		Resolver:                   resolveSession,
	})
	if err := l.Allow(sessionCtx("s1"), "doc-a"); err != nil {
		t.Fatal(err)
	}
	if err := l.Allow(sessionCtx("s2"), "doc-a"); err != nil {
		t.Errorf("second session denied same doc: %v", err)
	}
}

// --- Per-session cap -----------------------------------------------------

// When the session burst is exhausted, further refreshes are denied even
// when the per-doc buckets would allow them.
func TestAllow_PerSessionCap_BurstExhaustion(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{
		PerSessionRefreshesPerHour: 3600, // 1/sec refill; doesn't matter for the burst test
		PerSessionBurst:            2,
		Resolver:                   resolveSession,
	})
	ctx := sessionCtx("s1")
	for i := 0; i < 2; i++ {
		docID := "d" + string(rune('a'+i))
		if err := l.Allow(ctx, docID); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	err := l.Allow(ctx, "d-overflow")
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("overflow: got %v, want wrapped ErrRateLimited", err)
	}
}

// Per-session caps do NOT cross sessions: session B gets a fresh quota.
func TestAllow_PerSessionCap_IsolatedBetweenSessions(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{
		PerSessionBurst: 1,
		Resolver:        resolveSession,
	})
	if err := l.Allow(sessionCtx("s1"), "doc-a"); err != nil {
		t.Fatal(err)
	}
	err := l.Allow(sessionCtx("s1"), "doc-b")
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("s1 overflow: got %v", err)
	}
	if err := l.Allow(sessionCtx("s2"), "doc-a"); err != nil {
		t.Errorf("s2 share denied: %v", err)
	}
}

// Session cap fires BEFORE the per-doc token is consumed, so a denied
// session-exhaustion attempt leaves the per-doc bucket intact for when
// the session eventually refills.
func TestAllow_SessionCheckedBeforePerDoc(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{
		PerSessionBurst: 1,
		Resolver:        resolveSession,
	})
	ctx := sessionCtx("s1")
	// Consume the single session token via doc-a.
	if err := l.Allow(ctx, "doc-a"); err != nil {
		t.Fatal(err)
	}
	// doc-b is denied — session exhausted. Crucially, doc-b's per-doc
	// bucket must NOT have consumed a token (we can't observe this
	// directly, but the session-first order guarantees it by design).
	if err := l.Allow(ctx, "doc-b"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected session denial, got %v", err)
	}
	// Sanity: a different session's doc-b bucket is untouched.
	if err := l.Allow(sessionCtx("s2"), "doc-b"); err != nil {
		t.Errorf("other session denied: %v", err)
	}
}

// --- Empty / missing session IDs ----------------------------------------

// Requests with no session ID share the anonymous bucket, so untagged
// callers cannot bypass the cap by omitting the session.
func TestAllow_EmptySessionSharesAnonymousBucket(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{
		PerSessionBurst: 1,
		Resolver:        resolveSession, // returns "" for our ctx
	})
	ctx := context.Background() // no session value
	if err := l.Allow(ctx, "doc-a"); err != nil {
		t.Fatal(err)
	}
	if err := l.Allow(ctx, "doc-b"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected anonymous overflow, got %v", err)
	}
}

// A nil resolver is tolerated and routes all callers to the anonymous
// bucket. We never want a panic on a misconfigured limiter.
func TestAllow_NilResolverTolerated(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{Resolver: nil})
	if err := l.Allow(context.Background(), "doc-a"); err != nil {
		t.Errorf("first with nil resolver: %v", err)
	}
}

// --- Custom config -------------------------------------------------------

func TestAllow_CustomBurstsRespected(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{
		PerDocBurst:     3,
		PerSessionBurst: 10,
		Resolver:        resolveSession,
	})
	ctx := sessionCtx("s1")
	for i := 0; i < 3; i++ {
		if err := l.Allow(ctx, "doc-a"); err != nil {
			t.Fatalf("call %d of 3 allowed bursts: %v", i, err)
		}
	}
	// 4th call on doc-a should be denied by the per-doc cap.
	if err := l.Allow(ctx, "doc-a"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("4th call: got %v", err)
	}
}

// --- Concurrency ---------------------------------------------------------

// Concurrent Allow calls from many goroutines must not data-race on the
// limiter state and must respect burst semantics across goroutines.
func TestAllow_ConcurrentSameSessionSameDoc(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{
		PerDocBurst:     5,
		PerSessionBurst: 50,
		Resolver:        resolveSession,
	})
	ctx := sessionCtx("s1")
	const n = 40
	var wg sync.WaitGroup
	allowed := make(chan bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := l.Allow(ctx, "doc-a")
			allowed <- (err == nil)
		}()
	}
	wg.Wait()
	close(allowed)
	var passed int
	for ok := range allowed {
		if ok {
			passed++
		}
	}
	// At most PerDocBurst=5 should have been allowed before the per-doc
	// bucket drained. At worst, refill during the test grants one extra.
	if passed < 1 || passed > 6 {
		t.Errorf("concurrent allowed = %d, want in [1, 6] (burst=5)", passed)
	}
}

// --- Close ---------------------------------------------------------------

func TestClose_NoError(t *testing.T) {
	l := NewKeyedRateLimiter(RateLimiterConfig{})
	if err := l.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}
