package lang_test

import (
	"testing"

	"github.com/sjarmak/livedocs/extractor/lang"
)

func TestClaimMappingLookup(t *testing.T) {
	t.Parallel()
	reg := lang.NewRegistry()

	tests := []struct {
		language string
		nodeType string
		wantPred string
		wantOK   bool
	}{
		// TypeScript defines
		{"typescript", "function_declaration", "defines", true},
		{"typescript", "class_declaration", "defines", true},
		{"typescript", "interface_declaration", "defines", true},
		{"typescript", "enum_declaration", "defines", true},
		{"typescript", "type_alias_declaration", "defines", true},
		{"typescript", "method_definition", "defines", true},
		{"typescript", "lexical_declaration", "defines", true},
		// TypeScript imports
		{"typescript", "import_statement", "imports", true},
		// TypeScript exports
		{"typescript", "export_statement", "exports", true},
		// TypeScript docs
		{"typescript", "comment", "has_doc", true},

		// Python defines
		{"python", "function_definition", "defines", true},
		{"python", "class_definition", "defines", true},
		{"python", "decorated_definition", "defines", true},
		// Python imports
		{"python", "import_statement", "imports", true},
		{"python", "import_from_statement", "imports", true},
		// Python docs
		{"python", "comment", "has_doc", true},
		{"python", "expression_statement", "has_doc", true}, // docstrings

		// Shell defines
		{"shell", "function_definition", "defines", true},
		// Shell imports
		{"shell", "command", "imports", true}, // source/. commands
		// Shell docs
		{"shell", "comment", "has_doc", true},

		// Go defines
		{"go", "function_declaration", "defines", true},
		{"go", "method_declaration", "defines", true},
		{"go", "type_declaration", "defines", true},
		// Go imports
		{"go", "import_declaration", "imports", true},
		// Go docs
		{"go", "comment", "has_doc", true},

		// Unknown node type
		{"typescript", "unknown_node_xyz", "", false},
		// Unknown language
		{"nonexistent", "function_declaration", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.language+"/"+tt.nodeType, func(t *testing.T) {
			t.Parallel()
			cfg, ok := reg.LookupByLanguage(tt.language)
			if !ok {
				if tt.wantOK {
					t.Fatalf("language %q not found in registry", tt.language)
				}
				return
			}

			mapping, ok := cfg.NodeMapping(tt.nodeType)
			if ok != tt.wantOK {
				t.Fatalf("NodeMapping(%q) ok = %v, want %v", tt.nodeType, ok, tt.wantOK)
			}
			if ok && mapping.Predicate != tt.wantPred {
				t.Errorf("NodeMapping(%q).Predicate = %q, want %q", tt.nodeType, mapping.Predicate, tt.wantPred)
			}
		})
	}
}

func TestClaimMappingPredicateBoundary(t *testing.T) {
	t.Parallel()
	reg := lang.NewRegistry()

	// All mappings must use only tree-sitter-safe predicates.
	allowedPredicates := map[string]bool{
		"defines":      true,
		"imports":      true,
		"exports":      true,
		"has_doc":      true,
		"is_generated": true,
		"is_test":      true,
	}

	for _, name := range reg.AllLanguages() {
		cfg, _ := reg.LookupByLanguage(name)
		for _, m := range cfg.AllMappings() {
			t.Run(name+"/"+m.NodeType+"/"+m.Predicate, func(t *testing.T) {
				t.Parallel()
				if !allowedPredicates[m.Predicate] {
					t.Errorf("mapping %s→%s uses forbidden predicate %q (not in tree-sitter-safe set)",
						m.NodeType, m.Predicate, m.Predicate)
				}
			})
		}
	}
}

func TestClaimMappingConfidence(t *testing.T) {
	t.Parallel()
	reg := lang.NewRegistry()

	for _, name := range reg.AllLanguages() {
		cfg, _ := reg.LookupByLanguage(name)
		for _, m := range cfg.AllMappings() {
			t.Run(name+"/"+m.NodeType, func(t *testing.T) {
				t.Parallel()
				if m.Confidence < 0 || m.Confidence > 1.0 {
					t.Errorf("confidence %f out of range [0, 1]", m.Confidence)
				}
				// has_doc should be 0.85 per PRD
				if m.Predicate == "has_doc" && m.Confidence != 0.85 {
					t.Errorf("has_doc confidence = %f, want 0.85", m.Confidence)
				}
				// structural predicates should be 1.0
				if m.Predicate != "has_doc" && m.Confidence != 1.0 {
					t.Errorf("%s confidence = %f, want 1.0", m.Predicate, m.Confidence)
				}
			})
		}
	}
}

func TestClaimMappingSymbolKind(t *testing.T) {
	t.Parallel()
	reg := lang.NewRegistry()

	validKinds := map[string]bool{
		"func": true, "class": true, "type": true, "interface": true,
		"enum": true, "const": true, "var": true, "module": true,
		"method": true, "": true, // empty allowed for non-defines
	}

	for _, name := range reg.AllLanguages() {
		cfg, _ := reg.LookupByLanguage(name)
		for _, m := range cfg.AllMappings() {
			if m.Predicate == "defines" && m.SymbolKind == "" {
				t.Errorf("%s/%s: defines predicate must have a SymbolKind", name, m.NodeType)
			}
			if !validKinds[m.SymbolKind] {
				t.Errorf("%s/%s: invalid SymbolKind %q", name, m.NodeType, m.SymbolKind)
			}
		}
	}
}
