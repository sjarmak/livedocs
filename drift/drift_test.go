package drift

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestExtractReadmeSymbols_BacktickIdentifiers(t *testing.T) {
	content := "Use `NewInformer` to create an informer. The `Store` interface provides `List` and `Get`."
	symbols, _ := ExtractReadmeSymbols(content)

	want := map[string]bool{
		"NewInformer": true,
		"Store":       true,
		"List":        true,
		"Get":         true,
	}
	for _, s := range symbols {
		if want[s] {
			delete(want, s)
		}
	}
	for missing := range want {
		t.Errorf("expected symbol %q not found", missing)
	}
}

func TestExtractReadmeSymbols_FiltersNonSymbols(t *testing.T) {
	content := "`-vmodule` flag, `-v` level, `%s` format, `key=value` pairs, `/path/to/file`"
	symbols, _ := ExtractReadmeSymbols(content)

	forbidden := map[string]bool{
		"-vmodule":  true,
		"-v":        true,
		"%s":        true,
		"key=value": true,
	}
	for _, s := range symbols {
		if forbidden[s] {
			t.Errorf("should have filtered out %q", s)
		}
	}
}

func TestExtractReadmeSymbols_PascalCaseIdentifiers(t *testing.T) {
	content := "The ErrorList type contains validation errors. Call NonEmpty to check."
	symbols, _ := ExtractReadmeSymbols(content)

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["ErrorList"] {
		t.Error("expected ErrorList in symbols")
	}
	if !symbolSet["NonEmpty"] {
		t.Error("expected NonEmpty in symbols")
	}
}

func TestExtractReadmeSymbols_FilterCommonWords(t *testing.T) {
	// Common English words should not be extracted from prose.
	content := "The This Example Code See Note Please Also"
	symbols, _ := ExtractReadmeSymbols(content)

	// None of these plain capitalized words should appear as symbols
	// since they lack code-like patterns (no dots, camelCase, snake_case,
	// or multiple uppercase transitions).
	if len(symbols) > 0 {
		t.Errorf("expected no symbols from plain English words, got %v", symbols)
	}
}

func TestExtractReadmeSymbols_PackagePaths(t *testing.T) {
	content := "Import `k8s.io/client-go/tools/cache` and `k8s.io/apimachinery/pkg/runtime`."
	_, pkgs := ExtractReadmeSymbols(content)

	pkgSet := make(map[string]bool)
	for _, p := range pkgs {
		pkgSet[p] = true
	}

	if !pkgSet["k8s.io/client-go/tools/cache"] {
		t.Error("expected k8s.io/client-go/tools/cache in package paths")
	}
	if !pkgSet["k8s.io/apimachinery/pkg/runtime"] {
		t.Error("expected k8s.io/apimachinery/pkg/runtime in package paths")
	}
}

func TestExtractReadmeSymbols_SkipsMarkdownLinkDefs(t *testing.T) {
	content := "[GoDocWidget]: https://godoc.org/k8s.io/client-go?status.svg\n[GoDocReference]:https://godoc.org/k8s.io/client-go"
	symbols, _ := ExtractReadmeSymbols(content)

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}
	// GoDocWidget and GoDocReference should not appear as symbols.
	if symbolSet["GoDocWidget"] {
		t.Error("GoDocWidget should be filtered (markdown link def)")
	}
	if symbolSet["GoDocReference"] {
		t.Error("GoDocReference should be filtered (markdown link def)")
	}
}

