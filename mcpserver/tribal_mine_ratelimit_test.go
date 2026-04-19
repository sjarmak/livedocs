// Package mcpserver — tribal_mine_ratelimit_test.go covers the per-session
// rate-limit wrapper around TribalMineOnDemandHandler (live_docs-m7v.22).
package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sjarmak/livedocs/extractor/tribal"
)

// ---------------------------------------------------------------------------
// Session-ID injection seam (live_docs-m7v.25)
//
// Production code reads the session ID from mcp-go's server.ClientSession via
// SessionIDFromContext. Tests cannot construct a real ClientSession cheaply
// (it is interface-only; implementations live in internal mcp-go types), so
// the handler accepts a WithSessionIDResolver option that tests install to
// inject a deterministic session ID. Unlike the prior package-level var
// pattern, the resolver is captured in the handler closure at construction
// time, so concurrent tests (including t.Parallel()) are race-free.
// ---------------------------------------------------------------------------

// constSessionID returns a SessionIDResolver that always yields id. Used for
// tests that need a single fixed session for the lifetime of the handler.
func constSessionID(id string) SessionIDResolver {
	return func(_ context.Context) string { return id }
}

// mutableSessionID returns a (setter, resolver) pair whose resolver reads
// from a test-local atomic pointer. Tests that need to switch the session ID
// between handler invocations (e.g., per-session isolation tests) use this
// helper so each test owns its own resolver state. The atomic load makes the
// helper safe to use from handler goroutines even when the test drives it
// serially.
func mutableSessionID(initial string) (setID func(string), resolver SessionIDResolver) {
	var cur atomic.Value
	cur.Store(initial)
	setID = func(id string) { cur.Store(id) }
	resolver = func(_ context.Context) string {
		v, _ := cur.Load().(string)
		return v
	}
	return setID, resolver
}

// ---------------------------------------------------------------------------
// Rate-limit tests
// ---------------------------------------------------------------------------

// Single-session: N rapid calls — first Burst succeed, the rest are
// rate-limited with a safe caller-facing message.
func TestTribalMineOnDemand_RateLimitSingleSession(t *testing.T) {
	t.Parallel()
	// rate=0.0001 → practically zero refill during the test window so the
	// burst-then-deny sequencing is deterministic.
	handler, req := buildRateLimitHandler(t, rateLimitFixture{
		Burst:       2,
		Rate:        0.0001,
		SessionID:   "sess-A",
		DailyBudget: 100,
	})

	// First 2 calls: within burst — not blocked by the limiter (though
	// they may produce empty-result text since miner finds no PRs).
	for i := 0; i < 2; i++ {
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("call %d: unexpected transport err: %v", i, err)
		}
		if errors.Is(ResultCause(result), ErrRateLimited) {
			t.Fatalf("call %d: rate-limited within burst (text=%q)",
				i, result.Text())
		}
	}

	// 3rd call: must be rate-limited.
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("3rd call: unexpected transport err: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("3rd call: expected error result, got text: %q", result.Text())
	}
	if !errors.Is(ResultCause(result), ErrRateLimited) {
		t.Errorf("3rd call: cause should be ErrRateLimited, got %v (text=%q)",
			ResultCause(result), result.Text())
	}
	// Error message must be short / safe — no internal paths.
	if len(result.Text()) > 512 {
		t.Errorf("rate-limit error too long — possible leak: %q", result.Text())
	}
}

// Regression: the rate-limited denial result carries the exported
// ErrRateLimited sentinel as its cause so callers can distinguish it
// from budget-exceeded or transport errors without string-matching the
// user-facing message (live_docs-m7v.26).
func TestTribalMineOnDemand_RateLimitDenialCarriesSentinel(t *testing.T) {
	t.Parallel()
	// Burst=1 + practically-zero refill → first call drains the bucket,
	// second call is guaranteed to be rate-limited (no timing flake).
	handler, req := buildRateLimitHandler(t, rateLimitFixture{
		Burst:       1,
		Rate:        0.0001,
		SessionID:   "sess-sentinel",
		DailyBudget: 100,
	})

	// Drain the bucket.
	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("drain call: unexpected transport err: %v", err)
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected transport err: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result, got text: %q", result.Text())
	}
	if !errors.Is(ResultCause(result), ErrRateLimited) {
		t.Fatalf("expected cause errors.Is ErrRateLimited, got %v", ResultCause(result))
	}
	// User-visible text must NOT embed the sentinel's Error() string —
	// the cause is for programmatic identification only; the text stays
	// user-friendly and can be reworded independently.
	if strings.Contains(result.Text(), ErrRateLimited.Error()) {
		t.Errorf("user-facing text leaked sentinel Error() string: %q", result.Text())
	}
}

