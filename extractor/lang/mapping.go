// Package lang defines tree-sitter AST node to claim predicate mappings
// for the language registry. These mappings determine which tree-sitter
// node types produce which claim predicates during fast-path extraction.
//
// Only tree-sitter-safe predicates are allowed:
// defines, imports, exports, has_doc, is_generated, is_test.
package lang

// ClaimMapping maps a tree-sitter AST node type to a claim predicate.
type ClaimMapping struct {
	// NodeType is the tree-sitter grammar node type (e.g., "function_declaration").
	NodeType string

	// Predicate is the claim predicate produced (must be tree-sitter-safe).
	Predicate string

	// Confidence is the confidence score for claims produced by this mapping.
	// Structural predicates use 1.0; has_doc uses 0.85 per PRD.
	Confidence float64

	// SymbolKind categorises the symbol (e.g., "func", "class", "type").
	// Required when Predicate is "defines"; empty otherwise.
	SymbolKind string

	// NeedsChildInspection indicates whether the extractor must inspect
	// child nodes to fully resolve the claim. For example, Shell "command"
	// nodes only produce "imports" if the command name is "source" or ".".
	NeedsChildInspection bool
}

// AllowedPredicates is the set of tree-sitter-safe predicates.
// Deep-extractor-only predicates (has_kind, implements, has_signature, encloses)
// are forbidden at this tier.
var AllowedPredicates = map[string]bool{
	"defines":      true,
	"imports":      true,
	"exports":      true,
	"has_doc":      true,
	"is_generated": true,
	"is_test":      true,
}
