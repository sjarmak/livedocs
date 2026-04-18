package mcpserver

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/sjarmak/livedocs/db"
)

// createTestDBWithSymbols creates a claims DB at the given path with the
// specified symbols inserted.
func createTestDBWithSymbols(t *testing.T, dir, repoName string, symbols []db.Symbol) {
	t.Helper()
	dbPath := filepath.Join(dir, repoName+claimsDBSuffix)
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open claims db %s: %v", dbPath, err)
	}
	defer cdb.Close()

	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema for %s: %v", repoName, err)
	}

	for _, sym := range symbols {
		if _, err := cdb.UpsertSymbol(sym); err != nil {
			t.Fatalf("upsert symbol %s in %s: %v", sym.SymbolName, repoName, err)
		}
	}
}

func TestRoutingIndex_Build(t *testing.T) {
	dir := t.TempDir()

	// Create two repos with different symbols.
	createTestDBWithSymbols(t, dir, "repo-a", []db.Symbol{
		{Repo: "repo-a", ImportPath: "pkg/server", SymbolName: "NewServer", Language: "go", Kind: "function", Visibility: "public"},
		{Repo: "repo-a", ImportPath: "pkg/server", SymbolName: "Handler", Language: "go", Kind: "type", Visibility: "public"},
	})
	createTestDBWithSymbols(t, dir, "repo-b", []db.Symbol{
		{Repo: "repo-b", ImportPath: "pkg/client", SymbolName: "NewClient", Language: "go", Kind: "function", Visibility: "public"},
		{Repo: "repo-b", ImportPath: "pkg/client", SymbolName: "NewServer", Language: "go", Kind: "function", Visibility: "public"},
	})

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	index := NewRoutingIndex()
	if err := index.Build(pool); err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if got := index.RepoCount(); got != 2 {
		t.Errorf("RepoCount() = %d, want 2", got)
	}
}

func TestRoutingIndex_Lookup_ExactPrefix(t *testing.T) {
	dir := t.TempDir()

	createTestDBWithSymbols(t, dir, "repo-a", []db.Symbol{
		{Repo: "repo-a", ImportPath: "pkg/a", SymbolName: "NewServer", Language: "go", Kind: "function", Visibility: "public"},
	})
	createTestDBWithSymbols(t, dir, "repo-b", []db.Symbol{
		{Repo: "repo-b", ImportPath: "pkg/b", SymbolName: "NewClient", Language: "go", Kind: "function", Visibility: "public"},
	})
	createTestDBWithSymbols(t, dir, "repo-c", []db.Symbol{
		{Repo: "repo-c", ImportPath: "pkg/c", SymbolName: "FooBar", Language: "go", Kind: "function", Visibility: "public"},
	})

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	index := NewRoutingIndex()
	if err := index.Build(pool); err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// "NewServer" has prefix "new" -> should match repo-a and repo-b (both have "New*" symbols).
	got := index.Lookup("NewServer")
	sort.Strings(got)
	if len(got) != 2 {
		t.Fatalf("Lookup('NewServer') returned %d repos, want 2: %v", len(got), got)
	}
	if got[0] != "repo-a" || got[1] != "repo-b" {
		t.Errorf("Lookup('NewServer') = %v, want [repo-a, repo-b]", got)
	}

	// "FooBar" has prefix "foo" -> should only match repo-c.
	got = index.Lookup("FooBar")
	if len(got) != 1 || got[0] != "repo-c" {
		t.Errorf("Lookup('FooBar') = %v, want [repo-c]", got)
	}

	// "ZZZ" has no matches.
	got = index.Lookup("ZZZNotExist")
	if len(got) != 0 {
		t.Errorf("Lookup('ZZZNotExist') = %v, want empty", got)
	}
}

func TestRoutingIndex_Lookup_ShortQuery(t *testing.T) {
	dir := t.TempDir()

	createTestDBWithSymbols(t, dir, "repo-a", []db.Symbol{
		{Repo: "repo-a", ImportPath: "pkg/a", SymbolName: "NewServer", Language: "go", Kind: "function", Visibility: "public"},
	})

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	index := NewRoutingIndex()
	if err := index.Build(pool); err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Query shorter than 3 chars should return all repos.
	got := index.Lookup("Ne")
	if len(got) != 1 || got[0] != "repo-a" {
		t.Errorf("Lookup('Ne') = %v, want [repo-a] (all repos fallback)", got)
	}
}

func TestRoutingIndex_Lookup_Wildcard(t *testing.T) {
	dir := t.TempDir()

	createTestDBWithSymbols(t, dir, "repo-a", []db.Symbol{
		{Repo: "repo-a", ImportPath: "pkg/a", SymbolName: "NewServer", Language: "go", Kind: "function", Visibility: "public"},
	})
	createTestDBWithSymbols(t, dir, "repo-b", []db.Symbol{
		{Repo: "repo-b", ImportPath: "pkg/b", SymbolName: "FooBar", Language: "go", Kind: "function", Visibility: "public"},
	})

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	index := NewRoutingIndex()
	if err := index.Build(pool); err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Wildcard query should return all repos.
	got := index.Lookup("%Server%")
	sort.Strings(got)
	if len(got) != 2 {
		t.Errorf("Lookup('%%Server%%') returned %d repos, want 2 (all repos fallback)", len(got))
	}
}

func TestRoutingIndex_Build_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	index := NewRoutingIndex()
	if err := index.Build(pool); err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if got := index.RepoCount(); got != 0 {
		t.Errorf("RepoCount() = %d, want 0", got)
	}

	got := index.Lookup("NewServer")
	if len(got) != 0 {
		t.Errorf("Lookup on empty index returned %v, want empty", got)
	}
}

func TestRoutingIndex_Build_SkipsBrokenDB(t *testing.T) {
	dir := t.TempDir()

	// Create a valid repo.
	createTestDBWithSymbols(t, dir, "repo-good", []db.Symbol{
		{Repo: "repo-good", ImportPath: "pkg/a", SymbolName: "NewServer", Language: "go", Kind: "function", Visibility: "public"},
	})

	// Create a broken DB file (just garbage bytes).
	brokenPath := filepath.Join(dir, "repo-broken"+claimsDBSuffix)
	if err := os.WriteFile(brokenPath, []byte("not a sqlite db"), 0o644); err != nil {
		t.Fatalf("write broken db: %v", err)
	}

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	index := NewRoutingIndex()
	// Build should not fail — it skips broken repos.
	if err := index.Build(pool); err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Should still have both repos in allRepos (from manifest), but only
	// repo-good contributes prefixes.
	if got := index.RepoCount(); got != 2 {
		t.Errorf("RepoCount() = %d, want 2 (manifest includes both)", got)
	}

	got := index.Lookup("NewServer")
	if len(got) != 1 || got[0] != "repo-good" {
		t.Errorf("Lookup('NewServer') = %v, want [repo-good]", got)
	}
}
