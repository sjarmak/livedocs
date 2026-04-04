// Package mcpserver implements an MCP (Model Context Protocol) server that
// exposes livedocs functionality as tools. It provides seven tools:
//   - query_claims: search claims by symbol name and optional predicate
//   - check_drift: detect documentation drift for a file
//   - verify_section: check if claims anchored to a line range are still valid
//   - check_ai_context: verify AI context files for stale references
//   - list_repos: list all repositories in multi-repo mode
//   - list_packages: list import paths for a repository
//   - describe_package: render Markdown documentation for a package
//
// The server communicates over stdio using JSON-RPC per the MCP spec.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/live-docs/live_docs/aicontext"
	"github.com/live-docs/live_docs/anchor"
	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/drift"
	"github.com/live-docs/live_docs/extractor"
)

// Config holds the configuration for the MCP server.
type Config struct {
	// DBPath is the path to the claims SQLite database (single-DB mode).
	DBPath string
	// DataDir is the directory containing per-repo .claims.db files (multi-repo mode).
	DataDir string
	// Telemetry enables opt-in anonymous telemetry collection.
	Telemetry bool
	// RepoRoots maps repo names to their source code root directories on disk.
	// When set, enables lazy staleness checking in MCP query paths: if a queried
	// file has changed on disk since last extraction, it is re-extracted before
	// returning the response. Optional — if empty, staleness checking is disabled.
	RepoRoots map[string]string
	// ExtractorRegistry is the extractor registry used for lazy re-extraction.
	// Required when RepoRoots is set and re-extraction is desired. If nil,
	// staleness is detected but files are not re-extracted (warning only).
	ExtractorRegistry *extractor.Registry
}

// Server wraps the MCP server and its dependencies.
type Server struct {
	registry  ToolRegistry
	claimsDB  *db.ClaimsDB
	pool      *DBPool
	telemetry *Collector
	staleness *StalenessChecker
}

