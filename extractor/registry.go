package extractor

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
)

// generatedSuffixes lists filename suffixes that indicate generated code.
// Files matching any of these patterns are excluded from extraction.
var generatedSuffixes = []string{
	"_generated.go",
	"pb.go",
}

// generatedPrefixes lists filename prefixes that indicate generated code.
var generatedPrefixes = []string{
	"zz_generated",
}

// generatedInfixes lists substrings that, when present anywhere in the
// filename (base name), indicate generated code.
var generatedInfixes = []string{
	"_zz_generated",
}

// IsGenerated reports whether the given file path matches a generated-code
// pattern and should be excluded from extraction.
func IsGenerated(path string) bool {
	base := filepath.Base(path)
	for _, suffix := range generatedSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	for _, prefix := range generatedPrefixes {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	for _, infix := range generatedInfixes {
		if strings.Contains(base, infix) {
			return true
		}
	}
	return false
}

// LanguageConfig describes how to extract claims for a given language.
type LanguageConfig struct {
	// Language is the canonical language name (e.g. "go", "typescript").
	Language string

	// Extensions lists file extensions that map to this language,
	// including the leading dot (e.g. ".go", ".ts", ".tsx").
	Extensions []string

	// TreeSitterGrammar is the grammar name for the tree-sitter fast path
	// (e.g. "tree-sitter-go"). Empty if no grammar is available.
	TreeSitterGrammar string

	// DeepExtractor is the optional compiler-grade extractor. Nil means
	// only the tree-sitter baseline is available.
	DeepExtractor Extractor

	// FastExtractor is the tree-sitter extractor. Nil means no fast path.
	FastExtractor Extractor
}

// Registry maps file extensions and language names to extractor configurations.
// It is safe for concurrent use.
type Registry struct {
	mu          sync.RWMutex
	byExtension map[string]*LanguageConfig // ".go" -> config
	byLanguage  map[string]*LanguageConfig // "go"  -> config
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		byExtension: make(map[string]*LanguageConfig),
		byLanguage:  make(map[string]*LanguageConfig),
	}
}

// Register adds a language configuration. It overwrites any previous
// registration for the same language or extensions.
func (r *Registry) Register(cfg LanguageConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()

	c := &cfg // store a copy behind pointer
	r.byLanguage[cfg.Language] = c
	for _, ext := range cfg.Extensions {
		r.byExtension[ext] = c
	}
}

// LookupByExtension returns the config for the given file extension
// (including leading dot). Returns nil if none registered.
func (r *Registry) LookupByExtension(ext string) *LanguageConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byExtension[ext]
}

// LookupByLanguage returns the config for the given language name.
// Returns nil if none registered.
func (r *Registry) LookupByLanguage(lang string) *LanguageConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byLanguage[lang]
}

// Languages returns all registered language names.
func (r *Registry) Languages() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byLanguage))
	for lang := range r.byLanguage {
		out = append(out, lang)
	}
	return out
}

// ExtractFile determines the language from the file path's extension,
// selects the best available extractor (deep if available, otherwise fast),
// and returns the resulting claims. Returns LanguageNotRegisteredError if
// no extractor is registered for the file's extension.
func (r *Registry) ExtractFile(ctx context.Context, path string) ([]Claim, error) {
	if IsGenerated(path) {
		return nil, nil
	}

	ext := strings.ToLower(filepath.Ext(path))
	cfg := r.LookupByExtension(ext)
	if cfg == nil {
		return nil, &LanguageNotRegisteredError{Key: ext}
	}

	// Prefer deep extractor when available.
	extractor := cfg.DeepExtractor
	if extractor == nil {
		extractor = cfg.FastExtractor
	}
	if extractor == nil {
		return nil, &LanguageNotRegisteredError{Key: cfg.Language}
	}

	claims, err := extractor.Extract(ctx, path, cfg.Language)
	if err != nil {
		return nil, err
	}

	// Enforce tree-sitter predicate boundary if this is a fast extractor.
	if _, ok := extractor.(TreeSitterExtractor); ok {
		if err := ValidateTreeSitterClaims(claims); err != nil {
			return nil, err
		}
	}

	return claims, nil
}
