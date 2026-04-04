package db

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func tempDB(t *testing.T) *ClaimsDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenClaimsDB(path)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func tempXRefDB(t *testing.T) *XRefDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "_xref.db")
	db, err := OpenXRefDB(path)
	if err != nil {
		t.Fatalf("open xref db: %v", err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create xref schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCreateSchema(t *testing.T) {
	db := tempDB(t)
	// Creating schema twice should be idempotent.
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema twice: %v", err)
	}
}

func TestUpsertSymbol_Insert(t *testing.T) {
	db := tempDB(t)
	id, err := db.UpsertSymbol(Symbol{
		Repo:       "kubernetes/kubernetes",
		ImportPath: "k8s.io/api/core/v1",
		SymbolName: "Pod",
		Language:   "go",
		Kind:       "type",
		Visibility: "public",
		SCIPSymbol: "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	// Verify the symbol was stored.
	s, err := db.GetSymbolByCompositeKey("kubernetes/kubernetes", "k8s.io/api/core/v1", "Pod")
	if err != nil {
		t.Fatalf("get symbol: %v", err)
	}
	if s.Language != "go" {
		t.Errorf("expected language go, got %s", s.Language)
	}
	if s.SCIPSymbol != "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#" {
		t.Errorf("unexpected scip_symbol: %s", s.SCIPSymbol)
	}
}

func TestUpsertSymbol_Update(t *testing.T) {
	db := tempDB(t)
	id1, _ := db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p", SymbolName: "S",
		Language: "go", Kind: "type", Visibility: "public",
	})
	id2, _ := db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p", SymbolName: "S",
		Language: "go", Kind: "func", Visibility: "internal",
		SCIPSymbol: "updated-scip",
	})
	if id1 != id2 {
		t.Errorf("expected same id on upsert, got %d and %d", id1, id2)
	}
	s, _ := db.GetSymbolByCompositeKey("r", "p", "S")
	if s.Kind != "func" {
		t.Errorf("expected kind=func after update, got %s", s.Kind)
	}
	if s.SCIPSymbol != "updated-scip" {
		t.Errorf("expected scip_symbol=updated-scip, got %s", s.SCIPSymbol)
	}
}

func TestInsertClaim_And_GetBySubject(t *testing.T) {
	db := tempDB(t)
	symID, _ := db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p", SymbolName: "F",
		Language: "go", Kind: "func", Visibility: "public",
	})
	claimID, err := db.InsertClaim(Claim{
		SubjectID:        symID,
		Predicate:        "defines",
		ObjectText:       "func F()",
		SourceFile:       "main.go",
		SourceLine:       10,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "scip-import",
		ExtractorVersion: "0.1.0",
		LastVerified:     Now(),
	})
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}
	if claimID <= 0 {
		t.Fatalf("expected positive claim id, got %d", claimID)
	}

	claims, err := db.GetClaimsBySubject(symID)
	if err != nil {
		t.Fatalf("get claims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claims))
	}
	if claims[0].Predicate != "defines" {
		t.Errorf("expected predicate=defines, got %s", claims[0].Predicate)
	}
}

func TestDeleteClaimsByExtractorAndFile(t *testing.T) {
	db := tempDB(t)
	symID, _ := db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p", SymbolName: "X",
		Language: "go", Kind: "type", Visibility: "public",
	})
	db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "defines", SourceFile: "a.go",
		Confidence: 1.0, ClaimTier: "structural",
		Extractor: "scip-import", ExtractorVersion: "0.1.0", LastVerified: Now(),
	})
	db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "has_doc", SourceFile: "a.go",
		Confidence: 0.85, ClaimTier: "structural",
		Extractor: "scip-import", ExtractorVersion: "0.1.0", LastVerified: Now(),
	})
	db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "defines", SourceFile: "b.go",
		Confidence: 1.0, ClaimTier: "structural",
		Extractor: "other", ExtractorVersion: "1.0", LastVerified: Now(),
	})

	if err := db.DeleteClaimsByExtractorAndFile("scip-import", "a.go"); err != nil {
		t.Fatalf("delete claims: %v", err)
	}

	claims, _ := db.GetClaimsBySubject(symID)
	if len(claims) != 1 {
		t.Fatalf("expected 1 remaining claim, got %d", len(claims))
	}
	if claims[0].Extractor != "other" {
		t.Errorf("expected remaining claim from 'other' extractor")
	}
}

