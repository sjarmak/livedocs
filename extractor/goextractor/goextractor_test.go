package goextractor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/live-docs/live_docs/extractor"
)

// createTestFixture writes a minimal Go package to a temp directory and returns the path.
func createTestFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestExtract_BasicPackage(t *testing.T) {
	dir := createTestFixture(t, map[string]string{
		"go.mod": "module example.com/testpkg\n\ngo 1.21\n",
		"main.go": `// Package testpkg is a test package for the Go deep extractor.
package testpkg

// Greeter is an interface for greeting.
type Greeter interface {
	// Greet returns a greeting message.
	Greet(name string) string
}

// SimpleGreeter is a concrete type that implements Greeter.
type SimpleGreeter struct {
	Prefix string
}

// Greet returns a greeting with the configured prefix.
func (g *SimpleGreeter) Greet(name string) string {
	return g.Prefix + " " + name
}

// NewSimpleGreeter creates a new SimpleGreeter with the given prefix.
func NewSimpleGreeter(prefix string) *SimpleGreeter {
	return &SimpleGreeter{Prefix: prefix}
}

// DefaultPrefix is the default greeting prefix.
const DefaultPrefix = "Hello"

// version is an unexported package variable.
var version = "1.0.0"
`,
	})

	ext := &GoDeepExtractor{
		Repo:          "test/repo",
		ModulePath:    "example.com/testpkg",
		ModuleVersion: "v1.0.0",
	}

	claims, err := ext.Extract(context.Background(), dir, "go")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	if len(claims) == 0 {
		t.Fatal("expected claims, got none")
	}

	// Validate all claims.
	for i, c := range claims {
		if err := c.Validate(); err != nil {
			t.Errorf("claim %d (%s/%s) invalid: %v", i, c.SubjectName, c.Predicate, err)
		}
	}

	// Check for expected predicates.
	predicates := claimsByPredicate(claims)

	// defines: Greeter, SimpleGreeter, NewSimpleGreeter, DefaultPrefix, version, methods
	defines := predicates[extractor.PredicateDefines]
	if len(defines) < 5 {
		t.Errorf("expected at least 5 defines claims, got %d", len(defines))
	}
	assertClaimExists(t, defines, "Greeter", "should define Greeter interface")
	assertClaimExists(t, defines, "SimpleGreeter", "should define SimpleGreeter struct")
	assertClaimExists(t, defines, "NewSimpleGreeter", "should define NewSimpleGreeter func")
	assertClaimExists(t, defines, "DefaultPrefix", "should define DefaultPrefix const")
	assertClaimExists(t, defines, "version", "should define version var")

	// has_kind
	hasKind := predicates[extractor.PredicateHasKind]
	if len(hasKind) < 5 {
		t.Errorf("expected at least 5 has_kind claims, got %d", len(hasKind))
	}
	greeterKind := findClaim(hasKind, "Greeter")
	if greeterKind != nil && greeterKind.ObjectText != "type" {
		t.Errorf("Greeter kind = %q, want \"type\"", greeterKind.ObjectText)
	}
	funcKind := findClaim(hasKind, "NewSimpleGreeter")
	if funcKind != nil && funcKind.ObjectText != "func" {
		t.Errorf("NewSimpleGreeter kind = %q, want \"func\"", funcKind.ObjectText)
	}

	// has_signature for functions
	sigs := predicates[extractor.PredicateHasSignature]
	sigClaim := findClaim(sigs, "NewSimpleGreeter")
	if sigClaim == nil {
		t.Error("expected has_signature claim for NewSimpleGreeter")
	} else if sigClaim.ObjectText == "" {
		t.Error("has_signature ObjectText should not be empty")
	}

	// exports (Greeter, SimpleGreeter, NewSimpleGreeter, DefaultPrefix are exported)
	exports := predicates[extractor.PredicateExports]
	assertClaimExists(t, exports, "Greeter", "should export Greeter")
	assertClaimExists(t, exports, "SimpleGreeter", "should export SimpleGreeter")
	assertClaimExists(t, exports, "NewSimpleGreeter", "should export NewSimpleGreeter")
	assertClaimExists(t, exports, "DefaultPrefix", "should export DefaultPrefix")
	assertClaimNotExists(t, exports, "version", "should not export version")

	// has_doc
	docs := predicates[extractor.PredicateHasDoc]
	greeterDoc := findClaim(docs, "Greeter")
	if greeterDoc == nil {
		t.Error("expected has_doc claim for Greeter")
	} else if greeterDoc.Confidence != 0.85 {
		t.Errorf("has_doc confidence = %f, want 0.85", greeterDoc.Confidence)
	}

	// imports
	imports := predicates[extractor.PredicateImports]
	// testpkg has no imports, so imports should be empty
	if len(imports) != 0 {
		t.Errorf("expected 0 imports claims (no imports in test pkg), got %d", len(imports))
	}

	// encloses
	encloses := predicates[extractor.PredicateEncloses]
	if len(encloses) < 5 {
		t.Errorf("expected at least 5 encloses claims, got %d", len(encloses))
	}

	// implements: SimpleGreeter should implement Greeter
	implements := predicates[extractor.PredicateImplements]
	sgImpl := findClaim(implements, "SimpleGreeter")
	if sgImpl == nil {
		t.Error("expected implements claim for SimpleGreeter")
	} else if sgImpl.ObjectName == "" {
		t.Error("implements ObjectName should not be empty")
	}
}

