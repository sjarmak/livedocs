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
			got := extractClaimFromContext(tt.context, "Symbol", extractor.PredicatePurpose)
			if got != tt.want {
				t.Errorf("extractClaimFromContext(%q) = %q, want %q", tt.context, got, tt.want)
			}
		})
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