// New creates a new MCP server with the given configuration.
// The caller must call Close when done.
//
// In single-DB mode (DBPath set), the server registers the 4 legacy tools.
// In multi-repo mode (DataDir set), the server additionally registers
// list_repos, list_packages, and describe_package.
func New(cfg Config) (*Server, error) {
	// Validate: at least one mode must be specified.
	if cfg.DBPath == "" && cfg.DataDir == "" {
		return nil, fmt.Errorf("either DBPath or DataDir must be specified")
	}

	// Validate DataDir exists if specified.
	if cfg.DataDir != "" {
		info, err := os.Stat(cfg.DataDir)
		if err != nil {
			return nil, fmt.Errorf("data directory %s: %w", cfg.DataDir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("data directory %s is not a directory", cfg.DataDir)
		}
	}

	var s *Server

	if cfg.DBPath != "" {
		// Single-DB mode: open the database and register legacy tools.
		claimsDB, err := db.OpenClaimsDB(cfg.DBPath)
		if err != nil {
			return nil, fmt.Errorf("open claims db: %w", err)
		}
		s = NewWithDB(claimsDB)
	} else {
		// Multi-repo mode: create pool, no single claimsDB.
		s = &Server{}
		s.registry = s.buildRegistry()
	}

	// If DataDir is set, create pool and register multi-repo tools.
	if cfg.DataDir != "" {
		pool := NewDBPool(cfg.DataDir, DefaultMaxOpenDBs)
		s.pool = pool

		// Create staleness checker if repo roots are configured.
		if len(cfg.RepoRoots) > 0 {
			s.staleness = NewStalenessChecker(cfg.RepoRoots, cfg.ExtractorRegistry)
		}

		s.registerMultiRepoTools(pool)
	}

	s.telemetry = NewCollector(CollectorConfig{
		Enabled: cfg.Telemetry,
	})

	return s, nil
}

// NewWithDB creates a new MCP server using an existing ClaimsDB.
// This is useful for testing. The caller is responsible for closing the DB.
func NewWithDB(claimsDB *db.ClaimsDB) *Server {
	s := &Server{claimsDB: claimsDB}
	s.registry = s.buildRegistry()
	return s
}

func (s *Server) buildRegistry() ToolRegistry {
	reg := NewRegistry("livedocs", "0.1.0")
	// Register legacy tools only when we have a single claimsDB.
	if s.claimsDB != nil {
		legacyDefs := []ToolDef{
			queryClaimsToolDef(s),
			checkDriftToolDef(s),
			verifySectionToolDef(s),
			checkAIContextToolDef(),
		}
		for _, def := range legacyDefs {
			def.Handler = s.withTelemetry(def.Name, def.Handler)
			reg.Register(def)
		}
	}
	return reg
}

// registerMultiRepoTools registers the multi-repo tools via the adapter layer.
// Builds a routing index for cross-repo symbol search.
func (s *Server) registerMultiRepoTools(pool *DBPool) {
	// Build routing index for search_symbols fan-out optimization.
	index := NewRoutingIndex()
	// Best-effort: if Build fails, search falls back to all repos.
	_ = index.Build(pool)

	defs := []ToolDef{
		ListReposToolDef(pool),
		ListPackagesToolDef(pool, s.staleness),
		DescribePackageToolDef(pool, s.staleness),
		SearchSymbolsToolDef(pool, index),
	}
	for _, def := range defs {
		s.registry.Register(def)
	}
}

// withTelemetry wraps a ToolHandler to record telemetry when enabled.
func (s *Server) withTelemetry(name string, handler ToolHandler) ToolHandler {
	return func(ctx context.Context, req ToolRequest) (ToolResult, error) {
		if s.telemetry != nil {
			repoPath := req.GetString("path", req.GetString("file_path", ""))
			s.telemetry.Record(name, repoPath)
		}
		return handler(ctx, req)
	}
}

// MCPServer returns the underlying MCP server for use with transports.
func (s *Server) MCPServer() interface{} {
	return s.registry.Underlying()
}

// Close releases server resources, flushing any pending telemetry.
func (s *Server) Close() error {
	if s.telemetry != nil {
		_ = s.telemetry.Flush()
	}
	if s.pool != nil {
		_ = s.pool.Close()
	}
	if s.claimsDB != nil {
		return s.claimsDB.Close()
	}
	return nil
}

// Serve starts the MCP server over stdio. Blocks until the connection closes
// or a SIGTERM/SIGINT signal is received. Emits a ready signal to stderr
// on successful init.
func (s *Server) Serve() error {
	// Emit ready signal to stderr.
	fmt.Fprintf(os.Stderr, "{\"status\":\"ready\"}\n")

	// Set up signal handling for clean shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Run the stdio server in a goroutine so we can react to signals.
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.registry.Serve()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Signal received — clean shutdown.
		return nil
	}
}

// --- Tool definitions ---

func queryClaimsToolDef(s *Server) ToolDef {
	return ToolDef{
		Name: "query_claims",
		Description: `Search documentation claims by symbol name and optional predicate.

Returns structured claims from the livedocs claims database, grouped by symbol.
Each claim includes its predicate (relationship type), source location, confidence score, and tier.

Example response:
{"symbols":[{"symbol":{"id":1,"repo":"myrepo","import_path":"pkg/server","name":"NewServer","kind":"function","language":"go"},"claims":[{"id":1,"predicate":"defines","object_text":"creates a new server instance","source_file":"server.go","source_line":42,"confidence":1.0,"tier":"structural"}]}],"total_claims":1}`,
		Params: []ParamDef{
			{Name: "symbol", Type: ParamString, Required: true, Description: "Symbol name to search for. Use exact names like 'NewServer' or SQL LIKE wildcards: 'New%' matches NewServer, NewClient, etc. Use '%Handler%' to find all handler symbols."},
			{Name: "predicate", Type: ParamString, Required: false, Description: "Filter by claim predicate. Common predicates: 'defines' (structural definition), 'has_doc' (documentation comment), 'imports' (import relationship), 'purpose' (semantic purpose). Omit to return all claims for matching symbols."},
		},
		Handler: s.handleQueryClaims,
	}
}