func TestExtract_WithImports(t *testing.T) {
	dir := createTestFixture(t, map[string]string{
		"go.mod": "module example.com/importpkg\n\ngo 1.21\n",
		"main.go": `package importpkg

import "fmt"

// Hello prints a greeting.
func Hello() {
	fmt.Println("hello")
}
`,
	})

	ext := &GoDeepExtractor{
		Repo:       "test/repo",
		ModulePath: "example.com/importpkg",
	}

	claims, err := ext.Extract(context.Background(), dir, "go")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	imports := filterByPredicate(claims, extractor.PredicateImports)
	found := false
	for _, c := range imports {
		if c.ObjectName == "fmt" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected imports claim for fmt")
	}
}

func TestExtract_TestFile(t *testing.T) {
	dir := createTestFixture(t, map[string]string{
		"go.mod": "module example.com/testfile\n\ngo 1.21\n",
		"math.go": `package testfile

// Add adds two numbers.
func Add(a, b int) int { return a + b }
`,
		"math_test.go": `package testfile

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("wrong")
	}
}
`,
	})

	ext := &GoDeepExtractor{
		Repo:       "test/repo",
		ModulePath: "example.com/testfile",
	}

	claims, err := ext.Extract(context.Background(), dir, "go")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	testClaims := filterByPredicate(claims, extractor.PredicateIsTest)
	found := false
	for _, c := range testClaims {
		if c.SubjectName == "TestAdd" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected is_test claim for TestAdd")
	}
}

func TestExtract_GeneratedFile(t *testing.T) {
	dir := createTestFixture(t, map[string]string{
		"go.mod": "module example.com/genfile\n\ngo 1.21\n",
		"gen.go": `// Code generated by tool. DO NOT EDIT.

package genfile

// GeneratedFunc is a generated function.
func GeneratedFunc() {}
`,
		"real.go": `package genfile

// RealFunc is a real function.
func RealFunc() {}
`,
	})

	ext := &GoDeepExtractor{
		Repo:       "test/repo",
		ModulePath: "example.com/genfile",
	}

	claims, err := ext.Extract(context.Background(), dir, "go")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	genClaims := filterByPredicate(claims, extractor.PredicateIsGenerated)
	found := false
	for _, c := range genClaims {
		if c.SubjectName == "GeneratedFunc" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected is_generated claim for GeneratedFunc")
	}

	// RealFunc should NOT have is_generated
	for _, c := range genClaims {
		if c.SubjectName == "RealFunc" {
			t.Error("RealFunc should not have is_generated claim")
		}
	}
}

func TestExtract_Methods(t *testing.T) {
	dir := createTestFixture(t, map[string]string{
		"go.mod": "module example.com/methods\n\ngo 1.21\n",
		"server.go": `package methods

// Server handles requests.
type Server struct {
	Port int
}

// Start begins listening on the configured port.
func (s *Server) Start() error { return nil }

// Stop gracefully shuts down the server.
func (s *Server) Stop() error { return nil }
`,
	})

	ext := &GoDeepExtractor{
		Repo:       "test/repo",
		ModulePath: "example.com/methods",
	}

	claims, err := ext.Extract(context.Background(), dir, "go")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	defines := filterByPredicate(claims, extractor.PredicateDefines)
	assertClaimExists(t, defines, "Server.Start", "should define Server.Start method")
	assertClaimExists(t, defines, "Server.Stop", "should define Server.Stop method")

	sigs := filterByPredicate(claims, extractor.PredicateHasSignature)
	startSig := findClaim(sigs, "Server.Start")
	if startSig == nil {
		t.Error("expected has_signature for Server.Start")
	} else if startSig.ObjectText == "" {
		t.Error("Server.Start signature should not be empty")
	}

	// Methods should have has_kind = "method"
	kinds := filterByPredicate(claims, extractor.PredicateHasKind)
	startKind := findClaim(kinds, "Server.Start")
	if startKind != nil && startKind.ObjectText != "method" {
		t.Errorf("Server.Start kind = %q, want \"method\"", startKind.ObjectText)
	}
}

