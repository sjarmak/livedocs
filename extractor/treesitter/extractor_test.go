package treesitter_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/live-docs/live_docs/extractor"
	"github.com/live-docs/live_docs/extractor/lang"
	"github.com/live-docs/live_docs/extractor/treesitter"
)

func newExtractor() *treesitter.UniversalExtractor {
	return treesitter.New(lang.NewRegistry())
}

func testdataPath(name string) string {
	return filepath.Join("testdata", name)
}

// claimsByPredicate filters claims by predicate string.
func claimsByPredicate(claims []extractor.Claim, pred extractor.Predicate) []extractor.Claim {
	var out []extractor.Claim
	for _, c := range claims {
		if c.Predicate == pred {
			out = append(out, c)
		}
	}
	return out
}

// claimNames returns the SubjectName of each claim.
func claimNames(claims []extractor.Claim) []string {
	names := make([]string, len(claims))
	for i, c := range claims {
		names[i] = c.SubjectName
	}
	return names
}

func containsName(claims []extractor.Claim, name string) bool {
	for _, c := range claims {
		if c.SubjectName == name {
			return true
		}
	}
	return false
}

// --- Interface compliance ---

func TestUniversalExtractorImplementsTreeSitterExtractor(t *testing.T) {
	t.Parallel()
	var _ extractor.TreeSitterExtractor = newExtractor()
}

func TestNameAndVersion(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	if e.Name() != "tree-sitter" {
		t.Errorf("Name() = %q, want %q", e.Name(), "tree-sitter")
	}
	if e.Version() == "" {
		t.Error("Version() is empty")
	}
}

// --- Go extraction ---

func TestExtractGo_Defines(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.go"), "go")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	defines := claimsByPredicate(claims, extractor.PredicateDefines)
	names := claimNames(defines)
	t.Logf("Go defines: %v", names)

	// Expect: Greet, Server, Start
	for _, want := range []string{"Greet", "Server", "Start"} {
		if !containsName(defines, want) {
			t.Errorf("missing defines claim for %q; got %v", want, names)
		}
	}
}

func TestExtractGo_Imports(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.go"), "go")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	imports := claimsByPredicate(claims, extractor.PredicateImports)
	if len(imports) == 0 {
		t.Fatal("expected at least one imports claim")
	}
	// The import declaration should reference fmt and os.
	found := false
	for _, c := range imports {
		if c.ObjectName != "" {
			found = true
		}
	}
	if !found {
		t.Error("imports claim has no ObjectName")
	}
}

func TestExtractGo_HasDoc(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.go"), "go")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	docs := claimsByPredicate(claims, extractor.PredicateHasDoc)
	if len(docs) == 0 {
		t.Fatal("expected at least one has_doc claim")
	}
	for _, d := range docs {
		if d.Confidence != 0.85 {
			t.Errorf("has_doc confidence = %f, want 0.85", d.Confidence)
		}
	}
}

func TestExtractGo_Visibility(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.go"), "go")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	defines := claimsByPredicate(claims, extractor.PredicateDefines)
	for _, c := range defines {
		switch c.SubjectName {
		case "Greet", "Server", "Start":
			if c.Visibility != extractor.VisibilityPublic {
				t.Errorf("%s visibility = %q, want public", c.SubjectName, c.Visibility)
			}
		case "defaultTimeout":
			// unexported — tree-sitter may or may not catch this as a var
		}
	}
}

func TestExtractGo_IsTest(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample_test.go"), "go")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	testClaims := claimsByPredicate(claims, extractor.PredicateIsTest)
	if len(testClaims) == 0 {
		t.Fatal("expected is_test claim for test file")
	}
}

func TestExtractGo_IsGenerated(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("generated.go"), "go")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	genClaims := claimsByPredicate(claims, extractor.PredicateIsGenerated)
	if len(genClaims) == 0 {
		t.Fatal("expected is_generated claim for generated file")
	}
}

// --- Python extraction ---

func TestExtractPython_Defines(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.py"), "python")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	defines := claimsByPredicate(claims, extractor.PredicateDefines)
	names := claimNames(defines)
	t.Logf("Python defines: %v", names)

	for _, want := range []string{"Greeter", "greet", "main"} {
		if !containsName(defines, want) {
			t.Errorf("missing defines claim for %q; got %v", want, names)
		}
	}
}

func TestExtractPython_Imports(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.py"), "python")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	imports := claimsByPredicate(claims, extractor.PredicateImports)
	if len(imports) < 2 {
		t.Fatalf("expected at least 2 imports claims, got %d", len(imports))
	}
}