func TestExtractGoExports(t *testing.T) {
	// Create a temp dir with a Go file.
	dir := t.TempDir()
	goFile := filepath.Join(dir, "example.go")
	err := os.WriteFile(goFile, []byte(`package example

// Foo is exported.
func Foo() {}

// bar is unexported.
func bar() {}

// MyType is an exported type.
type MyType struct{}

// Process is a method on MyType.
func (m *MyType) Process() {}

// internal is unexported.
func (m *MyType) internal() {}

// MaxRetries is an exported constant.
const MaxRetries = 3

// unexportedConst is not exported.
const unexportedConst = 1

// DefaultName is an exported var.
var DefaultName = "test"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractGoExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	wantExported := []string{"Foo", "MyType", "MyType.Process", "MaxRetries", "DefaultName"}
	for _, want := range wantExported {
		if !symbolSet[want] {
			t.Errorf("expected exported symbol %q, got symbols: %v", want, symbols)
		}
	}

	wantNotExported := []string{"bar", "internal", "unexportedConst"}
	for _, unwant := range wantNotExported {
		if symbolSet[unwant] {
			t.Errorf("unexported symbol %q should not be in exports", unwant)
		}
	}
}

func TestExtractGoExports_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main
func Exported() {}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(`package main
func TestOnlySymbol() {}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractGoExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["Exported"] {
		t.Error("expected Exported symbol from main.go")
	}
	if symbolSet["TestOnlySymbol"] {
		t.Error("TestOnlySymbol from test file should be excluded")
	}
}

func TestDetect_StaleAndUndocumented(t *testing.T) {
	dir := t.TempDir()

	// Write a Go source file.
	err := os.WriteFile(filepath.Join(dir, "api.go"), []byte(`package api

// RealFunc is exported.
func RealFunc() {}

// AnotherExport is exported.
type AnotherExport struct{}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Write a README referencing one real and one stale symbol.
	readmePath := filepath.Join(dir, "README.md")
	err = os.WriteFile(readmePath, []byte("Use `RealFunc` for processing. Also see `RemovedFunc` for legacy support.\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	report, err := Detect(readmePath, "")
	if err != nil {
		t.Fatal(err)
	}

	if report.StaleCount == 0 {
		t.Error("expected at least one stale reference (RemovedFunc)")
	}

	// Check RemovedFunc is in stale findings.
	foundStale := false
	for _, f := range report.Findings {
		if f.Kind == StaleReference && f.Symbol == "RemovedFunc" {
			foundStale = true
		}
	}
	if !foundStale {
		t.Error("RemovedFunc should be a stale reference")
	}

	// AnotherExport should be undocumented.
	foundUndoc := false
	for _, f := range report.Findings {
		if f.Kind == Undocumented && f.Symbol == "AnotherExport" {
			foundUndoc = true
		}
	}
	if !foundUndoc {
		t.Error("AnotherExport should be undocumented")
	}
}

func TestDetect_EmptyCodeDir(t *testing.T) {
	dir := t.TempDir()
	readmePath := filepath.Join(dir, "README.md")
	err := os.WriteFile(readmePath, []byte("Use `SomeFunc` for processing.\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	report, err := Detect(readmePath, "")
	if err != nil {
		t.Fatal(err)
	}

	// All README symbols should be stale since there's no Go code.
	if report.StaleCount == 0 {
		t.Error("expected stale references when no Go code exists")
	}
}

func TestFormatReport(t *testing.T) {
	r := &Report{
		ReadmePath:        "/test/README.md",
		PackageDir:        "/test",
		ReadmeSymbols:     []string{"Foo", "Bar"},
		CodeSymbols:       []string{"Foo", "Baz"},
		StaleCount:        1,
		UndocumentedCount: 1,
		Findings: []Finding{
			{Kind: StaleReference, Symbol: "Bar", SourceFile: "/test/README.md"},
			{Kind: Undocumented, Symbol: "Baz", SourceFile: "/test"},
		},
	}

	output := FormatReport(r)
	if output == "" {
		t.Fatal("FormatReport returned empty string")
	}
	if !contains(output, "Bar") {
		t.Error("expected Bar in stale references section")
	}
	if !contains(output, "Baz") {
		t.Error("expected Baz in undocumented section")
	}
}

func TestDetectMultiple(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	for _, dir := range []string{dir1, dir2} {
		os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc Hello() {}\n"), 0644)
		os.WriteFile(filepath.Join(dir, "README.md"), []byte("Use `Hello` here.\n"), 0644)
	}

	targets := []Target{
		{ReadmePath: filepath.Join(dir1, "README.md")},
		{ReadmePath: filepath.Join(dir2, "README.md")},
	}
	reports, err := DetectMultiple(targets)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Errorf("expected 2 reports, got %d", len(reports))
	}
}

func TestExtractReadmeSymbols_FalsePositiveCapitalizedEnglish(t *testing.T) {
	// Capitalized English words at sentence start should NOT be extracted as symbols.
	content := "Backward compatibility is important. Breaking changes require review. Communication between components is key. Documentation should be current."
	symbols, _ := ExtractReadmeSymbols(content)

	forbidden := map[string]bool{
		"Backward":      true,
		"Breaking":      true,
		"Communication": true,
		"Documentation": true,
	}
	for _, s := range symbols {
		if forbidden[s] {
			t.Errorf("capitalized English word %q should NOT be treated as a code symbol", s)
		}
	}
}

func TestExtractReadmeSymbols_CodeLikeProseIdentifiers(t *testing.T) {
	// Words with code-like patterns in prose SHOULD be extracted.
	content := "The ConfigMap stores config. Use parseJSON to decode. Call pkg.NewClient for setup. Use my_handler for routing. The NewInformerFunc is important."
	symbols, _ := ExtractReadmeSymbols(content)

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	// camelCase: starts lowercase, has uppercase inside
	if !symbolSet["parseJSON"] {
		t.Error("expected camelCase identifier parseJSON in symbols")
	}

	// Dotted: pkg.NewClient
	if !symbolSet["pkg.NewClient"] {
		t.Error("expected dotted identifier pkg.NewClient in symbols")
	}

	// snake_case: contains underscore
	if !symbolSet["my_handler"] {
		t.Error("expected snake_case identifier my_handler in symbols")
	}

	// PascalCase with mixed case suffix that looks like a Go symbol (has both upper and lowercase after first char cluster)
	if !symbolSet["ConfigMap"] {
		t.Error("expected ConfigMap in symbols (uppercase transition mid-word)")
	}

	if !symbolSet["NewInformerFunc"] {
		t.Error("expected NewInformerFunc in symbols (multiple uppercase transitions)")
	}
}

func TestExtractReadmeSymbols_BacktickAlwaysExtracted(t *testing.T) {
	// Anything in backticks should be extracted regardless of pattern.
	content := "Use `Backward` and `Simple` as symbols when in backticks."
	symbols, _ := ExtractReadmeSymbols(content)

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["Backward"] {
		t.Error("expected Backward in symbols when in backticks")
	}
	if !symbolSet["Simple"] {
		t.Error("expected Simple in symbols when in backticks")
	}
}

func TestExtractReadmeSymbols_ProseOnlySimplePascalCaseExcluded(t *testing.T) {
	// Simple PascalCase words (single uppercase then all lowercase) in prose
	// should NOT be extracted -- they're likely just capitalized English.
	content := "Server handles requests. Handler processes events. Middleware wraps calls. Controller manages state."
	symbols, _ := ExtractReadmeSymbols(content)

	forbidden := map[string]bool{
		"Server":     true,
		"Handler":    true,
		"Middleware": true,
		"Controller": true,
	}
	for _, s := range symbols {
		if forbidden[s] {
			t.Errorf("simple PascalCase English word %q in prose should NOT be a symbol", s)
		}
	}
}

func TestExtractReadmeSymbols_RelativePackagePaths(t *testing.T) {
	content := "The `tools/cache` package is useful for writing controllers."
	_, pkgs := ExtractReadmeSymbols(content)

	pkgSet := make(map[string]bool)
	for _, p := range pkgs {
		pkgSet[p] = true
	}
	if !pkgSet["tools/cache"] {
		t.Errorf("expected tools/cache in package paths, got %v", pkgs)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && sort.SearchStrings([]string{s}, substr) >= 0 || // fallback
		len(substr) > 0 && findSubstring(s, substr)
}

func findSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