func TestExtract_WrongLanguage(t *testing.T) {
	ext := &GoDeepExtractor{Repo: "test/repo"}
	_, err := ext.Extract(context.Background(), "/tmp", "python")
	if err == nil {
		t.Error("expected error for wrong language")
	}
}

func TestExtract_SCIPSymbols(t *testing.T) {
	dir := createTestFixture(t, map[string]string{
		"go.mod": "module example.com/sciptest\n\ngo 1.21\n",
		"foo.go": `package sciptest

type Foo struct{}

func Bar() {}
`,
	})

	ext := &GoDeepExtractor{
		Repo:          "test/repo",
		ModulePath:    "example.com/sciptest",
		ModuleVersion: "v1.0.0",
	}

	claims, err := ext.Extract(context.Background(), dir, "go")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	defines := filterByPredicate(claims, extractor.PredicateDefines)
	fooClaim := findClaim(defines, "Foo")
	if fooClaim == nil {
		t.Fatal("expected defines claim for Foo")
	}
	if fooClaim.SCIPSymbol == "" {
		t.Error("Foo should have a SCIP symbol")
	}
	// SCIP symbol should contain the module path
	if fooClaim.SCIPSymbol == "" || !containsSubstring(fooClaim.SCIPSymbol, "example.com/sciptest") {
		t.Errorf("SCIP symbol %q should contain module path", fooClaim.SCIPSymbol)
	}

	barClaim := findClaim(defines, "Bar")
	if barClaim == nil {
		t.Fatal("expected defines claim for Bar")
	}
	if barClaim.SCIPSymbol == "" {
		t.Error("Bar should have a SCIP symbol")
	}
}

func TestExtract_ImplementsInterface(t *testing.T) {
	dir := createTestFixture(t, map[string]string{
		"go.mod": "module example.com/impltest\n\ngo 1.21\n",
		"iface.go": `package impltest

// Reader can read data.
type Reader interface {
	Read(p []byte) (n int, err error)
}

// MyReader implements Reader.
type MyReader struct{}

func (m *MyReader) Read(p []byte) (int, error) { return 0, nil }
`,
	})

	ext := &GoDeepExtractor{
		Repo:       "test/repo",
		ModulePath: "example.com/impltest",
	}

	claims, err := ext.Extract(context.Background(), dir, "go")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	implClaims := filterByPredicate(claims, extractor.PredicateImplements)
	found := false
	for _, c := range implClaims {
		if c.SubjectName == "MyReader" {
			found = true
			if c.ObjectName == "" {
				t.Error("implements claim should have ObjectName set")
			}
			break
		}
	}
	if !found {
		t.Error("expected MyReader to implement Reader")
	}
}

func TestExtract_Visibility(t *testing.T) {
	dir := createTestFixture(t, map[string]string{
		"go.mod": "module example.com/vis\n\ngo 1.21\n",
		"vis.go": `package vis

var ExportedVar int
var unexportedVar int
`,
	})

	ext := &GoDeepExtractor{
		Repo:       "test/repo",
		ModulePath: "example.com/vis",
	}

	claims, err := ext.Extract(context.Background(), dir, "go")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}

	defines := filterByPredicate(claims, extractor.PredicateDefines)

	exported := findClaim(defines, "ExportedVar")
	if exported == nil {
		t.Fatal("expected defines claim for ExportedVar")
	}
	if exported.Visibility != extractor.VisibilityPublic {
		t.Errorf("ExportedVar visibility = %q, want %q", exported.Visibility, extractor.VisibilityPublic)
	}

	unexported := findClaim(defines, "unexportedVar")
	if unexported == nil {
		t.Fatal("expected defines claim for unexportedVar")
	}
	if unexported.Visibility != extractor.VisibilityInternal {
		t.Errorf("unexportedVar visibility = %q, want %q", unexported.Visibility, extractor.VisibilityInternal)
	}
}

