// Package mcpserver tools.go defines the three multi-repo tool handlers:
// list_repos, list_packages, and describe_package. These handlers use ONLY
// adapter types (ToolRequest, ToolResult, ToolHandler) — no mcp-go imports.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/renderer"
)

// stalenessThreshold is the duration after which extracted data is considered stale.
const stalenessThreshold = 7 * 24 * time.Hour

// ---------------------------------------------------------------------------
// list_repos
// ---------------------------------------------------------------------------

// repoInfo is the JSON structure for each repo in list_repos output.
type repoInfo struct {
	Repo            string `json:"repo"`
	Symbols         int    `json:"symbols"`
	Claims          int    `json:"claims"`
	ExtractedAt     string `json:"extracted_at,omitempty"`
	ExtractedCommit string `json:"extracted_commit,omitempty"`
}

// listReposResponse wraps the list_repos JSON output.
type listReposResponse struct {
	Repos        []repoInfo `json:"repos"`
	StaleWarning string     `json:"stale_warning,omitempty"`
}

// ListReposHandler returns a ToolHandler that lists all repos in the pool.
func ListReposHandler(pool *DBPool) ToolHandler {
	return func(_ context.Context, _ ToolRequest) (ToolResult, error) {
		manifest, err := pool.Manifest()
		if err != nil {
			return NewErrorResultf("list repos: %v", err), nil
		}

		resp := listReposResponse{
			Repos: make([]repoInfo, 0, len(manifest)),
		}
		var anyStale bool

		for _, repoName := range manifest {
			cdb, err := pool.Open(repoName)
			if err != nil {
				return NewErrorResultf("open repo %s: %v", repoName, err), nil
			}

			symbols, err := cdb.CountSymbols()
			if err != nil {
				return NewErrorResultf("count symbols for %s: %v", repoName, err), nil
			}
			claims, err := cdb.CountClaims()
			if err != nil {
				return NewErrorResultf("count claims for %s: %v", repoName, err), nil
			}

			ts, _ := cdb.GetLatestLastIndexed()
			meta, _ := cdb.GetExtractionMeta()

			// Skip empty repos (no extracted symbols).
			if symbols == 0 {
				continue
			}

			// Prefer extraction_meta timestamp over source_files fallback.
			extractedAt := ts
			if meta.ExtractedAt != "" {
				extractedAt = meta.ExtractedAt
			}

			info := repoInfo{
				Repo:            repoName,
				Symbols:         symbols,
				Claims:          claims,
				ExtractedAt:     extractedAt,
				ExtractedCommit: meta.CommitSHA,
			}
			resp.Repos = append(resp.Repos, info)

			if ts != "" && isStale(ts) {
				anyStale = true
			}
		}

		// Sort by symbol count descending — most relevant repos first.
		sort.Slice(resp.Repos, func(i, j int) bool {
			return resp.Repos[i].Symbols > resp.Repos[j].Symbols
		})

		if anyStale {
			resp.StaleWarning = "One or more repos have data older than 7 days. Re-run extraction for fresh results."
		}

		data, err := json.Marshal(resp)
		if err != nil {
			return NewErrorResultf("marshal result: %v", err), nil
		}
		return NewTextResult(string(data)), nil
	}
}

// ListReposToolDef returns the ToolDef for list_repos.
func ListReposToolDef(pool *DBPool) ToolDef {
	return ToolDef{
		Name: "list_repos",
		Description: `List all repositories available in the data directory.

Returns a JSON array with repo name, symbol count, and claim count for each repository.
Includes extracted_at timestamp and warns if data is older than 7 days.`,
		Handler: ListReposHandler(pool),
	}
}

// ---------------------------------------------------------------------------
// list_packages
// ---------------------------------------------------------------------------

// listPackagesResponse is the JSON output for list_packages.
type listPackagesResponse struct {
	ImportPaths     []string `json:"import_paths"`
	TotalCount      int      `json:"total_count"`
	ExtractedAt     string   `json:"extracted_at,omitempty"`
	ExtractedCommit string   `json:"extracted_commit,omitempty"`
	StaleWarning    string   `json:"stale_warning,omitempty"`
}