// Regression: admitted calls (not rate-limited) carry a nil cause so
// callers cannot mistake a successful invocation — or a non-rate-limit
// error — for a rate-limit denial. Pairs with
// TestTribalMineOnDemand_RateLimitDenialCarriesSentinel.
func TestTribalMineOnDemand_AdmittedCallHasNilCause(t *testing.T) {
	t.Parallel()
	// Permissive limiter (100/100): the call is always admitted; this
	// test pins the cause-is-nil invariant for the admitted path.
	handler, req := buildRateLimitHandler(t, rateLimitFixture{
		Burst:       100,
		Rate:        100,
		SessionID:   "sess-ok",
		DailyBudget: 10,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// The call was admitted — not an error result at all, and therefore
	// no rate-limit cause attached. Asserting both pins the invariant
	// against a future bug where the handler returns a non-rate-limit
	// error result with a nil cause (which would pass only the sentinel
	// check).
	if result.IsError() {
		t.Fatalf("admitted call must not yield an error result (text=%q)", result.Text())
	}
	if errors.Is(ResultCause(result), ErrRateLimited) {
		t.Errorf("admitted call must not carry ErrRateLimited cause (text=%q)", result.Text())
	}
}

// Two sessions: each gets its own bucket (isolated).
func TestTribalMineOnDemand_RateLimitPerSessionIsolation(t *testing.T) {
	t.Parallel()
	const (
		repo    = "test-repo"
		symbol  = "HandleRequest"
		relPath = "pkg/handler.go"
	)
	pool := setupMineTestPool(t, repo, symbol, relPath)

	runner := &fakeMineRunner{}
	llm := &fakeMineLLM{}
	factory := buildFactory(llm, runner.Run, 100)

	limiter := tribal.NewKeyedLimiter(tribal.KeyedLimiterConfig{
		Rate:    0.0001,
		Burst:   1,
		MaxKeys: 16,
	})
	t.Cleanup(func() { _ = limiter.Close() })

	setSession, resolver := mutableSessionID("sess-A")
	handler := TribalMineOnDemandRateLimitedHandler(
		pool, factory, limiter, nil,
		WithSessionIDResolver(resolver),
	)
	req := &tribalFakeRequest{args: map[string]any{"symbol": symbol, "repo": repo}}

	// Session A: first call OK, second rate-limited.
	// Primary discriminator is errors.Is against the exported sentinel
	// (live_docs-m7v.26); text check kept as a secondary smoke signal.
	if r, _ := handler(context.Background(), req); errors.Is(ResultCause(r), ErrRateLimited) {
		t.Fatalf("sess-A first call should not be rate-limited (text=%q)", r.Text())
	}
	r2, _ := handler(context.Background(), req)
	if !r2.IsError() || !errors.Is(ResultCause(r2), ErrRateLimited) {
		t.Fatalf("sess-A second call should be rate-limited, got text=%q cause=%v",
			r2.Text(), ResultCause(r2))
	}

	// Session B: independent bucket — first call must succeed even though
	// sess-A has exhausted its bucket.
	setSession("sess-B")
	rB, _ := handler(context.Background(), req)
	if errors.Is(ResultCause(rB), ErrRateLimited) {
		t.Errorf("sess-B leaked sess-A bucket — got rate-limit cause: %v (text=%q)",
			ResultCause(rB), rB.Text())
	}
}

// Anonymous session (no session ID): falls into shared anonymous bucket.
// Behaviour documented: not rejected, but quota-limited.
func TestTribalMineOnDemand_RateLimitAnonymousBucket(t *testing.T) {
	t.Parallel()
	const (
		repo    = "test-repo"
		symbol  = "HandleRequest"
		relPath = "pkg/handler.go"
	)
	pool := setupMineTestPool(t, repo, symbol, relPath)

	runner := &fakeMineRunner{}
	llm := &fakeMineLLM{}
	factory := buildFactory(llm, runner.Run, 100)

	limiter := tribal.NewKeyedLimiter(tribal.KeyedLimiterConfig{
		Rate:    0.0001,
		Burst:   1,
		MaxKeys: 16,
	})
	t.Cleanup(func() { _ = limiter.Close() })

	handler := TribalMineOnDemandRateLimitedHandler(
		pool, factory, limiter, nil,
		WithSessionIDResolver(constSessionID("")), // anonymous
	)
	req := &tribalFakeRequest{args: map[string]any{"symbol": symbol, "repo": repo}}

	// First anonymous call succeeds.
	// Primary discriminator is errors.Is against ErrRateLimited
	// (live_docs-m7v.26).
	r1, _ := handler(context.Background(), req)
	if errors.Is(ResultCause(r1), ErrRateLimited) {
		t.Fatalf("anon first call should not be rate-limited: %q", r1.Text())
	}

	// Second anonymous call hits the shared anon bucket → rate-limited.
	r2, _ := handler(context.Background(), req)
	if !r2.IsError() || !errors.Is(ResultCause(r2), ErrRateLimited) {
		t.Errorf("anon second call should be rate-limited (shared bucket), got text=%q cause=%v",
			r2.Text(), ResultCause(r2))
	}
}

// Logging contract: when a mine attempt succeeds, the server logs session
// identity alongside repo+symbol so budget deductions are attributable.
func TestTribalMineOnDemand_LogsSessionIdentity(t *testing.T) {
	const (
		repo    = "test-repo"
		symbol  = "HandleRequest"
		relPath = "pkg/handler.go"
	)
	pool := setupMineTestPool(t, repo, symbol, relPath)

	runner := &fakeMineRunner{
		prList: "42\n",
		apiResp: samplePRCommentJSONLine(
			"must hold lock",
			relPath,
			"https://github.com/org/test-repo/pull/42#r1",
		),
	}
	llm := &fakeMineLLM{responses: []string{
		`{"kind":"invariant","body":"must hold lock","confidence":0.85}`,
	}}
	factory := buildFactory(llm, runner.Run, 100)

	limiter := tribal.NewKeyedLimiter(tribal.KeyedLimiterConfig{
		Rate:    100, // no throttle
		Burst:   100,
		MaxKeys: 16,
	})
	t.Cleanup(func() { _ = limiter.Close() })

	// Capture log output. This test mutates the process-global default
	// logger, so it cannot run in parallel with other tests that do the
	// same. Keep it serial.
	var buf bytes.Buffer
	var bufMu sync.Mutex
	prev := log.Writer()
	log.SetOutput(&syncWriter{w: &buf, mu: &bufMu})
	t.Cleanup(func() { log.SetOutput(prev) })

	handler := TribalMineOnDemandRateLimitedHandler(
		pool, factory, limiter, nil,
		WithSessionIDResolver(constSessionID("sess-attributable")),
	)
	req := &tribalFakeRequest{args: map[string]any{"symbol": symbol, "repo": repo}}

	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("handler err: %v", err)
	}

	bufMu.Lock()
	logged := buf.String()
	bufMu.Unlock()

	if !strings.Contains(logged, "sess-attributable") {
		t.Errorf("logs missing session ID (accountability): %q", logged)
	}
	if !strings.Contains(logged, repo) {
		t.Errorf("logs missing repo: %q", logged)
	}
	if !strings.Contains(logged, symbol) {
		t.Errorf("logs missing symbol: %q", logged)
	}
}

// DailyBudget unchanged: a rate-limiter denial does not leak into
// budget accounting, and a budget-exceeded error is still surfaced
// normally even under a permissive limiter.
func TestTribalMineOnDemand_BudgetExceededStillSurfaced(t *testing.T) {
	t.Parallel()
	const (
		repo    = "test-repo"
		symbol  = "HandleRequest"
		relPath = "pkg/handler.go"
	)
	pool := setupMineTestPool(t, repo, symbol, relPath)

	runner := &fakeMineRunner{
		prList: "1\n2\n",
		apiResp: samplePRCommentJSONLine("one", relPath, "https://github.com/org/test-repo/pull/1#r1") + "\n" +
			samplePRCommentJSONLine("two", relPath, "https://github.com/org/test-repo/pull/1#r2"),
	}
	llm := &fakeMineLLM{responses: []string{
		`{"kind":"rationale","body":"one","confidence":0.7}`,
		`{"kind":"rationale","body":"two","confidence":0.7}`,
	}}
	factory := buildFactory(llm, runner.Run, 1) // budget=1

	// Permissive limiter: should NOT pre-empt budget-exceeded.
	limiter := tribal.NewKeyedLimiter(tribal.KeyedLimiterConfig{
		Rate:    100,
		Burst:   100,
		MaxKeys: 16,
	})
	t.Cleanup(func() { _ = limiter.Close() })

	handler := TribalMineOnDemandRateLimitedHandler(
		pool, factory, limiter, nil,
		WithSessionIDResolver(constSessionID("sess-X")),
	)
	req := &tribalFakeRequest{args: map[string]any{"symbol": symbol, "repo": repo}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	// Unconditional invariants: with budget=1 the rate-limiter must NOT
	// have short-circuited (this test is specifically about budget, not
	// rate), and the LLM must not have been called more than budget allows.
	// If the handler returns IsError()==false here, the budget error must
	// still be observable in the result body — assert that unconditionally
	// rather than gating the check on IsError().
	text := strings.ToLower(result.Text())
	// Primary discriminator for "rate-limit did not fire" is errors.Is
	// against the exported sentinel; budget errors never carry this cause
	// (live_docs-m7v.26).
	if errors.Is(ResultCause(result), ErrRateLimited) {
		t.Fatalf("rate-limit fired before budget — should be orthogonal: text=%q cause=%v",
			result.Text(), ResultCause(result))
	}
	if !strings.Contains(text, "budget") {
		t.Errorf("budget-exceeded path did not surface 'budget' in result text: %q", result.Text())
	}
	if llm.Calls() > 1 {
		t.Errorf("LLM called %d times with budget=1 — budget bypassed", llm.Calls())
	}
}

// Rate-limiter nil: handler falls back to the unrestricted path for safe
// defaults in tests and legacy callers (parity with existing
// TribalMineOnDemandHandler).
func TestTribalMineOnDemand_NilLimiterBehavesAsUnlimited(t *testing.T) {
	t.Parallel()
	const (
		repo    = "test-repo"
		symbol  = "HandleRequest"
		relPath = "pkg/handler.go"
	)
	pool := setupMineTestPool(t, repo, symbol, relPath)

	runner := &fakeMineRunner{}
	llm := &fakeMineLLM{}
	factory := buildFactory(llm, runner.Run, 10)

	handler := TribalMineOnDemandRateLimitedHandler(
		pool, factory, nil, nil,
		WithSessionIDResolver(constSessionID("any")),
	)
	req := &tribalFakeRequest{args: map[string]any{"symbol": symbol, "repo": repo}}

	// Even 100 rapid calls must never rate-limit with a nil limiter.
	// Primary discriminator is the exported ErrRateLimited sentinel
	// (live_docs-m7v.26).
	for i := 0; i < 100; i++ {
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("call %d err: %v", i, err)
		}
		if errors.Is(ResultCause(result), ErrRateLimited) {
			t.Fatalf("nil limiter unexpectedly rate-limited at call %d: %q",
				i, result.Text())
		}
	}
}

// Log-injection / log-size DoS: hostile session ID and repo/symbol values
// are truncated and %q-escaped so a multi-megabyte or newline-laden input
// cannot forge log lines or bloat log storage.
func TestTribalMineOnDemand_LogFieldsBoundedAndEscaped(t *testing.T) {
	const repo = "test-repo"
	pool := setupMineTestPool(t, repo, "Sym", "pkg/x.go")

	runner := &fakeMineRunner{}
	llm := &fakeMineLLM{}
	factory := buildFactory(llm, runner.Run, 10)

	limiter := tribal.NewKeyedLimiter(tribal.KeyedLimiterConfig{
		Rate:    100,
		Burst:   100,
		MaxKeys: 16,
	})
	t.Cleanup(func() { _ = limiter.Close() })

	// This test mutates the process-global default logger. Keep it serial
	// so concurrent log-capture tests do not clobber each other.
	var buf bytes.Buffer
	var bufMu sync.Mutex
	prev := log.Writer()
	log.SetOutput(&syncWriter{w: &buf, mu: &bufMu})
	t.Cleanup(func() { log.SetOutput(prev) })

	// 10 KB session ID with embedded newlines — must be truncated and
	// %q-escaped so no raw newline reaches the log writer.
	hostileID := strings.Repeat("A\n", 5000)

	handler := TribalMineOnDemandRateLimitedHandler(
		pool, factory, limiter, nil,
		WithSessionIDResolver(constSessionID(hostileID)),
	)
	req := &tribalFakeRequest{args: map[string]any{
		"symbol": "Sym",
		"repo":   repo,
	}}
	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("handler err: %v", err)
	}

	bufMu.Lock()
	logged := buf.String()
	bufMu.Unlock()

	// The log line must NOT contain literal newlines beyond the single
	// trailing newline the logger itself appends. If log-injection were
	// possible, we'd see many.
	if count := strings.Count(logged, "\n"); count > 1 {
		t.Errorf("log contains %d newlines — injection via session ID: %q", count, logged)
	}
	// Total log bytes must be well under the raw hostile input size.
	if len(logged) > 2000 {
		t.Errorf("log not truncated: %d bytes (hostile id was %d bytes)", len(logged), len(hostileID))
	}
}

