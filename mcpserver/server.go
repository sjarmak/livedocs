// Package mcpserver implements an MCP (Model Context Protocol) server that
// exposes livedocs functionality as tools. It provides three tools:
//   - query_claims: search claims by symbol name and optional predicate
//   - check_drift: detect documentation drift for a file
//   - verify_section: check if claims anchored to a line range are still valid
//
// The server communicates over stdio using JSON-RPC per the MCP spec.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/live-docs/live_docs/anchor"
	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/drift"
)

// Config holds the configuration for the MCP server.
type Config struct {
	// DBPath is the path to the claims SQLite database.
	DBPath string
}

// Server wraps the MCP server and its dependencies.
type Server struct {
	mcpServer *server.MCPServer
	claimsDB  *db.ClaimsDB
}

// New creates a new MCP server with the given configuration.
// The caller must call Close when done.
func New(cfg Config) (*Server, error) {
	claimsDB, err := db.OpenClaimsDB(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open claims db: %w", err)
	}
	return NewWithDB(claimsDB), nil
}

// NewWithDB creates a new MCP server using an existing ClaimsDB.
// This is useful for testing. The caller is responsible for closing the DB.
func NewWithDB(claimsDB *db.ClaimsDB) *Server {
	s := &Server{claimsDB: claimsDB}
	s.mcpServer = s.buildMCPServer()
	return s
}

func (s *Server) buildMCPServer() *server.MCPServer {
	srv := server.NewMCPServer(
		"livedocs",
		"0.1.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)
	srv.AddTool(queryClaimsTool(), s.handleQueryClaims)
	srv.AddTool(checkDriftTool(), s.handleCheckDrift)
	srv.AddTool(verifySectionTool(), s.handleVerifySection)
	return srv
}

// MCPServer returns the underlying MCP server for use with transports.
func (s *Server) MCPServer() *server.MCPServer {
	return s.mcpServer
}

// Close releases server resources.
func (s *Server) Close() error {
	if s.claimsDB != nil {
		return s.claimsDB.Close()
	}
	return nil
}

// Serve starts the MCP server over stdio. Blocks until the connection closes.
func (s *Server) Serve() error {
	return server.ServeStdio(s.mcpServer)
}

// --- Tool definitions ---

func queryClaimsTool() mcp.Tool {
	return mcp.NewTool("query_claims",
		mcp.WithDescription("Search documentation claims by symbol name and optional predicate. Returns claims from the livedocs claims database."),
		mcp.WithString("symbol",
			mcp.Required(),
			mcp.Description("Symbol name to search for. Supports SQL LIKE wildcards (% for any chars)."),
		),
		mcp.WithString("predicate",
			mcp.Description("Optional predicate filter (e.g. 'defines', 'imports', 'has_doc', 'purpose'). If omitted, returns all claims for matching symbols."),
		),
	)
}

func checkDriftTool() mcp.Tool {
	return mcp.NewTool("check_drift",
		mcp.WithDescription("Detect documentation drift for a file by comparing README symbol references against code exports."),
		mcp.WithString("file_path",
			mcp.Required(),
			mcp.Description("Path to a README/markdown file to check for drift."),
		),
		mcp.WithString("code_dir",
			mcp.Description("Optional code directory to compare against. Defaults to the file's directory."),
		),
	)
}

func verifySectionTool() mcp.Tool {
	return mcp.NewTool("verify_section",
		mcp.WithDescription("Check if claims anchored to a specific file and line range are still valid."),
		mcp.WithString("file_path",
			mcp.Required(),
			mcp.Description("Source file path whose claims to verify."),
		),
		mcp.WithNumber("start_line",
			mcp.Required(),
			mcp.Description("Start line of the range to check."),
		),
		mcp.WithNumber("end_line",
			mcp.Required(),
			mcp.Description("End line of the range to check."),
		),
	)
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

func (s *Server) handleQueryClaims(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	symbol, err := req.RequireString("symbol")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	predicate := req.GetString("predicate", "")

	symbols, err := s.claimsDB.SearchSymbolsByName(symbol)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search symbols: %v", err)), nil
	}

	if len(symbols) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No symbols found matching %q", symbol)), nil
	}

	result := queryClaimsResult{}
	for _, sym := range symbols {
		claims, err := s.claimsDB.GetClaimsBySubject(sym.ID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("get claims for symbol %d: %v", sym.ID, err)), nil
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
			msg += fmt.Sprintf(" with predicate %q", predicate)
		}
		return mcp.NewToolResultText(msg), nil
	}

	data, err := json.Marshal(result)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
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

func (s *Server) handleCheckDrift(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	filePath, err := req.RequireString("file_path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	codeDir := req.GetString("code_dir", "")

	report, err := drift.Detect(filePath, codeDir)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("drift detect: %v", err)), nil
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
		return mcp.NewToolResultError(fmt.Sprintf("marshal result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
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

func (s *Server) handleVerifySection(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	filePath, err := req.RequireString("file_path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	startLine, err := req.RequireInt("start_line")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	endLine, err := req.RequireInt("end_line")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if startLine < 0 || endLine < 0 {
		return mcp.NewToolResultError("start_line and end_line must be non-negative"), nil
	}
	if startLine > endLine && endLine != 0 {
		return mcp.NewToolResultError("start_line must be <= end_line"), nil
	}

	claims, err := s.claimsDB.GetClaimsByFileAndLineRange(filePath, startLine, endLine)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("get claims: %v", err)), nil
	}

	if len(claims) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No claims found for %s lines %d-%d", filePath, startLine, endLine)), nil
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
		if s, ok := anchorStatus[cl.ID]; ok {
			status = s
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
		return mcp.NewToolResultError(fmt.Sprintf("marshal result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}
