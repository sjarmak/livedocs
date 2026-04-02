package renderer

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/live-docs/live_docs/db"
)

func tempDB(t *testing.T) *db.ClaimsDB {
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

func mustUpsertSymbol(t *testing.T, cdb *db.ClaimsDB, s db.Symbol) int64 {
	t.Helper()
	id, err := cdb.UpsertSymbol(s)
	if err != nil {
		t.Fatalf("upsert symbol %s: %v", s.SymbolName, err)
	}
	return id
}

func mustInsertClaim(t *testing.T, cdb *db.ClaimsDB, cl db.Claim) {
	t.Helper()
	_, err := cdb.InsertClaim(cl)
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}
}

func newStructuralClaim(subjectID int64, predicate, objectText, sourceFile string) db.Claim {
	return db.Claim{
		SubjectID:        subjectID,
		Predicate:        predicate,
		ObjectText:       objectText,
		SourceFile:       sourceFile,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "go-deep",
		ExtractorVersion: "0.1.0",
		LastVerified:     db.Now(),
	}
}

// seedKubeletConfig populates a claims DB with data approximating
// k8s.io/kubernetes/pkg/kubelet/config for integration testing.
func seedKubeletConfig(t *testing.T, cdb *db.ClaimsDB) {
	t.Helper()
	repo := "kubernetes/kubernetes"
	importPath := "k8s.io/kubernetes/pkg/kubelet/config"
	lang := "go"

	// Types
	podConfigID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "PodConfig",
		Language: lang, Kind: "type", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(podConfigID, "has_kind", "struct", "config.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(podConfigID, "defines", "PodConfig", "config.go"))

	sourcesReadyID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "SourcesReady",
		Language: lang, Kind: "type", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(sourcesReadyID, "has_kind", "interface", "config.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(sourcesReadyID, "encloses", "AllReady", "config.go"))

	sourcesReadyFnID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "SourcesReadyFn",
		Language: lang, Kind: "type", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(sourcesReadyFnID, "has_kind", "func_type", "config.go"))
	// SourcesReadyFn implements SourcesReady
	mustInsertClaim(t, cdb, newStructuralClaim(sourcesReadyFnID, "implements", "SourcesReady", "config.go"))

	// Exported functions
	newPodConfigID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "NewPodConfig",
		Language: lang, Kind: "func", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(newPodConfigID, "has_kind", "constructor", "config.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(newPodConfigID, "defines", "NewPodConfig", "config.go"))

	newSourceApiserverID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "NewSourceApiserver",
		Language: lang, Kind: "func", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(newSourceApiserverID, "has_kind", "constructor", "apiserver.go"))

	newSourceFileID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "NewSourceFile",
		Language: lang, Kind: "func", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(newSourceFileID, "has_kind", "constructor", "file.go"))

	newSourceURLID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "NewSourceURL",
		Language: lang, Kind: "func", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(newSourceURLID, "has_kind", "constructor", "http.go"))

	newSourcesReadyID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "NewSourcesReady",
		Language: lang, Kind: "func", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(newSourcesReadyID, "has_kind", "constructor", "config.go"))

	// Forward dependencies (imports)
	// Use the package-level pseudo-symbol to hold imports.
	pkgID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "_package_",
		Language: lang, Kind: "module", Visibility: "public",
	})
	forwardDeps := []string{
		"k8s.io/api/core/v1",
		"k8s.io/apimachinery/pkg/apis/meta/v1",
		"k8s.io/apimachinery/pkg/fields",
		"k8s.io/apimachinery/pkg/runtime",
		"k8s.io/apimachinery/pkg/types",
		"k8s.io/client-go/kubernetes",
		"k8s.io/client-go/tools/cache",
		"k8s.io/klog/v2",
	}
	for _, dep := range forwardDeps {
		mustInsertClaim(t, cdb, newStructuralClaim(pkgID, "imports", dep, "config.go"))
	}

	// Reverse dependencies
	reverseDeps := []string{
		"k8s.io/kubernetes/cmd/kubelet/app",
		"k8s.io/kubernetes/pkg/kubelet",
		"k8s.io/kubernetes/pkg/kubelet/cm",
	}
	for _, rdep := range reverseDeps {
		mustInsertClaim(t, cdb, newStructuralClaim(pkgID, "exports", "reverse_dep:"+rdep, "config.go"))
	}

	// Test file markers
	testSymID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "TestPodConfig",
		Language: lang, Kind: "func", Visibility: "private",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(testSymID, "is_test", "true", "config_test.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(testSymID, "defines", "TestPodConfig", "config_test.go"))

	testSym2ID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "TestSourceFile",
		Language: lang, Kind: "func", Visibility: "private",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(testSym2ID, "is_test", "true", "file_test.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(testSym2ID, "defines", "TestSourceFile", "file_test.go"))
}

func TestLoadPackageData_KubeletConfig(t *testing.T) {
	cdb := tempDB(t)
	seedKubeletConfig(t, cdb)

	pd, err := LoadPackageData(cdb, "k8s.io/kubernetes/pkg/kubelet/config")
	if err != nil {
		t.Fatalf("LoadPackageData: %v", err)
	}

	if pd.ImportPath != "k8s.io/kubernetes/pkg/kubelet/config" {
		t.Errorf("import path = %q, want k8s.io/kubernetes/pkg/kubelet/config", pd.ImportPath)
	}
	if pd.Language != "go" {
		t.Errorf("language = %q, want go", pd.Language)
	}

	// Interfaces
	if len(pd.Interfaces) != 1 {
		t.Fatalf("interfaces = %d, want 1", len(pd.Interfaces))
	}
	if pd.Interfaces[0].Name != "SourcesReady" {
		t.Errorf("interface name = %q, want SourcesReady", pd.Interfaces[0].Name)
	}
	if len(pd.Interfaces[0].Methods) != 1 || pd.Interfaces[0].Methods[0] != "AllReady" {
		t.Errorf("interface methods = %v, want [AllReady]", pd.Interfaces[0].Methods)
	}

	// Interface satisfaction
	if len(pd.InterfaceSatisfactions) != 1 {
		t.Fatalf("satisfactions = %d, want 1", len(pd.InterfaceSatisfactions))
	}
	if pd.InterfaceSatisfactions[0].ConcreteType != "SourcesReadyFn" {
		t.Errorf("concrete type = %q, want SourcesReadyFn", pd.InterfaceSatisfactions[0].ConcreteType)
	}

	// Forward deps
	if len(pd.ForwardDeps) != 8 {
		t.Errorf("forward deps = %d, want 8", len(pd.ForwardDeps))
	}

	// Reverse deps
	if len(pd.ReverseDeps) != 3 {
		t.Errorf("reverse deps = %d, want 3", len(pd.ReverseDeps))
	}
	if pd.ReverseDepCount != 3 {
		t.Errorf("reverse dep count = %d, want 3", pd.ReverseDepCount)
	}

	// Function categories
	foundConstructors := false
	for _, cat := range pd.FunctionCategories {
		if cat.Name == "constructor" {
			foundConstructors = true
			if len(cat.Functions) != 5 {
				t.Errorf("constructor functions = %d, want 5: %v", len(cat.Functions), cat.Functions)
			}
		}
	}
	if !foundConstructors {
		t.Error("expected 'constructor' function category")
	}

	// Test files
	if len(pd.TestFiles) != 2 {
		t.Errorf("test files = %d, want 2", len(pd.TestFiles))
	}
}

func TestLoadPackageData_EmptyImportPath(t *testing.T) {
	cdb := tempDB(t)
	_, err := LoadPackageData(cdb, "nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent import path")
	}
}