func checkDriftToolDef(s *Server) ToolDef {
	return ToolDef{
		Name: "check_drift",
		Description: `Detect documentation drift by comparing symbol references in a README against actual code exports.

Finds symbols mentioned in documentation that no longer exist in code (stale references)
and exported symbols in code that are not mentioned in documentation (undocumented).

Example response:
{"file_path":"pkg/server/README.md","has_drift":true,"stale_count":1,"undocumented_count":2,"stale_package_count":0,"findings":[{"kind":"stale_symbol","symbol":"OldHandler","detail":"referenced in README but not found in code"}]}`,
		Params: []ParamDef{
			{Name: "file_path", Type: ParamString, Required: true, Description: "Absolute or relative path to a README or markdown file to check for drift. Example: 'pkg/server/README.md'."},
			{Name: "code_dir", Type: ParamString, Required: false, Description: "Code directory to compare against. Defaults to the directory containing file_path. Use when docs and code are in different directories."},
		},
		Handler: s.handleCheckDrift,
	}
}

func verifySectionToolDef(s *Server) ToolDef {
	return ToolDef{
		Name: "verify_section",
		Description: `Verify whether documentation claims anchored to a specific file and line range are still valid.

Returns each claim's status: verified (still accurate), stale (code changed), or invalid (anchor lost).
Use this after editing code to check if nearby documentation needs updating.

Example response:
{"file_path":"server.go","start_line":40,"end_line":50,"total_anchors":2,"verified":1,"stale":1,"invalid":0,"claims":[{"claim_id":1,"predicate":"defines","object_text":"creates a new server","source_line":42,"status":"verified"}}`,
		Params: []ParamDef{
			{Name: "file_path", Type: ParamString, Required: true, Description: "Source file path whose claims to verify. Must match the path used during claim extraction (e.g. 'server.go' or 'pkg/server/server.go')."},
			{Name: "start_line", Type: ParamNumber, Required: true, Description: "First line number of the range to check (1-based, inclusive)."},
			{Name: "end_line", Type: ParamNumber, Required: true, Description: "Last line number of the range to check (1-based, inclusive). Must be >= start_line."},
		},
		Handler: s.handleVerifySection,
	}
}

// --- Tool handlers ---

// queryClaimsResult is the JSON response for query_claims.
type queryClaimsResult struct {
	Symbols []symbolWithClaims `json:"symbols"`
	Total   int                `json:"total_claims"`
}

type symbolWithClaims struct {
	Symbol symbolInfo  `json:"symbol"`
	Claims []claimInfo `json:"claims"`
}

