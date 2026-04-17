// Package mcpserver tribal_tools.go defines three tribal knowledge MCP tools:
// tribal_context_for_symbol, tribal_owners, and tribal_why_this_way.
// These handlers use ONLY adapter types — no mcp-go imports.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/live-docs/live_docs/db"
)

// corroborationDegradedThreshold is the corroboration level below which
// LLM-extracted facts are served with degraded=true. Intentionally higher
// than the gate threshold (2) in appendFilteredFact: the gate excludes
// completely uncorroborated facts, while degraded flags "served but uncertain."
const corroborationDegradedThreshold = 3

// ---------------------------------------------------------------------------
// Provenance envelope types
// ---------------------------------------------------------------------------

// tribalEvidenceEnvelope is the JSON representation of a single evidence row.
type tribalEvidenceEnvelope struct {
	SourceType string `json:"source_type"`
	SourceRef  string `json:"source_ref"`
	Author     string `json:"author,omitempty"`
	AuthoredAt string `json:"authored_at,omitempty"`
}

// tribalFactEnvelope is the non-negotiable provenance envelope.
// Every field is required; facts missing any field are rejected.
type tribalFactEnvelope struct {
	Body          string                   `json:"body"`
	SourceQuote   string                   `json:"source_quote"`
	Kind          string                   `json:"kind"`
	Confidence    float64                  `json:"confidence"`
	Corroboration int                      `json:"corroboration"`
	Status        string                   `json:"status"`
	Evidence      []tribalEvidenceEnvelope `json:"evidence"`
	Extractor     string                   `json:"extractor"`
	Model         string                   `json:"model"`
	LastVerified  string                   `json:"last_verified"`
	// Degraded is true when the fact is an LLM-extracted fact with
	// corroboration < 3. Consumers should treat degraded facts with
	// reduced trust until S4 validation is available.
	Degraded bool `json:"degraded,omitempty"`
}

// tribalResponse is the JSON response for tribal tools.
type tribalResponse struct {
	Symbol string               `json:"symbol"`
	Facts  []tribalFactEnvelope `json:"facts"`
	Total  int                  `json:"total"`
}

// ---------------------------------------------------------------------------
// Provenance validation
// ---------------------------------------------------------------------------

// validateProvenanceEnvelope checks that a TribalFact has all required
// provenance fields. Returns an error describing the first missing field.
func validateProvenanceEnvelope(fact db.TribalFact) error {
	if fact.SourceQuote == "" {
		return fmt.Errorf("fact %d missing required field: source_quote", fact.ID)
	}
	if len(fact.Evidence) == 0 {
		return fmt.Errorf("fact %d missing required field: evidence (no evidence rows)", fact.ID)
	}
	if fact.Status == "" {
		return fmt.Errorf("fact %d missing required field: status", fact.ID)
	}
	if fact.Extractor == "" {
		return fmt.Errorf("fact %d missing required field: extractor", fact.ID)
	}
	if fact.LastVerified == "" {
		return fmt.Errorf("fact %d missing required field: last_verified", fact.ID)
	}
	return nil
}

// factToEnvelope converts a validated TribalFact into a tribalFactEnvelope.
// LLM-extracted facts (model != "") with corroboration < 3 are marked as
// degraded per the S4 gate failsafe: until S4 ships, these facts should
// only be served at explicit opt-in or with the degraded flag set.
func factToEnvelope(fact db.TribalFact) tribalFactEnvelope {
	evidence := make([]tribalEvidenceEnvelope, 0, len(fact.Evidence))
	for _, ev := range fact.Evidence {
		evidence = append(evidence, tribalEvidenceEnvelope{
			SourceType: ev.SourceType,
			SourceRef:  ev.SourceRef,
			Author:     ev.Author,
			AuthoredAt: ev.AuthoredAt,
		})
	}
	extractor := fact.Extractor
	if fact.ExtractorVersion != "" {
		extractor = fact.Extractor + "@" + fact.ExtractorVersion
	}
	// S4 failsafe: LLM facts below the degraded threshold are flagged.
	// corroborationDegradedThreshold (3) > corroboration gate (2) intentionally:
	// gate excludes uncorroborated facts entirely; degraded marks "uncertain but
	// served" facts so consumers can distinguish quality tiers.
	degraded := fact.Model != "" && fact.Corroboration < corroborationDegradedThreshold
	return tribalFactEnvelope{
		Body:          fact.Body,
		SourceQuote:   fact.SourceQuote,
		Kind:          fact.Kind,
		Confidence:    fact.Confidence,
		Corroboration: fact.Corroboration,
		Status:        fact.Status,
		Evidence:      evidence,
		Extractor:     extractor,
		Model:         fact.Model,
		LastVerified:  fact.LastVerified,
		Degraded:      degraded,
	}
}