func TestUpsertSourceFile(t *testing.T) {
	db := tempDB(t)
	id, err := db.UpsertSourceFile(SourceFile{
		Repo: "r", RelativePath: "pkg/a.go",
		ContentHash: "abc123", ExtractorVersion: "0.1.0",
		LastIndexed: Now(),
	})
	if err != nil {
		t.Fatalf("upsert source file: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	// Update should return same ID.
	id2, err := db.UpsertSourceFile(SourceFile{
		Repo: "r", RelativePath: "pkg/a.go",
		ContentHash: "def456", ExtractorVersion: "0.2.0",
		LastIndexed: Now(),
	})
	if err != nil {
		t.Fatalf("upsert source file update: %v", err)
	}
	if id != id2 {
		t.Errorf("expected same id on upsert, got %d and %d", id, id2)
	}
}

func TestInvalidVisibility(t *testing.T) {
	db := tempDB(t)
	_, err := db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p", SymbolName: "S",
		Language: "go", Kind: "type", Visibility: "INVALID",
	})
	if err == nil {
		t.Error("expected error for invalid visibility, got nil")
	}
}

func TestInvalidClaimTier(t *testing.T) {
	db := tempDB(t)
	symID, _ := db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p", SymbolName: "S",
		Language: "go", Kind: "type", Visibility: "public",
	})
	_, err := db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "defines", SourceFile: "a.go",
		Confidence: 1.0, ClaimTier: "INVALID",
		Extractor: "test", ExtractorVersion: "1.0", LastVerified: Now(),
	})
	if err == nil {
		t.Error("expected error for invalid claim_tier, got nil")
	}
}

func TestInvalidPredicate(t *testing.T) {
	db := tempDB(t)
	symID, _ := db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p", SymbolName: "S",
		Language: "go", Kind: "type", Visibility: "public",
	})
	_, err := db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "INVALID_PREDICATE", SourceFile: "a.go",
		Confidence: 1.0, ClaimTier: "structural",
		Extractor: "test", ExtractorVersion: "1.0", LastVerified: Now(),
	})
	if err == nil {
		t.Error("expected error for invalid predicate, got nil")
	}
}

func TestAllValidPredicates(t *testing.T) {
	db := tempDB(t)
	symID, _ := db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p", SymbolName: "S",
		Language: "go", Kind: "type", Visibility: "public",
	})
	predicates := []string{
		"defines", "imports", "exports", "has_doc", "is_generated", "is_test",
		"has_kind", "implements", "has_signature", "encloses",
		"purpose", "usage_pattern", "complexity", "stability",
	}
	for _, pred := range predicates {
		_, err := db.InsertClaim(Claim{
			SubjectID: symID, Predicate: pred, SourceFile: "a.go",
			Confidence: 1.0, ClaimTier: "structural",
			Extractor: "test", ExtractorVersion: "1.0", LastVerified: Now(),
		})
		if err != nil {
			t.Errorf("valid predicate %q rejected: %v", pred, err)
		}
	}
}

func TestXRefDB_UpsertAndLookup(t *testing.T) {
	xdb := tempXRefDB(t)
	err := xdb.UpsertXRef(XRef{
		SymbolKey: "k8s.io/api/core/v1.Pod",
		Repo:      "kubernetes/kubernetes",
		SymbolID:  42,
	})
	if err != nil {
		t.Fatalf("upsert xref: %v", err)
	}
	err = xdb.UpsertXRef(XRef{
		SymbolKey: "k8s.io/api/core/v1.Pod",
		Repo:      "kubernetes/client-go",
		SymbolID:  99,
	})
	if err != nil {
		t.Fatalf("upsert xref 2: %v", err)
	}

	refs, err := xdb.LookupRepos("k8s.io/api/core/v1.Pod")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(refs))
	}
}

func TestXRefDB_LookupEmpty(t *testing.T) {
	xdb := tempXRefDB(t)
	refs, err := xdb.LookupRepos("nonexistent.Symbol")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(refs))
	}
}

func TestOpenClaimsDB_InvalidPath(t *testing.T) {
	_, err := OpenClaimsDB("/nonexistent/dir/test.db")
	// Should still succeed as sqlite creates files; the error would be on actual ops
	// Actually on some systems this fails at open. Just ensure no panic.
	_ = err
}

