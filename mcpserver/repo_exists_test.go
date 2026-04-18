package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/sjarmak/livedocs/db"
)

// TestListPackages_NonExistentRepo verifies that list_packages returns a clear
// error when called with a repo name that has no claims database.
func TestListPackages_NonExistentRepo(t *testing.T) {
	dir := t.TempDir()
	// Create a DB for "real-repo" but NOT for "fake-repo".
	createTestDBWithSymbols(t, dir, "real-repo", []db.Symbol{
		{Repo: "real-repo", ImportPath: "pkg/a", SymbolName: "Foo", Language: "go", Kind: "function", Visibility: "public"},
	})

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	handler := ListPackagesHandler(pool, nil)
	req := &testToolRequest{args: map[string]any{
		"repo": "fake-repo",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result for non-existent repo, got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "not found") {
		t.Errorf("error should mention 'not found', got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "fake-repo") {
		t.Errorf("error should mention the repo name, got: %s", result.Text())
	}
}

// TestDescribePackage_NonExistentRepo verifies that describe_package returns a
// clear error when called with a repo name that has no claims database.
func TestDescribePackage_NonExistentRepo(t *testing.T) {
	dir := t.TempDir()
	createTestDBWithSymbols(t, dir, "real-repo", []db.Symbol{
		{Repo: "real-repo", ImportPath: "pkg/a", SymbolName: "Foo", Language: "go", Kind: "function", Visibility: "public"},
	})

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	handler := DescribePackageHandler(pool, nil)
	req := &testToolRequest{args: map[string]any{
		"repo":        "nonexistent",
		"import_path": "pkg/a",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result for non-existent repo, got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "not found") {
		t.Errorf("error should mention 'not found', got: %s", result.Text())
	}
}

// TestSearchSymbols_NonExistentRepoFilter verifies that search_symbols returns
// a clear error when the optional repo filter names a non-existent repo.
func TestSearchSymbols_NonExistentRepoFilter(t *testing.T) {
	pool, index := setupSearchTestEnv(t)

	handler := SearchSymbolsHandler(pool, index)
	req := &testToolRequest{args: map[string]any{
		"query": "New%",
		"repo":  "does-not-exist",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result for non-existent repo filter, got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "not found") {
		t.Errorf("error should mention 'not found', got: %s", result.Text())
	}
}

// TestTribalProposeFact_NonExistentRepo verifies that tribal_propose_fact
// returns a clear error when the repo has no claims database.
func TestTribalProposeFact_NonExistentRepo(t *testing.T) {
	dir := t.TempDir()
	createTestDBWithSymbols(t, dir, "real-repo", []db.Symbol{
		{Repo: "real-repo", ImportPath: "pkg/a", SymbolName: "Foo", Language: "go", Kind: "function", Visibility: "public"},
	})

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	handler := tribalProposeFactHandler(pool)
	req := &testToolRequest{args: map[string]any{
		"symbol":       "Foo",
		"repo":         "ghost-repo",
		"kind":         "rationale",
		"body":         "test body",
		"source_quote": "test quote",
		"evidence":     `[{"source_type":"commit","source_ref":"abc123","content_hash":"def456"}]`,
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result for non-existent repo, got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "not found") {
		t.Errorf("error should mention 'not found', got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "ghost-repo") {
		t.Errorf("error should mention the repo name, got: %s", result.Text())
	}
}

// TestRequireRepoExists_ExistingRepo verifies that requireRepoExists returns
// nil for an existing repo, allowing the handler to proceed.
func TestRequireRepoExists_ExistingRepo(t *testing.T) {
	dir := createTestDBDir(t, []string{"my-repo"})
	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	result, err := requireRepoExists(pool, "my-repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for existing repo, got: %s", result.Text())
	}
}

// TestRequireRepoExists_PathTraversal verifies that requireRepoExists rejects
// repo names with path traversal sequences.
func TestRequireRepoExists_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	result, err := requireRepoExists(pool, "../etc/passwd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected error result for path traversal")
	}
	if !result.IsError() {
		t.Errorf("expected error result, got: %s", result.Text())
	}
}
