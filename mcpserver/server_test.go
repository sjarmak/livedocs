package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/live-docs/live_docs/db"
)

// setupTestDB creates a temporary SQLite database with schema and test data.
func setupTestDB(t *testing.T) *db.ClaimsDB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	// Insert test symbols and claims.
	symID, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       "test-repo",
		ImportPath: "github.com/test/pkg",
		SymbolName: "NewServer",
		Language:   "go",
		Kind:       "function",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}

	_, err = cdb.InsertClaim(db.Claim{
		SubjectID:        symID,
		Predicate:        "defines",
		ObjectText:       "creates a new server instance",
		SourceFile:       "server.go",
		SourceLine:       42,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "treesitter",
		ExtractorVersion: "v1",
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}

	_, err = cdb.InsertClaim(db.Claim{
		SubjectID:        symID,
		Predicate:        "has_doc",
		ObjectText:       "NewServer creates a new HTTP server",
		SourceFile:       "server.go",
		SourceLine:       41,
		Confidence:       0.9,
		ClaimTier:        "semantic",
		Extractor:        "llm",
		ExtractorVersion: "v1",
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}

	// Insert a second symbol.
	sym2ID, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       "test-repo",
		ImportPath: "github.com/test/pkg",
		SymbolName: "NewClient",
		Language:   "go",
		Kind:       "function",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}

	_, err = cdb.InsertClaim(db.Claim{
		SubjectID:        sym2ID,
		Predicate:        "defines",
		ObjectText:       "creates a new client",
		SourceFile:       "client.go",
		SourceLine:       10,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "treesitter",
		ExtractorVersion: "v1",
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}

	t.Cleanup(func() { cdb.Close() })
	return cdb
}

func makeRequest(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: args,
		},
	}
}

func TestNewAndClose(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create a valid database first.
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("setup db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	cdb.Close()

	srv, err := New(Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv.MCPServer() == nil {
		t.Error("MCPServer() returned nil")
	}
	if err := srv.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestNew_InvalidPath(t *testing.T) {
	_, err := New(Config{DBPath: "/nonexistent/path/claims.db"})
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestQueryClaims_ExactMatch(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeRequest(map[string]any{
		"symbol": "NewServer",
	})

	result, err := srv.handleQueryClaims(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}

	// Parse the JSON response.
	text := result.Content[0].(mcp.TextContent).Text
	var qr queryClaimsResult
	if err := json.Unmarshal([]byte(text), &qr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if qr.Total != 2 {
		t.Errorf("expected 2 claims, got %d", qr.Total)
	}
	if len(qr.Symbols) != 1 {
		t.Errorf("expected 1 symbol, got %d", len(qr.Symbols))
	}
	if qr.Symbols[0].Symbol.Name != "NewServer" {
		t.Errorf("expected symbol name NewServer, got %s", qr.Symbols[0].Symbol.Name)
	}
}

func TestQueryClaims_WildcardMatch(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeRequest(map[string]any{
		"symbol": "New%",
	})

	result, err := srv.handleQueryClaims(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	var qr queryClaimsResult
	if err := json.Unmarshal([]byte(text), &qr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(qr.Symbols) != 2 {
		t.Errorf("expected 2 symbols, got %d", len(qr.Symbols))
	}
	if qr.Total != 3 {
		t.Errorf("expected 3 claims total, got %d", qr.Total)
	}
}

func TestQueryClaims_PredicateFilter(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeRequest(map[string]any{
		"symbol":    "NewServer",
		"predicate": "has_doc",
	})

	result, err := srv.handleQueryClaims(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	var qr queryClaimsResult
	if err := json.Unmarshal([]byte(text), &qr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if qr.Total != 1 {
		t.Errorf("expected 1 claim with predicate has_doc, got %d", qr.Total)
	}
}

func TestQueryClaims_NoResults(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeRequest(map[string]any{
		"symbol": "NonExistent",
	})

	result, err := srv.handleQueryClaims(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text != `No symbols found matching "NonExistent"` {
		t.Errorf("unexpected text: %s", text)
	}
}

func TestQueryClaims_MissingSymbol(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeRequest(map[string]any{})

	result, err := srv.handleQueryClaims(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected error result for missing symbol")
	}
}

func TestCheckDrift(t *testing.T) {
	// Create a temp directory with a README and a Go file.
	tmpDir := t.TempDir()

	readme := filepath.Join(tmpDir, "README.md")
	goFile := filepath.Join(tmpDir, "main.go")

	if err := os.WriteFile(readme, []byte("# Test\n\nUses `FooBar` and `BazQux`.\n"), 0644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.WriteFile(goFile, []byte("package main\n\nfunc FooBar() {}\n"), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}

	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeRequest(map[string]any{
		"file_path": readme,
	})

	result, err := srv.handleCheckDrift(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}

	text := result.Content[0].(mcp.TextContent).Text
	var dr checkDriftResult
	if err := json.Unmarshal([]byte(text), &dr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if !dr.HasDrift {
		t.Errorf("expected drift (BazQux is stale)")
	}
	if dr.StaleCount < 1 {
		t.Errorf("expected at least 1 stale reference, got %d", dr.StaleCount)
	}
	// BazQux should appear among the stale findings.
	foundBazQux := false
	for _, f := range dr.Findings {
		if f.Symbol == "BazQux" {
			foundBazQux = true
		}
	}
	if !foundBazQux {
		t.Errorf("expected BazQux in stale findings")
	}
}

func TestCheckDrift_MissingFilePath(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeRequest(map[string]any{})

	result, err := srv.handleCheckDrift(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected error result for missing file_path")
	}
}

func TestVerifySection(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	// Query claims for server.go lines 40-45 (our test claims are at lines 41 and 42).
	req := makeRequest(map[string]any{
		"file_path":  "server.go",
		"start_line": 40,
		"end_line":   45,
	})

	result, err := srv.handleVerifySection(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.Content)
	}

	text := result.Content[0].(mcp.TextContent).Text
	var vr verifySectionResult
	if err := json.Unmarshal([]byte(text), &vr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(vr.ClaimsList) != 2 {
		t.Errorf("expected 2 claims in range, got %d", len(vr.ClaimsList))
	}
	if vr.FilePath != "server.go" {
		t.Errorf("expected file_path server.go, got %s", vr.FilePath)
	}
}

func TestVerifySection_NoClaims(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeRequest(map[string]any{
		"file_path":  "nonexistent.go",
		"start_line": 1,
		"end_line":   10,
	})

	result, err := srv.handleVerifySection(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text != "No claims found for nonexistent.go lines 1-10" {
		t.Errorf("unexpected text: %s", text)
	}
}

func TestVerifySection_InvalidRange(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeRequest(map[string]any{
		"file_path":  "server.go",
		"start_line": 50,
		"end_line":   10,
	})

	result, err := srv.handleVerifySection(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected error for invalid range")
	}
}
