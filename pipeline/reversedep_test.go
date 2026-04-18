package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
	"github.com/sjarmak/livedocs/gitdiff"
)

func TestReverseDepPaths_BasicImport(t *testing.T) {
	// Scenario: file A defines symbols, file B imports from A's import path.
	// When A changes, B should appear in the reverse-dep set.
	_, claimsDB := openTestDBs(t)

	// File A: defines symbol "Foo" with import_path "pkg/a"
	symA, err := claimsDB.UpsertSymbol(db.Symbol{
		Repo:       "test/repo",
		ImportPath: "pkg/a",
		SymbolName: "Foo",
		Language:   "go",
		Kind:       "func",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol A: %v", err)
	}
	_, err = claimsDB.InsertClaim(db.Claim{
		SubjectID:        symA,
		Predicate:        "defines",
		SourceFile:       "pkg/a/a.go",
		SourceLine:       10,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "test-ext",
		ExtractorVersion: "0.1.0",
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert defines claim: %v", err)
	}

	// File B: imports from "pkg/a" (symbol_name = "pkg/a" with predicate "imports")
	symImport, err := claimsDB.UpsertSymbol(db.Symbol{
		Repo:       "test/repo",
		ImportPath: "pkg/b/b.go",
		SymbolName: "pkg/a",
		Language:   "go",
		Kind:       "module",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert import symbol: %v", err)
	}
	_, err = claimsDB.InsertClaim(db.Claim{
		SubjectID:        symImport,
		Predicate:        "imports",
		SourceFile:       "pkg/b/b.go",
		SourceLine:       3,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "test-ext",
		ExtractorVersion: "0.1.0",
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert imports claim: %v", err)
	}

	// When A changes, B should be in the reverse-dep set.
	revDeps, err := reverseDepPaths(claimsDB, []string{"pkg/a/a.go"})
	if err != nil {
		t.Fatalf("reverseDepPaths: %v", err)
	}

	if len(revDeps) != 1 {
		t.Fatalf("expected 1 reverse dep, got %d: %v", len(revDeps), revDeps)
	}
	if revDeps[0] != "pkg/b/b.go" {
		t.Errorf("expected reverse dep %q, got %q", "pkg/b/b.go", revDeps[0])
	}
}

func TestReverseDepPaths_ExcludesAlreadyChanged(t *testing.T) {
	// If the reverse dep is already in changedPaths, it should not appear.
	_, claimsDB := openTestDBs(t)

	symA, _ := claimsDB.UpsertSymbol(db.Symbol{
		Repo: "r", ImportPath: "pkg/a", SymbolName: "Foo",
		Language: "go", Kind: "func", Visibility: "public",
	})
	claimsDB.InsertClaim(db.Claim{
		SubjectID: symA, Predicate: "defines", SourceFile: "pkg/a/a.go",
		Confidence: 1.0, ClaimTier: "structural", Extractor: "e", ExtractorVersion: "1", LastVerified: db.Now(),
	})

	symImp, _ := claimsDB.UpsertSymbol(db.Symbol{
		Repo: "r", ImportPath: "pkg/a/a.go", SymbolName: "pkg/a",
		Language: "go", Kind: "module", Visibility: "public",
	})
	claimsDB.InsertClaim(db.Claim{
		SubjectID: symImp, Predicate: "imports", SourceFile: "pkg/a/a.go",
		Confidence: 1.0, ClaimTier: "structural", Extractor: "e", ExtractorVersion: "1", LastVerified: db.Now(),
	})

	// Both files are already in changedPaths — should return nothing.
	revDeps, err := reverseDepPaths(claimsDB, []string{"pkg/a/a.go"})
	if err != nil {
		t.Fatalf("reverseDepPaths: %v", err)
	}
	if len(revDeps) != 0 {
		t.Errorf("expected 0 reverse deps (already changed), got %d: %v", len(revDeps), revDeps)
	}
}

func TestReverseDepPaths_EmptyInput(t *testing.T) {
	_, claimsDB := openTestDBs(t)

	revDeps, err := reverseDepPaths(claimsDB, nil)
	if err != nil {
		t.Fatalf("reverseDepPaths: %v", err)
	}
	if len(revDeps) != 0 {
		t.Errorf("expected 0 reverse deps for empty input, got %d", len(revDeps))
	}

	revDeps, err = reverseDepPaths(claimsDB, []string{})
	if err != nil {
		t.Fatalf("reverseDepPaths: %v", err)
	}
	if len(revDeps) != 0 {
		t.Errorf("expected 0 reverse deps for empty slice, got %d", len(revDeps))
	}
}

func TestReverseDepPaths_NoMatchingImports(t *testing.T) {
	// Changed file has defines claims, but nobody imports from it.
	_, claimsDB := openTestDBs(t)

	symA, _ := claimsDB.UpsertSymbol(db.Symbol{
		Repo: "r", ImportPath: "pkg/lonely", SymbolName: "Lonely",
		Language: "go", Kind: "func", Visibility: "public",
	})
	claimsDB.InsertClaim(db.Claim{
		SubjectID: symA, Predicate: "defines", SourceFile: "pkg/lonely/lonely.go",
		Confidence: 1.0, ClaimTier: "structural", Extractor: "e", ExtractorVersion: "1", LastVerified: db.Now(),
	})

	revDeps, err := reverseDepPaths(claimsDB, []string{"pkg/lonely/lonely.go"})
	if err != nil {
		t.Fatalf("reverseDepPaths: %v", err)
	}
	if len(revDeps) != 0 {
		t.Errorf("expected 0 reverse deps, got %d: %v", len(revDeps), revDeps)
	}
}

func TestReverseDepPaths_MultipleDeps(t *testing.T) {
	// Multiple files import from the same package.
	_, claimsDB := openTestDBs(t)

	symA, _ := claimsDB.UpsertSymbol(db.Symbol{
		Repo: "r", ImportPath: "pkg/core", SymbolName: "Core",
		Language: "go", Kind: "type", Visibility: "public",
	})
	claimsDB.InsertClaim(db.Claim{
		SubjectID: symA, Predicate: "defines", SourceFile: "pkg/core/core.go",
		Confidence: 1.0, ClaimTier: "structural", Extractor: "e", ExtractorVersion: "1", LastVerified: db.Now(),
	})

	// File B imports pkg/core
	symB, _ := claimsDB.UpsertSymbol(db.Symbol{
		Repo: "r", ImportPath: "pkg/b/b.go", SymbolName: "pkg/core",
		Language: "go", Kind: "module", Visibility: "public",
	})
	claimsDB.InsertClaim(db.Claim{
		SubjectID: symB, Predicate: "imports", SourceFile: "pkg/b/b.go",
		Confidence: 1.0, ClaimTier: "structural", Extractor: "e", ExtractorVersion: "1", LastVerified: db.Now(),
	})

	// File C also imports pkg/core
	symC, _ := claimsDB.UpsertSymbol(db.Symbol{
		Repo: "r", ImportPath: "pkg/c/c.go", SymbolName: "pkg/core",
		Language: "go", Kind: "module", Visibility: "public",
	})
	claimsDB.InsertClaim(db.Claim{
		SubjectID: symC, Predicate: "imports", SourceFile: "pkg/c/c.go",
		Confidence: 1.0, ClaimTier: "structural", Extractor: "e", ExtractorVersion: "1", LastVerified: db.Now(),
	})

	revDeps, err := reverseDepPaths(claimsDB, []string{"pkg/core/core.go"})
	if err != nil {
		t.Fatalf("reverseDepPaths: %v", err)
	}

	if len(revDeps) != 2 {
		t.Fatalf("expected 2 reverse deps, got %d: %v", len(revDeps), revDeps)
	}

	depSet := make(map[string]bool)
	for _, d := range revDeps {
		depSet[d] = true
	}
	if !depSet["pkg/b/b.go"] {
		t.Error("missing reverse dep pkg/b/b.go")
	}
	if !depSet["pkg/c/c.go"] {
		t.Error("missing reverse dep pkg/c/c.go")
	}
}

func TestRun_ReverseDepReextraction(t *testing.T) {
	// Integration test: Pipeline.Run should include reverse-dep files
	// in the extraction set when a file they import from changes.
	cacheStore, claimsDB := openTestDBs(t)

	// Pre-populate DB: file "lib.go" defines symbol with import_path "lib",
	// file "consumer.go" imports from "lib".
	symLib, err := claimsDB.UpsertSymbol(db.Symbol{
		Repo: "test/repo", ImportPath: "lib", SymbolName: "Helper",
		Language: "go", Kind: "func", Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert lib symbol: %v", err)
	}
	_, err = claimsDB.InsertClaim(db.Claim{
		SubjectID: symLib, Predicate: "defines", SourceFile: "lib.go",
		SourceLine: 5, Confidence: 1.0, ClaimTier: "structural",
		Extractor: "test-ext", ExtractorVersion: "0.1.0", LastVerified: db.Now(),
	})
	if err != nil {
		t.Fatalf("insert defines claim: %v", err)
	}

	symImp, err := claimsDB.UpsertSymbol(db.Symbol{
		Repo: "test/repo", ImportPath: "consumer.go", SymbolName: "lib",
		Language: "go", Kind: "module", Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert import symbol: %v", err)
	}
	_, err = claimsDB.InsertClaim(db.Claim{
		SubjectID: symImp, Predicate: "imports", SourceFile: "consumer.go",
		SourceLine: 3, Confidence: 1.0, ClaimTier: "structural",
		Extractor: "test-ext", ExtractorVersion: "0.1.0", LastVerified: db.Now(),
	})
	if err != nil {
		t.Fatalf("insert imports claim: %v", err)
	}

	// Use mockFileSource: only lib.go is in the diff, but consumer.go
	// should be pulled in as a reverse dep.
	stub := &stubExtractor{
		name:    "test-ext",
		version: "0.1.0",
		claims: map[string][]extractor.Claim{
			"lib.go": {
				{
					SubjectRepo: "test/repo", SubjectImportPath: "lib",
					SubjectName: "Helper", Language: "go",
					Kind: extractor.KindFunc, Visibility: extractor.VisibilityPublic,
					Predicate: extractor.PredicateDefines, SourceFile: "lib.go",
					SourceLine: 5, Confidence: 1.0, ClaimTier: extractor.TierStructural,
					Extractor: "test-ext", ExtractorVersion: "0.1.0",
					LastVerified: time.Now(),
				},
			},
			"consumer.go": {
				{
					SubjectRepo: "test/repo", SubjectImportPath: "consumer.go",
					SubjectName: "lib", Language: "go",
					Kind: extractor.KindModule, Visibility: extractor.VisibilityPublic,
					Predicate: extractor.PredicateImports, SourceFile: "consumer.go",
					SourceLine: 3, Confidence: 1.0, ClaimTier: extractor.TierStructural,
					Extractor: "test-ext", ExtractorVersion: "0.1.0",
					LastVerified: time.Now(),
				},
			},
		},
	}
	reg := extractor.NewRegistry()
	reg.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: stub,
	})

	fs := &mockFileSource{
		files: map[string][]byte{
			"lib.go":      []byte("package lib // v2"),
			"consumer.go": []byte("package consumer"),
		},
		diff: []gitdiff.FileChange{
			{Status: gitdiff.StatusModified, Path: "lib.go"},
		},
	}

	p := New(Config{
		Repo:       "test/repo",
		RepoDir:    "/nonexistent",
		Cache:      cacheStore,
		ClaimsDB:   claimsDB,
		Registry:   reg,
		FileSource: fs,
	})

	result, err := p.Run(context.Background(), "aaa", "bbb")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// lib.go (from diff) + consumer.go (reverse dep) = 2 changed
	if result.FilesChanged != 2 {
		t.Errorf("FilesChanged: got %d, want 2", result.FilesChanged)
	}
	if result.ReverseDepFiles != 1 {
		t.Errorf("ReverseDepFiles: got %d, want 1", result.ReverseDepFiles)
	}
	if result.FilesExtracted != 2 {
		t.Errorf("FilesExtracted: got %d, want 2", result.FilesExtracted)
	}

	// Verify consumer.go appears in ChangedPaths.
	pathSet := make(map[string]bool, len(result.ChangedPaths))
	for _, p := range result.ChangedPaths {
		pathSet[p] = true
	}
	if !pathSet["consumer.go"] {
		t.Errorf("ChangedPaths missing consumer.go; got %v", result.ChangedPaths)
	}
	if !pathSet["lib.go"] {
		t.Errorf("ChangedPaths missing lib.go; got %v", result.ChangedPaths)
	}
}
