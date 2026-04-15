package tribal

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssertionExtractorRequireNoError(t *testing.T) {
	src := []byte(`package x

func run(t T) {
	err := doIt()
	require.NoError(t, err)
}
`)
	facts, err := ExtractInvariants("x.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	f := facts[0]
	if f.Kind != "invariant" {
		t.Errorf("kind: want invariant, got %q", f.Kind)
	}
	if f.Model != "" {
		t.Errorf("model: want empty, got %q", f.Model)
	}
	if f.Confidence != 1.0 {
		t.Errorf("confidence: want 1.0, got %v", f.Confidence)
	}
	if !strings.Contains(f.Body, "require.NoError") {
		t.Errorf("body should mention require.NoError, got %q", f.Body)
	}
	if !strings.Contains(f.Body, "run") {
		t.Errorf("body should contain enclosing func name 'run', got %q", f.Body)
	}
}

func TestAssertionExtractorRequireError(t *testing.T) {
	src := []byte(`package x

func run(t T) {
	require.Error(t, maybeFail())
}
`)
	facts, err := ExtractInvariants("x.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if !strings.Contains(facts[0].Body, "require.Error") {
		t.Errorf("body should mention require.Error, got %q", facts[0].Body)
	}
}

func TestAssertionExtractorAssertTrue(t *testing.T) {
	src := []byte(`package x

func run(t T) {
	assert.True(t, ok())
}
`)
	facts, err := ExtractInvariants("x.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if !strings.Contains(facts[0].Body, "assert.True") {
		t.Errorf("body should mention assert.True, got %q", facts[0].Body)
	}
}

func TestAssertionExtractorAssertFalse(t *testing.T) {
	src := []byte(`package x

func run(t T) {
	assert.False(t, broken())
}
`)
	facts, err := ExtractInvariants("x.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if !strings.Contains(facts[0].Body, "assert.False") {
		t.Errorf("body should mention assert.False, got %q", facts[0].Body)
	}
}

func TestAssertionExtractorPanicStringLiteral(t *testing.T) {
	src := []byte(`package x

func run() {
	panic("unreachable")
}
`)
	facts, err := ExtractInvariants("x.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if !strings.Contains(facts[0].Body, "panic") {
		t.Errorf("body should mention panic, got %q", facts[0].Body)
	}
	if !strings.Contains(facts[0].Body, "unreachable") {
		t.Errorf("body should contain the literal text, got %q", facts[0].Body)
	}
}

func TestAssertionExtractorPanicNonLiteralIgnored(t *testing.T) {
	// panic with a variable argument is NOT a matched invariant.
	src := []byte(`package x

func run(err error) {
	panic(err)
}
`)
	facts, err := ExtractInvariants("x.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("expected 0 facts (panic with non-literal), got %d", len(facts))
	}
}

func TestAssertionExtractorNolintWithReason(t *testing.T) {
	src := []byte(`package x

func run() {
	//nolint:errcheck // deliberately ignore result
	_ = doIt()
}
`)
	facts, err := ExtractInvariants("x.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	f := facts[0]
	if !strings.Contains(f.Body, "nolint") {
		t.Errorf("body should mention nolint, got %q", f.Body)
	}
	if !strings.Contains(f.Body, "errcheck") {
		t.Errorf("body should include linter name 'errcheck', got %q", f.Body)
	}
	if !strings.Contains(f.Body, "deliberately ignore result") {
		t.Errorf("body should include reason, got %q", f.Body)
	}
	if !strings.Contains(f.Body, "run") {
		t.Errorf("body should include enclosing func name 'run', got %q", f.Body)
	}
}

func TestAssertionExtractorFactMetadata(t *testing.T) {
	src := []byte(`package x

func run(t T) {
	require.NoError(t, err)
}
`)
	facts, err := ExtractInvariants("pkg/file.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	f := facts[0]
	if f.Kind != "invariant" {
		t.Errorf("kind: want invariant, got %q", f.Kind)
	}
	if f.Extractor != "assertion" {
		t.Errorf("extractor: want assertion, got %q", f.Extractor)
	}
	if f.ExtractorVersion == "" {
		t.Error("extractor version should be non-empty")
	}
	if f.Model != "" {
		t.Errorf("model: want empty (deterministic), got %q", f.Model)
	}
	if f.Confidence != 1.0 {
		t.Errorf("confidence: want 1.0, got %v", f.Confidence)
	}
	if f.Status != "active" {
		t.Errorf("status: want active, got %q", f.Status)
	}
	if f.StalenessHash == "" {
		t.Error("staleness hash should be non-empty")
	}

	if len(f.Evidence) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(f.Evidence))
	}
	ev := f.Evidence[0]
	if ev.SourceType != "assertion" {
		t.Errorf("evidence source_type: want assertion, got %q", ev.SourceType)
	}
	// require.NoError sits on line 4 of the source.
	if ev.SourceRef != "pkg/file.go:4" {
		t.Errorf("evidence source_ref: want pkg/file.go:4, got %q", ev.SourceRef)
	}
	if f.SourceQuote == "" {
		t.Error("source_quote should be non-empty (verbatim line)")
	}
	if !strings.Contains(f.SourceQuote, "require.NoError") {
		t.Errorf("source_quote should contain the verbatim assertion, got %q", f.SourceQuote)
	}
}

func TestAssertionExtractorSubjectResolutionMethod(t *testing.T) {
	src := []byte(`package x

type S struct{}

func (s *S) Run(t T) {
	require.NoError(t, err)
}
`)
	facts, err := ExtractInvariants("x.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if !strings.Contains(facts[0].Body, "S.Run") {
		t.Errorf("body should include method receiver type prefix S.Run, got %q", facts[0].Body)
	}
}

func TestAssertionExtractorIntegrationFixture(t *testing.T) {
	path := filepath.Join("testdata", "invariant_sample.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	facts, err := ExtractInvariants(path, src)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(facts) < 6 {
		t.Fatalf("expected at least 6 facts from synthetic fixture, got %d", len(facts))
	}

	// Confirm every pattern type produced at least one fact.
	type check struct {
		name  string
		match func(body string) bool
	}
	checks := []check{
		{"require.NoError", func(b string) bool { return strings.Contains(b, "require.NoError") }},
		{"require.Error", func(b string) bool { return strings.Contains(b, "require.Error") }},
		{"assert.True", func(b string) bool { return strings.Contains(b, "assert.True") }},
		{"assert.False", func(b string) bool { return strings.Contains(b, "assert.False") }},
		{"panic literal", func(b string) bool {
			return strings.Contains(b, "panic(") && strings.Contains(b, "invariant violated")
		}},
		{"nolint", func(b string) bool { return strings.Contains(b, "nolint") }},
	}
	for _, c := range checks {
		found := false
		for _, f := range facts {
			if c.match(f.Body) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no fact produced for pattern %q", c.name)
		}
	}

	// Every fact must be kind=invariant with model="".
	for i, f := range facts {
		if f.Kind != "invariant" {
			t.Errorf("facts[%d].Kind: want invariant, got %q", i, f.Kind)
		}
		if f.Model != "" {
			t.Errorf("facts[%d].Model: want empty, got %q", i, f.Model)
		}
		if f.Confidence != 1.0 {
			t.Errorf("facts[%d].Confidence: want 1.0, got %v", i, f.Confidence)
		}
	}
}

// TestAssertionExtractorNoCrossPackageImports parses assertion.go itself
// and verifies the extractor has no imports from mcpserver/, cmd/, or
// sub-packages of db/. Importing the db root package (for TribalFact types)
// is permitted — matches the inline_marker.go convention.
func TestAssertionExtractorNoCrossPackageImports(t *testing.T) {
	src, err := os.ReadFile("assertion.go")
	if err != nil {
		t.Fatalf("read assertion.go: %v", err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "assertion.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse assertion.go: %v", err)
	}

	const modPrefix = "github.com/live-docs/live_docs/"
	forbiddenPrefixes := []string{
		modPrefix + "mcpserver",
		modPrefix + "cmd",
		modPrefix + "db/", // db sub-packages
	}

	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		for _, bad := range forbiddenPrefixes {
			if strings.HasPrefix(path, bad) {
				t.Errorf("assertion.go imports forbidden package %q (matches %q)", path, bad)
			}
		}
	}
}