// ---------------------------------------------------------------------------
// Shared query logic
// ---------------------------------------------------------------------------

// parseKinds splits a comma-separated kinds string into a slice.
// Returns nil if the input is empty.
func parseKinds(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// queryTribalFactsForSymbol searches for symbols by name across the pool,
// retrieves tribal facts, and applies kind/confidence/corroboration filters.
// It validates the provenance envelope for every fact and returns an error if
// any fact fails validation.
//
// When corroborationGateActive is true, LLM-extracted facts (model != "") with
// corroboration < 2 are excluded. This prevents uncorroborated LLM facts from
// being served unless the caller explicitly requests them via min_confidence.
func queryTribalFactsForSymbol(
	pool *DBPool,
	symbolName string,
	kinds []string,
	minConfidence float64,
	corroborationGateActive bool,
) (tribalResponse, error) {
	resp := tribalResponse{
		Symbol: symbolName,
		Facts:  make([]tribalFactEnvelope, 0),
	}

	// Build a set for O(1) kind lookups.
	kindSet := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		kindSet[k] = true
	}

	manifest, err := pool.Manifest()
	if err != nil {
		return resp, fmt.Errorf("list repos: %w", err)
	}

	for _, repoName := range manifest {
		cdb, err := pool.Open(repoName)
		if err != nil {
			return resp, fmt.Errorf("open repo %s: %w", repoName, err)
		}

		symbols, err := cdb.SearchSymbolsByName(symbolName)
		if err != nil {
			if db.IsMissingTableErr(err) {
				continue
			}
			return resp, fmt.Errorf("search symbols in repo %s: %w", repoName, err)
		}

		// Pass 1: direct symbol-level tribal facts.
		// Track whether Pass 1 actually *found* any facts (before filtering)
		// so the fallback only runs when the symbol has truly no tribal
		// knowledge attached — not when its facts exist but were filtered
		// out by kind/confidence/status. This prevents returning unrelated
		// file-level facts when direct facts exist but are stale/quarantined.
		pass1QueriedOK := false
		pass1FoundFacts := false
		for _, sym := range symbols {
			facts, err := cdb.GetTribalFactsBySubject(sym.ID)
			if err != nil {
				if db.IsMissingTableErr(err) {
					continue
				}
				return resp, fmt.Errorf("get tribal facts for symbol %d in repo %s: %w", sym.ID, repoName, err)
			}
			pass1QueriedOK = true
			if len(facts) > 0 {
				pass1FoundFacts = true
			}
			for _, fact := range facts {
				if err := appendFilteredFact(&resp, fact, kindSet, minConfidence, corroborationGateActive); err != nil {
					return resp, err
				}
			}
		}

		// Pass 2: file-level fallback. Runs when Pass 1 successfully queried
		// at least one symbol and found zero raw facts, meaning the symbol
		// exists but tribal facts are stored at file-level granularity.
		// Resolves the symbol's import_path to a local directory prefix and
		// fetches file-level tribal facts in a single indexed query.
		if pass1QueriedOK && !pass1FoundFacts {
			importPaths, err := cdb.GetImportPathsForSymbolName(symbolName)
			if err != nil {
				if db.IsMissingTableErr(err) {
					continue
				}
				return resp, fmt.Errorf("get import paths in repo %s: %w", repoName, err)
			}
			for _, ip := range importPaths {
				dirPrefix := importPathToLocalDir(ip)
				if dirPrefix == "" {
					continue
				}
				facts, err := cdb.GetTribalFactsByPathPrefix(repoName, escapeLike(dirPrefix))
				if err != nil {
					if db.IsMissingTableErr(err) {
						continue
					}
					return resp, fmt.Errorf("get facts by path prefix in repo %s: %w", repoName, err)
				}
				for _, fact := range facts {
					if err := appendFilteredFact(&resp, fact, kindSet, minConfidence, corroborationGateActive); err != nil {
						return resp, err
					}
				}
			}
		}
	}

	resp.Total = len(resp.Facts)
	return resp, nil
}

