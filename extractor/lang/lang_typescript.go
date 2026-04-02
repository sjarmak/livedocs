package lang

func typescriptConfig() *Config {
	return &Config{
		Language:    "typescript",
		GrammarName: "tree-sitter-typescript",
		Extensions:  []string{".ts", ".tsx"},
		TestPatterns: []string{
			"*.test.ts",
			"*.test.tsx",
			"*.spec.ts",
			"*.spec.tsx",
			"__tests__/*",
		},
		GeneratedPatterns: []string{
			"^// This file is auto-generated",
			"^/\\* eslint-disable \\*/\\s*// auto-generated",
		},
		mappings: map[string]ClaimMapping{
			"function_declaration": {
				NodeType:   "function_declaration",
				Predicate:  "defines",
				Confidence: 1.0,
				SymbolKind: "func",
			},
			"class_declaration": {
				NodeType:   "class_declaration",
				Predicate:  "defines",
				Confidence: 1.0,
				SymbolKind: "class",
			},
			"interface_declaration": {
				NodeType:   "interface_declaration",
				Predicate:  "defines",
				Confidence: 1.0,
				SymbolKind: "interface",
			},
			"enum_declaration": {
				NodeType:   "enum_declaration",
				Predicate:  "defines",
				Confidence: 1.0,
				SymbolKind: "enum",
			},
			"type_alias_declaration": {
				NodeType:   "type_alias_declaration",
				Predicate:  "defines",
				Confidence: 1.0,
				SymbolKind: "type",
			},
			"method_definition": {
				NodeType:   "method_definition",
				Predicate:  "defines",
				Confidence: 1.0,
				SymbolKind: "method",
			},
			"lexical_declaration": {
				NodeType:   "lexical_declaration",
				Predicate:  "defines",
				Confidence: 1.0,
				SymbolKind: "const", // const/let — refinement needs child inspection
			},
			"import_statement": {
				NodeType:   "import_statement",
				Predicate:  "imports",
				Confidence: 1.0,
			},
			"export_statement": {
				NodeType:   "export_statement",
				Predicate:  "exports",
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
