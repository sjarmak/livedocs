// Package integration contains end-to-end tests that exercise the full
// extraction-to-storage pipeline across language boundaries.
package integration

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
	"github.com/sjarmak/livedocs/extractor/lang"
	"github.com/sjarmak/livedocs/extractor/treesitter"
)

// testdataPath returns the absolute path to a file in extractor/treesitter/testdata/.
func testdataPath(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "extractor", "treesitter", "testdata", name)
}

// tempClaimsDB creates a fresh SQLite claims database in a temp directory.
func tempClaimsDB(t *testing.T) *db.ClaimsDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test_ts.db")
	cdb, err := db.OpenClaimsDB(path)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	return cdb
}

// storeClaims upserts symbols and inserts claims into the DB, returning
// a map from symbol name to symbol ID for later queries.
func storeClaims(t *testing.T, cdb *db.ClaimsDB, claims []extractor.Claim, repo, importPath string) map[string]int64 {
	t.Helper()
	symbolIDs := make(map[string]int64)

	for _, c := range claims {
		// Only "defines" claims carry the authoritative kind for a symbol.
		// Other predicates (exports, imports, has_doc) reference a symbol
		// but should not overwrite its kind with a fallback value.
		kind := string(c.Kind)
		if kind == "" {
			kind = "module"
		}
		vis := string(c.Visibility)

		// If the symbol already exists and this is not a defines claim,
		// preserve the existing kind/visibility rather than overwriting
		// with fallback values.
		if string(c.Predicate) != "defines" {
			if existing, err := cdb.GetSymbolByCompositeKey(repo, importPath, c.SubjectName); err == nil {
				kind = existing.Kind
				vis = existing.Visibility
			}
		}

		symID, err := cdb.UpsertSymbol(db.Symbol{
			Repo:       repo,
			ImportPath: importPath,
			SymbolName: c.SubjectName,
			Language:   c.Language,
			Kind:       kind,
			Visibility: vis,
		})
		if err != nil {
			t.Fatalf("upsert symbol %q: %v", c.SubjectName, err)
		}
		symbolIDs[c.SubjectName] = symID

		_, err = cdb.InsertClaim(db.Claim{
			SubjectID:        symID,
			Predicate:        string(c.Predicate),
			ObjectText:       c.ObjectText,
			SourceFile:       c.SourceFile,
			SourceLine:       c.SourceLine,
			Confidence:       c.Confidence,
			ClaimTier:        string(c.ClaimTier),
			Extractor:        c.Extractor,
			ExtractorVersion: c.ExtractorVersion,
			LastVerified:     c.LastVerified.Format("2006-01-02T15:04:05Z07:00"),
		})
		if err != nil {
			t.Fatalf("insert claim for %q predicate=%s: %v", c.SubjectName, c.Predicate, err)
		}
	}
	return symbolIDs
}