func TestRenderMarkdown_KubeletConfig(t *testing.T) {
	cdb := tempDB(t)
	seedKubeletConfig(t, cdb)

	pd, err := LoadPackageData(cdb, "k8s.io/kubernetes/pkg/kubelet/config")
	if err != nil {
		t.Fatalf("LoadPackageData: %v", err)
	}

	md := RenderMarkdown(pd)

	// Verify key structural elements are present.
	checks := []struct {
		name    string
		content string
	}{
		{"title", "# k8s.io/kubernetes/pkg/kubelet/config"},
		{"tier 1 marker", "Tier 1"},
		{"import path", "**Import path:** `k8s.io/kubernetes/pkg/kubelet/config`"},
		{"language", "**Language:** go"},
		{"interface section", "## Exported Interfaces (1)"},
		{"interface table header", "| Interface | Methods | Key Implementations |"},
		{"SourcesReady interface", "| `SourcesReady` |"},
		{"implements section", "## Implements"},
		{"SourcesReadyFn implements", "`SourcesReadyFn` implements `SourcesReady`"},
		{"cross-package section", "## Cross-Package References"},
		{"forward dep", "`k8s.io/api/core/v1`"},
		{"forward dep cache", "`k8s.io/client-go/tools/cache`"},
		{"used by section", "## Used By"},
		{"used by count", "3 packages depend on this package"},
		{"reverse dep kubelet", "`k8s.io/kubernetes/pkg/kubelet`"},
		{"function cat section", "## Exported Functions by Category"},
		{"constructor category", "**constructor (5):**"},
		{"NewPodConfig", "NewPodConfig"},
		{"NewSourceApiserver", "NewSourceApiserver"},
		{"test coverage section", "## Test Coverage"},
		{"test file count", "2 test files"},
	}

	for _, check := range checks {
		if !strings.Contains(md, check.content) {
			t.Errorf("missing %s: expected output to contain %q", check.name, check.content)
		}
	}
}

