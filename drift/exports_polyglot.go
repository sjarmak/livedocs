package drift

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// pythonTestPatterns matches Python test files that should be excluded.
var pythonTestPatterns = []string{
	"test_*.py",
	"*_test.py",
}

// tsTestPatterns matches TypeScript test files that should be excluded.
var tsTestPatterns = []string{
	"*.test.ts",
	"*.test.tsx",
	"*.spec.ts",
	"*.spec.tsx",
}

// isPythonTestFile returns true if the filename matches a Python test pattern.
func isPythonTestFile(name string) bool {
	for _, p := range pythonTestPatterns {
		if matched, _ := filepath.Match(p, name); matched {
			return true
		}
	}
	return false
}

// isTSTestFile returns true if the filename matches a TypeScript test pattern.
func isTSTestFile(name string) bool {
	for _, p := range tsTestPatterns {
		if matched, _ := filepath.Match(p, name); matched {
			return true
		}
	}
	return false
}

// isPythonPublic returns true if the name is a public Python symbol
// (no leading underscore).
func isPythonPublic(name string) bool {
	return name != "" && !strings.HasPrefix(name, "_")
}

// ExtractPythonExports parses Python files in the given directory (non-recursive)
// and returns all public symbol names (classes, functions, module-level variables).
// Symbols prefixed with underscore are excluded. Only top-level definitions are
// included; methods nested inside classes are not.
func ExtractPythonExports(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}

	lang := python.GetLanguage()
	ctx := context.Background()
	symbolSet := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".py") {
			continue
		}
		if isPythonTestFile(name) {
			continue
		}

		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}

		root, err := sitter.ParseCtx(ctx, src, lang)
		if err != nil {
			continue
		}

		extractPythonTopLevel(root, src, symbolSet)
	}

	symbols := make([]string, 0, len(symbolSet))
	for s := range symbolSet {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	return symbols, nil
}

// extractPythonTopLevel walks only the top-level children of a Python module
// and extracts public symbols: function defs, class defs, decorated defs,
// and module-level variable assignments.
func extractPythonTopLevel(root *sitter.Node, src []byte, symbols map[string]bool) {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		switch child.Type() {
		case "function_definition", "class_definition":
			name := childFieldContent(child, "name", src)
			if isPythonPublic(name) {
				symbols[name] = true
			}

		case "decorated_definition":
			// Look inside for the wrapped function or class definition.
			for j := 0; j < int(child.NamedChildCount()); j++ {
				inner := child.NamedChild(j)
				if inner.Type() == "function_definition" || inner.Type() == "class_definition" {
					name := childFieldContent(inner, "name", src)
					if isPythonPublic(name) {
						symbols[name] = true
					}
				}
			}

		case "expression_statement":
			// Module-level assignments: MAX_RETRIES = 3
			extractPythonAssignment(child, src, symbols)
		}
	}
}

// extractPythonAssignment extracts variable names from assignment expressions
// inside expression_statement nodes. Only simple identifier targets are
// extracted (not tuple unpacking, subscripts, etc.).
func extractPythonAssignment(node *sitter.Node, src []byte, symbols map[string]bool) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "assignment" {
			// The left side of the assignment is the target.
			left := child.ChildByFieldName("left")
			if left != nil && left.Type() == "identifier" {
				name := left.Content(src)
				if isPythonPublic(name) {
					symbols[name] = true
				}
			}
		}
	}
}

// ExtractTypeScriptExports parses TypeScript files in the given directory
// (non-recursive) and returns all exported symbol names (functions, classes,
// interfaces, types, enums, constants).
func ExtractTypeScriptExports(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}

	lang := typescript.GetLanguage()
	ctx := context.Background()
	symbolSet := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".ts") && !strings.HasSuffix(name, ".tsx") {
			continue
		}
		if isTSTestFile(name) {
			continue
		}
		if strings.HasSuffix(name, ".d.ts") {
			continue
		}

		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}

		root, err := sitter.ParseCtx(ctx, src, lang)
		if err != nil {
			continue
		}

		extractTSExports(root, src, symbolSet)
	}

	symbols := make([]string, 0, len(symbolSet))
	for s := range symbolSet {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	return symbols, nil
}

// extractTSExports walks the top-level of a TypeScript program and extracts
// names from export_statement nodes.
func extractTSExports(root *sitter.Node, src []byte, symbols map[string]bool) {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		child := root.NamedChild(i)
		if child.Type() != "export_statement" {
			continue
		}

		// export_statement wraps the actual declaration.
		// Walk its children to find the declaration and extract its name.
		for j := 0; j < int(child.NamedChildCount()); j++ {
			decl := child.NamedChild(j)
			switch decl.Type() {
			case "function_declaration", "class_declaration",
				"interface_declaration", "type_alias_declaration",
				"enum_declaration":
				name := childFieldContent(decl, "name", src)
				if name != "" {
					symbols[name] = true
				}

			case "lexical_declaration":
				// export const X = ... / export let X = ...
				extractTSLexicalNames(decl, src, symbols)

			case "export_clause":
				// export { A, B, C }
				extractTSExportClause(decl, src, symbols)
			}
		}
	}
}

// extractTSLexicalNames extracts variable names from a lexical_declaration
// (const/let). Each variable_declarator child has a "name" field.
func extractTSLexicalNames(node *sitter.Node, src []byte, symbols map[string]bool) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "variable_declarator" {
			name := childFieldContent(child, "name", src)
			if name != "" {
				symbols[name] = true
			}
		}
	}
}

// extractTSExportClause extracts names from an export clause: export { A, B }.
func extractTSExportClause(node *sitter.Node, src []byte, symbols map[string]bool) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() == "export_specifier" {
			name := childFieldContent(child, "name", src)
			if name != "" {
				symbols[name] = true
			}
			// Fallback: first identifier child.
			if name == "" {
				for k := 0; k < int(child.NamedChildCount()); k++ {
					ident := child.NamedChild(k)
					if ident.Type() == "identifier" {
						symbols[ident.Content(src)] = true
						break
					}
				}
			}
		}
	}
}

// childFieldContent returns the text content of a named field on a node.
func childFieldContent(node *sitter.Node, field string, src []byte) string {
	child := node.ChildByFieldName(field)
	if child != nil {
		return child.Content(src)
	}
	return ""
}

// ExtractCodeExports extracts exported symbols from all supported languages
// found in the given directory. It dispatches to language-specific extractors
// based on file extensions present.
func ExtractCodeExports(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}

	hasGo := false
	hasPython := false
	hasTS := false

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".go"):
			hasGo = true
		case strings.HasSuffix(name, ".py"):
			hasPython = true
		case strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".tsx"):
			hasTS = true
		}
	}

	symbolSet := make(map[string]bool)

	if hasGo {
		goSymbols, err := ExtractGoExports(dir)
		if err == nil {
			for _, s := range goSymbols {
				symbolSet[s] = true
			}
		}
	}

	if hasPython {
		pySymbols, err := ExtractPythonExports(dir)
		if err == nil {
			for _, s := range pySymbols {
				symbolSet[s] = true
			}
		}
	}

	if hasTS {
		tsSymbols, err := ExtractTypeScriptExports(dir)
		if err == nil {
			for _, s := range tsSymbols {
				symbolSet[s] = true
			}
		}
	}

	symbols := make([]string, 0, len(symbolSet))
	for s := range symbolSet {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	return symbols, nil
}
