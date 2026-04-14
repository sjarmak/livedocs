package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
)

// tribalSearchResponse is the JSON response for the tribal_search tool.
type tribalSearchResponse struct {
	Query string               `json:"query"`
	Kind  string               `json:"kind,omitempty"`
	Facts []tribalFactEnvelope `json:"facts"`
	Total int                  `json:"total"`
}

// TribalSearchHandler returns a ToolHandler that performs BM25-ranked
// full-text search over tribal facts across all repos in the pool.
func TribalSearchHandler(pool *DBPool) ToolHandler {
	return func(_ context.Context, req ToolRequest) (ToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return NewErrorResult("missing required parameter 'query'"), nil
		}

		kind := req.GetString("kind", "")
		limit := req.GetInt("limit", 10)
		if limit <= 0 {
			limit = 10
		}

		manifest, err := pool.Manifest()
		if err != nil {
			return NewErrorResultf("tribal_search: list repos: %v", err), nil
		}

		resp := tribalSearchResponse{
			Query: query,
			Kind:  kind,
			Facts: make([]tribalFactEnvelope, 0),
		}

		for _, repoName := range manifest {
			cdb, err := pool.Open(repoName)
			if err != nil {
				return NewErrorResultf("tribal_search: open repo %s: %v", repoName, err), nil
			}

			facts, err := cdb.SearchTribalFactsBM25(repoName, query, kind, limit-resp.Total)
			if err != nil {
				if isMissingTableErr(err) {
					continue
				}
				return NewErrorResultf("tribal_search: search repo %s: %v", repoName, err), nil
			}

			for _, fact := range facts {
				if err := validateProvenanceEnvelope(fact); err != nil {
					return NewErrorResultf("tribal_search: %v", err), nil
				}
				resp.Facts = append(resp.Facts, factToEnvelope(fact))
				if len(resp.Facts) >= limit {
					break
				}
			}

			if len(resp.Facts) >= limit {
				break
			}
		}

		resp.Total = len(resp.Facts)

		if resp.Total == 0 {
			return NewTextResult(fmt.Sprintf("No tribal knowledge facts found matching query %q.", query)), nil
		}

		data, err := json.Marshal(resp)
		if err != nil {
			return NewErrorResultf("tribal_search: marshal result: %v", err), nil
		}
		return NewTextResult(string(data)), nil
	}
}

// TribalSearchToolDef returns the ToolDef for the tribal_search tool.
func TribalSearchToolDef(pool *DBPool) ToolDef {
	return ToolDef{
		Name: "tribal_search",
		Description: `Full-text search over tribal knowledge facts using BM25 ranking.

Searches the body and source_quote fields of all active tribal facts across repos.
Results are ranked by BM25 relevance. Use this to find tribal knowledge by topic
or keyword when you don't know which symbol it's attached to.

Only active facts are returned. Every fact is validated for completeness before emission.`,
		Params: []ParamDef{
			{Name: "query", Type: ParamString, Required: true, Description: "Full-text search query. Supports FTS5 query syntax: simple words, phrases (\"exact phrase\"), prefix queries (word*), boolean operators (AND, OR, NOT)."},
			{Name: "kind", Type: ParamString, Required: false, Description: "Filter by fact kind. Valid kinds: ownership, rationale, invariant, quirk, todo, deprecation. Omit to search all kinds."},
			{Name: "limit", Type: ParamNumber, Required: false, Description: "Maximum number of results to return. Default: 10."},
		},
		Handler: TribalSearchHandler(pool),
	}
}