type symbolInfo struct {
	ID         int64  `json:"id"`
	Repo       string `json:"repo"`
	ImportPath string `json:"import_path"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Language   string `json:"language"`
}

type claimInfo struct {
	ID         int64   `json:"id"`
	Predicate  string  `json:"predicate"`
	ObjectText string  `json:"object_text,omitempty"`
	SourceFile string  `json:"source_file"`
	SourceLine int     `json:"source_line"`
	Confidence float64 `json:"confidence"`
	Tier       string  `json:"tier"`
}

func (s *Server) handleQueryClaims(_ context.Context, req ToolRequest) (ToolResult, error) {
	symbol, err := req.RequireString("symbol")
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}
	predicate := req.GetString("predicate", "")

	symbols, err := s.claimsDB.SearchSymbolsByName(symbol)
	if err != nil {
		return NewErrorResultf("search symbols: %v", err), nil
	}

	if len(symbols) == 0 {
		return NewTextResult(fmt.Sprintf("No symbols found matching %q. Try a broader wildcard pattern like '%%%s%%' or check that claims have been extracted for this repository.", symbol, symbol)), nil
	}

	result := queryClaimsResult{}
	for _, sym := range symbols {
		claims, err := s.claimsDB.GetClaimsBySubject(sym.ID)
		if err != nil {
			return NewErrorResultf("get claims for symbol %d: %v", sym.ID, err), nil
		}

		var filtered []claimInfo
		for _, cl := range claims {
			if predicate != "" && cl.Predicate != predicate {
				continue
			}
			filtered = append(filtered, claimInfo{
				ID:         cl.ID,
				Predicate:  cl.Predicate,
				ObjectText: cl.ObjectText,
				SourceFile: cl.SourceFile,
				SourceLine: cl.SourceLine,
				Confidence: cl.Confidence,
				Tier:       cl.ClaimTier,
			})
		}

		if len(filtered) > 0 {
			result.Symbols = append(result.Symbols, symbolWithClaims{
				Symbol: symbolInfo{
					ID:         sym.ID,
					Repo:       sym.Repo,
					ImportPath: sym.ImportPath,
					Name:       sym.SymbolName,
					Kind:       sym.Kind,
					Language:   sym.Language,
				},
				Claims: filtered,
			})
			result.Total += len(filtered)
		}
	}

	if result.Total == 0 {
		msg := fmt.Sprintf("Found %d symbol(s) matching %q but no claims", len(symbols), symbol)
		if predicate != "" {
			msg += fmt.Sprintf(" with predicate %q. Available predicates: defines, has_doc, imports, purpose", predicate)
		}
		return NewTextResult(msg), nil
	}

	data, err := json.Marshal(result)
	if err != nil {
		return NewErrorResultf("marshal result: %v", err), nil
	}
	return NewTextResult(string(data)), nil
}

// checkDriftResult is the JSON response for check_drift.
type checkDriftResult struct {
	FilePath          string         `json:"file_path"`
	HasDrift          bool           `json:"has_drift"`
	StaleCount        int            `json:"stale_count"`
	UndocumentedCount int            `json:"undocumented_count"`
	StalePackageCount int            `json:"stale_package_count"`
	Findings          []driftFinding `json:"findings,omitempty"`
}

type driftFinding struct {
	Kind   string `json:"kind"`
	Symbol string `json:"symbol"`
	Detail string `json:"detail"`
}

func (s *Server) handleCheckDrift(_ context.Context, req ToolRequest) (ToolResult, error) {
	filePath, err := req.RequireString("file_path")
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}
	codeDir := req.GetString("code_dir", "")

	report, err := drift.Detect(filePath, codeDir)
	if err != nil {
		return NewErrorResultf("drift detect: %v", err), nil
	}

	result := checkDriftResult{
		FilePath:          filePath,
		HasDrift:          report.StaleCount > 0 || report.StalePackageCount > 0,
		StaleCount:        report.StaleCount,
		UndocumentedCount: report.UndocumentedCount,
		StalePackageCount: report.StalePackageCount,
	}

	for _, f := range report.Findings {
		result.Findings = append(result.Findings, driftFinding{
			Kind:   string(f.Kind),
			Symbol: f.Symbol,
			Detail: f.Detail,
		})
	}

	data, err := json.Marshal(result)
	if err != nil {
		return NewErrorResultf("marshal result: %v", err), nil
	}
	return NewTextResult(string(data)), nil
}

// verifySectionResult is the JSON response for verify_section.
type verifySectionResult struct {
	FilePath   string              `json:"file_path"`
	StartLine  int                 `json:"start_line"`
	EndLine    int                 `json:"end_line"`
	TotalAnch  int                 `json:"total_anchors"`
	Verified   int                 `json:"verified"`
	Stale      int                 `json:"stale"`
	Invalid    int                 `json:"invalid"`
	ClaimsList []verifySectionItem `json:"claims,omitempty"`
}

type verifySectionItem struct {
	ClaimID    int64  `json:"claim_id"`
	Predicate  string `json:"predicate"`
	ObjectText string `json:"object_text,omitempty"`
	SourceLine int    `json:"source_line"`
	Status     string `json:"status"`
}

func (s *Server) handleVerifySection(_ context.Context, req ToolRequest) (ToolResult, error) {
	filePath, err := req.RequireString("file_path")
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}
	startLine, err := req.RequireInt("start_line")
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}
	endLine, err := req.RequireInt("end_line")
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}

	if startLine < 0 || endLine < 0 {
		return NewErrorResult("start_line and end_line must be non-negative (1-based line numbers)"), nil
	}
	if startLine > endLine && endLine != 0 {
		return NewErrorResultf("start_line (%d) must be <= end_line (%d)", startLine, endLine), nil
	}

	claims, err := s.claimsDB.GetClaimsByFileAndLineRange(filePath, startLine, endLine)
	if err != nil {
		return NewErrorResultf("get claims: %v", err), nil
	}

	if len(claims) == 0 {
		return NewTextResult(fmt.Sprintf("No claims found for %s lines %d-%d. Ensure claims have been extracted for this file and that the path matches exactly.", filePath, startLine, endLine)), nil
	}

	// Build anchor index from the claims and check which overlap with the range.
	const anchorRadius = 3
	idx := anchor.BuildFromClaims(claims, anchorRadius)
	anchors := idx.ForFile(filePath)

	result := verifySectionResult{
		FilePath:  filePath,
		StartLine: startLine,
		EndLine:   endLine,
	}

	// Map claim ID to anchor status for reporting.
	anchorStatus := make(map[int64]anchor.Status)
	for _, a := range anchors {
		if a.Overlaps(startLine, endLine) {
			anchorStatus[a.ClaimID] = a.Status
			result.TotalAnch++
			switch a.Status {
			case anchor.StatusVerified:
				result.Verified++
			case anchor.StatusStale:
				result.Stale++
			case anchor.StatusInvalid:
				result.Invalid++
			}
		}
	}

	for _, cl := range claims {
		status := anchor.StatusVerified
		if st, ok := anchorStatus[cl.ID]; ok {
			status = st
		}
		result.ClaimsList = append(result.ClaimsList, verifySectionItem{
			ClaimID:    cl.ID,
			Predicate:  cl.Predicate,
			ObjectText: cl.ObjectText,
			SourceLine: cl.SourceLine,
			Status:     string(status),
		})
	}

	data, err := json.Marshal(result)
	if err != nil {
		return NewErrorResultf("marshal result: %v", err), nil
	}
	return NewTextResult(string(data)), nil
}

// --- check_ai_context tool ---

func checkAIContextToolDef() ToolDef {
	return ToolDef{
		Name: "check_ai_context",
		Description: `Verify AI context files (CLAUDE.md, AGENTS.md, .cursorrules, .cursor/rules, etc.) for stale references.

Scans for file paths that no longer exist, broken package references, and outdated claims
in AI assistant instruction files. Helps keep AI context accurate as code evolves.

Example response:
{"path":"/repo","files":["CLAUDE.md"],"total_claims":5,"valid_count":4,"stale_count":1,"has_drift":true,"findings":[{"kind":"file_path","value":"src/old.go","source_file":"CLAUDE.md","line":12,"status":"stale","detail":"file does not exist"}]}`,
		Params: []ParamDef{
			{Name: "path", Type: ParamString, Required: true, Description: "Repository root path to scan. AI context files are discovered automatically (CLAUDE.md, AGENTS.md, .cursorrules, .cursor/rules/, etc.)."},
		},
		Handler: handleCheckAIContext,
	}
}

// aiContextResult is the JSON response for check_ai_context.
type aiContextResult struct {
	Path        string             `json:"path"`
	Files       []string           `json:"files"`
	TotalClaims int                `json:"total_claims"`
	ValidCount  int                `json:"valid_count"`
	StaleCount  int                `json:"stale_count"`
	HasDrift    bool               `json:"has_drift"`
	Findings    []aiContextFinding `json:"findings,omitempty"`
}

type aiContextFinding struct {
	Kind       string `json:"kind"`
	Value      string `json:"value"`
	SourceFile string `json:"source_file"`
	Line       int    `json:"line"`
	Status     string `json:"status"`
	Detail     string `json:"detail"`
}

func handleCheckAIContext(_ context.Context, req ToolRequest) (ToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}

	report, err := aicontext.Check(path)
	if err != nil {
		return NewErrorResultf("check AI context: %v", err), nil
	}

	result := aiContextResult{
		Path:        path,
		Files:       report.Files,
		TotalClaims: report.TotalClaims,
		ValidCount:  report.ValidCount,
		StaleCount:  report.StaleCount,
		HasDrift:    report.HasDrift(),
	}

	for _, f := range report.Findings {
		result.Findings = append(result.Findings, aiContextFinding{
			Kind:       string(f.Claim.Kind),
			Value:      f.Claim.Value,
			SourceFile: f.Claim.SourceFile,
			Line:       f.Claim.Line,
			Status:     string(f.Status),
			Detail:     f.Detail,
		})
	}

	data, err := json.Marshal(result)
	if err != nil {
		return NewErrorResultf("marshal result: %v", err), nil
	}
	return NewTextResult(string(data)), nil
}