// importPathToLocalDir extracts a local directory prefix from a Go import path
// by stripping known module prefixes. Returns a path with trailing "/".
// Examples:
//
//	"github.com/live-docs/live_docs/db" → "db/"
//	"github.com/live-docs/live_docs/extractor/tribal" → "extractor/tribal/"
//	"db" → "db/"
func importPathToLocalDir(importPath string) string {
	// Common Go module prefixes: 3-segment (github.com/org/repo).
	parts := strings.Split(importPath, "/")
	if len(parts) >= 3 && (parts[0] == "github.com" || parts[0] == "gitlab.com" || parts[0] == "bitbucket.org") {
		// Strip "github.com/org/repo" prefix.
		local := strings.Join(parts[3:], "/")
		if local == "" {
			// Root package — tribal subjects would be at repo root.
			return ""
		}
		return local + "/"
	}
	// For non-standard import paths, use the import_path directly as prefix.
	if importPath != "" {
		return importPath + "/"
	}
	return ""
}

// escapeLike escapes SQL LIKE wildcard characters so they match literally.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}


// appendFilteredFact applies kind, confidence, status, and corroboration-gate
// filters to a tribal fact, validates its provenance envelope, and appends it
// to the response.
//
// When corroborationGateActive is true, LLM-extracted facts (model != "") with
// corroboration < 2 are excluded. Deterministic facts (model="") are never
// affected by the gate. Callers that explicitly pass min_confidence disable the
// gate so all facts meeting that threshold are returned.
func appendFilteredFact(resp *tribalResponse, fact db.TribalFact, kindSet map[string]bool, minConfidence float64, corroborationGateActive bool) error {
	if len(kindSet) > 0 && !kindSet[fact.Kind] {
		return nil
	}
	if fact.Confidence < minConfidence {
		return nil
	}
	if fact.Status != "active" {
		return nil
	}
	// Corroboration gate: exclude uncorroborated LLM facts at default confidence.
	if corroborationGateActive && fact.Model != "" && fact.Corroboration < 2 {
		return nil
	}
	if err := validateProvenanceEnvelope(fact); err != nil {
		return err
	}
	resp.Facts = append(resp.Facts, factToEnvelope(fact))
	return nil
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

// TribalContextForSymbolHandler returns a ToolHandler that queries all active
// tribal facts for a symbol with full provenance envelopes.
func TribalContextForSymbolHandler(pool *DBPool) ToolHandler {
	return func(_ context.Context, req ToolRequest) (ToolResult, error) {
		symbol, err := req.RequireString("symbol")
		if err != nil {
			return NewErrorResult("missing required parameter 'symbol'"), nil
		}
		kindsRaw := req.GetString("kinds", "")
		minConfidence := req.GetFloat("min_confidence", 0.0)

		// Corroboration gate: active when the caller did NOT explicitly set
		// min_confidence. If the caller explicitly passes min_confidence (even 0),
		// all facts meeting that threshold are returned including uncorroborated
		// LLM facts.
		_, minConfExplicit := req.GetArguments()["min_confidence"]
		corroborationGateActive := !minConfExplicit

		kinds := parseKinds(kindsRaw)
		resp, err := queryTribalFactsForSymbol(pool, symbol, kinds, minConfidence, corroborationGateActive)
		if err != nil {
			return NewErrorResultf("tribal_context_for_symbol: %v", err), nil
		}

		if resp.Total == 0 {
			return NewTextResult(fmt.Sprintf("No tribal knowledge facts found for symbol %q.", symbol)), nil
		}

		data, err := json.Marshal(resp)
		if err != nil {
			return NewErrorResultf("marshal result: %v", err), nil
		}
		return NewTextResult(string(data)), nil
	}
}

// TribalOwnersHandler returns a ToolHandler that queries ownership-kind
// tribal facts for a symbol.
func TribalOwnersHandler(pool *DBPool) ToolHandler {
	return func(_ context.Context, req ToolRequest) (ToolResult, error) {
		symbol, err := req.RequireString("symbol")
		if err != nil {
			return NewErrorResult("missing required parameter 'symbol'"), nil
		}

		// Corroboration gate always active for tribal_owners (no min_confidence param).
		resp, err := queryTribalFactsForSymbol(pool, symbol, []string{"ownership"}, 0.0, true)
		if err != nil {
			return NewErrorResultf("tribal_owners: %v", err), nil
		}

		if resp.Total == 0 {
			return NewTextResult(fmt.Sprintf("No ownership facts found for symbol %q.", symbol)), nil
		}

		data, err := json.Marshal(resp)
		if err != nil {
			return NewErrorResultf("marshal result: %v", err), nil
		}
		return NewTextResult(string(data)), nil
	}
}

// TribalWhyThisWayHandler returns a ToolHandler that queries rationale and
// invariant tribal facts for a symbol.
func TribalWhyThisWayHandler(pool *DBPool) ToolHandler {
	return func(_ context.Context, req ToolRequest) (ToolResult, error) {
		symbol, err := req.RequireString("symbol")
		if err != nil {
			return NewErrorResult("missing required parameter 'symbol'"), nil
		}

		// Corroboration gate always active for tribal_why_this_way (no min_confidence param).
		resp, err := queryTribalFactsForSymbol(pool, symbol, []string{"rationale", "invariant"}, 0.0, true)
		if err != nil {
			return NewErrorResultf("tribal_why_this_way: %v", err), nil
		}

		if resp.Total == 0 {
			return NewTextResult(fmt.Sprintf("No rationale or invariant facts found for symbol %q.", symbol)), nil
		}

		data, err := json.Marshal(resp)
		if err != nil {
			return NewErrorResultf("marshal result: %v", err), nil
		}
		return NewTextResult(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

// TribalContextForSymbolToolDef returns the ToolDef for tribal_context_for_symbol.
func TribalContextForSymbolToolDef(pool *DBPool) ToolDef {
	return ToolDef{
		Name: "tribal_context_for_symbol",
		Description: `Query all active tribal knowledge facts for a symbol with full provenance.

Returns facts with complete provenance envelopes including body, source_quote, kind,
confidence, corroboration count, status, evidence array, extractor, model, and last_verified.

Supports filtering by fact kinds (comma-separated) and minimum confidence threshold.
Only active facts are returned. Every fact is validated for completeness before emission.`,
		Params: []ParamDef{
			{Name: "symbol", Type: ParamString, Required: true, Description: "Symbol name to search for. Supports SQL LIKE wildcards (e.g., 'NewServer', '%Handler%')."},
			{Name: "kinds", Type: ParamString, Required: false, Description: "Comma-separated list of fact kinds to include. Valid kinds: ownership, rationale, invariant, quirk, todo, deprecation. Omit to return all kinds."},
			{Name: "min_confidence", Type: ParamNumber, Required: false, Description: "Minimum confidence threshold (0.0-1.0). Facts below this threshold are excluded. Default: 0.0 (return all)."},
		},
		Handler: TribalContextForSymbolHandler(pool),
	}
}

// TribalOwnersToolDef returns the ToolDef for tribal_owners.
func TribalOwnersToolDef(pool *DBPool) ToolDef {
	return ToolDef{
		Name: "tribal_owners",
		Description: `Fast-path ownership query for a symbol.

Returns only ownership-kind tribal facts with full provenance envelopes.
Shortcut for tribal_context_for_symbol with kinds=ownership.`,
		Params: []ParamDef{
			{Name: "symbol", Type: ParamString, Required: true, Description: "Symbol name to search for. Supports SQL LIKE wildcards."},
		},
		Handler: TribalOwnersHandler(pool),
	}
}

// TribalWhyThisWayToolDef returns the ToolDef for tribal_why_this_way.
func TribalWhyThisWayToolDef(pool *DBPool) ToolDef {
	return ToolDef{
		Name: "tribal_why_this_way",
		Description: `Query rationale and invariant facts for a symbol.

Returns only rationale and invariant tribal facts with full provenance envelopes
and confidence/corroboration labels. Use this to understand why code is structured
a certain way or what constraints must be maintained.`,
		Params: []ParamDef{
			{Name: "symbol", Type: ParamString, Required: true, Description: "Symbol name to search for. Supports SQL LIKE wildcards."},
		},
		Handler: TribalWhyThisWayHandler(pool),
	}
}