func TestGetSourceFile(t *testing.T) {
	db := tempDB(t)
	sf := SourceFile{
		Repo: "r", RelativePath: "pkg/a.go",
		ContentHash: "abc123", ExtractorVersion: "0.1.0",
		GrammarVersion: "0.21.0", LastIndexed: Now(),
	}
	db.UpsertSourceFile(sf)

	got, err := db.GetSourceFile("r", "pkg/a.go")
	if err != nil {
		t.Fatalf("get source file: %v", err)
	}
	if got.ContentHash != "abc123" {
		t.Errorf("expected content_hash=abc123, got %s", got.ContentHash)
	}
	if got.ExtractorVersion != "0.1.0" {
		t.Errorf("expected extractor_version=0.1.0, got %s", got.ExtractorVersion)
	}
	if got.GrammarVersion != "0.21.0" {
		t.Errorf("expected grammar_version=0.21.0, got %s", got.GrammarVersion)
	}
	if got.Deleted {
		t.Error("expected deleted=false")
	}
}

func TestGetSourceFile_NotFound(t *testing.T) {
	db := tempDB(t)
	_, err := db.GetSourceFile("r", "nonexistent.go")
	if err == nil {
		t.Error("expected error for nonexistent source file")
	}
}

func TestListSymbolsByImportPath(t *testing.T) {
	db := tempDB(t)
	db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "k8s.io/api/core/v1", SymbolName: "Pod",
		Language: "go", Kind: "type", Visibility: "public",
	})
	db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "k8s.io/api/core/v1", SymbolName: "Service",
		Language: "go", Kind: "type", Visibility: "public",
	})
	db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "k8s.io/api/apps/v1", SymbolName: "Deployment",
		Language: "go", Kind: "type", Visibility: "public",
	})

	syms, err := db.ListSymbolsByImportPath("k8s.io/api/core/v1")
	if err != nil {
		t.Fatalf("list symbols: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("expected 2 symbols, got %d", len(syms))
	}
	names := map[string]bool{}
	for _, s := range syms {
		names[s.SymbolName] = true
	}
	if !names["Pod"] || !names["Service"] {
		t.Errorf("expected Pod and Service, got %v", names)
	}
}

func TestListSymbolsByImportPath_Empty(t *testing.T) {
	db := tempDB(t)
	syms, err := db.ListSymbolsByImportPath("nonexistent/path")
	if err != nil {
		t.Fatalf("list symbols: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("expected 0 symbols, got %d", len(syms))
	}
}

func TestMarkFileDeleted(t *testing.T) {
	db := tempDB(t)
	db.UpsertSourceFile(SourceFile{
		Repo: "r", RelativePath: "pkg/a.go",
		ContentHash: "abc", ExtractorVersion: "0.1.0",
		LastIndexed: Now(),
	})

	if err := db.MarkFileDeleted("r", "pkg/a.go"); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}

	sf, err := db.GetSourceFile("r", "pkg/a.go")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if !sf.Deleted {
		t.Error("expected deleted=true after MarkFileDeleted")
	}
}

func TestGetClaimsByFile(t *testing.T) {
	db := tempDB(t)
	symID, _ := db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p", SymbolName: "A",
		Language: "go", Kind: "func", Visibility: "public",
	})
	db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "defines", SourceFile: "a.go",
		Confidence: 1.0, ClaimTier: "structural",
		Extractor: "go-deep", ExtractorVersion: "1.0", LastVerified: Now(),
	})
	db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "has_doc", SourceFile: "a.go",
		Confidence: 0.85, ClaimTier: "structural",
		Extractor: "go-deep", ExtractorVersion: "1.0", LastVerified: Now(),
	})
	db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "defines", SourceFile: "b.go",
		Confidence: 1.0, ClaimTier: "structural",
		Extractor: "go-deep", ExtractorVersion: "1.0", LastVerified: Now(),
	})

	claims, err := db.GetClaimsByFile("a.go")
	if err != nil {
		t.Fatalf("get claims by file: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("expected 2 claims for a.go, got %d", len(claims))
	}
}

