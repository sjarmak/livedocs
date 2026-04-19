// Package mcpserver — errors.go declares the package's exported error
// sentinels in one place so the surface is discoverable without grepping
// through tool-specific files. Sentinels declared here MUST:
//
//   - Be stable across implementations (godoc'd Error() string pinned by
//     errors_test.go so a casual reword cannot break errors.Is callers).
//   - Carry no provider- or transport-specific detail in the sentinel itself
//     — wrapping callers attach context with %w.
//   - Be referenced by callers via errors.Is, never by string match.
//
// History: previously declared inline in tribal_mine.go alongside the
// rate-limit handler that attaches them. Moved here under live_docs-m7v.39
// so additions to the package's error surface land in a single, obvious
// file.
package mcpserver

import "errors"

// ErrLLMClientUnavailable is the sentinel MiningServiceFactory
// implementations return (wrapped) when neither the primary nor the
// fallback LLM client can be resolved at call time. The handler uses
// errors.Is to classify this distinct from generic factory failures
// (missing git metadata, DB error, etc.) so the MCP client sees an
// actionable message rather than the generic
// "mining service unavailable" fallback.
//
// The factory MAY wrap the sentinel with additional context (e.g. "claude
// CLI not on PATH and ANTHROPIC_API_KEY unset") — that context is
// preserved when the handler renders the caller-facing message. The
// sentinel itself never embeds provider-specific details so it remains a
// stable errors.Is target across implementations.
var ErrLLMClientUnavailable = errors.New("llm client unavailable")

// ErrRateLimited is the stable sentinel carried (via NewErrorResultWithCause)
// on the rate-limit denial ToolResult returned by the per-session wrapper
// installed in TribalMineOnDemandRateLimitedHandler. Middleware and tests
// detect rate-limit denials with `errors.Is(ResultCause(result),
// ErrRateLimited)` — a string-stable discriminator that cannot drift when
// the caller-facing text is reworded.
//
// Callers MUST retrieve the cause through ResultCause; `errors.Is` applied
// directly to a ToolResult value will return false because resultAdapter's
// Unwrap returns *mcp.CallToolResult rather than satisfying the standard
// errors.Unwrap() error convention (deliberate: keeps cause off the wire).
//
//   - Attached ONLY to the per-session rate-limit denial in the
//     rate-limited wrapper — NOT to budget-exceeded, LLM-unavailable, or
//     other mining errors. Those have their own discriminators
//     (MiningError.Code="budget_exceeded", ErrLLMClientUnavailable, etc.).
//   - Server-side only: adaptHandler forwards only the raw
//     *mcp.CallToolResult to the mcp-go transport, so the cause never
//     crosses the wire and clients see only the user-visible Text().
//   - Error() string ("mcpserver: rate limit exceeded") is deliberately
//     distinct from the user-facing text so the text can be reworded
//     without breaking `errors.Is` semantics. errors_test.go pins the
//     string so a future reword breaks the build, not silent callers.
var ErrRateLimited = errors.New("mcpserver: rate limit exceeded")
