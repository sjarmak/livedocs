// Package tribal — assertion.go provides a deterministic extractor that
// surfaces invariants from Go source code. It recognizes testify assertions
// (require.NoError, require.Error, assert.True, assert.False), panic calls
// with string literals, and //nolint:... // reason comments.
//
// The extractor is zero-LLM, zero-budget, and emits tribal facts with
// kind="invariant", model="" (deterministic), and confidence=1.0. It is
// intended to balance LLM-biased rationale/quirk sources with a deterministic
// invariant source.
package tribal

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"

	"github.com/sjarmak/livedocs/db"
)

const (
	assertionExtractorName    = "assertion"
	assertionExtractorVersion = "0.1.0"
)

// ExtractInvariants parses the given Go source and returns TribalFact
// entries for each matched invariant-producing statement: require.NoError,
// require.Error, assert.True, assert.False, panic("..."), and
// //nolint:lintername // reason comments.
//
// The sourcePath is used only for source_ref metadata. Returns an error only
// if the source fails to parse as Go.
func ExtractInvariants(sourcePath string, src []byte) ([]db.TribalFact, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, sourcePath, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("assertion extractor: parse %q: %w", sourcePath, err)
	}

	lines := splitLinesBytes(src)
	var facts []db.TribalFact

	// Walk the AST looking for call expressions that match our invariant
	// patterns. Enclosing function is resolved positionally via the AST's
	// declaration list.
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if f := matchCallExpr(call, fset, file, lines, sourcePath); f != nil {
				facts = append(facts, *f)
			}
		}
		return true
	})

	// Walk comments for //nolint:... directives.
	for _, group := range file.Comments {
		if group == nil {
			continue
		}
		for _, c := range group.List {
			if f := matchNolintComment(c, fset, file, lines, sourcePath); f != nil {
				facts = append(facts, *f)
			}
		}
	}

	return facts, nil
}

// matchCallExpr inspects a call expression and returns a TribalFact if it
// matches one of the recognized invariant patterns. Returns nil otherwise.
func matchCallExpr(call *ast.CallExpr, fset *token.FileSet, file *ast.File, lines [][]byte, sourcePath string) *db.TribalFact {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		ident, ok := fun.X.(*ast.Ident)
		if !ok {
			return nil
		}
		selName := fun.Sel.Name
		switch ident.Name {
		case "require":
			if selName == "NoError" || selName == "Error" {
				line := fset.Position(call.Pos()).Line
				funcName := enclosingFuncForPos(file, call.Pos())
				body := fmt.Sprintf("%s: require.%s", funcName, selName)
				return newInvariantFact(sourcePath, line, body, lines)
			}
		case "assert":
			if selName == "True" || selName == "False" {
				line := fset.Position(call.Pos()).Line
				funcName := enclosingFuncForPos(file, call.Pos())
				body := fmt.Sprintf("%s: assert.%s", funcName, selName)
				return newInvariantFact(sourcePath, line, body, lines)
			}
		}
	case *ast.Ident:
		if fun.Name == "panic" && len(call.Args) >= 1 {
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				line := fset.Position(call.Pos()).Line
				funcName := enclosingFuncForPos(file, call.Pos())
				body := fmt.Sprintf("%s: panic(%s)", funcName, lit.Value)
				return newInvariantFact(sourcePath, line, body, lines)
			}
		}
	}
	return nil
}

// matchNolintComment returns a TribalFact if the comment matches the
// //nolint:lintername // reason pattern. Only line comments (//) are
// considered — /* */ nolint comments are not idiomatic.
func matchNolintComment(c *ast.Comment, fset *token.FileSet, file *ast.File, lines [][]byte, sourcePath string) *db.TribalFact {
	text := c.Text
	if !strings.HasPrefix(text, "//nolint") {
		return nil
	}
	// Strip leading "//" for parsing.
	remainder := strings.TrimPrefix(text, "//")
	// remainder is now "nolint[:names][ // reason]"
	linters := ""
	reason := ""

	// Split reason on the inner "//"
	if idx := strings.Index(remainder, "//"); idx >= 0 {
		directive := strings.TrimSpace(remainder[:idx])
		reason = strings.TrimSpace(remainder[idx+2:])
		remainder = directive
	}

	// remainder = "nolint" or "nolint:linter1,linter2"
	switch {
	case strings.HasPrefix(remainder, "nolint:"):
		linters = strings.TrimSpace(strings.TrimPrefix(remainder, "nolint:"))
	case remainder == "nolint":
		linters = ""
	default:
		return nil
	}

	line := fset.Position(c.Pos()).Line
	funcName := enclosingFuncForPos(file, c.Pos())

	if reason == "" {
		reason = "(no reason)"
	}
	var factBody string
	if linters == "" {
		factBody = fmt.Sprintf("%s: nolint — %s", funcName, reason)
	} else {
		factBody = fmt.Sprintf("%s: nolint %s — %s", funcName, linters, reason)
	}

	return newInvariantFact(sourcePath, line, factBody, lines)
}

// enclosingFuncForPos returns the name of the function whose body contains
// the given position. Returns "(file-level)" when the position sits outside
// any function body. Method receivers are prefixed (e.g., "Foo.Bar").
func enclosingFuncForPos(file *ast.File, pos token.Pos) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if pos >= fn.Body.Lbrace && pos <= fn.Body.Rbrace {
			return funcDeclName(fn)
		}
	}
	return "(file-level)"
}

// funcDeclName returns a display name for a function declaration, prefixing
// the receiver type when present.
func funcDeclName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		if t := recvTypeName(fn.Recv.List[0].Type); t != "" {
			return t + "." + fn.Name.Name
		}
	}
	return fn.Name.Name
}

// recvTypeName extracts the receiver type name from a method declaration.
// Handles both value (T) and pointer (*T) receivers.
func recvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// splitLinesBytes returns the source split into lines. The returned slice
// has one entry per line (no trailing empty line for a terminating \n).
func splitLinesBytes(src []byte) [][]byte {
	if len(src) == 0 {
		return nil
	}
	var out [][]byte
	start := 0
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			out = append(out, src[start:i])
			start = i + 1
		}
	}
	if start < len(src) {
		out = append(out, src[start:])
	}
	return out
}

// sourceLine returns the trimmed source line for the given 1-based line
// number, or empty string when out of range.
func sourceLine(lines [][]byte, lineNum int) string {
	if lineNum < 1 || lineNum > len(lines) {
		return ""
	}
	return strings.TrimSpace(string(lines[lineNum-1]))
}

// newInvariantFact assembles a TribalFact for an invariant match. It fills
// SourceQuote from the raw source line and SourceRef as "sourcePath:line".
func newInvariantFact(sourcePath string, lineNum int, body string, lines [][]byte) *db.TribalFact {
	quote := sourceLine(lines, lineNum)
	sourceRef := fmt.Sprintf("%s:%d", sourcePath, lineNum)
	hashInput := fmt.Sprintf("%s|%s|%s", sourceRef, body, quote)
	hash := sha256.Sum256([]byte(hashInput))
	hashHex := fmt.Sprintf("%x", hash)

	return &db.TribalFact{
		Kind:             "invariant",
		Body:             body,
		SourceQuote:      quote,
		Confidence:       1.0,
		Corroboration:    1,
		Extractor:        assertionExtractorName,
		ExtractorVersion: assertionExtractorVersion,
		Model:            "",
		StalenessHash:    hashHex,
		Status:           "active",
		Evidence: []db.TribalEvidence{
			{
				SourceType:  "assertion",
				SourceRef:   sourceRef,
				ContentHash: hashHex,
			},
		},
	}
}
