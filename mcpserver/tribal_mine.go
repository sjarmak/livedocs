// Package mcpserver tribal_mine.go defines the tribal_mine_on_demand MCP
// tool. Agents invoke this tool with (symbol, repo) to run JIT PR-comment
// mining for files containing the given symbol. The handler is a thin
// adapter — all orchestration (cursor, budget, upsert, generation counter)
// lives in extractor/tribal.TribalMiningService, per M7.
//
// This file uses ONLY adapter types — no mcp-go imports.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor/tribal"
)

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// SessionIDResolver resolves the MCP client's session identifier for the
// current request context. Production callers inject
// adapter.go:SessionIDFromContext; tests inject a deterministic closure via
// the WithSessionIDResolver option. Resolvers MUST be safe to call
// concurrently from multiple goroutines.
type SessionIDResolver func(context.Context) string

// mineHandlerOpts holds configuration overrides for
// TribalMineOnDemandRateLimitedHandler. Fields are unexported; callers set
// them exclusively through MineHandlerOption values. Each handler captures
// a snapshot of its opts at construction time so concurrent handlers never
// share mutable state (live_docs-m7v.25 — race safety).
type mineHandlerOpts struct {
	sessionIDResolver SessionIDResolver
}

// MineHandlerOption configures an on-demand mining handler. The variadic
// option pattern preserves source compatibility for callers that want
// default behavior and keeps the test-injection seam visible in the
// handler's exported signature.
type MineHandlerOption func(*mineHandlerOpts)

// WithSessionIDResolver overrides the default MCP session-ID resolver
// (adapter.go:SessionIDFromContext). Tests use this option to inject a
// deterministic session ID without constructing a real mcp-go
// ClientSession. Production callers SHOULD NOT set this — the default
// correctly resolves the session from ctx.
//
// Passing a nil resolver leaves the default in place.
func WithSessionIDResolver(r SessionIDResolver) MineHandlerOption {
	return func(o *mineHandlerOpts) {
		if r != nil {
			o.sessionIDResolver = r
		}
	}
}

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
//     without breaking `errors.Is` semantics.
var ErrRateLimited = errors.New("mcpserver: rate limit exceeded")

// MiningServiceFactory constructs a tribal.TribalMiningService bound to the
// given repo and claims DB. Callers that expose the tribal_mine_on_demand
// tool must supply a factory that wires in the appropriate LLM client,
// command runner, and daily budget for the deployment environment.
//
// The factory is called once per handler invocation, so implementations can
// freely re-read configuration or refresh credentials on each call. The
// factory MUST return a service whose underlying miner shares state (via
// the claims DB) with the service returned by subsequent calls on the same
// repo — this is what allows the M3 cursor to make repeat invocations
// idempotent.
//
// When the LLM client cannot be resolved at call time (a transient probe
// failure — e.g. PATH temporarily lacking the `claude` binary, credential
// rotation race), the factory MUST return an error wrapping
// ErrLLMClientUnavailable so the handler can render an actionable MCP
// error result. See live_docs-m7v.23 for rationale.
type MiningServiceFactory func(repo string, cdb *db.ClaimsDB) (*tribal.TribalMiningService, error)

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