func TestExtractPython_Docstrings(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.py"), "python")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	docs := claimsByPredicate(claims, extractor.PredicateHasDoc)
	// Module docstring, class docstring, method docstrings, comments
	if len(docs) == 0 {
		t.Fatal("expected has_doc claims for Python docstrings")
	}
}

// --- TypeScript extraction ---

func TestExtractTypeScript_Defines(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.ts"), "typescript")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	defines := claimsByPredicate(claims, extractor.PredicateDefines)
	names := claimNames(defines)
	t.Logf("TypeScript defines: %v", names)

	for _, want := range []string{"Greeter", "SimpleGreeter", "main", "Config", "LogLevel"} {
		if !containsName(defines, want) {
			t.Errorf("missing defines claim for %q; got %v", want, names)
		}
	}
}

func TestExtractTypeScript_Imports(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.ts"), "typescript")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	imports := claimsByPredicate(claims, extractor.PredicateImports)
	if len(imports) == 0 {
		t.Fatal("expected at least one imports claim")
	}
}

func TestExtractTypeScript_Exports(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.ts"), "typescript")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	exports := claimsByPredicate(claims, extractor.PredicateExports)
	if len(exports) == 0 {
		t.Fatal("expected at least one exports claim")
	}
}

// --- Shell extraction ---

func TestExtractShell_Defines(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.sh"), "shell")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	defines := claimsByPredicate(claims, extractor.PredicateDefines)
	names := claimNames(defines)
	t.Logf("Shell defines: %v", names)

	for _, want := range []string{"greet", "cleanup"} {
		if !containsName(defines, want) {
			t.Errorf("missing defines claim for %q; got %v", want, names)
		}
	}
}

func TestExtractShell_SourceImports(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.sh"), "shell")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	imports := claimsByPredicate(claims, extractor.PredicateImports)
	if len(imports) < 2 {
		t.Fatalf("expected at least 2 imports claims (source + .), got %d", len(imports))
	}
}

func TestExtractShell_NonSourceCommandsIgnored(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	claims, err := e.Extract(context.Background(), testdataPath("sample.sh"), "shell")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	imports := claimsByPredicate(claims, extractor.PredicateImports)
	for _, c := range imports {
		if c.ObjectName == "rm" || c.ObjectName == "echo" {
			t.Errorf("non-source command %q should not produce imports claim", c.ObjectName)
		}
	}
}

// --- Language inference ---

func TestExtractInfersLanguageFromExtension(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	// Empty language string — should infer from .go extension.
	claims, err := e.Extract(context.Background(), testdataPath("sample.go"), "")
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}
	if len(claims) == 0 {
		t.Fatal("expected claims from inferred language")
	}
	for _, c := range claims {
		if c.Language != "go" {
			t.Errorf("inferred language = %q, want %q", c.Language, "go")
		}
	}
}

// --- Error cases ---

func TestExtractUnknownLanguage(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	_, err := e.Extract(context.Background(), testdataPath("sample.go"), "cobol")
	if err == nil {
		t.Fatal("expected error for unknown language")
	}
}