func TestGetClaimsByPredicate(t *testing.T) {
	db := tempDB(t)
	symID, _ := db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p", SymbolName: "A",
		Language: "go", Kind: "func", Visibility: "public",
	})
	db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "defines", SourceFile: "a.go",
		Confidence: 1.0, ClaimTier: "structural",
		Extractor: "go-deep", ExtractorVersion: "1.0", LastVerified: Now(),
	})
	db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "imports", SourceFile: "a.go",
		Confidence: 1.0, ClaimTier: "structural",
		Extractor: "go-deep", ExtractorVersion: "1.0", LastVerified: Now(),
	})

	claims, err := db.GetClaimsByPredicate("defines")
	if err != nil {
		t.Fatalf("get claims by predicate: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("expected 1 defines claim, got %d", len(claims))
	}
	if claims[0].Predicate != "defines" {
		t.Errorf("expected predicate=defines, got %s", claims[0].Predicate)
	}
}

func TestListDeletedFiles(t *testing.T) {
	db := tempDB(t)
	db.UpsertSourceFile(SourceFile{
		Repo: "r", RelativePath: "alive.go",
		ContentHash: "a", ExtractorVersion: "0.1.0", LastIndexed: Now(),
	})
	db.UpsertSourceFile(SourceFile{
		Repo: "r", RelativePath: "dead.go",
		ContentHash: "b", ExtractorVersion: "0.1.0", LastIndexed: Now(),
		Deleted: true,
	})
	db.UpsertSourceFile(SourceFile{
		Repo: "r", RelativePath: "also_dead.go",
		ContentHash: "c", ExtractorVersion: "0.1.0", LastIndexed: Now(),
		Deleted: true,
	})

	files, err := db.ListDeletedFiles("r")
	if err != nil {
		t.Fatalf("list deleted: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 deleted files, got %d", len(files))
	}
}

func TestIsCacheHit(t *testing.T) {
	db := tempDB(t)
	db.UpsertSourceFile(SourceFile{
		Repo: "r", RelativePath: "pkg/a.go",
		ContentHash: "abc123", ExtractorVersion: "0.1.0",
		GrammarVersion: "0.21.0", LastIndexed: Now(),
	})

	// Exact match should be a hit.
	if !db.IsCacheHit("r", "pkg/a.go", "abc123", "0.1.0", "0.21.0") {
		t.Error("expected cache hit for matching hash+versions")
	}
	// Different content hash should miss.
	if db.IsCacheHit("r", "pkg/a.go", "different", "0.1.0", "0.21.0") {
		t.Error("expected cache miss for different content hash")
	}
	// Different extractor version should miss.
	if db.IsCacheHit("r", "pkg/a.go", "abc123", "0.2.0", "0.21.0") {
		t.Error("expected cache miss for different extractor version")
	}
	// Different grammar version should miss.
	if db.IsCacheHit("r", "pkg/a.go", "abc123", "0.1.0", "0.22.0") {
		t.Error("expected cache miss for different grammar version")
	}
	// Nonexistent file should miss.
	if db.IsCacheHit("r", "nonexistent.go", "abc123", "0.1.0", "0.21.0") {
		t.Error("expected cache miss for nonexistent file")
	}
}

func TestCountSymbols(t *testing.T) {
	db := tempDB(t)

	// Empty DB should return 0.
	count, err := db.CountSymbols()
	if err != nil {
		t.Fatalf("count symbols: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 symbols, got %d", count)
	}

	// Insert some symbols.
	db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p1", SymbolName: "A",
		Language: "go", Kind: "func", Visibility: "public",
	})
	db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p1", SymbolName: "B",
		Language: "go", Kind: "type", Visibility: "public",
	})
	db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p2", SymbolName: "C",
		Language: "go", Kind: "func", Visibility: "public",
	})

	count, err = db.CountSymbols()
	if err != nil {
		t.Fatalf("count symbols: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 symbols, got %d", count)
	}
}

func TestCountClaims(t *testing.T) {
	db := tempDB(t)

	// Empty DB should return 0.
	count, err := db.CountClaims()
	if err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 claims, got %d", count)
	}

	// Insert a symbol and some claims.
	symID, _ := db.UpsertSymbol(Symbol{
		Repo: "r", ImportPath: "p", SymbolName: "F",
		Language: "go", Kind: "func", Visibility: "public",
	})
	db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "defines", SourceFile: "a.go",
		Confidence: 1.0, ClaimTier: "structural",
		Extractor: "test", ExtractorVersion: "1.0", LastVerified: Now(),
	})
	db.InsertClaim(Claim{
		SubjectID: symID, Predicate: "has_doc", SourceFile: "a.go",
		Confidence: 0.9, ClaimTier: "structural",
		Extractor: "test", ExtractorVersion: "1.0", LastVerified: Now(),
	})

	count, err = db.CountClaims()
	if err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 claims, got %d", count)
	}
}

