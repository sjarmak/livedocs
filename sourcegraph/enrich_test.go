package sourcegraph

import (
	"context"
	"fmt"
	"testing"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
)

// mockRouter implements PredicateRouter for testing.
type mockRouter struct {
	calls   []routeCall
	results map[string]string // key: "predicate:symbolName" -> context text
	err     error
}

type routeCall struct {
	predicate extractor.Predicate
	sym       SymbolContext
}

func (m *mockRouter) Route(ctx context.Context, predicate extractor.Predicate, sym SymbolContext) (string, error) {
	m.calls = append(m.calls, routeCall{predicate: predicate, sym: sym})
	if m.err != nil {
		return "", m.err
	}
	key := string(predicate) + ":" + sym.Name
	if text, ok := m.results[key]; ok {
		return text, nil
	}
	return "Default context about " + sym.Name, nil
}

// setupTestDB creates a temporary ClaimsDB with schema and test data.
func setupTestDB(t *testing.T) *db.ClaimsDB {
	t.Helper()
	cdb, err := db.OpenClaimsDB(":memory:")
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	return cdb
}

// insertSymbol is a test helper that inserts a symbol and returns its ID.
func insertSymbol(t *testing.T, cdb *db.ClaimsDB, repo, importPath, name, lang, kind, vis string) int64 {
	t.Helper()
	id, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       repo,
		ImportPath: importPath,
		SymbolName: name,
		Language:   lang,
		Kind:       kind,
		Visibility: vis,
	})
	if err != nil {
		t.Fatalf("insert symbol %s: %v", name, err)
	}
	return id
}