// Race-freedom: multiple handlers, each with its own session resolver,
// execute in parallel. The old package-level var sessionIDResolver pattern
// would fail -race under this test because withTestSessionID mutates the
// shared var while sibling goroutines read it. The constructor-injected
// seam (WithSessionIDResolver) captures the resolver per-handler, so each
// parallel goroutine reads only its own closure state. This test exists
// specifically to pin the m7v.25 fix in place.
func TestTribalMineOnDemand_ParallelHandlersAreRaceFree(t *testing.T) {
	t.Parallel()
	const (
		repo    = "test-repo"
		symbol  = "HandleRequest"
		relPath = "pkg/handler.go"
	)
	pool := setupMineTestPool(t, repo, symbol, relPath)

	// Permissive limiter: these sessions should never be rejected by the
	// bucket; the point is to exercise the resolver under race conditions.
	limiter := tribal.NewKeyedLimiter(tribal.KeyedLimiterConfig{
		Rate:    100,
		Burst:   100,
		MaxKeys: 64,
	})
	t.Cleanup(func() { _ = limiter.Close() })

	sessions := []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8"}
	for _, sid := range sessions {
		sid := sid
		t.Run(sid, func(t *testing.T) {
			t.Parallel()
			runner := &fakeMineRunner{}
			llm := &fakeMineLLM{}
			factory := buildFactory(llm, runner.Run, 10)

			handler := TribalMineOnDemandRateLimitedHandler(
				pool, factory, limiter, nil,
				WithSessionIDResolver(constSessionID(sid)),
			)
			req := &tribalFakeRequest{args: map[string]any{
				"symbol": symbol,
				"repo":   repo,
			}}
			for i := 0; i < 5; i++ {
				if _, err := handler(context.Background(), req); err != nil {
					t.Fatalf("%s call %d: %v", sid, i, err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// rlTestRepo, rlTestSymbol, and rlTestRelPath are the canonical fixture
// identifiers used by every rate-limit test that does not specifically need
// a different shape. Centralising them lets buildRateLimitHandler return
// a ready-to-call (handler, req) pair without per-test repetition.
const (
	rlTestRepo    = "test-repo"
	rlTestSymbol  = "HandleRequest"
	rlTestRelPath = "pkg/handler.go"
)

// rateLimitFixture bundles the per-test variation axes for
// buildRateLimitHandler. Burst (int) and DailyBudget (int) share a type
// and were silently transposable when passed positionally, so callers
// must now use named-field struct literals (live_docs-m7v.45).
type rateLimitFixture struct {
	Burst       int
	Rate        float64
	SessionID   string
	DailyBudget int
}

// buildRateLimitHandler assembles the standard rate-limit test scaffolding
// (pool, no-op runner+llm, factory, keyed limiter with MaxKeys=16, handler
// with a constant session-ID resolver) and returns the constructed handler
// plus a canned tribalFakeRequest for the fixture symbol/repo. The limiter
// is registered for cleanup via t.Cleanup; the pool registers its own
// cleanup inside setupMineTestPool.
//
// fx carries the per-test variation axes: Burst+Rate determine whether a
// call is admitted or denied; SessionID determines bucket attribution;
// DailyBudget is included so tests that want to exercise the orthogonal
// budget path can do so without reaching for the lower-level
// setupMineTestPool/buildFactory primitives.
//
// Extracted under live_docs-m7v.39 from RateLimitSingleSession,
// RateLimitDenialCarriesSentinel, and AdmittedCallHasNilCause; collapsed
// from 5 positional args to a fixture struct under live_docs-m7v.45.
func buildRateLimitHandler(t *testing.T, fx rateLimitFixture) (ToolHandler, *tribalFakeRequest) {
	t.Helper()
	pool := setupMineTestPool(t, rlTestRepo, rlTestSymbol, rlTestRelPath)

	runner := &fakeMineRunner{}
	llm := &fakeMineLLM{}
	factory := buildFactory(llm, runner.Run, fx.DailyBudget)

	limiter := tribal.NewKeyedLimiter(tribal.KeyedLimiterConfig{
		Rate:    fx.Rate,
		Burst:   fx.Burst,
		MaxKeys: 16,
	})
	t.Cleanup(func() { _ = limiter.Close() })

	handler := TribalMineOnDemandRateLimitedHandler(
		pool, factory, limiter, nil,
		WithSessionIDResolver(constSessionID(fx.SessionID)),
	)
	req := &tribalFakeRequest{args: map[string]any{
		"symbol": rlTestSymbol,
		"repo":   rlTestRepo,
	}}
	return handler, req
}

// syncWriter serialises writes to an underlying buffer so the standard
// logger can safely emit from the handler goroutine while tests inspect
// the buffer.
type syncWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
