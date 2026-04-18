// Package treesitter implements a universal tree-sitter extractor that uses
// smacker/go-tree-sitter (CGO) to parse source files and extract structural
// claims. It operates at the tree-sitter tier: only tree-sitter-safe predicates
// (defines, imports, exports, has_doc, is_test, is_generated) are emitted.
//
// The extractor uses the claim mappings from extractor/lang/ to convert AST
// nodes to claims, supporting any language with a tree-sitter grammar and
// registered mappings.
package treesitter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/sjarmak/livedocs/extractor"
	"github.com/sjarmak/livedocs/extractor/lang"
)

const (
	extractorVersion = "0.1.0"
)

// UniversalExtractor implements extractor.TreeSitterExtractor using CGO-based
// tree-sitter parsing. It supports any language registered in the lang.Registry
// and emits only tree-sitter-safe predicates.
type UniversalExtractor struct {
	registry *lang.Registry
}

// Ensure UniversalExtractor satisfies the TreeSitterExtractor interface.
var _ extractor.TreeSitterExtractor = (*UniversalExtractor)(nil)

// New returns a new UniversalExtractor with the given language registry.
func New(registry *lang.Registry) *UniversalExtractor {
	return &UniversalExtractor{registry: registry}
}

// Name returns the extractor identifier.
func (e *UniversalExtractor) Name() string { return "tree-sitter" }

// Version returns the extractor version.
func (e *UniversalExtractor) Version() string { return extractorVersion }

// IsTreeSitter is the marker method for the TreeSitterExtractor interface.
func (e *UniversalExtractor) IsTreeSitter() {}

// Extract parses the file at path for the given language and returns claims.
// If lang is empty, it is inferred from the file extension.
func (e *UniversalExtractor) Extract(ctx context.Context, path string, language string) ([]extractor.Claim, error) {
	cfg, err := e.resolveConfig(path, language)
	if err != nil {
		return nil, err
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("treesitter: reading %s: %w", path, err)
	}

	return e.extractFromBytes(ctx, src, path, cfg)
}

// ExtractBytes parses the provided source bytes as if they came from relPath.
// If lang is empty, it is inferred from the relPath extension.
func (e *UniversalExtractor) ExtractBytes(ctx context.Context, src []byte, relPath string, lang string) ([]extractor.Claim, error) {
	cfg, err := e.resolveConfig(relPath, lang)
	if err != nil {
		return nil, err
	}

	return e.extractFromBytes(ctx, src, relPath, cfg)
}

// extractFromBytes is the shared implementation for Extract and ExtractBytes.
func (e *UniversalExtractor) extractFromBytes(ctx context.Context, src []byte, relPath string, cfg lang.Config) ([]extractor.Claim, error) {
	grammar, ok := LookupGrammar(cfg.GrammarName)
	if !ok {
		return nil, fmt.Errorf("treesitter: no grammar for %q", cfg.GrammarName)
	}

	root, err := sitter.ParseCtx(ctx, src, grammar)
	if err != nil {
		return nil, fmt.Errorf("treesitter: parsing %s: %w", relPath, err)
	}

	now := time.Now()

	var claims []extractor.Claim

	// Check is_test via file path patterns.
	isTest := matchesAnyPattern(relPath, cfg.TestPatterns)

	// Check is_generated via first comment.
	isGenerated := checkGenerated(root, src, cfg.GeneratedPatterns)

	// Walk the AST using DFS.
	iter := sitter.NewNamedIterator(root, sitter.DFSMode)
	for {
		node, iterErr := iter.Next()
		if iterErr != nil {
			break // io.EOF
		}

		nodeType := node.Type()
		mapping, hasMappng := cfg.NodeMapping(nodeType)
		if !hasMappng {
			continue
		}

		// Handle NeedsChildInspection cases.
		if mapping.NeedsChildInspection {
			if !inspectChildren(node, src, cfg.Language, mapping) {
				continue
			}
		}

		claim := buildClaim(node, src, mapping, cfg, relPath, now)
		claims = append(claims, claim)
	}

	// Emit file-level is_test claim if matched.
	if isTest {
		claims = append(claims, extractor.Claim{
			SubjectRepo:       "", // caller fills
			SubjectImportPath: "", // caller fills
			SubjectName:       filepath.Base(relPath),
			Language:          cfg.Language,
			Kind:              extractor.KindModule,
			Visibility:        extractor.VisibilityPublic,
			Predicate:         extractor.PredicateIsTest,
			SourceFile:        relPath,
			Confidence:        1.0,
			ClaimTier:         extractor.TierStructural,
			Extractor:         "tree-sitter-" + cfg.Language,
			ExtractorVersion:  extractorVersion,
			LastVerified:      now,
		})
	}

	// Emit file-level is_generated claim if matched.
	if isGenerated {
		claims = append(claims, extractor.Claim{
			SubjectRepo:       "", // caller fills
			SubjectImportPath: "", // caller fills
			SubjectName:       filepath.Base(relPath),
			Language:          cfg.Language,
			Kind:              extractor.KindModule,
			Visibility:        extractor.VisibilityPublic,
			Predicate:         extractor.PredicateIsGenerated,
			SourceFile:        relPath,
			Confidence:        1.0,
			ClaimTier:         extractor.TierStructural,
			Extractor:         "tree-sitter-" + cfg.Language,
			ExtractorVersion:  extractorVersion,
			LastVerified:      now,
		})
	}

	// Validate predicate boundary.
	if err := extractor.ValidateTreeSitterClaims(claims); err != nil {
		return nil, fmt.Errorf("treesitter: %w", err)
	}

	return claims, nil
}