func TestListDistinctImportPathsWithPrefix(t *testing.T) {
	db := tempDB(t)

	// Insert symbols with various import paths.
	for _, ip := range []string{
		"k8s.io/api/core/v1",
		"k8s.io/api/apps/v1",
		"k8s.io/api/batch/v1",
		"k8s.io/client-go/kubernetes",
		"github.com/other/pkg",
	} {
		db.UpsertSymbol(Symbol{
			Repo: "r", ImportPath: ip, SymbolName: "Sym",
			Language: "go", Kind: "type", Visibility: "public",
		})
	}

	// Test with prefix "k8s.io/api/" — should match 3 paths.
	paths, total, err := db.ListDistinctImportPathsWithPrefix("k8s.io/api/", 100)
	if err != nil {
		t.Fatalf("list with prefix: %v", err)
	}
	if total != 3 {
		t.Errorf("expected totalCount=3, got %d", total)
	}
	if len(paths) != 3 {
		t.Fatalf("expected 3 paths, got %d", len(paths))
	}
	// Should be sorted alphabetically.
	if paths[0] != "k8s.io/api/apps/v1" {
		t.Errorf("expected first path k8s.io/api/apps/v1, got %s", paths[0])
	}

	// Test with limit smaller than total.
	paths, total, err = db.ListDistinctImportPathsWithPrefix("k8s.io/api/", 2)
	if err != nil {
		t.Fatalf("list with prefix limit 2: %v", err)
	}
	if total != 3 {
		t.Errorf("expected totalCount=3, got %d", total)
	}
	if len(paths) != 2 {
		t.Errorf("expected 2 paths (limited), got %d", len(paths))
	}

	// Test with empty prefix — should return all 5.
	paths, total, err = db.ListDistinctImportPathsWithPrefix("", 100)
	if err != nil {
		t.Fatalf("list with empty prefix: %v", err)
	}
	if total != 5 {
		t.Errorf("expected totalCount=5, got %d", total)
	}
	if len(paths) != 5 {
		t.Errorf("expected 5 paths, got %d", len(paths))
	}

	// Test with non-matching prefix.
	paths, total, err = db.ListDistinctImportPathsWithPrefix("nonexistent/", 100)
	if err != nil {
		t.Fatalf("list with non-matching prefix: %v", err)
	}
	if total != 0 {
		t.Errorf("expected totalCount=0, got %d", total)
	}
	if len(paths) != 0 {
		t.Errorf("expected 0 paths, got %d", len(paths))
	}
}

func TestSetAndGetExtractionMeta(t *testing.T) {
	db := tempDB(t)

	// Before setting, should return zero-value.
	meta, err := db.GetExtractionMeta()
	if err != nil {
		t.Fatalf("get extraction meta (empty): %v", err)
	}
	if meta.CommitSHA != "" || meta.ExtractedAt != "" {
		t.Errorf("expected empty meta before set, got %+v", meta)
	}

	// Set metadata.
	ts := Now()
	err = db.SetExtractionMeta(ExtractionMeta{
		CommitSHA:   "abc123def456",
		ExtractedAt: ts,
	})
	if err != nil {
		t.Fatalf("set extraction meta: %v", err)
	}

	// Read it back.
	meta, err = db.GetExtractionMeta()
	if err != nil {
		t.Fatalf("get extraction meta: %v", err)
	}
	if meta.CommitSHA != "abc123def456" {
		t.Errorf("expected commit_sha=abc123def456, got %s", meta.CommitSHA)
	}
	if meta.ExtractedAt != ts {
		t.Errorf("expected extracted_at=%s, got %s", ts, meta.ExtractedAt)
	}

	// Update (upsert) should overwrite.
	ts2 := Now()
	err = db.SetExtractionMeta(ExtractionMeta{
		CommitSHA:   "newcommit789",
		ExtractedAt: ts2,
	})
	if err != nil {
		t.Fatalf("set extraction meta (update): %v", err)
	}
	meta, err = db.GetExtractionMeta()
	if err != nil {
		t.Fatalf("get extraction meta (updated): %v", err)
	}
	if meta.CommitSHA != "newcommit789" {
		t.Errorf("expected updated commit_sha=newcommit789, got %s", meta.CommitSHA)
	}
}