// ListPackagesHandler returns a ToolHandler that lists packages for a repo.
func ListPackagesHandler(pool *DBPool) ToolHandler {
	return func(_ context.Context, req ToolRequest) (ToolResult, error) {
		repoName, err := req.RequireString("repo")
		if err != nil {
			return NewErrorResult("missing required parameter 'repo'"), nil
		}
		prefix := req.GetString("prefix", "")

		cdb, err := pool.Open(repoName)
		if err != nil {
			return NewErrorResultf("open repo %s: %v", repoName, err), nil
		}

		paths, totalCount, err := cdb.ListDistinctImportPathsWithPrefix(prefix, 200)
		if err != nil {
			return NewErrorResultf("list packages for %s: %v", repoName, err), nil
		}

		ts, _ := cdb.GetLatestLastIndexed()
		meta, _ := cdb.GetExtractionMeta()

		extractedAt := ts
		if meta.ExtractedAt != "" {
			extractedAt = meta.ExtractedAt
		}

		resp := listPackagesResponse{
			ImportPaths:     paths,
			TotalCount:      totalCount,
			ExtractedAt:     extractedAt,
			ExtractedCommit: meta.CommitSHA,
		}
		if resp.ImportPaths == nil {
			resp.ImportPaths = []string{}
		}

		if ts != "" && isStale(ts) {
			resp.StaleWarning = "Data is older than 7 days. Re-run extraction for fresh results."
		}

		data, err := json.Marshal(resp)
		if err != nil {
			return NewErrorResultf("marshal result: %v", err), nil
		}
		return NewTextResult(string(data)), nil
	}
}

// ListPackagesToolDef returns the ToolDef for list_packages.
func ListPackagesToolDef(pool *DBPool) ToolDef {
	return ToolDef{
		Name: "list_packages",
		Description: `List import paths (packages) for a given repository.

Returns up to 200 import paths matching an optional prefix, with a total count.
Includes extracted_at timestamp and warns if data is older than 7 days.

Example: list_packages(repo="kubernetes", prefix="k8s.io/api/") returns all packages under that prefix.`,
		Params: []ParamDef{
			{Name: "repo", Type: ParamString, Required: true, Description: "Repository name (matches the .claims.db filename without extension)."},
			{Name: "prefix", Type: ParamString, Required: false, Description: "Optional import path prefix to filter packages. Example: 'k8s.io/api/'."},
		},
		Handler: ListPackagesHandler(pool),
	}
}

// ---------------------------------------------------------------------------
// describe_package
// ---------------------------------------------------------------------------