// resolveConfig looks up the language config, using either the explicit language
// name or the file extension.
func (e *UniversalExtractor) resolveConfig(path string, language string) (lang.Config, error) {
	if language != "" {
		cfg, ok := e.registry.LookupByLanguage(language)
		if !ok {
			return lang.Config{}, &extractor.LanguageNotRegisteredError{Key: language}
		}
		return cfg, nil
	}
	ext := filepath.Ext(path)
	cfg, ok := e.registry.LookupByExtension(ext)
	if !ok {
		return lang.Config{}, &extractor.LanguageNotRegisteredError{Key: ext}
	}
	return cfg, nil
}

// buildClaim constructs a Claim from a matched AST node.
func buildClaim(node *sitter.Node, src []byte, mapping lang.ClaimMapping, cfg lang.Config, relPath string, now time.Time) extractor.Claim {
	name := extractName(node, src, cfg.Language, mapping)
	kind := extractor.SymbolKind(mapping.SymbolKind)
	if !kind.IsValid() {
		kind = "" // non-defines predicates may not have a kind
	}

	vis := inferVisibility(name, cfg.Language, mapping)

	claim := extractor.Claim{
		SubjectRepo:       "", // caller fills in
		SubjectImportPath: "", // caller fills in
		SubjectName:       name,
		Language:          cfg.Language,
		Kind:              kind,
		Visibility:        vis,
		Predicate:         extractor.Predicate(mapping.Predicate),
		SourceFile:        relPath,
		SourceLine:        int(node.StartPoint().Row) + 1, // 1-based
		Confidence:        mapping.Confidence,
		ClaimTier:         extractor.TierStructural,
		Extractor:         "tree-sitter-" + cfg.Language,
		ExtractorVersion:  extractorVersion,
		LastVerified:      now,
	}

	// For imports, the object_name is the import path.
	if mapping.Predicate == "imports" {
		claim.ObjectName = extractImportPath(node, src, cfg.Language)
	}

	// For has_doc, the object_text is the comment content.
	if mapping.Predicate == "has_doc" {
		claim.ObjectText = node.Content(src)
	}

	// For exports, extract what is being exported.
	if mapping.Predicate == "exports" {
		claim.ObjectName = extractExportName(node, src)
	}

	return claim
}

// extractName pulls the symbol name from an AST node. For most definitions,
// this is the "name" or "identifier" child. Falls back to node content snippet.
func extractName(node *sitter.Node, src []byte, language string, mapping lang.ClaimMapping) string {
	// For imports, the name is the import path.
	if mapping.Predicate == "imports" {
		return extractImportPath(node, src, language)
	}

	// For has_doc, use the content.
	if mapping.Predicate == "has_doc" {
		content := node.Content(src)
		if len(content) > 60 {
			return content[:60] + "..."
		}
		return content
	}

	// For exports, use the exported name.
	if mapping.Predicate == "exports" {
		return extractExportName(node, src)
	}

	// Look for common name children.
	nameNode := node.ChildByFieldName("name")
	if nameNode != nil {
		return nameNode.Content(src)
	}

	// Go type_declaration wraps type_spec children.
	if language == "go" && node.Type() == "type_declaration" {
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if child.Type() == "type_spec" {
				specName := child.ChildByFieldName("name")
				if specName != nil {
					return specName.Content(src)
				}
			}
		}
	}

	// Python decorated_definition: look inside the wrapped definition.
	if language == "python" && node.Type() == "decorated_definition" {
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if child.Type() == "function_definition" || child.Type() == "class_definition" {
				defName := child.ChildByFieldName("name")
				if defName != nil {
					return defName.Content(src)
				}
			}
		}
	}

	// Fallback: first named child that is an identifier.
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "identifier" || child.Type() == "name" {
			return child.Content(src)
		}
	}

	// Last resort: truncated content.
	content := node.Content(src)
	if len(content) > 60 {
		return content[:60] + "..."
	}
	return content
}

