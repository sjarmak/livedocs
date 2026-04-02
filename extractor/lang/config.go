package lang

// Config holds the tree-sitter configuration for a single language.
type Config struct {
	// Language is the canonical language identifier (e.g., "go", "typescript").
	Language string

	// GrammarName is the tree-sitter grammar name (e.g., "tree-sitter-go").
	GrammarName string

	// Extensions lists file extensions that map to this language (e.g., ".ts", ".tsx").
	Extensions []string

	// TestPatterns are file path patterns that indicate test files.
	// Matching files produce is_test claims automatically.
	TestPatterns []string

	// GeneratedPatterns are comment content patterns that indicate generated files.
	// Matching comments produce is_generated claims automatically.
	GeneratedPatterns []string

	// mappings maps tree-sitter node types to claim mappings.
	mappings map[string]ClaimMapping
}

// NodeMapping returns the claim mapping for the given tree-sitter node type.
func (c Config) NodeMapping(nodeType string) (ClaimMapping, bool) {
	m, ok := c.mappings[nodeType]
	return m, ok
}

// AllMappings returns all claim mappings for this language.
func (c Config) AllMappings() []ClaimMapping {
	result := make([]ClaimMapping, 0, len(c.mappings))
	for _, m := range c.mappings {
		result = append(result, m)
	}
	return result
}