func TestRenderMarkdown_EmptyPackage(t *testing.T) {
	pd := &PackageData{
		ImportPath: "empty/pkg",
		Language:   "go",
	}
	md := RenderMarkdown(pd)

	if !strings.Contains(md, "# empty/pkg") {
		t.Error("missing title")
	}
	// Should not contain section headers for empty data.
	if strings.Contains(md, "## Exported Interfaces") {
		t.Error("should not render empty interfaces section")
	}
	if strings.Contains(md, "## Implements") {
		t.Error("should not render empty implements section")
	}
	if strings.Contains(md, "## Used By") {
		t.Error("should not render empty used by section")
	}
	if strings.Contains(md, "## Cross-Package References") {
		t.Error("should not render empty cross-package references section")
	}
}

// TestRenderMarkdown_InterfaceHeavyPackage tests the renderer with a more
// complex package resembling client-go/tools/cache with multiple interfaces
// and implementations.
func TestRenderMarkdown_InterfaceHeavyPackage(t *testing.T) {
	cdb := tempDB(t)
	repo := "kubernetes/kubernetes"
	importPath := "k8s.io/client-go/tools/cache"
	lang := "go"

	// Store interface
	storeID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "Store",
		Language: lang, Kind: "type", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(storeID, "has_kind", "interface", "store.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(storeID, "encloses", "Add", "store.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(storeID, "encloses", "Update", "store.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(storeID, "encloses", "Delete", "store.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(storeID, "encloses", "List", "store.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(storeID, "encloses", "Get", "store.go"))

	// Indexer interface (embeds Store)
	indexerID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "Indexer",
		Language: lang, Kind: "type", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(indexerID, "has_kind", "interface", "index.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(indexerID, "encloses", "Index", "index.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(indexerID, "encloses", "IndexKeys", "index.go"))

	// DeltaFIFO implements Store (and more)
	deltaID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "DeltaFIFO",
		Language: lang, Kind: "type", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(deltaID, "has_kind", "struct", "delta_fifo.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(deltaID, "implements", "Store", "delta_fifo.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(deltaID, "implements", "TransformingStore", "delta_fifo.go"))

	// ExpirationCache implements Store
	expirationID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "ExpirationCache",
		Language: lang, Kind: "type", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(expirationID, "has_kind", "struct", "expiration_cache.go"))
	mustInsertClaim(t, cdb, newStructuralClaim(expirationID, "implements", "Store", "expiration_cache.go"))

	// Constructor functions
	newStoreID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "NewStore",
		Language: lang, Kind: "func", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(newStoreID, "has_kind", "store constructor", "store.go"))

	newIndexerID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "NewIndexer",
		Language: lang, Kind: "func", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(newIndexerID, "has_kind", "store constructor", "store.go"))

	newDeltaFIFOID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "NewDeltaFIFO",
		Language: lang, Kind: "func", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(newDeltaFIFOID, "has_kind", "queue constructor", "delta_fifo.go"))

	keyFuncID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "MetaNamespaceKeyFunc",
		Language: lang, Kind: "func", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(keyFuncID, "has_kind", "key function", "store.go"))

	pd, err := LoadPackageData(cdb, importPath)
	if err != nil {
		t.Fatalf("LoadPackageData: %v", err)
	}

	md := RenderMarkdown(pd)

	// Verify interface table
	if !strings.Contains(md, "## Exported Interfaces (2)") {
		t.Error("expected 2 interfaces (Store, Indexer)")
	}
	if !strings.Contains(md, "`Store`") {
		t.Error("missing Store interface in table")
	}
	if !strings.Contains(md, "`Indexer`") {
		t.Error("missing Indexer interface in table")
	}

	// Verify implementations appear in the interface table
	if !strings.Contains(md, "DeltaFIFO") {
		t.Error("missing DeltaFIFO as Store implementation")
	}
	if !strings.Contains(md, "ExpirationCache") {
		t.Error("missing ExpirationCache as Store implementation")
	}

	// Verify implements section
	if !strings.Contains(md, "`DeltaFIFO` implements") {
		t.Error("missing DeltaFIFO satisfaction")
	}
	if !strings.Contains(md, "`Store`") && !strings.Contains(md, "`TransformingStore`") {
		t.Error("missing interfaces in DeltaFIFO satisfaction")
	}

	// Verify function categories
	if !strings.Contains(md, "store constructor") {
		t.Error("missing store constructor category")
	}
	if !strings.Contains(md, "queue constructor") {
		t.Error("missing queue constructor category")
	}
	if !strings.Contains(md, "key function") {
		t.Error("missing key function category")
	}
}

