// Package mcpserver — slog_adapter_test.go verifies that SlogMineLogger
// satisfies the MineLogger interface and forwards Printf-style log lines
// into the underlying *slog.Logger. The project targets Go 1.21+, so slog
// is the idiomatic structured-logging API for new callers; the adapter
// exists so those callers can opt in without rewriting the five
// MineLogger call sites inside tribal_mine.go (live_docs-m7v.48).
package mcpserver

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// TestSlogMineLogger_ForwardsPrintfToSlogHandler pins the primary
// contract: a Printf call on the adapter produces exactly one line on
// the underlying slog.Handler at INFO level, with the fully-formatted
// message in the standard `msg=` slot.
func TestSlogMineLogger_ForwardsPrintfToSlogHandler(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var bufMu sync.Mutex
	handler := slog.NewTextHandler(&lockedWriter{w: &buf, mu: &bufMu}, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	logger := NewSlogMineLogger(slog.New(handler))

	// Printf with mixed format verbs must render identically to fmt.Sprintf.
	logger.Printf("tribal_mine_on_demand: factory error for repo=%q: %v",
		"test-repo", "unspecified failure")

	bufMu.Lock()
	out := buf.String()
	bufMu.Unlock()

	if !strings.Contains(out, "level=INFO") {
		t.Errorf("expected INFO level in output, got %q", out)
	}
	// The formatted message must land in the msg= slot. slog's text handler
	// wraps the full msg value in outer double-quotes and escapes any inner
	// quotes with a backslash, so the literal bytes for a %q-quoted repo
	// appear as `\"test-repo\"` inside the record. Both the repo and the
	// wrapped error text must be present.
	if !strings.Contains(out, `\"test-repo\"`) {
		t.Errorf("expected escaped %%q-quoted repo in msg, got %q", out)
	}
	if !strings.Contains(out, "unspecified failure") {
		t.Errorf("expected wrapped error text in msg, got %q", out)
	}
	// Regression guard: exactly one log line (adapter must not fan out).
	if count := strings.Count(strings.TrimRight(out, "\n"), "\n"); count != 0 {
		t.Errorf("expected exactly one log line, got %d: %q", count+1, out)
	}
}

// TestSlogMineLogger_NoArgs verifies the zero-args path does not double-
// format or panic when the format string contains no verbs.
func TestSlogMineLogger_NoArgs(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var bufMu sync.Mutex
	handler := slog.NewTextHandler(&lockedWriter{w: &buf, mu: &bufMu}, nil)
	logger := NewSlogMineLogger(slog.New(handler))

	logger.Printf("tribal_mine_on_demand: ready")

	bufMu.Lock()
	out := buf.String()
	bufMu.Unlock()

	if !strings.Contains(out, "tribal_mine_on_demand: ready") {
		t.Errorf("expected literal message in output, got %q", out)
	}
}

// TestSlogMineLogger_SatisfiesMineLogger is a compile-time assertion via
// a runtime smoke test: passing the adapter into a function typed as
// MineLogger must compile and must forward Printf correctly. If the
// adapter stops satisfying the interface, this test fails to build.
func TestSlogMineLogger_SatisfiesMineLogger(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var bufMu sync.Mutex
	handler := slog.NewTextHandler(&lockedWriter{w: &buf, mu: &bufMu}, nil)

	var ml MineLogger = NewSlogMineLogger(slog.New(handler))
	ml.Printf("session_id=%q outcome=%q", "sess-A", "ok")

	bufMu.Lock()
	out := buf.String()
	bufMu.Unlock()

	// slog.TextHandler escapes inner quotes inside the msg field, so the
	// literal bytes are `\"sess-A\"` and `\"ok\"` rather than bare quotes.
	if !strings.Contains(out, `\"sess-A\"`) || !strings.Contains(out, `\"ok\"`) {
		t.Errorf("MineLogger-typed call did not forward format args: %q", out)
	}
}

// TestNewSlogMineLogger_NilFallsBackToDefault verifies that passing a nil
// *slog.Logger yields an adapter that safely delegates to slog.Default
// rather than panicking on the first Printf. The MineLogger interface
// already documents nil-safety at the call-site layer (mineLogf falls back
// to log.Printf when the adapter itself is nil); this test pins the
// orthogonal invariant that a non-nil adapter wrapping a nil logger is
// also safe to use — matching the permissive contract of the existing
// `log` package default logger.
func TestNewSlogMineLogger_NilFallsBackToDefault(t *testing.T) {
	t.Parallel()

	// Swap slog.Default so the fallback is observable without racing other
	// tests that inspect slog.Default concurrently. t.Parallel is safe
	// because we restore prev in Cleanup and no other test here mutates
	// slog.Default.
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	var bufMu sync.Mutex
	handler := slog.NewTextHandler(&lockedWriter{w: &buf, mu: &bufMu}, nil)
	slog.SetDefault(slog.New(handler))

	logger := NewSlogMineLogger(nil)
	// Must not panic.
	logger.Printf("fallback message repo=%q", "r")

	bufMu.Lock()
	out := buf.String()
	bufMu.Unlock()

	if !strings.Contains(out, "fallback message") {
		t.Errorf("expected fallback message routed through slog.Default, got %q", out)
	}
}

// lockedWriter serialises writes to an underlying bytes.Buffer so tests
// can safely inspect the buffer while the slog handler emits from a
// different goroutine. Kept local to this file to avoid colliding with
// syncWriter in tribal_mine_ratelimit_test.go (they serve the same role
// but are named distinctly to underline that this one is scoped to the
// slog-adapter tests).
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