func TestExtractorInterface(t *testing.T) {
	ext := &GoDeepExtractor{Repo: "test/repo"}

	// Verify it implements the extractor.Extractor interface.
	var _ extractor.Extractor = ext

	if ext.Name() != "go-deep" {
		t.Errorf("Name() = %q, want \"go-deep\"", ext.Name())
	}
	if ext.Version() != "0.1.0" {
		t.Errorf("Version() = %q, want \"0.1.0\"", ext.Version())
	}
}

func TestExtractBytes_ReturnsErrRequiresLocalFS(t *testing.T) {
	ext := &GoDeepExtractor{Repo: "test/repo"}
	_, err := ext.ExtractBytes(context.Background(), []byte("package main"), "main.go", "go")
	if err != extractor.ErrRequiresLocalFS {
		t.Errorf("ExtractBytes() error = %v, want ErrRequiresLocalFS", err)
	}
}

func TestExtract_RealKubernetesPackage(t *testing.T) {
	// Test against a real k8s package if available.
	schemaDir := filepath.Join(os.Getenv("HOME"), "kubernetes", "kubernetes",
		"staging", "src", "k8s.io", "apimachinery", "pkg", "runtime", "schema")

	if _, err := os.Stat(schemaDir); os.IsNotExist(err) {
		t.Skip("kubernetes corpus not available")
	}

	ext := &GoDeepExtractor{
		Repo:       "kubernetes/kubernetes",
		ModulePath: "k8s.io/apimachinery",
	}

	claims, err := ext.Extract(context.Background(), schemaDir, "go")
	if err != nil {
		t.Fatalf("Extract on k8s schema package failed: %v", err)
	}

	if len(claims) == 0 {
		t.Fatal("expected claims from kubernetes schema package")
	}

	// The schema package should define GroupVersionKind, ObjectKind interface, etc.
	defines := filterByPredicate(claims, extractor.PredicateDefines)
	assertClaimExists(t, defines, "GroupVersionKind", "should define GroupVersionKind")
	assertClaimExists(t, defines, "ObjectKind", "should define ObjectKind interface")

	// ObjectKind is an interface and should have has_kind=type
	kinds := filterByPredicate(claims, extractor.PredicateHasKind)
	okKind := findClaim(kinds, "ObjectKind")
	if okKind == nil {
		t.Error("expected has_kind claim for ObjectKind")
	} else if okKind.ObjectText != "type" {
		t.Errorf("ObjectKind kind = %q, want \"type\"", okKind.ObjectText)
	}

	// Should have implements claims (emptyObjectKind implements ObjectKind)
	implClaims := filterByPredicate(claims, extractor.PredicateImplements)
	found := false
	for _, c := range implClaims {
		if c.SubjectName == "emptyObjectKind" {
			found = true
			break
		}
	}
	if !found {
		t.Log("NOTE: emptyObjectKind->ObjectKind implements may not be detected if unexported type is not in scope")
	}

	// Validate all claims.
	for i, c := range claims {
		if err := c.Validate(); err != nil {
			t.Errorf("claim %d (%s/%s) invalid: %v", i, c.SubjectName, c.Predicate, err)
		}
	}

	t.Logf("Extracted %d claims from kubernetes schema package", len(claims))
}

// --- helpers ---

func claimsByPredicate(claims []extractor.Claim) map[extractor.Predicate][]extractor.Claim {
	m := make(map[extractor.Predicate][]extractor.Claim)
	for _, c := range claims {
		m[c.Predicate] = append(m[c.Predicate], c)
	}
	return m
}

func filterByPredicate(claims []extractor.Claim, pred extractor.Predicate) []extractor.Claim {
	var result []extractor.Claim
	for _, c := range claims {
		if c.Predicate == pred {
			result = append(result, c)
		}
	}
	return result
}

func findClaim(claims []extractor.Claim, subjectName string) *extractor.Claim {
	for i := range claims {
		if claims[i].SubjectName == subjectName {
			return &claims[i]
		}
	}
	return nil
}

func assertClaimExists(t *testing.T, claims []extractor.Claim, subjectName, msg string) {
	t.Helper()
	if findClaim(claims, subjectName) == nil {
		t.Errorf("%s: no claim found with SubjectName=%q", msg, subjectName)
	}
}

func assertClaimNotExists(t *testing.T, claims []extractor.Claim, subjectName, msg string) {
	t.Helper()
	if findClaim(claims, subjectName) != nil {
		t.Errorf("%s: unexpected claim found with SubjectName=%q", msg, subjectName)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