// TestTypeScriptExtractToClaims_E2E exercises the full pipeline:
// tree-sitter parse -> extractor claims -> SQLite storage -> read-back verification.
// This validates that the claims schema works for non-Go languages.
func TestTypeScriptExtractToClaims_E2E(t *testing.T) {
	t.Parallel()

	// --- Step 1: Extract claims from sample.ts ---
	ext := treesitter.New(lang.NewRegistry())
	tsFile := testdataPath(t, "sample.ts")

	claims, err := ext.Extract(context.Background(), tsFile, "typescript")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}
	if len(claims) == 0 {
		t.Fatal("expected non-zero claims from sample.ts")
	}

	// Verify all claims have language=typescript.
	for i, c := range claims {
		if c.Language != "typescript" {
			t.Errorf("claim[%d] language = %q, want typescript", i, c.Language)
		}
	}

	// --- Step 2: Store all claims in SQLite ---
	cdb := tempClaimsDB(t)
	const repo = "test/ts-smoke"
	const importPath = "sample"

	symbolIDs := storeClaims(t, cdb, claims, repo, importPath)

	// --- Step 3: Read back and verify predicates ---

	// Collect claims by predicate from the DB.
	predicateCounts := map[string]int{}
	for _, symID := range symbolIDs {
		dbClaims, err := cdb.GetClaimsBySubject(symID)
		if err != nil {
			t.Fatalf("get claims by subject: %v", err)
		}
		for _, dc := range dbClaims {
			predicateCounts[dc.Predicate]++
		}
	}

	// We expect defines, imports, exports, and has_doc from sample.ts.
	expectedPredicates := []string{"defines", "imports", "exports", "has_doc"}
	for _, pred := range expectedPredicates {
		if predicateCounts[pred] == 0 {
			t.Errorf("expected at least one %q claim stored in DB, got 0", pred)
		}
	}
	t.Logf("predicate counts in DB: %v", predicateCounts)

	// --- Step 4: Verify specific symbols round-trip correctly ---

	// Check the Greeter interface was stored.
	greeter, err := cdb.GetSymbolByCompositeKey(repo, importPath, "Greeter")
	if err != nil {
		t.Fatalf("get Greeter symbol: %v", err)
	}
	if greeter.Language != "typescript" {
		t.Errorf("Greeter language = %q, want typescript", greeter.Language)
	}
	if greeter.Kind != "interface" {
		t.Errorf("Greeter kind = %q, want interface", greeter.Kind)
	}
	if greeter.Visibility != "public" {
		t.Errorf("Greeter visibility = %q, want public", greeter.Visibility)
	}

	// Check SimpleGreeter class.
	sg, err := cdb.GetSymbolByCompositeKey(repo, importPath, "SimpleGreeter")
	if err != nil {
		t.Fatalf("get SimpleGreeter symbol: %v", err)
	}
	if sg.Kind != "class" {
		t.Errorf("SimpleGreeter kind = %q, want class", sg.Kind)
	}

	// Check main function.
	mainSym, err := cdb.GetSymbolByCompositeKey(repo, importPath, "main")
	if err != nil {
		t.Fatalf("get main symbol: %v", err)
	}
	if mainSym.Kind != "func" {
		t.Errorf("main kind = %q, want func", mainSym.Kind)
	}

	// Check Config type alias.
	config, err := cdb.GetSymbolByCompositeKey(repo, importPath, "Config")
	if err != nil {
		t.Fatalf("get Config symbol: %v", err)
	}
	if config.Kind != "type" {
		t.Errorf("Config kind = %q, want type", config.Kind)
	}

	// Check LogLevel enum.
	logLevel, err := cdb.GetSymbolByCompositeKey(repo, importPath, "LogLevel")
	if err != nil {
		t.Fatalf("get LogLevel symbol: %v", err)
	}
	if logLevel.Kind != "enum" {
		t.Errorf("LogLevel kind = %q, want enum", logLevel.Kind)
	}

	// --- Step 5: Verify claims can be queried by file ---
	fileClaims, err := cdb.GetClaimsByFile(tsFile)
	if err != nil {
		t.Fatalf("get claims by file: %v", err)
	}
	if len(fileClaims) == 0 {
		t.Fatal("expected claims queryable by source file")
	}
	for _, fc := range fileClaims {
		if fc.ClaimTier != "structural" {
			t.Errorf("expected claim_tier=structural, got %q", fc.ClaimTier)
		}
		if fc.Extractor != "tree-sitter-typescript" {
			t.Errorf("expected extractor=tree-sitter-typescript, got %q", fc.Extractor)
		}
	}

	// --- Step 6: Verify import claim has ObjectText or is well-formed ---
	importClaims, err := cdb.GetClaimsByPredicate("imports")
	if err != nil {
		t.Fatalf("get imports claims: %v", err)
	}
	if len(importClaims) == 0 {
		t.Fatal("expected at least one imports claim in DB")
	}

	t.Logf("total claims stored: %d, symbols stored: %d", len(fileClaims), len(symbolIDs))
}
