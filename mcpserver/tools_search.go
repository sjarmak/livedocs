// Package mcpserver tools_search.go implements the search_symbols tool for
// cross-repo symbol search with routing index fan-out. Uses ONLY adapter types
// — no mcp-go imports.
package mcpserver

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"
)

// maxSearchResults caps the number of symbol matches returned.
const maxSearchResults = 50

// searchConcurrencyLimit is the maximum number of concurrent repo searches.
const searchConcurrencyLimit = 10

// symbolMatch represents a single symbol search result.
type symbolMatch struct {
	Repo       string `json:"repo"`
	ImportPath string `json:"import_path"`
	SymbolName string `json:"symbol_name"`
	Kind       string `json:"kind"`
	Visibility string `json:"visibility"`
}

// searchSymbolsResponse is the JSON output for search_symbols.
type searchSymbolsResponse struct {
	Results    []symbolMatch `json:"results"`
	TotalCount int           `json:"total_count"`
}

// SearchSymbolsHandler returns a ToolHandler that searches for symbols across
// repos using the routing index to narrow candidates before fan-out.
func SearchSymbolsHandler(pool *DBPool, index *RoutingIndex) ToolHandler {
	return func(ctx context.Context, req ToolRequest) (ToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return NewErrorResult("missing required parameter 'query'"), nil
		}

		repoFilter := req.GetString("repo", "")

		// Single-repo mode: search only the specified repo.
		if repoFilter != "" {
			if result, err := requireRepoExists(pool, repoFilter); result != nil {
				return result, err
			}
			return searchSingleRepo(pool, repoFilter, query)
		}

		// Multi-repo mode: use routing index for candidate selection.
		candidates := index.Lookup(query)
		if len(candidates) == 0 {
			resp := searchSymbolsResponse{
				Results:    []symbolMatch{},
				TotalCount: 0,
			}
			data, _ := json.Marshal(resp)
			return NewTextResult(string(data)), nil
		}

		return searchMultiRepo(ctx, pool, candidates, query)
	}
}

// searchSingleRepo searches a single repo for symbols matching the query.
func searchSingleRepo(pool *DBPool, repoName, query string) (ToolResult, error) {
	cdb, err := pool.Open(repoName)
	if err != nil {
		return NewErrorResultf("open repo %s: %v", repoName, err), nil
	}

	symbols, err := cdb.SearchSymbolsByName(query)
	if err != nil {
		return NewErrorResultf("search symbols in %s: %v", repoName, err), nil
	}

	matches := make([]symbolMatch, 0, len(symbols))
	for _, s := range symbols {
		matches = append(matches, symbolMatch{
			Repo:       s.Repo,
			ImportPath: s.ImportPath,
			SymbolName: s.SymbolName,
			Kind:       s.Kind,
			Visibility: s.Visibility,
		})
	}

	totalCount := len(matches)
	if len(matches) > maxSearchResults {
		matches = matches[:maxSearchResults]
	}

	resp := searchSymbolsResponse{
		Results:    matches,
		TotalCount: totalCount,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return NewErrorResultf("marshal result: %v", err), nil
	}
	return NewTextResult(string(data)), nil
}

// searchMultiRepo fans out to multiple repos using errgroup with concurrency limit.
func searchMultiRepo(ctx context.Context, pool *DBPool, repos []string, query string) (ToolResult, error) {
	var mu sync.Mutex
	var allMatches []symbolMatch

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(searchConcurrencyLimit)

	for _, repoName := range repos {
		g.Go(func() error {
			cdb, err := pool.Open(repoName)
			if err != nil {
				// Skip repos that fail to open.
				return nil
			}

			symbols, err := cdb.SearchSymbolsByName(query)
			if err != nil {
				// Skip repos with search errors.
				return nil
			}

			if len(symbols) == 0 {
				return nil
			}

			matches := make([]symbolMatch, 0, len(symbols))
			for _, s := range symbols {
				matches = append(matches, symbolMatch{
					Repo:       s.Repo,
					ImportPath: s.ImportPath,
					SymbolName: s.SymbolName,
					Kind:       s.Kind,
					Visibility: s.Visibility,
				})
			}

			mu.Lock()
			allMatches = append(allMatches, matches...)
			mu.Unlock()

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return NewErrorResultf("search symbols: %v", err), nil
	}

	// Sort by repo then symbol name for deterministic output.
	sort.Slice(allMatches, func(i, j int) bool {
		if allMatches[i].Repo != allMatches[j].Repo {
			return allMatches[i].Repo < allMatches[j].Repo
		}
		return allMatches[i].SymbolName < allMatches[j].SymbolName
	})

	totalCount := len(allMatches)
	if len(allMatches) > maxSearchResults {
		allMatches = allMatches[:maxSearchResults]
	}

	resp := searchSymbolsResponse{
		Results:    allMatches,
		TotalCount: totalCount,
	}
	if resp.Results == nil {
		resp.Results = []symbolMatch{}
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return NewErrorResultf("marshal result: %v", err), nil
	}
	return NewTextResult(string(data)), nil
}

// SearchSymbolsToolDef returns the ToolDef for search_symbols.
func SearchSymbolsToolDef(pool *DBPool, index *RoutingIndex) ToolDef {
	return ToolDef{
		Name: "search_symbols",
		Description: `Search for symbols across all repositories by name pattern.

Uses an in-memory routing index to narrow candidate repos before searching,
avoiding full fan-out to all databases. Supports SQL LIKE wildcards: use '%'
for multi-character wildcards and '_' for single-character wildcards.

Returns up to 50 results with total_count metadata. Each result includes
repo, import_path, symbol_name, kind, and visibility.

Examples:
  search_symbols(query="NewServer") — exact match across all repos
  search_symbols(query="New%") — all symbols starting with "New"
  search_symbols(query="%Handler%", repo="kubernetes") — handlers in one repo`,
		Params: []ParamDef{
			{Name: "query", Type: ParamString, Required: true, Description: "Symbol name pattern. Supports SQL LIKE wildcards: '%' for any characters, '_' for single character. Example: 'NewServer', 'New%', '%Handler%'."},
			{Name: "repo", Type: ParamString, Required: false, Description: "Optional repository name to restrict search to a single repo. If omitted, searches across all repos using the routing index."},
		},
		Handler: SearchSymbolsHandler(pool, index),
	}
}
