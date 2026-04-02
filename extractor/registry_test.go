package extractor

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

func TestRegistry_RegisterAndLookup(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Register(LanguageConfig{
		Language:          "go",
		Extensions:        []string{".go"},
		TreeSitterGrammar: "tree-sitter-go",
	})

	cfg := r.LookupByLanguage("go")
	if cfg == nil {
		t.Fatal("LookupByLanguage(\"go\") returned nil")
	}
	if cfg.Language != "go" {
		t.Errorf("got language %q, want %q", cfg.Language, "go")
	}

	cfg = r.LookupByExtension(".go")
	if cfg == nil {
		t.Fatal("LookupByExtension(\".go\") returned nil")
	}
	if cfg.TreeSitterGrammar != "tree-sitter-go" {
		t.Errorf("got grammar %q, want %q", cfg.TreeSitterGrammar, "tree-sitter-go")
	}
}

func TestRegistry_LookupMissing(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if cfg := r.LookupByLanguage("rust"); cfg != nil {
		t.Errorf("expected nil for unregistered language, got %+v", cfg)
	}
	if cfg := r.LookupByExtension(".rs"); cfg != nil {
		t.Errorf("expected nil for unregistered extension, got %+v", cfg)
	}
}

func TestRegistry_MultipleExtensions(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Register(LanguageConfig{
		Language:          "typescript",
		Extensions:        []string{".ts", ".tsx"},
		TreeSitterGrammar: "tree-sitter-typescript",
	})
	for _, ext := range []string{".ts", ".tsx"} {
		cfg := r.LookupByExtension(ext)
		if cfg == nil {
			t.Fatalf("LookupByExtension(%q) returned nil", ext)
		}
		if cfg.Language != "typescript" {
			t.Errorf("extension %q -> language %q, want %q", ext, cfg.Language, "typescript")
		}
	}
}

func TestRegistry_OverwritePrevious(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Register(LanguageConfig{
		Language:          "go",
		Extensions:        []string{".go"},
		TreeSitterGrammar: "old-grammar",
	})
	r.Register(LanguageConfig{
		Language:          "go",
		Extensions:        []string{".go"},
		TreeSitterGrammar: "new-grammar",
	})
	cfg := r.LookupByLanguage("go")
	if cfg.TreeSitterGrammar != "new-grammar" {
		t.Errorf("got grammar %q, want %q", cfg.TreeSitterGrammar, "new-grammar")
	}
}

func TestRegistry_Languages(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Register(LanguageConfig{Language: "go", Extensions: []string{".go"}})
	r.Register(LanguageConfig{Language: "python", Extensions: []string{".py"}})
	r.Register(LanguageConfig{Language: "typescript", Extensions: []string{".ts"}})

	langs := r.Languages()
	sort.Strings(langs)
	want := []string{"go", "python", "typescript"}
	if len(langs) != len(want) {
		t.Fatalf("got %d languages, want %d", len(langs), len(want))
	}
	for i := range want {
		if langs[i] != want[i] {
			t.Errorf("langs[%d] = %q, want %q", i, langs[i], want[i])
		}
	}
}

func TestRegistry_ExtractFile_DeepPreferred(t *testing.T) {
	t.Parallel()
	now := time.Now()
	deepClaim := Claim{
		SubjectRepo: "r", SubjectImportPath: "p", SubjectName: "Deep",
		Language: "go", Kind: KindFunc, Visibility: VisibilityPublic,
		Predicate: PredicateDefines, SourceFile: "f.go",
		Confidence: 1.0, ClaimTier: TierStructural,
		Extractor: "go-deep", ExtractorVersion: "1.0", LastVerified: now,
	}
	fastClaim := Claim{
		SubjectRepo: "r", SubjectImportPath: "p", SubjectName: "Fast",
		Language: "go", Kind: KindFunc, Visibility: VisibilityPublic,
		Predicate: PredicateDefines, SourceFile: "f.go",
		Confidence: 1.0, ClaimTier: TierStructural,
		Extractor: "ts-go", ExtractorVersion: "0.1", LastVerified: now,
	}
	r := NewRegistry()
	r.Register(LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		DeepExtractor: &mockExtractor{name: "go-deep", version: "1.0", claims: []Claim{deepClaim}},
		FastExtractor: &mockExtractor{name: "ts-go", version: "0.1", claims: []Claim{fastClaim}},
	})

	claims, err := r.ExtractFile(context.Background(), "/src/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(claims) != 1 || claims[0].SubjectName != "Deep" {
		t.Errorf("expected deep extractor claim, got %+v", claims)
	}
}

