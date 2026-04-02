package scip

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	scipb "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"

	"github.com/live-docs/live_docs/db"
)

func testClaimsDB(t *testing.T) *db.ClaimsDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
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

func testXRefDB(t *testing.T) *db.XRefDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "_xref.db")
	xdb, err := db.OpenXRefDB(path)
	if err != nil {
		t.Fatalf("open xref db: %v", err)
	}
	if err := xdb.CreateSchema(); err != nil {
		t.Fatalf("create xref schema: %v", err)
	}
	t.Cleanup(func() { xdb.Close() })
	return xdb
}

// marshalIndex serializes a SCIP Index to bytes (non-streaming format).
func marshalIndex(t *testing.T, idx *scipb.Index) []byte {
	t.Helper()
	data, err := proto.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	return data
}

func TestImportReader_BasicDocument(t *testing.T) {
	cdb := testClaimsDB(t)
	xdb := testXRefDB(t)
	imp := NewImporter("kubernetes/kubernetes", "go", cdb, xdb)

	idx := &scipb.Index{
		Metadata: &scipb.Metadata{
			Version:     scipb.ProtocolVersion_UnspecifiedProtocolVersion,
			ToolInfo:    &scipb.ToolInfo{Name: "scip-go", Version: "0.4.0"},
			ProjectRoot: "file:///home/user/kubernetes",
		},
		Documents: []*scipb.Document{
			{
				RelativePath: "staging/src/k8s.io/api/core/v1/types.go",
				Language:     "go",
				Symbols: []*scipb.SymbolInformation{
					{
						Symbol:        "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#",
						Documentation: []string{"Pod is a collection of containers."},
						Kind:          scipb.SymbolInformation_Struct,
						DisplayName:   "Pod",
					},
					{
						Symbol:        "scip-go gomod k8s.io/api v0.28.0 core/v1/PodSpec#",
						Documentation: []string{"PodSpec describes the spec of a pod."},
						Kind:          scipb.SymbolInformation_Struct,
						DisplayName:   "PodSpec",
						Relationships: []*scipb.Relationship{
							{
								Symbol:           "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#",
								IsImplementation: false,
								IsReference:      true,
							},
						},
					},
				},
			},
		},
	}

	data := marshalIndex(t, idx)
	result, err := imp.ImportReader(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if result.DocumentsVisited != 1 {
		t.Errorf("documents visited: got %d, want 1", result.DocumentsVisited)
	}
	if result.SymbolsImported != 2 {
		t.Errorf("symbols imported: got %d, want 2", result.SymbolsImported)
	}
	// Each symbol gets: defines + has_kind + has_doc = 3 claims. 2 symbols = 6 claims.
	if result.ClaimsCreated != 6 {
		t.Errorf("claims created: got %d, want 6", result.ClaimsCreated)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}

	// Verify the symbol was stored with correct composite key.
	sym, err := cdb.GetSymbolByCompositeKey("kubernetes/kubernetes", "k8s.io/api/core/v1", "Pod")
	if err != nil {
		t.Fatalf("get symbol: %v", err)
	}
	if sym.Kind != "type" {
		t.Errorf("kind: got %q, want %q", sym.Kind, "type")
	}
	if sym.SCIPSymbol != "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#" {
		t.Errorf("scip_symbol: got %q", sym.SCIPSymbol)
	}
	if sym.Visibility != "public" {
		t.Errorf("visibility: got %q, want %q", sym.Visibility, "public")
	}

	// Verify claims.
	claims, _ := cdb.GetClaimsBySubject(sym.ID)
	predicates := make(map[string]int)
	for _, c := range claims {
		predicates[c.Predicate]++
	}
	if predicates["defines"] != 1 {
		t.Errorf("expected 1 defines claim, got %d", predicates["defines"])
	}
	if predicates["has_kind"] != 1 {
		t.Errorf("expected 1 has_kind claim, got %d", predicates["has_kind"])
	}
	if predicates["has_doc"] != 1 {
		t.Errorf("expected 1 has_doc claim, got %d", predicates["has_doc"])
	}

	// Verify xref was created.
	refs, _ := xdb.LookupRepos("k8s.io/api/core/v1.Pod")
	if len(refs) != 1 {
		t.Fatalf("expected 1 xref, got %d", len(refs))
	}
	if refs[0].Repo != "kubernetes/kubernetes" {
		t.Errorf("xref repo: got %q", refs[0].Repo)
	}
}

func TestImportReader_WithImplements(t *testing.T) {
	cdb := testClaimsDB(t)
	imp := NewImporter("kubernetes/kubernetes", "go", cdb, nil)

	idx := &scipb.Index{
		Metadata: &scipb.Metadata{
			ToolInfo: &scipb.ToolInfo{Name: "scip-go"},
		},
		Documents: []*scipb.Document{
			{
				RelativePath: "pkg/runtime/interfaces.go",
				Language:     "go",
				Symbols: []*scipb.SymbolInformation{
					{
						Symbol:      "scip-go gomod k8s.io/apimachinery v0.28.0 runtime/Object#",
						Kind:        scipb.SymbolInformation_Interface,
						DisplayName: "Object",
					},
					{
						Symbol:      "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#",
						Kind:        scipb.SymbolInformation_Struct,
						DisplayName: "Pod",
						Relationships: []*scipb.Relationship{
							{
								Symbol:           "scip-go gomod k8s.io/apimachinery v0.28.0 runtime/Object#",
								IsImplementation: true,
							},
						},
					},
				},
			},
		},
	}

	data := marshalIndex(t, idx)
	result, err := imp.ImportReader(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	// Pod should have: defines + has_kind + implements = 3 claims
	// Object should have: defines + has_kind = 2 claims
	// Total = 5
	if result.ClaimsCreated != 5 {
		t.Errorf("claims created: got %d, want 5", result.ClaimsCreated)
	}

	// Verify the implements claim.
	sym, _ := cdb.GetSymbolByCompositeKey("kubernetes/kubernetes", "k8s.io/api/core/v1", "Pod")
	claims, _ := cdb.GetClaimsBySubject(sym.ID)
	found := false
	for _, c := range claims {
		if c.Predicate == "implements" {
			found = true
			if c.ObjectText != "scip-go gomod k8s.io/apimachinery v0.28.0 runtime/Object#" {
				t.Errorf("implements object_text: got %q", c.ObjectText)
			}
		}
	}
	if !found {
		t.Error("expected an implements claim for Pod")
	}
}

func TestImportReader_LocalSymbolsSkipped(t *testing.T) {
	cdb := testClaimsDB(t)
	imp := NewImporter("test/repo", "go", cdb, nil)

	idx := &scipb.Index{
		Metadata: &scipb.Metadata{ToolInfo: &scipb.ToolInfo{Name: "scip-go"}},
		Documents: []*scipb.Document{
			{
				RelativePath: "main.go",
				Language:     "go",
				Symbols: []*scipb.SymbolInformation{
					{Symbol: "local 42"},
					{
						Symbol: "scip-go gomod example.com/pkg v1.0.0 Foo#",
						Kind:   scipb.SymbolInformation_Struct,
					},
				},
			},
		},
	}

	data := marshalIndex(t, idx)
	result, _ := imp.ImportReader(context.Background(), bytes.NewReader(data))
	if result.SymbolsImported != 1 {
		t.Errorf("expected 1 symbol imported (local skipped), got %d", result.SymbolsImported)
	}
}

func TestImportReader_ExternalSymbols(t *testing.T) {
	cdb := testClaimsDB(t)
	xdb := testXRefDB(t)
	imp := NewImporter("test/repo", "go", cdb, xdb)

	idx := &scipb.Index{
		Metadata: &scipb.Metadata{ToolInfo: &scipb.ToolInfo{Name: "scip-go"}},
		ExternalSymbols: []*scipb.SymbolInformation{
			{
				Symbol:        "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#",
				Documentation: []string{"Pod is a collection of containers."},
				Kind:          scipb.SymbolInformation_Struct,
			},
		},
	}

	data := marshalIndex(t, idx)
	result, err := imp.ImportReader(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.ExternalSymbols != 1 {
		t.Errorf("external symbols: got %d, want 1", result.ExternalSymbols)
	}

	// Verify external symbol was stored.
	sym, err := cdb.GetSymbolByCompositeKey("test/repo", "k8s.io/api/core/v1", "Pod")
	if err != nil {
		t.Fatalf("get external symbol: %v", err)
	}
	if sym.Kind != "type" {
		t.Errorf("kind: got %q, want %q", sym.Kind, "type")
	}

	// Verify xref for external symbol.
	refs, _ := xdb.LookupRepos("k8s.io/api/core/v1.Pod")
	if len(refs) != 1 {
		t.Fatalf("expected 1 xref for external symbol, got %d", len(refs))
	}
}

func TestImportReader_IdempotentReimport(t *testing.T) {
	cdb := testClaimsDB(t)
	imp := NewImporter("test/repo", "go", cdb, nil)

	idx := &scipb.Index{
		Metadata: &scipb.Metadata{ToolInfo: &scipb.ToolInfo{Name: "scip-go"}},
		Documents: []*scipb.Document{
			{
				RelativePath: "main.go",
				Language:     "go",
				Symbols: []*scipb.SymbolInformation{
					{
						Symbol: "scip-go gomod example.com/pkg v1.0.0 Foo#",
						Kind:   scipb.SymbolInformation_Struct,
					},
				},
			},
		},
	}

	data := marshalIndex(t, idx)

	// Import twice.
	imp.ImportReader(context.Background(), bytes.NewReader(data))
	result, _ := imp.ImportReader(context.Background(), bytes.NewReader(data))

	// After re-import, should still have exactly 1 symbol with 2 claims (defines + has_kind).
	if result.SymbolsImported != 1 {
		t.Errorf("symbols imported: got %d, want 1", result.SymbolsImported)
	}

	sym, _ := cdb.GetSymbolByCompositeKey("test/repo", "example.com/pkg", "Foo")
	claims, _ := cdb.GetClaimsBySubject(sym.ID)
	if len(claims) != 2 {
		t.Errorf("expected 2 claims after re-import, got %d", len(claims))
	}
}

func TestImportReader_EmptyIndex(t *testing.T) {
	cdb := testClaimsDB(t)
	imp := NewImporter("test/repo", "go", cdb, nil)

	idx := &scipb.Index{
		Metadata: &scipb.Metadata{ToolInfo: &scipb.ToolInfo{Name: "scip-go"}},
	}

	data := marshalIndex(t, idx)
	result, err := imp.ImportReader(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("import empty index: %v", err)
	}
	if result.DocumentsVisited != 0 {
		t.Errorf("documents visited: got %d, want 0", result.DocumentsVisited)
	}
	if result.SymbolsImported != 0 {
		t.Errorf("symbols imported: got %d, want 0", result.SymbolsImported)
	}
}

func TestImportReader_GoVisibility(t *testing.T) {
	cdb := testClaimsDB(t)
	imp := NewImporter("test/repo", "go", cdb, nil)

	idx := &scipb.Index{
		Metadata: &scipb.Metadata{ToolInfo: &scipb.ToolInfo{Name: "scip-go"}},
		Documents: []*scipb.Document{
			{
				RelativePath: "internal.go",
				Language:     "go",
				Symbols: []*scipb.SymbolInformation{
					{
						Symbol: "scip-go gomod example.com/pkg v1.0.0 myPrivateFunc().",
						Kind:   scipb.SymbolInformation_Function,
					},
				},
			},
		},
	}

	data := marshalIndex(t, idx)
	imp.ImportReader(context.Background(), bytes.NewReader(data))

	sym, err := cdb.GetSymbolByCompositeKey("test/repo", "example.com/pkg", "myPrivateFunc")
	if err != nil {
		t.Fatalf("get symbol: %v", err)
	}
	if sym.Visibility != "internal" {
		t.Errorf("visibility: got %q, want %q", sym.Visibility, "internal")
	}
}

func TestImportFile_NonExistent(t *testing.T) {
	cdb := testClaimsDB(t)
	imp := NewImporter("test/repo", "go", cdb, nil)

	_, err := imp.ImportFile(context.Background(), "/nonexistent/index.scip")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}
