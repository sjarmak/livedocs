package extractor

import "context"

// Extractor is the core interface that all language-specific extractors
// implement. Deep extractors (go/packages, TypeScript compiler API) and
// tree-sitter extractors both satisfy this interface.
//
// The Claims DB does not know which extractor produced a claim — it only
// sees []Claim values with an extractor name and version for provenance.
type Extractor interface {
	// Extract analyses the file at path for the given language and returns
	// zero or more claims. Implementations should respect context cancellation.
	Extract(ctx context.Context, path string, lang string) ([]Claim, error)

	// Name returns a stable identifier for this extractor, e.g. "go-deep"
	// or "tree-sitter-go".
	Name() string

	// Version returns the extractor version string, e.g. "1.2.0".
	Version() string
}

// TreeSitterExtractor is a marker interface for extractors that operate at
// the tree-sitter tier. They are subject to the strict predicate boundary:
// only the 6 tree-sitter-safe predicates may appear in their output.
type TreeSitterExtractor interface {
	Extractor
	IsTreeSitter()
}

// ValidateTreeSitterClaims checks that all claims use only tree-sitter-safe
// predicates. Returns an error listing the first offending predicate found.
func ValidateTreeSitterClaims(claims []Claim) error {
	for i := range claims {
		if !claims[i].Predicate.IsTreeSitterSafe() {
			return &PredicateBoundaryError{
				Predicate: claims[i].Predicate,
				Symbol:    claims[i].SubjectName,
			}
		}
	}
	return nil
}