func TestRegistry_ExtractFile_FallsBackToFast(t *testing.T) {
	t.Parallel()
	now := time.Now()
	fastClaim := Claim{
		SubjectRepo: "r", SubjectImportPath: "p", SubjectName: "Fast",
		Language: "go", Kind: KindFunc, Visibility: VisibilityPublic,
		Predicate: PredicateDefines, SourceFile: "f.go",
		Confidence: 1.0, ClaimTier: TierStructural,
		Extractor: "ts-go", ExtractorVersion: "0.1", LastVerified: now,
	}
	r := NewRegistry()
	r.Register(LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		FastExtractor: &mockExtractor{name: "ts-go", version: "0.1", claims: []Claim{fastClaim}},
	})

	claims, err := r.ExtractFile(context.Background(), "/src/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(claims) != 1 || claims[0].SubjectName != "Fast" {
		t.Errorf("expected fast extractor claim, got %+v", claims)
	}
}

func TestRegistry_ExtractFile_UnknownExtension(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	_, err := r.ExtractFile(context.Background(), "/src/main.xyz")
	if err == nil {
		t.Fatal("expected error for unknown extension")
	}
	var lnr *LanguageNotRegisteredError
	if !errors.As(err, &lnr) {
		t.Fatalf("expected *LanguageNotRegisteredError, got %T: %v", err, err)
	}
}

func TestRegistry_ExtractFile_TreeSitterBoundaryEnforced(t *testing.T) {
	t.Parallel()
	// A tree-sitter extractor that emits a deep-only predicate should fail.
	badClaim := Claim{
		SubjectRepo: "r", SubjectImportPath: "p", SubjectName: "Bad",
		Language: "go", Kind: KindFunc, Visibility: VisibilityPublic,
		Predicate:  PredicateImplements, // deep-only!
		SourceFile: "f.go",
		Confidence: 1.0, ClaimTier: TierStructural,
		Extractor: "ts-go", ExtractorVersion: "0.1", LastVerified: time.Now(),
	}
	r := NewRegistry()
	r.Register(LanguageConfig{
		Language:   "go",
		Extensions: []string{".go"},
		FastExtractor: &mockTreeSitterExtractor{
			mockExtractor: mockExtractor{
				name: "ts-go", version: "0.1", claims: []Claim{badClaim},
			},
		},
	})

	_, err := r.ExtractFile(context.Background(), "/src/main.go")
	if err == nil {
		t.Fatal("expected predicate boundary error")
	}
	var pbe *PredicateBoundaryError
	if !errors.As(err, &pbe) {
		t.Fatalf("expected *PredicateBoundaryError, got %T: %v", err, err)
	}
}

func TestRegistry_ExtractFile_NoExtractors(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Register(LanguageConfig{
		Language:   "go",
		Extensions: []string{".go"},
		// No deep or fast extractor.
	})
	_, err := r.ExtractFile(context.Background(), "/src/main.go")
	if err == nil {
		t.Fatal("expected error when no extractors configured")
	}
	var lnr *LanguageNotRegisteredError
	if !errors.As(err, &lnr) {
		t.Fatalf("expected *LanguageNotRegisteredError, got %T: %v", err, err)
	}
}

func TestRegistry_ExtractFile_ExtractorError(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Register(LanguageConfig{
		Language:   "go",
		Extensions: []string{".go"},
		DeepExtractor: &mockExtractor{
			name:    "go-deep",
			version: "1.0",
			err:     errors.New("extraction failed"),
		},
	})
	_, err := r.ExtractFile(context.Background(), "/src/main.go")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "extraction failed" {
		t.Errorf("got error %q, want %q", err.Error(), "extraction failed")
	}
}
