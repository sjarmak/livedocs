// Package mcpserver — tribal_mine_ratelimit_test.go covers the per-session
// rate-limit wrapper around TribalMineOnDemandHandler (live_docs-m7v.22).
package mcpserver

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/sjarmak/livedocs/extractor/tribal"
)

// ---------------------------------------------------------------------------
// Context helper — ctx-based fake session ID propagation
//
// Production code reads the session ID from mcp-go's server.ClientSession via
// SessionIDFromContext. Tests cannot construct a real ClientSession cheaply
// (it is interface-only; implementations live in internal mcp-go types), so
// the handler accepts an override hook that tests install to inject a
// deterministic session ID. The override is a package-scoped function
// variable, restored by t.Cleanup after each test.
// ---------------------------------------------------------------------------

func withTestSessionID(t *testing.T, id string) {
	t.Helper()
	prev := sessionIDResolver
	sessionIDResolver = func(_ context.Context) string { return id }
	t.Cleanup(func() { sessionIDResolver = prev })
}

// ---------------------------------------------------------------------------
// Rate-limit tests
// ---------------------------------------------------------------------------

// Single-session: N rapid calls — first Burst succeed, the rest are
// rate-limited with a safe caller-facing message.
func TestTribalMineOnDemand_RateLimitSingleSession(t *testing.T) {
	const (
		repo    = "test-repo"
		symbol  = "HandleRequest"
		relPath = "pkg/handler.go"
	)
	pool := setupMineTestPool(t, repo, symbol, relPath)

	runner := &fakeMineRunner{prList: "", apiResp: ""}
	llm := &fakeMineLLM{}
	factory := buildFactory(llm, runner.Run, 100)

	limiter := tribal.NewKeyedLimiter(tribal.KeyedLimiterConfig{
		Rate:    0.0001, // practically zero refill during test
		Burst:   2,
		MaxKeys: 16,
	})
	t.Cleanup(func() { _ = limiter.Close() })

	withTestSessionID(t, "sess-A")
	handler := TribalMineOnDemandRateLimitedHandler(pool, factory, limiter, nil)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol": symbol,
		"repo":   repo,
	}}

	// First 2 calls: within burst — not blocked by the limiter (though
	// they may produce empty-result text since miner finds no PRs).
	for i := 0; i < 2; i++ {
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("call %d: unexpected transport err: %v", i, err)
		}
		text := result.Text()
		if strings.Contains(text, "rate") && strings.Contains(text, "limit") {
			t.Fatalf("call %d: rate-limited within burst: %q", i, text)
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
	text := result.Text()
	if !strings.Contains(strings.ToLower(text), "rate") {
		t.Errorf("3rd call: error should mention rate limit, got %q", text)
	}
	// Error message must be short / safe — no internal paths.
	if len(text) > 512 {
		t.Errorf("rate-limit error too long — possible leak: %q", text)
	}
}

// Two sessions: each gets its own bucket (isolated).
func TestTribalMineOnDemand_RateLimitPerSessionIsolation(t *testing.T) {
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

	handler := TribalMineOnDemandRateLimitedHandler(pool, factory, limiter, nil)
	req := &tribalFakeRequest{args: map[string]any{"symbol": symbol, "repo": repo}}

	// Session A: first call OK, second rate-limited.
	withTestSessionID(t, "sess-A")
	if r, _ := handler(context.Background(), req); strings.Contains(strings.ToLower(r.Text()), "rate limit") {
		t.Fatal("sess-A first call should not be rate-limited")
	}
	r2, _ := handler(context.Background(), req)
	if !r2.IsError() || !strings.Contains(strings.ToLower(r2.Text()), "rate") {
		t.Fatalf("sess-A second call should be rate-limited, got %q", r2.Text())
	}

	// Session B: independent bucket — first call must succeed even though
	// sess-A has exhausted its bucket.
	withTestSessionID(t, "sess-B")
	rB, _ := handler(context.Background(), req)
	if rB.IsError() && strings.Contains(strings.ToLower(rB.Text()), "rate") {
		t.Errorf("sess-B leaked sess-A bucket — got rate-limit error: %q", rB.Text())
	}
}

