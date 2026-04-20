// Package mcpserver — slog_adapter.go bridges the mcpserver MineLogger
// interface to the standard library's log/slog package. The project
// targets Go 1.21+ (currently Go 1.25.7) so slog is the idiomatic
// structured-logging API; this adapter lets callers pass an *slog.Logger
// wherever a MineLogger is accepted without forcing the existing
// log.Printf-style call sites inside tribal_mine.go to be rewritten at
// once (live_docs-m7v.48).
//
// Migration path:
//   - Existing callers that pass nil or a *log.Logger keep working
//     unchanged.
//   - New callers that already hold an *slog.Logger wrap it once via
//     NewSlogMineLogger and pass the result.
//   - Future beads may upgrade individual log sites to structured
//     key-value form; that is explicitly out of scope here.
package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
)

// SlogMineLogger adapts an *slog.Logger so it satisfies the MineLogger
// interface. Printf-style format strings are rendered via fmt.Sprintf
// and forwarded as the slog message at INFO level. The adapter does NOT
// promote format arguments to structured key-value attributes — that is
// a deliberate trade-off (see live_docs-m7v.48 design notes): the five
// production call sites inside tribal_mine.go continue to use %q-quoted
// positional formatting, which matches the security invariant that
// session IDs and repo/symbol values are escaped before reaching the
// log writer (tribal_mine.go:truncateForLog + %q).
//
// Callers that want fully structured logging for a specific site should
// obtain the underlying *slog.Logger through their own seam and call its
// methods directly, bypassing this adapter. This type exists only to
// satisfy MineLogger.
//
// The zero value is usable and routes through slog.Default(), matching
// the permissive contract of the standard library's package-level log
// functions. Prefer NewSlogMineLogger at construction sites so the
// intent (this is an adapter, not a struct embedding) is explicit.
type SlogMineLogger struct {
	// logger is the destination for formatted log lines. A nil logger
	// falls back to slog.Default() at call time so the adapter is safe
	// even when the caller has not explicitly configured a logger
	// (mirrors the log.Printf fallback contract of the existing
	// MineLogger nil-handling inside tribal_mine.go:mineLogf).
	logger *slog.Logger
}

// NewSlogMineLogger returns a MineLogger that forwards Printf calls to l
// at INFO level. If l is nil, the adapter delegates to slog.Default() at
// each call, which matches the permissive behaviour of the standard
// library's package-level log functions.
//
// Returning the MineLogger interface (rather than a concrete
// *SlogMineLogger) keeps the adapter an implementation detail: callers
// hold the interface, so future changes to the adapter's internals do
// not ripple through their code.
func NewSlogMineLogger(l *slog.Logger) MineLogger {
	return &SlogMineLogger{logger: l}
}

// Printf renders format/args via fmt.Sprintf and emits the result as a
// single INFO-level slog record. Matches the signature of the MineLogger
// interface and of the existing *log.Logger.Printf method, so the two
// are freely interchangeable at call sites.
//
// Short-circuits via l.Enabled when the handler is configured above INFO:
// in that case the Sprintf result would be discarded anyway, so skipping
// the format work saves an allocation per suppressed call. Matters for
// hot paths like per-session rate-limit accounting that can fire thousands
// of times per minute in production.
func (s *SlogMineLogger) Printf(format string, args ...any) {
	l := s.logger
	if l == nil {
		l = slog.Default()
	}
	if !l.Enabled(context.Background(), slog.LevelInfo) {
		return
	}
	l.Info(fmt.Sprintf(format, args...))
}
