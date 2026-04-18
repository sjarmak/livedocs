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
			// The factory may include provider-specific error details; keep
			// the caller-facing message generic.
			return NewErrorResult("tribal_mine_on_demand: mining service unavailable"), nil
		}
		if svc == nil {
			return NewErrorResult("tribal_mine_on_demand: mining service unavailable"), nil
		}

		// --- Delegate to service ---
		results, mineErr := svc.MineSymbol(ctx, symbol, tribal.TriggerJITOnDemand)

		// Translate structured MiningError into safe, caller-facing results.
		if mineErr != nil {
			var me *tribal.MiningError
			if errors.As(mineErr, &me) {
				// For budget exhaustion, the service may have produced partial
				// results before the budget tripped. Surface whatever is in
				// `results` alongside a safe error message. The MCP contract
				// allows either an error result OR a success result with
				// partial data; we choose an error result for clarity.
				return NewErrorResultf(
					"tribal_mine_on_demand: %s", me.SafeMessage(),
				), nil
			}
			// Context cancellation or other unstructured errors. Return a
			// generic message so internal paths never reach the caller.
			if errors.Is(mineErr, context.Canceled) || errors.Is(mineErr, context.DeadlineExceeded) {
				return NewErrorResult("tribal_mine_on_demand: request canceled"), nil
			}
			return NewErrorResult("tribal_mine_on_demand: mining failed"), nil
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
// Tool definition
// ---------------------------------------------------------------------------

// TribalMineOnDemandToolDef returns the ToolDef for tribal_mine_on_demand.
// If factory is nil, the caller should not register this tool.
func TribalMineOnDemandToolDef(pool *DBPool, factory MiningServiceFactory) ToolDef {
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
		Handler: TribalMineOnDemandHandler(pool, factory),
	}
}