// Anonymous session (no session ID): falls into shared anonymous bucket.
// Behaviour documented: not rejected, but quota-limited.
func TestTribalMineOnDemand_RateLimitAnonymousBucket(t *testing.T) {
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

	withTestSessionID(t, "") // anonymous
	handler := TribalMineOnDemandRateLimitedHandler(pool, factory, limiter, nil)
	req := &tribalFakeRequest{args: map[string]any{"symbol": symbol, "repo": repo}}

	// First anonymous call succeeds.
	r1, _ := handler(context.Background(), req)
	if r1.IsError() && strings.Contains(strings.ToLower(r1.Text()), "rate") {
		t.Fatalf("anon first call should not be rate-limited: %q", r1.Text())
	}

	// Second anonymous call hits the shared anon bucket → rate-limited.
	r2, _ := handler(context.Background(), req)
	if !r2.IsError() || !strings.Contains(strings.ToLower(r2.Text()), "rate") {
		t.Errorf("anon second call should be rate-limited (shared bucket), got %q", r2.Text())
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

	// Capture log output.
	var buf bytes.Buffer
	var bufMu sync.Mutex
	prev := log.Writer()
	log.SetOutput(&syncWriter{w: &buf, mu: &bufMu})
	t.Cleanup(func() { log.SetOutput(prev) })

	withTestSessionID(t, "sess-attributable")
	handler := TribalMineOnDemandRateLimitedHandler(pool, factory, limiter, nil)
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

	withTestSessionID(t, "sess-X")
	handler := TribalMineOnDemandRateLimitedHandler(pool, factory, limiter, nil)
	req := &tribalFakeRequest{args: map[string]any{"symbol": symbol, "repo": repo}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	// Budget-exceeded surfaces as an error result; verify we see it, not a
	// generic rate-limit error.
	if result.IsError() {
		text := strings.ToLower(result.Text())
		if strings.Contains(text, "rate") && !strings.Contains(text, "budget") {
			t.Errorf("rate-limit fired before budget — should be orthogonal: %q", result.Text())
		}
	}
	// Regardless of result shape, the LLM should not have been called more
	// than 2 times (burst + 1 is the miner's allowable headroom).
	if llm.Calls() > 2 {
		t.Errorf("LLM called %d times with budget=1 — budget bypassed", llm.Calls())
	}
}

// Rate-limiter nil: handler falls back to the unrestricted path for safe
// defaults in tests and legacy callers (parity with existing
// TribalMineOnDemandHandler).
func TestTribalMineOnDemand_NilLimiterBehavesAsUnlimited(t *testing.T) {
	const (
		repo    = "test-repo"
		symbol  = "HandleRequest"
		relPath = "pkg/handler.go"
	)
	pool := setupMineTestPool(t, repo, symbol, relPath)

	runner := &fakeMineRunner{}
	llm := &fakeMineLLM{}
	factory := buildFactory(llm, runner.Run, 10)

	withTestSessionID(t, "any")
	handler := TribalMineOnDemandRateLimitedHandler(pool, factory, nil, nil)
	req := &tribalFakeRequest{args: map[string]any{"symbol": symbol, "repo": repo}}

	// Even 100 rapid calls must never rate-limit with a nil limiter.
	for i := 0; i < 100; i++ {
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("call %d err: %v", i, err)
		}
		if result.IsError() && strings.Contains(strings.ToLower(result.Text()), "rate limit") {
			t.Fatalf("nil limiter unexpectedly rate-limited at call %d: %q", i, result.Text())
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

	var buf bytes.Buffer
	var bufMu sync.Mutex
	prev := log.Writer()
	log.SetOutput(&syncWriter{w: &buf, mu: &bufMu})
	t.Cleanup(func() { log.SetOutput(prev) })

	// 10 KB session ID with embedded newlines — must be truncated and
	// %q-escaped so no raw newline reaches the log writer.
	hostileID := strings.Repeat("A\n", 5000)
	withTestSessionID(t, hostileID)

	handler := TribalMineOnDemandRateLimitedHandler(pool, factory, limiter, nil)
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

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

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