func TestRenderMarkdown_TypeCategories(t *testing.T) {
	cdb := tempDB(t)
	repo := "kubernetes/kubernetes"
	importPath := "k8s.io/api/core/v1"

	podID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "Pod",
		Language: "go", Kind: "type", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(podID, "has_kind", "workload resource", "types.go"))

	serviceID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "Service",
		Language: "go", Kind: "type", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(serviceID, "has_kind", "networking resource", "types.go"))

	configMapID := mustUpsertSymbol(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "ConfigMap",
		Language: "go", Kind: "type", Visibility: "public",
	})
	mustInsertClaim(t, cdb, newStructuralClaim(configMapID, "has_kind", "configuration resource", "types.go"))

	pd, err := LoadPackageData(cdb, importPath)
	if err != nil {
		t.Fatalf("LoadPackageData: %v", err)
	}

	md := RenderMarkdown(pd)

	if !strings.Contains(md, "## Exported Types by Category") {
		t.Error("missing type categories section")
	}
	if !strings.Contains(md, "workload resource") {
		t.Error("missing workload resource category")
	}
	if !strings.Contains(md, "Pod") {
		t.Error("missing Pod in type categories")
	}
}

func TestRenderMarkdown_ForwardDepAnnotations(t *testing.T) {
	pd := &PackageData{
		ImportPath: "test/pkg",
		Language:   "go",
		ForwardDeps: []Dependency{
			{ImportPath: "k8s.io/api/core/v1", Annotation: "Pod types"},
			{ImportPath: "k8s.io/klog/v2"},
		},
	}

	md := RenderMarkdown(pd)

	if !strings.Contains(md, "`k8s.io/api/core/v1` — Pod types") {
		t.Error("missing annotated dependency")
	}
	if !strings.Contains(md, "- `k8s.io/klog/v2`\n") {
		t.Error("missing unannotated dependency")
	}
}

func TestRenderMarkdown_MultipleInterfaceSatisfaction(t *testing.T) {
	pd := &PackageData{
		ImportPath: "test/pkg",
		Language:   "go",
		InterfaceSatisfactions: []Satisfaction{
			{ConcreteType: "Scheme", Interfaces: []string{"ObjectConvertor", "ObjectCreater", "ObjectDefaulter", "ObjectTyper"}},
		},
	}

	md := RenderMarkdown(pd)

	if !strings.Contains(md, "`Scheme` implements `ObjectConvertor`, `ObjectCreater`, `ObjectDefaulter`, `ObjectTyper`") {
		t.Errorf("unexpected satisfaction format in:\n%s", md)
	}
}