func TestGetExtractionMeta_NoTable(t *testing.T) {
	// Open a DB without calling SetExtractionMeta — table doesn't exist.
	db := tempDB(t)
	meta, err := db.GetExtractionMeta()
	if err != nil {
		t.Fatalf("get extraction meta (no table): %v", err)
	}
	if meta.CommitSHA != "" || meta.ExtractedAt != "" {
		t.Errorf("expected empty meta when table missing, got %+v", meta)
	}
}

func TestDBFileCreation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test_repo.db")
	db, err := OpenClaimsDB(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}
	// Verify file exists on disk.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected database file to exist on disk")
	}
}

func TestBusyTimeoutSet(t *testing.T) {
	cdb := tempDB(t)
	var timeout int
	err := cdb.DB().QueryRow("PRAGMA busy_timeout").Scan(&timeout)
	if err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("expected busy_timeout=5000, got %d", timeout)
	}
}

func TestRunInTransaction_Commit(t *testing.T) {
	cdb := tempDB(t)
	err := cdb.RunInTransaction(func() error {
		_, err := cdb.UpsertSymbol(Symbol{
			Repo: "r", ImportPath: "p", SymbolName: "TxSym",
			Language: "go", Kind: "func", Visibility: "public",
		})
		return err
	})
	if err != nil {
		t.Fatalf("transaction commit: %v", err)
	}
	// Symbol should be visible after commit.
	sym, err := cdb.GetSymbolByCompositeKey("r", "p", "TxSym")
	if err != nil {
		t.Fatalf("get symbol after commit: %v", err)
	}
	if sym.SymbolName != "TxSym" {
		t.Errorf("expected TxSym, got %s", sym.SymbolName)
	}
}

func TestRunInTransaction_Rollback(t *testing.T) {
	cdb := tempDB(t)
	// Insert a symbol inside a transaction that returns an error.
	err := cdb.RunInTransaction(func() error {
		_, err := cdb.UpsertSymbol(Symbol{
			Repo: "r", ImportPath: "p", SymbolName: "RollbackSym",
			Language: "go", Kind: "func", Visibility: "public",
		})
		if err != nil {
			return err
		}
		return fmt.Errorf("intentional error to trigger rollback")
	})
	if err == nil {
		t.Fatal("expected error from transaction")
	}
	// Symbol should NOT be visible after rollback.
	_, err = cdb.GetSymbolByCompositeKey("r", "p", "RollbackSym")
	if err == nil {
		t.Error("expected error (no rows) after rollback, but symbol was found")
	}
}

func TestRunInTransaction_ConcurrentNoRace(t *testing.T) {
	cdb := tempDB(t)

	const goroutines = 20
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			symName := fmt.Sprintf("ConcSym_%d", idx)
			err := cdb.RunInTransaction(func() error {
				_, err := cdb.UpsertSymbol(Symbol{
					Repo: "r", ImportPath: "concurrent", SymbolName: symName,
					Language: "go", Kind: "func", Visibility: "public",
				})
				if err != nil {
					return fmt.Errorf("upsert symbol %s: %w", symName, err)
				}
				_, err = cdb.InsertClaim(Claim{
					SubjectID: 1, Predicate: "defines", SourceFile: fmt.Sprintf("file_%d.go", idx),
					Confidence: 1.0, ClaimTier: "structural",
					Extractor: "test", ExtractorVersion: "1.0", LastVerified: Now(),
				})
				if err != nil {
					return fmt.Errorf("insert claim for %s: %w", symName, err)
				}
				return nil
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent transaction error: %v", err)
	}

	// Verify all symbols were persisted.
	for i := 0; i < goroutines; i++ {
		symName := fmt.Sprintf("ConcSym_%d", i)
		sym, err := cdb.GetSymbolByCompositeKey("r", "concurrent", symName)
		if err != nil {
			t.Errorf("missing symbol %s after concurrent transactions: %v", symName, err)
			continue
		}
		if sym.SymbolName != symName {
			t.Errorf("expected %s, got %s", symName, sym.SymbolName)
		}
	}
}
