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
	StalePaths      []string `json:"stale_paths,omitempty"`
	ExtractedAt     string   `json:"extracted_at,omitempty"`
	ExtractedCommit string   `json:"extracted_commit,omitempty"`
	StaleWarning    string   `json:"stale_warning,omitempty"`
}

// ListPackagesHandler returns a ToolHandler that lists packages for a repo.
// If sc is non-nil, each listed package is checked for staleness and stale
// import paths are included in the stale_paths field of the response.
func ListPackagesHandler(pool *DBPool, sc *StalenessChecker) ToolHandler {
	return func(ctx context.Context, req ToolRequest) (ToolResult, error) {
		repoName, err := req.RequireString("repo")
		if err != nil {
			return NewErrorResult("missing required parameter 'repo'"), nil
		}
		prefix := req.GetString("prefix", "")

		if result, err := requireRepoExists(pool, repoName); result != nil {
			return result, err
		}

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

		// Check each package for staleness when StalenessChecker is available.
		if sc != nil {
			var stalePaths []string
			for _, ip := range paths {
				staleFiles := sc.CheckPackageStaleness(ctx, cdb, repoName, ip)
				if len(staleFiles) > 0 {
					stalePaths = append(stalePaths, ip)
				}
			}
			if len(stalePaths) > 0 {
				resp.StalePaths = stalePaths
			}
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
// If sc is non-nil, staleness checking is enabled for listed packages.
func ListPackagesToolDef(pool *DBPool, sc *StalenessChecker) ToolDef {
	return ToolDef{
		Name: "list_packages",
		Description: `List import paths (packages) for a given repository.

Returns up to 200 import paths matching an optional prefix, with a total count.
Includes extracted_at timestamp and warns if data is older than 7 days.
When repo roots are configured, includes stale_paths listing packages with changed files on disk.

Example: list_packages(repo="kubernetes", prefix="k8s.io/api/") returns all packages under that prefix.`,
		Params: []ParamDef{
			{Name: "repo", Type: ParamString, Required: true, Description: "Repository name (matches the .claims.db filename without extension)."},
			{Name: "prefix", Type: ParamString, Required: false, Description: "Optional import path prefix to filter packages. Example: 'k8s.io/api/'."},
		},
		Handler: ListPackagesHandler(pool, sc),
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

		if result, err := requireRepoExists(pool, repoName); result != nil {
			return result, err
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

		// Enrichment status line.
		enrichStatus := enrichmentStatus(cdb, importPath, time.Now())
		md += "\n> **Enrichment:** " + enrichStatus + "\n"

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

// requireRepoExists checks whether a claims DB exists for the given repo name.
// If the repo does not exist, it returns a non-nil ToolResult with a clear error
// message. If the repo exists, it returns (nil, nil) and the caller should proceed.
func requireRepoExists(pool *DBPool, repoName string) (ToolResult, error) {
	exists, err := pool.RepoExists(repoName)
	if err != nil {
		return NewErrorResultf("check repo %s: %v", repoName, err), nil
	}
	if !exists {
		return NewErrorResultf("repo %q not found: no claims database exists. Use list_repos to see available repositories or request_extraction to index a new one.", repoName), nil
	}
	return nil, nil
}

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

// enrichmentStatus computes the enrichment freshness line for a package.
// It returns one of:
//   - "Enriched at <date> (<age>)" when semantic claims exist and are fresh
//   - "Not yet enriched" when no semantic claims exist
//   - "Enrichment stale: source changed since <date>" when the source file
//     content hash has changed since the enrichment was produced
func enrichmentStatus(cdb *db.ClaimsDB, importPath string, now time.Time) string {
	symbols, err := cdb.ListSymbolsByImportPath(importPath)
	if err != nil || len(symbols) == 0 {
		return "Not yet enriched"
	}

	// Collect semantic claims and find the latest last_verified timestamp.
	var latestEnriched time.Time
	var latestEnrichedStr string
	var hasSemanticClaims bool

	for _, sym := range symbols {
		claims, err := cdb.GetClaimsBySubject(sym.ID)
		if err != nil {
			continue
		}
		for _, cl := range claims {
			if cl.ClaimTier != "semantic" {
				continue
			}
			hasSemanticClaims = true
			t, err := time.Parse(time.RFC3339, cl.LastVerified)
			if err != nil {
				continue
			}
			if t.After(latestEnriched) {
				latestEnriched = t
				latestEnrichedStr = cl.LastVerified
			}
		}
	}

	if !hasSemanticClaims {
		return "Not yet enriched"
	}

	// Check for staleness: compare source_files.last_indexed with the enrichment
	// timestamp. If any source file was re-indexed after the enrichment, the
	// enrichment is stale (content hash diverged).
	sourceFiles, err := cdb.GetSourceFilesByImportPath(importPath)
	if err == nil {
		for _, sf := range sourceFiles {
			sfTime, err := time.Parse(time.RFC3339, sf.LastIndexed)
			if err != nil {
				continue
			}
			if sfTime.After(latestEnriched) {
				dateStr := latestEnriched.Format("2006-01-02")
				return fmt.Sprintf("Enrichment stale: source changed since %s", dateStr)
			}
		}
	}

	dateStr := latestEnriched.Format("2006-01-02")
	age := formatAge(now.Sub(latestEnriched))
	_ = latestEnrichedStr // used for time parsing above
	return fmt.Sprintf("Enriched at %s (%s)", dateStr, age)
}

// formatAge renders a duration as a human-readable age string.
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d.Hours() / 24)
	if days == 0 {
		hours := int(d.Hours())
		if hours == 0 {
			return "just now"
		}
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	}
	if days == 1 {
		return "1 day ago"
	}
	if days < 30 {
		return fmt.Sprintf("%d days ago", days)
	}
	months := days / 30
	if months == 1 {
		return "1 month ago"
	}
	return fmt.Sprintf("%d months ago", months)
}