// DescribePackageHandler returns a ToolHandler that renders package documentation.
// If sc is non-nil, it performs a lazy staleness check and re-extracts changed
// files before rendering. This is best-effort — failures fall back to stale data.
func DescribePackageHandler(pool *DBPool, sc *StalenessChecker) ToolHandler {
	return func(ctx context.Context, req ToolRequest) (ToolResult, error) {
		repoName, err := req.RequireString("repo")
		if err != nil {
			return NewErrorResult("missing required parameter 'repo'"), nil
		}
		importPath, err := req.RequireString("import_path")
		if err != nil {
			return NewErrorResult("missing required parameter 'import_path'"), nil
		}

		cdb, err := pool.Open(repoName)
		if err != nil {
			return NewErrorResultf("open repo %s: %v", repoName, err), nil
		}

		// Lazy staleness check: detect and optionally re-extract changed files.
		var staleWarningMD string
		if sc != nil {
			staleFiles := sc.CheckPackageStaleness(ctx, cdb, repoName, importPath)
			if len(staleFiles) > 0 {
				refreshed, errs := sc.RefreshStaleFiles(ctx, cdb, staleFiles)
				staleWarningMD = stalenessWarning(staleFiles, refreshed, errs)
			}
		}

		pd, err := renderer.LoadPackageData(cdb, importPath)
		if err != nil {
			return NewErrorResultf("load package data for %s: %v", importPath, err), nil
		}

		md := renderer.RenderMarkdown(pd)

		// Append semantic sections: Purpose and Usage Patterns.
		semanticMD := buildSemanticSections(cdb, importPath)
		if semanticMD != "" {
			md += "\n" + semanticMD
		}

		// Staleness check and freshness metadata.
		ts, _ := cdb.GetLatestLastIndexed()
		meta, _ := cdb.GetExtractionMeta()

		extractedAt := ts
		if meta.ExtractedAt != "" {
			extractedAt = meta.ExtractedAt
		}

		commitInfo := ""
		if meta.CommitSHA != "" {
			commitInfo = fmt.Sprintf(" (commit: %s)", meta.CommitSHA)
		}

		if extractedAt != "" && isStale(extractedAt) {
			md = fmt.Sprintf("> **Warning:** Data extracted at %s%s is older than 7 days. Re-run extraction for fresh results.\n\n%s", extractedAt, commitInfo, md)
		} else if extractedAt != "" {
			md = fmt.Sprintf("> Data extracted at %s%s\n\n%s", extractedAt, commitInfo, md)
		}

		// Prepend lazy-staleness warning if applicable.
		if staleWarningMD != "" {
			md = staleWarningMD + md
		}

		return NewTextResult(md), nil
	}
}

// DescribePackageToolDef returns the ToolDef for describe_package.
// If sc is non-nil, lazy staleness checking is enabled for this tool.
func DescribePackageToolDef(pool *DBPool, sc *StalenessChecker) ToolDef {
	return ToolDef{
		Name: "describe_package",
		Description: `Render Markdown documentation for a package in a repository.

Produces structural documentation including interfaces, dependencies, reverse dependencies (Used By),
function categories, and test coverage. Also includes Purpose and Usage Patterns sections when
semantic claims exist.

Includes extracted_at timestamp and warns if data is older than 7 days.
When repo roots are configured, performs lazy staleness detection and re-extracts changed files on-the-fly.`,
		Params: []ParamDef{
			{Name: "repo", Type: ParamString, Required: true, Description: "Repository name (matches the .claims.db filename without extension)."},
			{Name: "import_path", Type: ParamString, Required: true, Description: "Full import path of the package to describe. Example: 'k8s.io/api/core/v1'."},
		},
		Handler: DescribePackageHandler(pool, sc),
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildSemanticSections queries for "purpose" and "usage_pattern" claims
// for symbols in the given import path and formats them as Markdown sections.
func buildSemanticSections(cdb *db.ClaimsDB, importPath string) string {
	symbols, err := cdb.ListSymbolsByImportPath(importPath)
	if err != nil {
		return ""
	}

	var purposes []string
	var usagePatterns []string

	for _, sym := range symbols {
		claims, err := cdb.GetClaimsBySubject(sym.ID)
		if err != nil {
			continue
		}
		for _, cl := range claims {
			switch cl.Predicate {
			case "purpose":
				if cl.ObjectText != "" {
					purposes = append(purposes, cl.ObjectText)
				}
			case "usage_pattern":
				if cl.ObjectText != "" {
					usagePatterns = append(usagePatterns, cl.ObjectText)
				}
			}
		}
	}

	var b strings.Builder

	if len(purposes) > 0 {
		b.WriteString("## Purpose\n\n")
		for _, p := range purposes {
			b.WriteString("- ")
			b.WriteString(p)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(usagePatterns) > 0 {
		b.WriteString("## Usage Patterns\n\n")
		for _, u := range usagePatterns {
			b.WriteString("- ")
			b.WriteString(u)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	return b.String()
}

// isStale returns true if the given RFC3339 timestamp is older than stalenessThreshold.
func isStale(ts string) bool {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return false
	}
	return time.Since(t) > stalenessThreshold
}