func TestExtractNonexistentFile(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	_, err := e.Extract(context.Background(), testdataPath("nonexistent.go"), "go")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// --- Predicate boundary ---

func TestAllClaimsRespectPredicateBoundary(t *testing.T) {
	t.Parallel()
	e := newExtractor()

	files := []struct {
		path string
		lang string
	}{
		{testdataPath("sample.go"), "go"},
		{testdataPath("sample.py"), "python"},
		{testdataPath("sample.ts"), "typescript"},
		{testdataPath("sample.sh"), "shell"},
	}

	for _, f := range files {
		t.Run(f.lang, func(t *testing.T) {
			t.Parallel()
			claims, err := e.Extract(context.Background(), f.path, f.lang)
			if err != nil {
				t.Fatalf("Extract() error: %v", err)
			}
			if err := extractor.ValidateTreeSitterClaims(claims); err != nil {
				t.Errorf("predicate boundary violation: %v", err)
			}
		})
	}
}

// --- Claim validation ---

func TestAllClaimsHaveRequiredFields(t *testing.T) {
	t.Parallel()
	e := newExtractor()

	files := []struct {
		path string
		lang string
	}{
		{testdataPath("sample.go"), "go"},
		{testdataPath("sample.py"), "python"},
		{testdataPath("sample.ts"), "typescript"},
		{testdataPath("sample.sh"), "shell"},
	}

	for _, f := range files {
		t.Run(f.lang, func(t *testing.T) {
			t.Parallel()
			claims, err := e.Extract(context.Background(), f.path, f.lang)
			if err != nil {
				t.Fatalf("Extract() error: %v", err)
			}
			for i, c := range claims {
				if c.Language == "" {
					t.Errorf("claim[%d] Language is empty", i)
				}
				if c.SourceFile == "" {
					t.Errorf("claim[%d] SourceFile is empty", i)
				}
				if c.Extractor == "" {
					t.Errorf("claim[%d] Extractor is empty", i)
				}
				if c.ExtractorVersion == "" {
					t.Errorf("claim[%d] ExtractorVersion is empty", i)
				}
				if c.LastVerified.IsZero() {
					t.Errorf("claim[%d] LastVerified is zero", i)
				}
				if c.Confidence < 0 || c.Confidence > 1 {
					t.Errorf("claim[%d] Confidence out of range: %f", i, c.Confidence)
				}
			}
		})
	}
}

// --- ExtractBytes ---

func TestExtractBytes_MatchesExtract(t *testing.T) {
	t.Parallel()
	e := newExtractor()
	ctx := context.Background()

	files := []struct {
		path string
		lang string
	}{
		{testdataPath("sample.go"), "go"},
		{testdataPath("sample.py"), "python"},
	}

	for _, f := range files {
		t.Run(f.lang, func(t *testing.T) {
			t.Parallel()

			fileClaims, err := e.Extract(ctx, f.path, f.lang)
			if err != nil {
				t.Fatalf("Extract() error: %v", err)
			}

			src, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatalf("ReadFile() error: %v", err)
			}

			bytesClaims, err := e.ExtractBytes(ctx, src, f.path, f.lang)
			if err != nil {
				t.Fatalf("ExtractBytes() error: %v", err)
			}

			if len(fileClaims) != len(bytesClaims) {
				t.Fatalf("claim count mismatch: Extract=%d, ExtractBytes=%d", len(fileClaims), len(bytesClaims))
			}

			for i := range fileClaims {
				fc := fileClaims[i]
				bc := bytesClaims[i]
				// Compare all fields except LastVerified (timestamp differs).
				if fc.SubjectName != bc.SubjectName {
					t.Errorf("claim[%d] SubjectName: Extract=%q, ExtractBytes=%q", i, fc.SubjectName, bc.SubjectName)
				}
				if fc.Predicate != bc.Predicate {
					t.Errorf("claim[%d] Predicate: Extract=%q, ExtractBytes=%q", i, fc.Predicate, bc.Predicate)
				}
				if fc.Language != bc.Language {
					t.Errorf("claim[%d] Language: Extract=%q, ExtractBytes=%q", i, fc.Language, bc.Language)
				}
				if fc.Kind != bc.Kind {
					t.Errorf("claim[%d] Kind: Extract=%q, ExtractBytes=%q", i, fc.Kind, bc.Kind)
				}
				if fc.SourceFile != bc.SourceFile {
					t.Errorf("claim[%d] SourceFile: Extract=%q, ExtractBytes=%q", i, fc.SourceFile, bc.SourceFile)
				}
				if fc.SourceLine != bc.SourceLine {
					t.Errorf("claim[%d] SourceLine: Extract=%d, ExtractBytes=%d", i, fc.SourceLine, bc.SourceLine)
				}
				if fc.ObjectName != bc.ObjectName {
					t.Errorf("claim[%d] ObjectName: Extract=%q, ExtractBytes=%q", i, fc.ObjectName, bc.ObjectName)
				}
			}
		})
	}
}

// --- Benchmark ---

func BenchmarkExtractGo(b *testing.B) {
	e := newExtractor()
	ctx := context.Background()
	path := testdataPath("sample.go")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := e.Extract(ctx, path, "go")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtractPython(b *testing.B) {
	e := newExtractor()
	ctx := context.Background()
	path := testdataPath("sample.py")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := e.Extract(ctx, path, "python")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtractTypeScript(b *testing.B) {
	e := newExtractor()
	ctx := context.Background()
	path := testdataPath("sample.ts")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := e.Extract(ctx, path, "typescript")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtractShell(b *testing.B) {
	e := newExtractor()
	ctx := context.Background()
	path := testdataPath("sample.sh")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := e.Extract(ctx, path, "shell")
		if err != nil {
			b.Fatal(err)
		}
	}
}