// insertClaim is a test helper that inserts a claim.
func insertClaim(t *testing.T, cdb *db.ClaimsDB, subjectID int64, pred, objText, sourceFile, tier, ext, extVer string, confidence float64) {
	t.Helper()
	_, err := cdb.InsertClaim(db.Claim{
		SubjectID:        subjectID,
		Predicate:        pred,
		ObjectText:       objText,
		SourceFile:       sourceFile,
		ClaimTier:        tier,
		Extractor:        ext,
		ExtractorVersion: extVer,
		Confidence:       1.0,
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}
}

// insertSourceFile is a test helper that inserts a source file record.
func insertSourceFile(t *testing.T, cdb *db.ClaimsDB, repo, path, hash string) {
	t.Helper()
	_, err := cdb.UpsertSourceFile(db.SourceFile{
		Repo:             repo,
		RelativePath:     path,
		ContentHash:      hash,
		ExtractorVersion: "test",
		LastIndexed:      db.Now(),
	})
	if err != nil {
		t.Fatalf("insert source file: %v", err)
	}
}

func TestSelectSymbols_PublicOnly(t *testing.T) {
	cdb := setupTestDB(t)

	// Insert public symbols of correct kinds.
	insertSymbol(t, cdb, "r", "pkg/a", "PubType", "go", "type", "public")
	insertSymbol(t, cdb, "r", "pkg/a", "PubFunc", "go", "func", "public")
	insertSymbol(t, cdb, "r", "pkg/a", "PubIface", "go", "interface", "public")
	insertSymbol(t, cdb, "r", "pkg/a", "PubMethod", "go", "method", "public")

	// Insert symbols that should be excluded.
	insertSymbol(t, cdb, "r", "pkg/a", "PrivType", "go", "type", "private")
	insertSymbol(t, cdb, "r", "pkg/a", "InternalFunc", "go", "func", "internal")
	insertSymbol(t, cdb, "r", "pkg/a", "PubConst", "go", "const", "public") // wrong kind
	insertSymbol(t, cdb, "r", "pkg/a", "PubVar", "go", "var", "public")     // wrong kind

	router := &mockRouter{results: map[string]string{}}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	symbols, err := enricher.selectSymbols(false)
	if err != nil {
		t.Fatalf("select symbols: %v", err)
	}

	if len(symbols) != 4 {
		t.Errorf("expected 4 public symbols of correct kinds, got %d", len(symbols))
		for _, s := range symbols {
			t.Logf("  %s (kind=%s, vis=%s)", s.SymbolName, s.Kind, s.Visibility)
		}
	}

	// Verify all are public.
	for _, s := range symbols {
		if s.Visibility != "public" {
			t.Errorf("expected public visibility, got %q for %s", s.Visibility, s.SymbolName)
		}
	}

	// Verify correct kinds.
	validKinds := map[string]bool{"type": true, "func": true, "interface": true, "method": true}
	for _, s := range symbols {
		if !validKinds[s.Kind] {
			t.Errorf("unexpected kind %q for %s", s.Kind, s.SymbolName)
		}
	}
}

func TestSelectSymbols_IncludeInternal(t *testing.T) {
	cdb := setupTestDB(t)

	insertSymbol(t, cdb, "r", "pkg/a", "PubType", "go", "type", "public")
	insertSymbol(t, cdb, "r", "pkg/a", "IntFunc", "go", "func", "internal")
	insertSymbol(t, cdb, "r", "pkg/a", "PrivType", "go", "type", "private") // still excluded

	router := &mockRouter{results: map[string]string{}}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	symbols, err := enricher.selectSymbols(true)
	if err != nil {
		t.Fatalf("select symbols: %v", err)
	}

	if len(symbols) != 2 {
		t.Errorf("expected 2 symbols (public + internal), got %d", len(symbols))
		for _, s := range symbols {
			t.Logf("  %s (kind=%s, vis=%s)", s.SymbolName, s.Kind, s.Visibility)
		}
	}
}

func TestReverseDepPrioritization(t *testing.T) {
	cdb := setupTestDB(t)

	// Package A has 0 reverse deps, Package B has 2.
	idA := insertSymbol(t, cdb, "r", "pkg/a", "TypeA", "go", "type", "public")
	idB := insertSymbol(t, cdb, "r", "pkg/b", "TypeB", "go", "type", "public")
	_ = insertSymbol(t, cdb, "r", "pkg/c", "TypeC", "go", "type", "public")

	// Two packages import pkg/b.
	insertClaim(t, cdb, idA, "imports", "pkg/b", "a.go", "structural", "test", "1.0", 1.0)
	// Another symbol also imports pkg/b (from a different file/package).
	otherID := insertSymbol(t, cdb, "r", "pkg/d", "HelperD", "go", "func", "public")
	insertClaim(t, cdb, otherID, "imports", "pkg/b", "d.go", "structural", "test", "1.0", 1.0)

	// No imports of pkg/a or pkg/c.
	_ = idB

	router := &mockRouter{results: map[string]string{}}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	symbols, err := enricher.selectSymbols(false)
	if err != nil {
		t.Fatalf("select symbols: %v", err)
	}

	ranked := enricher.rankByReverseDeps(symbols)
	if len(ranked) == 0 {
		t.Fatal("no ranked symbols")
	}

	// TypeB should be first (most reverse deps).
	if ranked[0].SymbolName != "TypeB" {
		t.Errorf("expected TypeB first (highest reverse deps), got %s", ranked[0].SymbolName)
	}
}

func TestCacheHit_Skip(t *testing.T) {
	cdb := setupTestDB(t)

	id := insertSymbol(t, cdb, "r", "pkg/a", "CachedType", "go", "type", "public")

	// Insert source file with a known hash.
	insertSourceFile(t, cdb, "r", "a.go", "abc123")

	// Insert a structural claim so we can find the source file.
	insertClaim(t, cdb, id, "defines", "CachedType", "a.go", "structural", "test", "1.0", 1.0)

	// Insert an existing semantic claim from our extractor with matching hash.
	_, err := cdb.InsertClaim(db.Claim{
		SubjectID:        id,
		Predicate:        "purpose",
		ObjectText:       "existing purpose",
		SourceFile:       "a.go",
		ClaimTier:        "semantic",
		Extractor:        enrichExtractorName,
		ExtractorVersion: enrichExtractorVersion + "@abc123",
		Confidence:       0.8,
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert semantic claim: %v", err)
	}

	router := &mockRouter{results: map[string]string{}}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	summary, err := enricher.Run(context.Background(), EnrichOpts{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.SymbolsSkipped != 1 {
		t.Errorf("expected 1 skipped, got %d", summary.SymbolsSkipped)
	}
	if summary.SymbolsEnriched != 0 {
		t.Errorf("expected 0 enriched, got %d", summary.SymbolsEnriched)
	}
	if summary.CallsMade != 0 {
		t.Errorf("expected 0 calls, got %d", summary.CallsMade)
	}
}

func TestCacheMiss_Enrich(t *testing.T) {
	cdb := setupTestDB(t)

	id := insertSymbol(t, cdb, "r", "pkg/a", "NewType", "go", "type", "public")
	insertSourceFile(t, cdb, "r", "a.go", "hash1")
	insertClaim(t, cdb, id, "defines", "NewType", "a.go", "structural", "test", "1.0", 1.0)

	// No existing semantic claims — cache miss.
	router := &mockRouter{
		results: map[string]string{
			"purpose:NewType":       "NewType is a data container for configuration",
			"usage_pattern:NewType": "NewType is typically constructed via NewNewType()",
			"complexity:NewType":    "NewType has moderate complexity",
			"stability:NewType":     "NewType is stable with few changes",
		},
	}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	summary, err := enricher.Run(context.Background(), EnrichOpts{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.SymbolsEnriched != 1 {
		t.Errorf("expected 1 enriched, got %d", summary.SymbolsEnriched)
	}
	if summary.CallsMade != 4 {
		t.Errorf("expected 4 calls (4 predicates), got %d", summary.CallsMade)
	}

	// Verify claims were stored.
	claims, err := cdb.GetClaimsBySubject(id)
	if err != nil {
		t.Fatalf("get claims: %v", err)
	}
	semanticCount := 0
	for _, cl := range claims {
		if cl.ClaimTier == "semantic" && cl.Extractor == enrichExtractorName {
			semanticCount++
		}
	}
	if semanticCount != 4 {
		t.Errorf("expected 4 semantic claims stored, got %d", semanticCount)
	}
}

func TestForceOverride(t *testing.T) {
	cdb := setupTestDB(t)

	id := insertSymbol(t, cdb, "r", "pkg/a", "ForcedType", "go", "type", "public")
	insertSourceFile(t, cdb, "r", "a.go", "hash1")
	insertClaim(t, cdb, id, "defines", "ForcedType", "a.go", "structural", "test", "1.0", 1.0)

	// Insert existing semantic claim with matching hash (would be cache hit).
	_, err := cdb.InsertClaim(db.Claim{
		SubjectID:        id,
		Predicate:        "purpose",
		ObjectText:       "old purpose",
		SourceFile:       "a.go",
		ClaimTier:        "semantic",
		Extractor:        enrichExtractorName,
		ExtractorVersion: enrichExtractorVersion + "@hash1",
		Confidence:       0.8,
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert semantic claim: %v", err)
	}

	router := &mockRouter{
		results: map[string]string{
			"purpose:ForcedType":       "ForcedType is updated purpose",
			"usage_pattern:ForcedType": "ForcedType usage updated",
			"complexity:ForcedType":    "ForcedType complexity updated",
			"stability:ForcedType":     "ForcedType stability updated",
		},
	}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	summary, err := enricher.Run(context.Background(), EnrichOpts{Force: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.SymbolsSkipped != 0 {
		t.Errorf("expected 0 skipped with force, got %d", summary.SymbolsSkipped)
	}
	if summary.SymbolsEnriched != 1 {
		t.Errorf("expected 1 enriched with force, got %d", summary.SymbolsEnriched)
	}
}

func TestBudgetCap(t *testing.T) {
	cdb := setupTestDB(t)

	// Insert 3 symbols.
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("Sym%d", i)
		id := insertSymbol(t, cdb, "r", "pkg/a", name, "go", "type", "public")
		insertSourceFile(t, cdb, "r", fmt.Sprintf("%d.go", i), fmt.Sprintf("hash%d", i))
		insertClaim(t, cdb, id, "defines", name, fmt.Sprintf("%d.go", i), "structural", "test", "1.0", 1.0)
	}

	router := &mockRouter{results: map[string]string{}}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	// Budget of 2 means at most 2 router calls.
	summary, err := enricher.Run(context.Background(), EnrichOpts{Budget: 2})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.CallsMade > 2 {
		t.Errorf("expected at most 2 calls with budget=2, got %d", summary.CallsMade)
	}
}

func TestNullClaimHandling(t *testing.T) {
	cdb := setupTestDB(t)

	id := insertSymbol(t, cdb, "r", "pkg/a", "NullType", "go", "type", "public")
	insertSourceFile(t, cdb, "r", "a.go", "hash1")
	insertClaim(t, cdb, id, "defines", "NullType", "a.go", "structural", "test", "1.0", 1.0)

	// Router returns "null" for purpose and empty for usage_pattern.
	router := &mockRouter{
		results: map[string]string{
			"purpose:NullType":       "null",
			"usage_pattern:NullType": "",
			"complexity:NullType":    "NullType is moderately complex",
			"stability:NullType":     "NullType is stable",
		},
	}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	if _, err := enricher.Run(context.Background(), EnrichOpts{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Only complexity and stability should be stored (purpose=null, usage_pattern=empty).
	claims, err := cdb.GetClaimsBySubject(id)
	if err != nil {
		t.Fatalf("get claims: %v", err)
	}
	semanticCount := 0
	for _, cl := range claims {
		if cl.ClaimTier == "semantic" && cl.Extractor == enrichExtractorName {
			semanticCount++
			// Verify null claims were not stored.
			if cl.Predicate == "purpose" || cl.Predicate == "usage_pattern" {
				t.Errorf("null claim was stored: predicate=%s", cl.Predicate)
			}
		}
	}
	if semanticCount != 2 {
		t.Errorf("expected 2 semantic claims (null excluded), got %d", semanticCount)
	}
}

func TestConfidenceScoring(t *testing.T) {
	cdb := setupTestDB(t)

	id := insertSymbol(t, cdb, "r", "pkg/a", "MySymbol", "go", "type", "public")
	insertSourceFile(t, cdb, "r", "a.go", "hash1")
	insertClaim(t, cdb, id, "defines", "MySymbol", "a.go", "structural", "test", "1.0", 1.0)

	router := &mockRouter{
		results: map[string]string{
			// Purpose mentions symbol name -> 0.8 confidence.
			"purpose:MySymbol": "MySymbol is used for data storage",
			// Usage pattern does NOT mention symbol name -> 0.4 confidence.
			"usage_pattern:MySymbol": "This data container is typically created via factory",
			"complexity:MySymbol":    "null", // null, should be skipped
			"stability:MySymbol":     "null", // null, should be skipped
		},
	}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	_, err = enricher.Run(context.Background(), EnrichOpts{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	claims, err := cdb.GetClaimsBySubject(id)
	if err != nil {
		t.Fatalf("get claims: %v", err)
	}

	for _, cl := range claims {
		if cl.Extractor != enrichExtractorName {
			continue
		}
		switch cl.Predicate {
		case "purpose":
			if cl.Confidence != 0.8 {
				t.Errorf("purpose confidence: want 0.8, got %f", cl.Confidence)
			}
		case "usage_pattern":
			if cl.Confidence != 0.4 {
				t.Errorf("usage_pattern confidence: want 0.4, got %f", cl.Confidence)
			}
		}
	}
}

func TestLowConfidenceSentinel_SkipsStorage(t *testing.T) {
	cdb := setupTestDB(t)

	id := insertSymbol(t, cdb, "r", "pkg/a", "SentinelSym", "go", "type", "public")
	insertSourceFile(t, cdb, "r", "a.go", "hash1")
	insertClaim(t, cdb, id, "defines", "SentinelSym", "a.go", "structural", "test", "1.0", 1.0)

	// Router returns the low-confidence sentinel.
	router := &mockRouter{
		results: map[string]string{
			"purpose:SentinelSym":       LowConfidenceSentinel,
			"usage_pattern:SentinelSym": LowConfidenceSentinel,
			"complexity:SentinelSym":    LowConfidenceSentinel,
			"stability:SentinelSym":     LowConfidenceSentinel,
		},
	}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	summary, err := enricher.Run(context.Background(), EnrichOpts{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// No claims should be stored for sentinel responses.
	if summary.SymbolsEnriched != 0 {
		t.Errorf("expected 0 enriched for sentinel, got %d", summary.SymbolsEnriched)
	}
	if summary.CallsMade != 4 {
		t.Errorf("expected 4 calls made, got %d", summary.CallsMade)
	}
}

func TestNewEnricher_Validation(t *testing.T) {
	cdb := setupTestDB(t)
	router := &mockRouter{}

	_, err := NewEnricher(nil, router)
	if err == nil {
		t.Error("expected error for nil claimsDB")
	}

	_, err = NewEnricher(cdb, nil)
	if err == nil {
		t.Error("expected error for nil router")
	}

	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enricher == nil {
		t.Error("expected non-nil enricher")
	}
}

func TestExtractClaimFromContext_NullHandling(t *testing.T) {
	tests := []struct {
		name    string
		context string
		want    string
	}{
		{"empty string", "", ""},
		{"null literal", "null", ""},
		{"NULL uppercase", "NULL", ""},
		{"whitespace only", "   ", ""},
		{"valid text", "Symbol does X", "Symbol does X"},
		{"valid with whitespace", "  Symbol does X  ", "Symbol does X"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractClaimFromContext(tt.context)
			if got != tt.want {
				t.Errorf("extractClaimFromContext(%q) = %q, want %q", tt.context, got, tt.want)
			}
		})
	}
}

func TestSymbolIDs_FilterExact(t *testing.T) {
	cdb := setupTestDB(t)

	// Insert 3 symbols, but only pass 2 IDs.
	id1 := insertSymbol(t, cdb, "r", "pkg/a", "SymA", "go", "type", "public")
	_ = insertSymbol(t, cdb, "r", "pkg/a", "SymB", "go", "func", "public")
	id3 := insertSymbol(t, cdb, "r", "pkg/a", "SymC", "go", "interface", "public")

	// Set up source files for all.
	insertSourceFile(t, cdb, "r", "a.go", "hashA")
	insertClaim(t, cdb, id1, "defines", "SymA", "a.go", "structural", "test", "1.0", 1.0)
	insertSourceFile(t, cdb, "r", "c.go", "hashC")
	insertClaim(t, cdb, id3, "defines", "SymC", "c.go", "structural", "test", "1.0", 1.0)

	router := &mockRouter{results: map[string]string{}}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	summary, err := enricher.Run(context.Background(), EnrichOpts{
		SymbolIDs: []int64{id1, id3},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Should enrich exactly 2 symbols (SymA and SymC), not SymB.
	if summary.SymbolsEnriched != 2 {
		t.Errorf("expected 2 enriched, got %d", summary.SymbolsEnriched)
	}

	// Verify router was only called for SymA and SymC.
	calledNames := map[string]bool{}
	for _, call := range router.calls {
		calledNames[call.sym.Name] = true
	}
	if calledNames["SymB"] {
		t.Error("SymB should not have been called (not in SymbolIDs)")
	}
	if !calledNames["SymA"] {
		t.Error("SymA should have been called")
	}
	if !calledNames["SymC"] {
		t.Error("SymC should have been called")
	}
}

func TestSymbolIDs_Empty_UnchangedBehavior(t *testing.T) {
	cdb := setupTestDB(t)

	insertSymbol(t, cdb, "r", "pkg/a", "TypeX", "go", "type", "public")

	router := &mockRouter{results: map[string]string{}}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	// Empty SymbolIDs: should use normal selectSymbols path.
	summary, err := enricher.Run(context.Background(), EnrichOpts{
		SymbolIDs: nil,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// TypeX has no source file, so it won't have content hash; still gets enriched.
	if summary.CallsMade != 4 {
		t.Errorf("expected 4 calls (normal path), got %d", summary.CallsMade)
	}
}

func TestTombstone_CreationAndRetry(t *testing.T) {
	cdb := setupTestDB(t)

	id := insertSymbol(t, cdb, "r", "pkg/a", "FailSym", "go", "type", "public")
	insertSourceFile(t, cdb, "r", "a.go", "hash1")
	insertClaim(t, cdb, id, "defines", "FailSym", "a.go", "structural", "test", "1.0", 1.0)

	// Router always returns error.
	router := &mockRouter{err: fmt.Errorf("sourcegraph unavailable")}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	// First run: should create tombstone.
	_, err = enricher.Run(context.Background(), EnrichOpts{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Verify tombstone was created.
	claims, err := cdb.GetClaimsBySubject(id)
	if err != nil {
		t.Fatalf("get claims: %v", err)
	}
	tombstoneCount := 0
	for _, cl := range claims {
		if cl.Extractor == enrichExtractorName && cl.Predicate == "enrichment_failed" {
			tombstoneCount++
			if cl.ClaimTier != "meta" {
				t.Errorf("expected claim_tier=meta, got %s", cl.ClaimTier)
			}
		}
	}
	if tombstoneCount != 1 {
		t.Errorf("expected 1 tombstone, got %d", tombstoneCount)
	}

	// Second run: tombstone should not prevent retry (isCacheHit returns false).
	router2 := &mockRouter{err: fmt.Errorf("still unavailable")}
	enricher2, _ := NewEnricher(cdb, router2)
	summary2, err := enricher2.Run(context.Background(), EnrichOpts{})
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	// Should have made calls (retried), not skipped.
	if summary2.CallsMade == 0 {
		t.Error("expected retry (calls > 0), but got 0 calls")
	}
}

func TestTombstone_ReplacementOnSuccess(t *testing.T) {
	cdb := setupTestDB(t)

	id := insertSymbol(t, cdb, "r", "pkg/a", "RecoverSym", "go", "type", "public")
	insertSourceFile(t, cdb, "r", "a.go", "hash1")
	insertClaim(t, cdb, id, "defines", "RecoverSym", "a.go", "structural", "test", "1.0", 1.0)

	// First run: router fails, creating a tombstone.
	router1 := &mockRouter{err: fmt.Errorf("fail")}
	enricher1, _ := NewEnricher(cdb, router1)
	enricher1.Run(context.Background(), EnrichOpts{})

	// Verify tombstone exists.
	claims, _ := cdb.GetClaimsBySubject(id)
	hasTombstone := false
	for _, cl := range claims {
		if cl.Predicate == "enrichment_failed" {
			hasTombstone = true
		}
	}
	if !hasTombstone {
		t.Fatal("expected tombstone after failure")
	}

	// Second run: router succeeds.
	router2 := &mockRouter{results: map[string]string{
		"purpose:RecoverSym":       "RecoverSym is a recovered type",
		"usage_pattern:RecoverSym": "RecoverSym usage pattern",
		"complexity:RecoverSym":    "RecoverSym complexity",
		"stability:RecoverSym":     "RecoverSym stability",
	}}
	enricher2, _ := NewEnricher(cdb, router2)
	summary, err := enricher2.Run(context.Background(), EnrichOpts{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if summary.SymbolsEnriched != 1 {
		t.Errorf("expected 1 enriched, got %d", summary.SymbolsEnriched)
	}

	// Verify tombstone was removed.
	claims, _ = cdb.GetClaimsBySubject(id)
	for _, cl := range claims {
		if cl.Predicate == "enrichment_failed" || cl.Predicate == "enrichment_permanently_failed" {
			t.Errorf("tombstone should have been removed, found predicate=%s", cl.Predicate)
		}
	}
}

func TestTombstone_PermanentAfterThreeFailures(t *testing.T) {
	cdb := setupTestDB(t)

	id := insertSymbol(t, cdb, "r", "pkg/a", "PermanentFail", "go", "type", "public")
	insertSourceFile(t, cdb, "r", "a.go", "hash1")
	insertClaim(t, cdb, id, "defines", "PermanentFail", "a.go", "structural", "test", "1.0", 1.0)

	// Fail 3 times consecutively.
	for i := 0; i < 3; i++ {
		router := &mockRouter{err: fmt.Errorf("fail %d", i)}
		enricher, _ := NewEnricher(cdb, router)
		enricher.Run(context.Background(), EnrichOpts{})
	}

	// Verify permanently failed tombstone.
	claims, _ := cdb.GetClaimsBySubject(id)
	hasPermanent := false
	hasNonPermanent := false
	for _, cl := range claims {
		if cl.Extractor == enrichExtractorName {
			if cl.Predicate == "enrichment_permanently_failed" {
				hasPermanent = true
			}
			if cl.Predicate == "enrichment_failed" {
				hasNonPermanent = true
			}
		}
	}
	if !hasPermanent {
		t.Error("expected enrichment_permanently_failed tombstone after 3 failures")
	}
	if hasNonPermanent {
		t.Error("non-permanent tombstones should have been cleaned up on escalation")
	}
}

func TestTombstone_PermanentSkippedUntilSourceChange(t *testing.T) {
	cdb := setupTestDB(t)

	id := insertSymbol(t, cdb, "r", "pkg/a", "PermSkip", "go", "type", "public")
	insertSourceFile(t, cdb, "r", "a.go", "hash1")
	insertClaim(t, cdb, id, "defines", "PermSkip", "a.go", "structural", "test", "1.0", 1.0)

	// Create permanent tombstone manually (simulating 3 failures).
	_, err := cdb.InsertClaim(db.Claim{
		SubjectID:        id,
		Predicate:        "enrichment_permanently_failed",
		ObjectText:       "enrichment failed (attempt 3)",
		SourceFile:       "a.go",
		Confidence:       0,
		ClaimTier:        "meta",
		Extractor:        enrichExtractorName,
		ExtractorVersion: enrichExtractorVersion + "@hash1",
		LastVerified:     db.Now(),
	})
	if err != nil {
		t.Fatalf("insert permanent tombstone: %v", err)
	}

	// Run with same hash: should skip (cache hit).
	router := &mockRouter{results: map[string]string{}}
	enricher, _ := NewEnricher(cdb, router)
	summary, err := enricher.Run(context.Background(), EnrichOpts{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if summary.SymbolsSkipped != 1 {
		t.Errorf("expected 1 skipped (permanent tombstone), got %d", summary.SymbolsSkipped)
	}
	if summary.CallsMade != 0 {
		t.Errorf("expected 0 calls (skipped), got %d", summary.CallsMade)
	}

	// Change the source hash.
	insertSourceFile(t, cdb, "r", "a.go", "hash2")

	// Run again: should retry now (hash changed).
	router2 := &mockRouter{results: map[string]string{
		"purpose:PermSkip":       "PermSkip purpose after fix",
		"usage_pattern:PermSkip": "PermSkip usage",
		"complexity:PermSkip":    "PermSkip complexity",
		"stability:PermSkip":     "PermSkip stability",
	}}
	enricher2, _ := NewEnricher(cdb, router2)
	summary2, err := enricher2.Run(context.Background(), EnrichOpts{})
	if err != nil {
		t.Fatalf("run after hash change: %v", err)
	}
	if summary2.SymbolsEnriched != 1 {
		t.Errorf("expected 1 enriched after hash change, got %d", summary2.SymbolsEnriched)
	}
}

func TestBatchSizeCap(t *testing.T) {
	cdb := setupTestDB(t)

	// Insert 60 symbols (exceeds maxBatchSize of 50).
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("BatchSym%03d", i)
		id := insertSymbol(t, cdb, "r", "pkg/a", name, "go", "type", "public")
		fname := fmt.Sprintf("batch%d.go", i)
		insertSourceFile(t, cdb, "r", fname, fmt.Sprintf("hash%d", i))
		insertClaim(t, cdb, id, "defines", name, fname, "structural", "test", "1.0", 1.0)
	}

	router := &mockRouter{results: map[string]string{}}
	enricher, _ := NewEnricher(cdb, router)

	// No budget/max-symbols limit, batch cap should kick in.
	summary, err := enricher.Run(context.Background(), EnrichOpts{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// At most 50 symbols should be enriched (batch cap).
	if summary.SymbolsEnriched > 50 {
		t.Errorf("expected at most 50 enriched (batch cap), got %d", summary.SymbolsEnriched)
	}
	// Verify calls are bounded: 50 symbols * 4 predicates = 200 max.
	if summary.CallsMade > 200 {
		t.Errorf("expected at most 200 calls (50*4), got %d", summary.CallsMade)
	}
}

func TestDefaultPredicates(t *testing.T) {
	preds := DefaultPredicates()
	if len(preds) != 4 {
		t.Errorf("expected 4 default predicates, got %d", len(preds))
	}
}

func TestEnrichmentSummary_ElapsedTime(t *testing.T) {
	cdb := setupTestDB(t)
	router := &mockRouter{results: map[string]string{}}
	enricher, err := NewEnricher(cdb, router)
	if err != nil {
		t.Fatalf("new enricher: %v", err)
	}

	summary, err := enricher.Run(context.Background(), EnrichOpts{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.ElapsedTime <= 0 {
		t.Error("expected positive elapsed time")
	}
}