// tribalMineResponse is the JSON response for tribal_mine_on_demand.
//
// FailedCount is the total number of per-fact UpsertTribalFact failures
// summed across every MiningResult returned by the service. It is the
// authoritative signal that partial work was silently dropped —
// clients MUST inspect FailedCount and surface a warning when it is
// non-zero even if len(Facts) > 0. The actual errors are logged
// server-side only; raw FailedErrors are intentionally NOT serialized
// to the client (see live_docs-m7v.21 — sanitization bead).
type tribalMineResponse struct {
	Symbol      string               `json:"symbol"`
	Repo        string               `json:"repo"`
	Facts       []tribalFactEnvelope `json:"facts"`
	Total       int                  `json:"total"`
	FailedCount int                  `json:"failed_count"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// TribalMineOnDemandHandler returns a ToolHandler that triggers PR-comment
// mining for files containing the requested symbol and returns the newly
// mined facts with full provenance envelopes.
//
// Behavior:
//   - Resolves repo -> *db.ClaimsDB via the DBPool (reusing repo validation
//     and LRU caching).
//   - Builds a TribalMiningService via the injected factory so the
//     mcpserver package never touches mcp-go, PRCommentMiner internals,
//     DailyBudget, or cursor columns directly (CLAUDE.md + M7 invariant).
//   - Delegates to MineSymbol, which owns symbol→files resolution, cursor
//     loading, budget enforcement, fact upsert, and generation bumping.
//   - Returns a JSON response with provenance envelopes for each newly
//     inserted fact, or a safe error message on failure.
func TribalMineOnDemandHandler(pool *DBPool, factory MiningServiceFactory) ToolHandler {
	return func(ctx context.Context, req ToolRequest) (ToolResult, error) {
		// --- Parameter parsing ---
		symbol, err := req.RequireString("symbol")
		if err != nil {
			return NewErrorResult("missing required parameter 'symbol'"), nil
		}
		if symbol == "" {
			return NewErrorResult("parameter 'symbol' must not be empty"), nil
		}
		repo, err := req.RequireString("repo")
		if err != nil {
			return NewErrorResult("missing required parameter 'repo'"), nil
		}
		if repo == "" {
			return NewErrorResult("parameter 'repo' must not be empty"), nil
		}

		// --- Defensive: factory must be wired ---
		if factory == nil {
			return NewErrorResult("tribal_mine_on_demand: mining service not configured"), nil
		}

		// --- Resolve repo (validateRepoName inside pool.Open rejects traversal) ---
		// Pre-check existence without opening so a missing repo produces a
		// caller-safe error message — pool.Open's underlying sqlite error
		// would otherwise leak the server's data directory path.
		if exists, existsErr := pool.RepoExists(repo); existsErr != nil {
			return NewErrorResultf("tribal_mine_on_demand: invalid repo %q", repo), nil
		} else if !exists {
			return NewErrorResultf("tribal_mine_on_demand: repo %q not found", repo), nil
		}
		cdb, err := pool.Open(repo)
		if err != nil {
			return NewErrorResultf("tribal_mine_on_demand: open repo %q", repo), nil
		}

		// --- Build the mining service for this repo ---
		svc, err := factory(repo, cdb)
		if err != nil {
			// Distinguish transient LLM-client unreachability from other
			// factory failures so operators can act on the error. The
			// sentinel is stable across factory implementations; the
			// wrapped message is preserved because it is produced by
			// code paths that already redact credentials (see
			// cmd/livedocs/mcp_mining_factory.go and the constructors
			// in semantic/). Other factory errors keep the generic
			// message so they do not accidentally leak internal paths or
			// DB details from the wrapping chain.
			if errors.Is(err, ErrLLMClientUnavailable) {
				// Log the server-side detail for operator diagnosis. The
				// MCP client sees only a generic message: the MCP transport
				// may be HTTP/SSE, so the actionable remediation (specific
				// CLI name, specific env-var name) must not reach remote
				// agents. Operators see the detail in their logs and can
				// surface it through their own tooling.
				log.Printf("tribal_mine_on_demand: llm client unreachable at call time: %v", err)
				return NewErrorResult(
					"tribal_mine_on_demand: LLM provider unavailable; contact the server operator",
				), nil
			}
			// Log the underlying error so operators can diagnose, but
			// return only a generic message to the caller to avoid
			// leaking internal paths or DB details via %w chains.
			log.Printf("tribal_mine_on_demand: factory error for repo=%q: %v", repo, err)
			return NewErrorResult("tribal_mine_on_demand: mining service unavailable"), nil
		}
		if svc == nil {
			return NewErrorResult("tribal_mine_on_demand: mining service unavailable"), nil
		}

		// --- Delegate to service ---
		results, mineErr := svc.MineSymbol(ctx, symbol, tribal.TriggerJITOnDemand)

		// Translate structured MiningError into safe, caller-facing results.
		if mineErr != nil {
			return renderMineError(mineErr), nil
		}

		// --- Build response ---
		// Log any per-file partial-upsert failures server-side BEFORE shaping
		// the response. FailedErrors may carry raw DB constraint text or
		// paths, so they never leave the server (m7v.21); only aggregated
		// counts reach the JSON envelope.
		logMiningFailures(repo, results)

		resp, textMsg, err := buildTribalMineResponse(symbol, repo, results)
		if err != nil {
			return NewErrorResultf("tribal_mine_on_demand: %v", err), nil
		}
		if textMsg != "" {
			return NewTextResult(textMsg), nil
		}

		data, err := json.Marshal(resp)
		if err != nil {
			return NewErrorResultf("tribal_mine_on_demand: marshal response: %v", err), nil
		}
		return NewTextResult(string(data)), nil
	}
}

// renderMineError translates a non-nil mining error from MineSymbol/MineFile
// into a caller-safe ToolResult. Classification order matters and is part of
// the contract: any future *tribal.MiningError code that wraps
// ErrMineThrottled for non-throttle semantics MUST be inserted before step 1
// with its own guard, otherwise it will be misclassified.
//
//  1. tribal.ErrMineThrottled — surfaced FIRST so MCP clients can detect
//     the per-file rate-limit denial via
//     errors.Is(ResultCause(r), tribal.ErrMineThrottled) and implement
//     backoff. Mirrors the ErrRateLimited cause-attachment pattern in
//     TribalMineOnDemandRateLimitedHandler. The user-visible text is
//     authored inline (intentionally distinct from MiningError.SafeMessage
//     so the wordings can evolve independently — SafeMessage is shaped for
//     log/diagnostic contexts, this text is shaped for MCP clients with an
//     explicit retry hint). The typed cause is never serialized to the
//     wire (server-side discriminator only).
//  2. context.Canceled / context.DeadlineExceeded — request lifecycle, not
//     a mining-domain failure. Distinct generic message.
//  3. Any *tribal.MiningError — render via SafeMessage() which omits paths
//     and wrapped error chains.
//  4. Anything else — a single safe fallback so internal error text never
//     reaches the caller.
//
// Reachability: throttle currently propagates only when MineFile is invoked
// directly. TribalMiningService.MineSymbol's per-file loop discards all
// non-budget errors via `continue` (extractor/tribal/service.go MineSymbol),
// so throttle from the symbol-mining path is silently dropped today. The
// fix belongs in the service loop and is tracked separately; this branch is
// kept correct so the handler is ready when the suppression is lifted.
//
// renderMineError MUST NOT be invoked with a nil error; callers gate this on
// `mineErr != nil` upstream.
func renderMineError(err error) ToolResult {
	if errors.Is(err, tribal.ErrMineThrottled) {
		return NewErrorResultWithCause(
			"tribal_mine_on_demand: per-file mining rate limit reached; "+
				"retry after a short pause",
			tribal.ErrMineThrottled,
		)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return NewErrorResult("tribal_mine_on_demand: request canceled")
	}
	var me *tribal.MiningError
	if errors.As(err, &me) {
		// For budget exhaustion, the service may have produced partial
		// results before the budget tripped. The MCP contract allows
		// either an error result OR a success result with partial data;
		// we choose an error result for clarity.
		return NewErrorResultf("tribal_mine_on_demand: %s", me.SafeMessage())
	}
	return NewErrorResult("tribal_mine_on_demand: mining failed")
}

// buildTribalMineResponse shapes the MCP response from a slice of MiningResult
// pointers. It aggregates FailedCount across all results and surfaces it in
// the envelope so clients can detect partial-upsert failures that the
// service reports via (non-nil result, nil error) semantics per m7v.14.
//
// Returns (response, textMsg, err):
//   - response is the populated envelope when at least one fact was mined.
//   - textMsg is a non-empty warning string for the zero-facts paths:
//     "No new tribal facts mined" for genuinely empty results, or a
//     partial-failure warning when FailedCount > 0. Callers must prefer
//     textMsg over JSON marshaling when it is non-empty.
//   - err is non-nil only when a fact fails the provenance-envelope check,
//     which is treated as a server-side invariant violation.
func buildTribalMineResponse(symbol, repo string, results []*tribal.MiningResult) (tribalMineResponse, string, error) {
	resp := tribalMineResponse{
		Symbol: symbol,
		Repo:   repo,
		Facts:  make([]tribalFactEnvelope, 0),
	}
	for _, r := range results {
		if r == nil {
			continue
		}
		resp.FailedCount += r.FailedCount
		for _, fact := range r.Facts {
			if vErr := validateProvenanceEnvelope(fact); vErr != nil {
				return tribalMineResponse{}, "", vErr
			}
			resp.Facts = append(resp.Facts, factToEnvelope(fact))
		}
	}
	resp.Total = len(resp.Facts)

	if resp.Total == 0 {
		if resp.FailedCount > 0 {
			// Contract fix (m7v.20): do NOT return the misleading
			// "No new tribal facts mined" text when upsert failures
			// silently dropped work. Agents need to know.
			return resp, fmt.Sprintf(
				"Tribal mining for symbol %q in repo %q produced 0 facts but encountered %d "+
					"fact-upsert failure(s); details logged server-side. "+
					"Retry or investigate before treating this symbol as having no tribal knowledge.",
				symbol, repo, resp.FailedCount,
			), nil
		}
		return resp, fmt.Sprintf(
			"No new tribal facts mined for symbol %q in repo %q. "+
				"(Symbol not found, cursor already advanced, or no PR comments classified as tribal.)",
			symbol, repo,
		), nil
	}

	return resp, "", nil
}

// logMiningFailures emits one log line per MiningResult that recorded
// per-fact upsert failures. FailedErrors carries sanitized canonical
// category strings (m7v.21) rather than raw error objects, so no
// log-injection escaping is required for the category value itself;
// repo and path are still %q-quoted since they originate from caller
// input. The first retained category is surfaced for quick triage;
// FailedCount is the authoritative total. Raw error details are logged
// by the mining service at capture time — this log line intentionally
// does not attempt to reconstruct them.
func logMiningFailures(repo string, results []*tribal.MiningResult) {
	for _, r := range results {
		if r == nil || r.FailedCount == 0 {
			continue
		}
		firstCategory := ""
		if len(r.FailedErrors) > 0 {
			firstCategory = r.FailedErrors[0]
		}
		log.Printf(
			"tribal_mine_on_demand: partial upsert failure repo=%q path=%q failed_count=%d retained_errors=%d first_category=%q",
			repo, r.Path, r.FailedCount, len(r.FailedErrors), firstCategory,
		)
	}
}

// ---------------------------------------------------------------------------
// Per-session rate-limiting wrapper (live_docs-m7v.22)
// ---------------------------------------------------------------------------

// MineLogger is the minimal log interface the rate-limited handler uses
// to record per-session accounting entries. *log.Logger satisfies it
// directly. A nil MineLogger falls back to the package default
// log.Printf.
type MineLogger interface {
	Printf(format string, args ...any)
}

// TribalMineOnDemandRateLimitedHandler returns a ToolHandler that enforces
// per-session rate-limiting (live_docs-m7v.22) in front of the standard
// TribalMineOnDemandHandler. Semantics:
//
//   - The MCP client's session identifier is resolved via
//     adapter.go:SessionIDFromContext. Empty IDs (stdio transport, test
//     contexts) bucket under KeyedLimiter.anonymousID to preserve the
//     quota without rejecting requests.
//   - When the per-session token bucket has no available tokens, the
//     handler returns a safe error result ("rate limit exceeded for this
//     session") WITHOUT invoking the MiningServiceFactory, so zero LLM
//     budget is consumed by rejected requests.
//   - When a call is admitted, the handler logs
//     {session_id, repo, symbol, outcome} via the provided MineLogger so
//     budget deductions are attributable (live_docs-m7v.22 accountability).
//   - When limiter is nil, the handler delegates to the unrestricted
//     TribalMineOnDemandHandler for parity with legacy callers and tests.
//
// This wrapper is the mcpserver-side entry point for the KeyedLimiter
// primitive exposed by extractor/tribal. Callers in other packages (e.g.,
// the MineFile singleflight dedup boundary tracked by live_docs-m7v.17)
// may construct their own KeyedLimiter directly without depending on
// this handler.
//
// The opts variadic accepts MineHandlerOption values that further
// configure the handler. The only option today is WithSessionIDResolver,
// which tests use to inject a deterministic session ID in place of the
// default SessionIDFromContext. Options are captured per-handler at
// construction time, so distinct handlers never share mutable resolver
// state (live_docs-m7v.25 — replaced the prior package-level var).
func TribalMineOnDemandRateLimitedHandler(
	pool *DBPool,
	factory MiningServiceFactory,
	limiter *tribal.KeyedLimiter,
	logger MineLogger,
	opts ...MineHandlerOption,
) ToolHandler {
	cfg := mineHandlerOpts{sessionIDResolver: SessionIDFromContext}
	for _, opt := range opts {
		opt(&cfg)
	}
	resolveSessionID := cfg.sessionIDResolver

	inner := TribalMineOnDemandHandler(pool, factory)
	if limiter == nil {
		return inner
	}
	return func(ctx context.Context, req ToolRequest) (ToolResult, error) {
		sessionID := resolveSessionID(ctx)
		if !limiter.Allow(sessionID) {
			// Log the denial at the same format as admitted calls so the
			// session is attributable in both directions. %q quotes the
			// session ID to neutralize any log-injection attempt via a
			// spoofed ID containing newlines.
			logMineAttempt(logger, sessionID,
				safeArg(req, "repo"), safeArg(req, "symbol"),
				"rate_limited")
			// Attach ErrRateLimited as the typed cause so callers can
			// distinguish this denial from budget or transport errors via
			// errors.Is(ResultCause(r), ErrRateLimited) without
			// string-matching the user-visible text. The cause stays
			// server-side — the mcp-go transport only ever sees the
			// caller-friendly text.
			return NewErrorResultWithCause(
				"tribal_mine_on_demand: rate limit exceeded for this session; "+
					"retry after a short pause",
				ErrRateLimited,
			), nil
		}
		repo := safeArg(req, "repo")
		symbol := safeArg(req, "symbol")
		result, err := inner(ctx, req)
		outcome := mineOutcome(result, err)
		logMineAttempt(logger, sessionID, repo, symbol, outcome)
		return result, err
	}
}

// safeArg returns the string argument for key, or an empty string if the
// value is missing or not a string. It intentionally mirrors the
// permissive behaviour of ToolRequest.GetString so logging works even
// before parameter validation runs inside the inner handler.
func safeArg(req ToolRequest, key string) string {
	return req.GetString(key, "")
}

// mineOutcome classifies an (ToolResult, error) pair into a single short
// token suitable for log accounting. Possible values: "ok", "error",
// "transport_error".
func mineOutcome(result ToolResult, err error) string {
	if err != nil {
		return "transport_error"
	}
	if result == nil {
		return "transport_error"
	}
	if result.IsError() {
		return "error"
	}
	return "ok"
}

// maxLogFieldLen caps the logged size of untrusted string fields
// (session ID, repo, symbol) so a hostile caller sending a multi-megabyte
// value cannot bloat logs. %q-quoting escapes control characters; this
// truncation addresses the orthogonal log-volume DoS.
const maxLogFieldLen = 256

// truncateForLog clamps s to at most maxLogFieldLen bytes and appends a
// single ellipsis marker on truncation.
func truncateForLog(s string) string {
	if len(s) <= maxLogFieldLen {
		return s
	}
	return s[:maxLogFieldLen] + "..."
}

// logMineAttempt writes one structured line per attempt. Session ID and
// caller-supplied fields are %q-quoted AND length-bounded so a spoofed
// value cannot (a) forge log lines with injected newlines or (b) bloat
// log storage with a multi-megabyte value.
func logMineAttempt(logger MineLogger, sessionID, repo, symbol, outcome string) {
	const format = "tribal_mine_on_demand: session_id=%q repo=%q symbol=%q outcome=%q"
	sID := truncateForLog(sessionID)
	r := truncateForLog(repo)
	sym := truncateForLog(symbol)
	if logger != nil {
		logger.Printf(format, sID, r, sym, outcome)
		return
	}
	log.Printf(format, sID, r, sym, outcome)
}

// ---------------------------------------------------------------------------
// Tool definition
// ---------------------------------------------------------------------------

// TribalMineOnDemandToolDef returns the ToolDef for tribal_mine_on_demand.
// If factory is nil, the caller should not register this tool.
//
// When limiter is non-nil, the registered handler enforces a per-MCP-session
// token-bucket rate limit (live_docs-m7v.22) and writes one accounting line
// per attempt to logger (or the standard library logger when logger is nil).
// When limiter is nil, the handler delegates to TribalMineOnDemandHandler
// directly for parity with legacy wiring.
//
// Optional MineHandlerOption values pass through to
// TribalMineOnDemandRateLimitedHandler. See WithSessionIDResolver for the
// test-injection seam (live_docs-m7v.25).
func TribalMineOnDemandToolDef(
	pool *DBPool,
	factory MiningServiceFactory,
	limiter *tribal.KeyedLimiter,
	logger MineLogger,
	opts ...MineHandlerOption,
) ToolDef {
	return ToolDef{
		Name: "tribal_mine_on_demand",
		Description: `Trigger on-demand PR-comment mining for files containing the given symbol.

Runs the LLM-classified PR comment miner against every file whose symbols match
the given name. Newly-mined tribal facts are inserted with full provenance
envelopes (source_quote, evidence, extractor, model, last_verified) and returned.

Behavior:
- Idempotent: a second call on the same symbol short-circuits via the shared
  PR cursor (source_files.last_pr_id_set) and consumes zero LLM calls.
- Budget-bounded: subject to the deployment's DailyBudget; when exhausted the
  tool returns a structured error instead of making additional LLM calls.
- Co-equal with batch mining: both paths share the TribalMiningService so
  cursor state, budget accounting, and normalization stay aligned.`,
		Params: []ParamDef{
			{
				Name:        "symbol",
				Type:        ParamString,
				Required:    true,
				Description: "Symbol name to mine (e.g., 'NewServer'). Resolves to all source files in the repo that define a symbol with this name.",
			},
			{
				Name:        "repo",
				Type:        ParamString,
				Required:    true,
				Description: "Repository name (must match an existing .claims.db file in the data directory).",
			},
		},
		Handler: TribalMineOnDemandRateLimitedHandler(pool, factory, limiter, logger, opts...),
	}
}