// extractImportPath pulls the import path string from an import node.
func extractImportPath(node *sitter.Node, src []byte, language string) string {
	switch language {
	case "go":
		// Go import_declaration contains import_spec_list or import_spec
		return extractGoImportPaths(node, src)
	case "python":
		// Python import_statement / import_from_statement
		return extractPythonImport(node, src)
	case "typescript":
		// TypeScript import_statement has a "source" field
		sourceNode := node.ChildByFieldName("source")
		if sourceNode != nil {
			return trimQuotes(sourceNode.Content(src))
		}
	case "shell":
		// Shell command node: second child is the path
		if node.ChildCount() > 1 {
			return node.Child(1).Content(src)
		}
	}
	return node.Content(src)
}

// extractGoImportPaths extracts all import paths from a Go import_declaration.
func extractGoImportPaths(node *sitter.Node, src []byte) string {
	var paths []string
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "import_spec_list":
			for j := 0; j < int(child.NamedChildCount()); j++ {
				spec := child.NamedChild(j)
				if spec.Type() == "import_spec" {
					pathNode := spec.ChildByFieldName("path")
					if pathNode != nil {
						paths = append(paths, trimQuotes(pathNode.Content(src)))
					}
				}
			}
		case "import_spec":
			pathNode := child.ChildByFieldName("path")
			if pathNode != nil {
				paths = append(paths, trimQuotes(pathNode.Content(src)))
			}
		case "interpreted_string_literal":
			paths = append(paths, trimQuotes(child.Content(src)))
		}
	}
	return strings.Join(paths, ", ")
}

// extractPythonImport extracts the module name from a Python import statement.
func extractPythonImport(node *sitter.Node, src []byte) string {
	// import_from_statement: "from X import Y"
	if node.Type() == "import_from_statement" {
		modNode := node.ChildByFieldName("module_name")
		if modNode != nil {
			return modNode.Content(src)
		}
	}
	// import_statement: "import X"
	nameNode := node.ChildByFieldName("name")
	if nameNode != nil {
		return nameNode.Content(src)
	}
	// Fallback: first dotted_name child.
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "dotted_name" || child.Type() == "aliased_import" {
			return child.Content(src)
		}
	}
	return node.Content(src)
}

// extractExportName extracts the name of an exported symbol from an export_statement.
func extractExportName(node *sitter.Node, src []byte) string {
	// TypeScript export_statement may wrap a declaration.
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		nameNode := child.ChildByFieldName("name")
		if nameNode != nil {
			return nameNode.Content(src)
		}
	}
	content := node.Content(src)
	if len(content) > 60 {
		return content[:60] + "..."
	}
	return content
}

// inspectChildren handles NeedsChildInspection cases.
func inspectChildren(node *sitter.Node, src []byte, language string, mapping lang.ClaimMapping) bool {
	switch language {
	case "shell":
		// Shell command: only "source" or "." commands produce imports.
		if node.ChildCount() > 0 {
			cmdName := node.Child(0).Content(src)
			return cmdName == "source" || cmdName == "."
		}
		return false

	case "python":
		// Python expression_statement: only string children are docstrings.
		if mapping.NodeType == "expression_statement" {
			for i := 0; i < int(node.NamedChildCount()); i++ {
				child := node.NamedChild(i)
				if child.Type() == "string" {
					return true
				}
			}
			return false
		}
	}
	return true
}

// inferVisibility determines visibility from the symbol name and language conventions.
func inferVisibility(name string, language string, mapping lang.ClaimMapping) extractor.Visibility {
	if mapping.Predicate != "defines" {
		return extractor.VisibilityPublic
	}
	switch language {
	case "go":
		if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
			return extractor.VisibilityPublic
		}
		return extractor.VisibilityInternal
	case "python":
		if strings.HasPrefix(name, "__") && !strings.HasSuffix(name, "__") {
			return extractor.VisibilityPrivate
		}
		if strings.HasPrefix(name, "_") {
			return extractor.VisibilityInternal
		}
		return extractor.VisibilityPublic
	default:
		return extractor.VisibilityPublic
	}
}

// checkGenerated checks whether the first comment in the file matches any
// generated-file pattern.
func checkGenerated(root *sitter.Node, src []byte, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	// Find the first comment node.
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if child.Type() == "comment" {
			content := child.Content(src)
			for _, p := range patterns {
				re, err := regexp.Compile(p)
				if err != nil {
					continue
				}
				if re.MatchString(content) {
					return true
				}
			}
			return false // only check first comment
		}
	}
	return false
}

// matchesAnyPattern checks if path matches any of the glob patterns.
func matchesAnyPattern(path string, patterns []string) bool {
	base := filepath.Base(path)
	for _, p := range patterns {
		if matched, _ := filepath.Match(p, base); matched {
			return true
		}
		// Also check if the path contains the pattern directory.
		if strings.Contains(path, strings.TrimSuffix(p, "/*")) {
			if strings.HasSuffix(p, "/*") {
				return true
			}
		}
	}
	return false
}

// trimQuotes removes surrounding quotes from a string literal.
func trimQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '`' && s[len(s)-1] == '`') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
