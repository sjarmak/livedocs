package lang

func goConfig() *Config {
	return &Config{
		Language:    "go",
		GrammarName: "tree-sitter-go",
		Extensions:  []string{".go"},
		TestPatterns: []string{
			"*_test.go",
		},
		GeneratedPatterns: []string{
			"^// Code generated .* DO NOT EDIT\\.$",
		},
		mappings: map[string]ClaimMapping{
			"function_declaration": {
				NodeType:   "function_declaration",
				Predicate:  "defines",
				Confidence: 1.0,
				SymbolKind: "func",
			},
			"method_declaration": {
				NodeType:   "method_declaration",
				Predicate:  "defines",
				Confidence: 1.0,
				SymbolKind: "method",
			},
			"type_declaration": {
				NodeType:   "type_declaration",
				Predicate:  "defines",
				Confidence: 1.0,
				SymbolKind: "type",
			},
			"import_declaration": {
				NodeType:   "import_declaration",
				Predicate:  "imports",
				Confidence: 1.0,
			},
			"comment": {
				NodeType:   "comment",
				Predicate:  "has_doc",
				Confidence: 0.85,
			},
		},
	}
}
