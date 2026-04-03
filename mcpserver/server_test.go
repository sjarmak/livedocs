package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/live-docs/live_docs/db"
)

// makeAdapterRequest creates a ToolRequest from a map of arguments for testing
// legacy handlers that now use adapter types.
func makeAdapterRequest(args map[string]any) ToolRequest {
	return WrapRequest(makeRequest(args))
}

func hasSubstr(s, substr string) bool { return strings.Contains(s, substr) }

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

// --- Tool schema quality tests ---

// dummyServer returns a Server with a nil claimsDB, used only to obtain ToolDef metadata.
func dummyServer() *Server {
	return &Server{}
}

func TestToolSchemas_HaveDescriptions(t *testing.T) {
	s := dummyServer()
	defs := []ToolDef{
		queryClaimsToolDef(s),
		checkDriftToolDef(s),
		verifySectionToolDef(s),
		checkAIContextToolDef(),
	}

	for _, def := range defs {
		t.Run(def.Name, func(t *testing.T) {
			if def.Description == "" {
				t.Errorf("tool %s has no description", def.Name)
			}
			if len(def.Description) < 50 {
				t.Errorf("tool %s description too short (%d chars), expected detailed description with examples", def.Name, len(def.Description))
			}
		})
	}
}

func TestToolSchemas_ParameterDescriptions(t *testing.T) {
	s := dummyServer()
	tests := []struct {
		def    ToolDef
		params []string
	}{
		{queryClaimsToolDef(s), []string{"symbol", "predicate"}},
		{checkDriftToolDef(s), []string{"file_path", "code_dir"}},
		{verifySectionToolDef(s), []string{"file_path", "start_line", "end_line"}},
		{checkAIContextToolDef(), []string{"path"}},
	}

	for _, tt := range tests {
		t.Run(tt.def.Name, func(t *testing.T) {
			// Build the mcp.Tool to inspect schema properties.
			tool := buildTool(tt.def)
			props := tool.InputSchema.Properties
			if props == nil {
				t.Fatalf("tool %s: InputSchema.Properties is nil", tt.def.Name)
			}
			for _, param := range tt.params {
				prop, ok := props[param]
				if !ok {
					t.Errorf("tool %s: missing parameter %q in schema", tt.def.Name, param)
					continue
				}
				propMap, ok := prop.(map[string]interface{})
				if !ok {
					t.Errorf("tool %s: parameter %q is not a map", tt.def.Name, param)
					continue
				}
				desc, ok := propMap["description"]
				if !ok || desc == "" {
					t.Errorf("tool %s: parameter %q has no description", tt.def.Name, param)
				}
			}
		})
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

	req := makeAdapterRequest(map[string]any{
		"symbol": "NewServer",
	})

	result, err := srv.handleQueryClaims(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("tool returned error: %s", result.Text())
	}

	// Parse the JSON response.
	text := result.Text()
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

	req := makeAdapterRequest(map[string]any{
		"symbol": "New%",
	})

	result, err := srv.handleQueryClaims(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var qr queryClaimsResult
	if err := json.Unmarshal([]byte(result.Text()), &qr); err != nil {
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

	req := makeAdapterRequest(map[string]any{
		"symbol":    "NewServer",
		"predicate": "has_doc",
	})

	result, err := srv.handleQueryClaims(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var qr queryClaimsResult
	if err := json.Unmarshal([]byte(result.Text()), &qr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if qr.Total != 1 {
		t.Errorf("expected 1 claim with predicate has_doc, got %d", qr.Total)
	}
}

func TestQueryClaims_NoResults(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeAdapterRequest(map[string]any{
		"symbol": "NonExistent",
	})

	result, err := srv.handleQueryClaims(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !hasSubstr(result.Text(), `No symbols found matching "NonExistent"`) {
		t.Errorf("unexpected text: %s", result.Text())
	}
}

func TestQueryClaims_MissingSymbol(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeAdapterRequest(map[string]any{})

	result, err := srv.handleQueryClaims(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
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

	req := makeAdapterRequest(map[string]any{
		"file_path": readme,
	})

	result, err := srv.handleCheckDrift(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("tool returned error: %s", result.Text())
	}

	var dr checkDriftResult
	if err := json.Unmarshal([]byte(result.Text()), &dr); err != nil {
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

	req := makeAdapterRequest(map[string]any{})

	result, err := srv.handleCheckDrift(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Errorf("expected error result for missing file_path")
	}
}

func TestVerifySection(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	// Query claims for server.go lines 40-45 (our test claims are at lines 41 and 42).
	req := makeAdapterRequest(map[string]any{
		"file_path":  "server.go",
		"start_line": 40,
		"end_line":   45,
	})

	result, err := srv.handleVerifySection(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("tool returned error: %s", result.Text())
	}

	var vr verifySectionResult
	if err := json.Unmarshal([]byte(result.Text()), &vr); err != nil {
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

	req := makeAdapterRequest(map[string]any{
		"file_path":  "nonexistent.go",
		"start_line": 1,
		"end_line":   10,
	})

	result, err := srv.handleVerifySection(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result")
	}

	if !hasSubstr(result.Text(), "No claims found for nonexistent.go lines 1-10") {
		t.Errorf("unexpected text: %s", result.Text())
	}
}

func TestVerifySection_InvalidRange(t *testing.T) {
	cdb := setupTestDB(t)
	srv := NewWithDB(cdb)

	req := makeAdapterRequest(map[string]any{
		"file_path":  "server.go",
		"start_line": 50,
		"end_line":   10,
	})

	result, err := srv.handleVerifySection(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Errorf("expected error for invalid range")
	}
}

// ---------------------------------------------------------------------------
// Multi-repo mode tests
// ---------------------------------------------------------------------------

// setupMultiRepoDB creates a temp directory with one or more .claims.db files
// containing test data. Returns the data directory path and a cleanup function.
func setupMultiRepoDB(t *testing.T, repos ...string) string {
	t.Helper()
	tmpDir := t.TempDir()

	for _, repoName := range repos {
		dbPath := filepath.Join(tmpDir, repoName+".claims.db")
		cdb, err := db.OpenClaimsDB(dbPath)
		if err != nil {
			t.Fatalf("open db for %s: %v", repoName, err)
		}
		if err := cdb.CreateSchema(); err != nil {
			t.Fatalf("create schema for %s: %v", repoName, err)
		}

		// Insert a symbol and claim.
		symID, err := cdb.UpsertSymbol(db.Symbol{
			Repo:       repoName,
			ImportPath: "example.com/" + repoName + "/pkg",
			SymbolName: "Func1",
			Language:   "go",
			Kind:       "func",
			Visibility: "public",
		})
		if err != nil {
			t.Fatalf("upsert symbol for %s: %v", repoName, err)
		}

		_, err = cdb.InsertClaim(db.Claim{
			SubjectID:        symID,
			Predicate:        "defines",
			ObjectText:       "does something",
			SourceFile:       "func1.go",
			SourceLine:       10,
			Confidence:       1.0,
			ClaimTier:        "structural",
			Extractor:        "treesitter",
			ExtractorVersion: "v1",
			LastVerified:     db.Now(),
		})
		if err != nil {
			t.Fatalf("insert claim for %s: %v", repoName, err)
		}

		// Insert a source_file record for staleness testing.
		_, err = cdb.UpsertSourceFile(db.SourceFile{
			Repo:             repoName,
			RelativePath:     "func1.go",
			ContentHash:      "abc123",
			ExtractorVersion: "v1",
			LastIndexed:      db.Now(),
		})
		if err != nil {
			t.Fatalf("upsert source file for %s: %v", repoName, err)
		}

		cdb.Close()
	}

	return tmpDir
}

func TestNew_DataDir(t *testing.T) {
	dataDir := setupMultiRepoDB(t, "repo-a", "repo-b")

	srv, err := New(Config{DataDir: dataDir})
	if err != nil {
		t.Fatalf("New with DataDir: %v", err)
	}
	defer srv.Close()

	if srv.MCPServer() == nil {
		t.Error("MCPServer() returned nil")
	}
	if srv.pool == nil {
		t.Error("pool is nil in multi-repo mode")
	}
}

func TestNew_DataDir_Nonexistent(t *testing.T) {
	_, err := New(Config{DataDir: "/nonexistent/path/data"})
	if err == nil {
		t.Error("expected error for nonexistent DataDir")
	}
}

func TestNew_NeitherDBPathNorDataDir(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Error("expected error when neither DBPath nor DataDir is set")
	}
}

func TestListRepos(t *testing.T) {
	dataDir := setupMultiRepoDB(t, "alpha", "beta")
	pool := NewDBPool(dataDir, DefaultMaxOpenDBs)
	defer pool.Close()

	handler := ListReposHandler(pool)
	result, err := handler(context.Background(), &requestAdapter{raw: makeRequest(nil)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("tool returned error: %s", result.Text())
	}

	var resp listReposResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(resp.Repos) != 2 {
		t.Errorf("expected 2 repos, got %d", len(resp.Repos))
	}

	// Check that each repo has at least 1 symbol and 1 claim.
	for _, r := range resp.Repos {
		if r.Symbols < 1 {
			t.Errorf("repo %s: expected at least 1 symbol, got %d", r.Repo, r.Symbols)
		}
		if r.Claims < 1 {
			t.Errorf("repo %s: expected at least 1 claim, got %d", r.Repo, r.Claims)
		}
		if r.ExtractedAt == "" {
			t.Errorf("repo %s: expected extracted_at timestamp", r.Repo)
		}
	}
}

func TestListPackages(t *testing.T) {
	dataDir := setupMultiRepoDB(t, "myrepo")
	pool := NewDBPool(dataDir, DefaultMaxOpenDBs)
	defer pool.Close()

	handler := ListPackagesHandler(pool)
	result, err := handler(context.Background(), &requestAdapter{raw: makeRequest(map[string]any{
		"repo": "myrepo",
	})})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("tool returned error: %s", result.Text())
	}

	var resp listPackagesResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.TotalCount != 1 {
		t.Errorf("expected total_count 1, got %d", resp.TotalCount)
	}
	if len(resp.ImportPaths) != 1 {
		t.Errorf("expected 1 import path, got %d", len(resp.ImportPaths))
	}
	if resp.ImportPaths[0] != "example.com/myrepo/pkg" {
		t.Errorf("expected 'example.com/myrepo/pkg', got %s", resp.ImportPaths[0])
	}
}

func TestListPackages_WithPrefix(t *testing.T) {
	dataDir := setupMultiRepoDB(t, "myrepo")
	pool := NewDBPool(dataDir, DefaultMaxOpenDBs)
	defer pool.Close()

	handler := ListPackagesHandler(pool)

	// Prefix that matches.
	result, err := handler(context.Background(), &requestAdapter{raw: makeRequest(map[string]any{
		"repo":   "myrepo",
		"prefix": "example.com/myrepo",
	})})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp listPackagesResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.TotalCount != 1 {
		t.Errorf("expected total_count 1, got %d", resp.TotalCount)
	}

	// Prefix that doesn't match.
	result2, err := handler(context.Background(), &requestAdapter{raw: makeRequest(map[string]any{
		"repo":   "myrepo",
		"prefix": "nonexistent/",
	})})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp2 listPackagesResponse
	if err := json.Unmarshal([]byte(result2.Text()), &resp2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp2.TotalCount != 0 {
		t.Errorf("expected total_count 0, got %d", resp2.TotalCount)
	}
}

func TestListPackages_MissingRepo(t *testing.T) {
	dataDir := setupMultiRepoDB(t, "myrepo")
	pool := NewDBPool(dataDir, DefaultMaxOpenDBs)
	defer pool.Close()

	handler := ListPackagesHandler(pool)
	result, err := handler(context.Background(), &requestAdapter{raw: makeRequest(map[string]any{})})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Errorf("expected error for missing repo")
	}
}

func TestDescribePackage(t *testing.T) {
	dataDir := setupMultiRepoDB(t, "myrepo")

	// Re-open the DB to add purpose and usage_pattern claims.
	dbPath := filepath.Join(dataDir, "myrepo.claims.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}

	sym, err := cdb.GetSymbolByCompositeKey("myrepo", "example.com/myrepo/pkg", "Func1")
	if err != nil {
		t.Fatalf("get symbol: %v", err)
	}

	_, err = cdb.InsertClaim(db.Claim{
		SubjectID:        sym.ID,
		Predicate:        "purpose",
		ObjectText:       "Provides core functionality for the package",
		SourceFile:       "func1.go",
		SourceLine:       1,
		Confidence:       0.85,
		ClaimTier:        "semantic",
		Extractor:        "llm",
		ExtractorVersion: "v1",
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert purpose claim: %v", err)
	}

	_, err = cdb.InsertClaim(db.Claim{
		SubjectID:        sym.ID,
		Predicate:        "usage_pattern",
		ObjectText:       "Call Func1() to initialize the subsystem",
		SourceFile:       "func1.go",
		SourceLine:       1,
		Confidence:       0.8,
		ClaimTier:        "semantic",
		Extractor:        "llm",
		ExtractorVersion: "v1",
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert usage_pattern claim: %v", err)
	}

	cdb.Close()

	pool := NewDBPool(dataDir, DefaultMaxOpenDBs)
	defer pool.Close()

	handler := DescribePackageHandler(pool)
	result, err := handler(context.Background(), &requestAdapter{raw: makeRequest(map[string]any{
		"repo":        "myrepo",
		"import_path": "example.com/myrepo/pkg",
	})})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("tool returned error: %s", result.Text())
	}

	text := result.Text()

	// Check for structural content.
	if !hasSubstr(text, "example.com/myrepo/pkg") {
		t.Errorf("expected import path in output")
	}

	// Check for semantic sections.
	if !hasSubstr(text, "## Purpose") {
		t.Errorf("expected Purpose section in output")
	}
	if !hasSubstr(text, "Provides core functionality") {
		t.Errorf("expected purpose claim text in output")
	}
	if !hasSubstr(text, "## Usage Patterns") {
		t.Errorf("expected Usage Patterns section in output")
	}
	if !hasSubstr(text, "Call Func1()") {
		t.Errorf("expected usage pattern text in output")
	}

	// Check for extracted_at metadata.
	if !hasSubstr(text, "Data extracted at") {
		t.Errorf("expected extracted_at metadata in output")
	}
}

func TestDescribePackage_MissingParams(t *testing.T) {
	dataDir := setupMultiRepoDB(t, "myrepo")
	pool := NewDBPool(dataDir, DefaultMaxOpenDBs)
	defer pool.Close()

	handler := DescribePackageHandler(pool)

	// Missing repo.
	result, err := handler(context.Background(), &requestAdapter{raw: makeRequest(map[string]any{
		"import_path": "example.com/myrepo/pkg",
	})})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Errorf("expected error for missing repo")
	}

	// Missing import_path.
	result2, err := handler(context.Background(), &requestAdapter{raw: makeRequest(map[string]any{
		"repo": "myrepo",
	})})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result2.IsError() {
		t.Errorf("expected error for missing import_path")
	}
}

func TestDescribePackage_FallsBackGracefully(t *testing.T) {
	// Test with a repo that has no semantic claims — should still work.
	dataDir := setupMultiRepoDB(t, "basic")
	pool := NewDBPool(dataDir, DefaultMaxOpenDBs)
	defer pool.Close()

	handler := DescribePackageHandler(pool)
	result, err := handler(context.Background(), &requestAdapter{raw: makeRequest(map[string]any{
		"repo":        "basic",
		"import_path": "example.com/basic/pkg",
	})})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("tool returned error: %s", result.Text())
	}

	text := result.Text()
	if !hasSubstr(text, "example.com/basic/pkg") {
		t.Errorf("expected import path in output")
	}
	// No Purpose section expected since no semantic claims exist.
	if hasSubstr(text, "## Purpose") {
		t.Errorf("unexpected Purpose section without semantic claims")
	}
}

func TestIsStale(t *testing.T) {
	// Recent timestamp should not be stale.
	recent := time.Now().UTC().Format(time.RFC3339)
	if isStale(recent) {
		t.Errorf("recent timestamp should not be stale")
	}

	// Old timestamp should be stale.
	old := time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if !isStale(old) {
		t.Errorf("8-day-old timestamp should be stale")
	}

	// Invalid timestamp should not be stale (graceful fallback).
	if isStale("not-a-timestamp") {
		t.Errorf("invalid timestamp should not be considered stale")
	}
}

func TestNew_BothModes(t *testing.T) {
	// Test that both DBPath and DataDir can be set simultaneously.
	dataDir := setupMultiRepoDB(t, "somerepo")

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "single.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("setup db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	cdb.Close()

	srv, err := New(Config{DBPath: dbPath, DataDir: dataDir})
	if err != nil {
		t.Fatalf("New with both modes: %v", err)
	}
	defer srv.Close()

	if srv.claimsDB == nil {
		t.Error("expected claimsDB in dual-mode")
	}
	if srv.pool == nil {
		t.Error("expected pool in dual-mode")
	}
}

// ---------------------------------------------------------------------------
// Path traversal validation tests
// ---------------------------------------------------------------------------

func TestValidateRepoName(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		wantErr bool
	}{
		{"valid simple name", "kubernetes", false},
		{"valid with hyphen", "my-repo", false},
		{"valid with dots", "repo.v2", false},
		{"empty name", "", true},
		{"path traversal dotdot", "../etc/passwd", true},
		{"path traversal dotdot only", "..", true},
		{"path traversal embedded", "foo/../bar", true},
		{"forward slash", "foo/bar", true},
		{"absolute path", "/etc/passwd", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRepoName(tt.repo)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRepoName(%q) error = %v, wantErr %v", tt.repo, err, tt.wantErr)
			}
		})
	}
}

func TestDBPool_Open_PathTraversal(t *testing.T) {
	dataDir := setupMultiRepoDB(t, "safe-repo")
	pool := NewDBPool(dataDir, DefaultMaxOpenDBs)
	defer pool.Close()

	// Attempt path traversal via Open.
	_, err := pool.Open("../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal repo name")
	}
	if !hasSubstr(err.Error(), "path traversal") {
		t.Errorf("expected path traversal error, got: %v", err)
	}

	// Valid repo should still work.
	_, err = pool.Open("safe-repo")
	if err != nil {
		t.Errorf("expected valid repo to open, got: %v", err)
	}
}
