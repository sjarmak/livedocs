package extractor

import "fmt"

// PredicateBoundaryError is returned when a tree-sitter extractor emits a
// predicate that is restricted to deep extractors.
type PredicateBoundaryError struct {
	Predicate Predicate
	Symbol    string
}

func (e *PredicateBoundaryError) Error() string {
	return fmt.Sprintf("predicate boundary violation: %q is deep-only, emitted for symbol %q", e.Predicate, e.Symbol)
}

// LanguageNotRegisteredError is returned when no extractor is registered
// for a requested language or file extension.
type LanguageNotRegisteredError struct {
	Key string // extension or language name
}

func (e *LanguageNotRegisteredError) Error() string {
	return fmt.Sprintf("no extractor registered for %q", e.Key)
}
