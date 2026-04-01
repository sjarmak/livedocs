// Package drift detects documentation drift by comparing symbol references
// in README files against symbols actually present in Go source code.
//
// It extracts three categories of references from Markdown:
//   - Backtick-quoted identifiers (e.g. `NewInformer`)
//   - Package path references (e.g. tools/cache, k8s.io/client-go)
//   - Code-like identifiers in prose: dotted (pkg.Symbol), camelCase,
//     snake_case, or PascalCase with multiple uppercase transitions (ConfigMap)
//
// Plain capitalized English words (e.g. "Backward", "Server") are not
// extracted from prose, avoiding false positives on natural language.
//
// These are compared against exported symbols extracted from the Go source
// tree to find stale references and undocumented symbols.
package drift

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Finding represents a single drift finding.
type Finding struct {
	Kind       FindingKind
	Symbol     string
	SourceFile string // README file or Go source file
	Detail     string
}

// FindingKind classifies a drift finding.
type FindingKind string

const (
	// StaleReference means a symbol is mentioned in the README but not found in code.
	StaleReference FindingKind = "stale"
	// Undocumented means a symbol exists in code but is not mentioned in the README.
	Undocumented FindingKind = "undocumented"
	// StalePackageRef means a package path is mentioned in the README but does not exist.
	StalePackageRef FindingKind = "stale_package"
)

// Report aggregates drift findings for a single README.
type Report struct {
	ReadmePath        string
	PackageDir        string
	StaleCount        int
	UndocumentedCount int
	StalePackageCount int
	Findings          []Finding
	ReadmeSymbols     []string // symbols extracted from README
	CodeSymbols       []string // exported symbols found in code
}

// backtickRe matches backtick-quoted identifiers.
var backtickRe = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_.]*)`")

// proseCodeRefRe matches identifiers in prose that look like code references:
//   - Dotted identifiers: pkg.Symbol, foo.Bar
//   - camelCase: parseJSON, myFunc
//   - snake_case: my_handler, some_func
//   - PascalCase with internal uppercase transitions: ConfigMap, NewInformerFunc
var proseCodeRefRe = regexp.MustCompile(`\b([a-zA-Z_][a-zA-Z0-9_]*\.[A-Z][a-zA-Z0-9_]*)\b|` + // pkg.Symbol
	`\b([a-z][a-zA-Z0-9]*[A-Z][a-zA-Z0-9]*)\b|` + // camelCase
	`\b([a-zA-Z][a-zA-Z0-9]*_[a-zA-Z0-9_]+)\b|` + // snake_case
	`\b([A-Z][a-z]+[A-Z][a-zA-Z0-9]*)\b`) // PascalCase with multiple uppercase transitions

// importPathRe matches Go import paths like k8s.io/foo/bar or tools/cache.
var importPathRe = regexp.MustCompile(`k8s\.io/[a-zA-Z0-9_/-]+`)

// relPkgPathRe matches relative package paths like tools/cache, plugin/pkg/client.
var relPkgPathRe = regexp.MustCompile(`(?:^|[\s` + "`" + `"])([a-z][a-z0-9_-]*/[a-z][a-z0-9_/-]*)`)

// urlLineRe detects lines that are primarily URLs (http/https/git links).
var urlLineRe = regexp.MustCompile(`https?://|git://`)

// ExtractReadmeSymbols parses a README and returns referenced Go symbols
// and package paths.
func ExtractReadmeSymbols(content string) (symbols []string, pkgPaths []string) {
	symbolSet := make(map[string]bool)
	pkgSet := make(map[string]bool)

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()

		// Skip markdown link definitions and image references.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]:") {
			continue
		}

		// Extract backtick-quoted identifiers.
		for _, match := range backtickRe.FindAllStringSubmatch(line, -1) {
			ident := match[1]
			// Filter out file paths, URLs, flags, and format strings.
			if strings.Contains(ident, "/") || strings.HasPrefix(ident, "-") ||
				strings.HasPrefix(ident, "%") || strings.Contains(ident, "=") ||
				strings.HasPrefix(ident, "v") && len(ident) > 1 && ident[1] >= '0' && ident[1] <= '9' {
				continue
			}
			// Must look like a Go identifier (start with letter).
			if len(ident) >= 2 && isGoIdentChar(rune(ident[0])) {
				symbolSet[ident] = true
			}
		}

		// Extract code-like identifiers from prose (not in code blocks or URLs).
		// Only matches patterns that are structurally code-like:
		// dotted (pkg.Symbol), camelCase, snake_case, or PascalCase with
		// multiple uppercase transitions (e.g. ConfigMap, NewInformerFunc).
		if !strings.HasPrefix(trimmed, "```") && !strings.HasPrefix(trimmed, "    ") {
			for _, match := range proseCodeRefRe.FindAllStringSubmatch(line, -1) {
				// proseCodeRefRe has 4 capture groups; pick the non-empty one.
				for _, ident := range match[1:] {
					if ident != "" && len(ident) >= 2 {
						symbolSet[ident] = true
					}
				}
			}
		}

		// Extract k8s.io import paths (only from non-URL lines).
		if !urlLineRe.MatchString(line) {
			for _, match := range importPathRe.FindAllString(line, -1) {
				pkgSet[match] = true
			}
			for _, match := range relPkgPathRe.FindAllStringSubmatch(line, -1) {
				if len(match) > 1 && match[1] != "" {
					path := match[1]
					// Filter out common false positives.
					if !strings.HasPrefix(path, "com/") && !strings.HasPrefix(path, "org/") &&
						!strings.HasPrefix(path, "io/") && !strings.HasPrefix(path, "forum/") &&
						!strings.HasPrefix(path, "msg/") && !strings.HasPrefix(path, "dev/") &&
						!strings.HasPrefix(path, "authn/") {
						pkgSet[path] = true
					}
				}
			}
		} else {
			// From URL lines, only extract k8s.io paths.
			for _, match := range importPathRe.FindAllString(line, -1) {
				pkgSet[match] = true
			}
		}
	}

	for s := range symbolSet {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)

	for p := range pkgSet {
		pkgPaths = append(pkgPaths, p)
	}
	sort.Strings(pkgPaths)

	return symbols, pkgPaths
}

// ExtractGoExports parses Go files in the given directory (non-recursive)
// and returns all exported symbol names.
func ExtractGoExports(dir string) ([]string, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		// Skip test files.
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse dir %s: %w", dir, err)
	}

	symbolSet := make(map[string]bool)
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if s.Name.IsExported() {
								symbolSet[s.Name.Name] = true
							}
						case *ast.ValueSpec:
							for _, name := range s.Names {
								if name.IsExported() {
									symbolSet[name.Name] = true
								}
							}
						}
					}
				case *ast.FuncDecl:
					if d.Name.IsExported() {
						name := d.Name.Name
						if d.Recv != nil && len(d.Recv.List) > 0 {
							// Method: prepend receiver type name.
							recvType := receiverTypeName(d.Recv.List[0].Type)
							if recvType != "" {
								name = recvType + "." + name
							}
						}
						symbolSet[name] = true
					}
				}
			}
		}
	}

	var symbols []string
	for s := range symbolSet {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	return symbols, nil
}

// CheckSubdirExists checks whether a subdirectory name exists under baseDir.
func CheckSubdirExists(baseDir, subdir string) bool {
	target := filepath.Join(baseDir, subdir)
	info, err := os.Stat(target)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// Detect runs drift detection: compares README symbols against code symbols.
// readmePath is the path to the README file, codeDir is the directory to scan
// for Go exports. If codeDir is empty, it defaults to the README's directory.
func Detect(readmePath string, codeDir string) (*Report, error) {
	content, err := os.ReadFile(readmePath)
	if err != nil {
		return nil, fmt.Errorf("read README %s: %w", readmePath, err)
	}

	if codeDir == "" {
		codeDir = filepath.Dir(readmePath)
	}

	readmeSymbols, pkgPaths := ExtractReadmeSymbols(string(content))

	codeSymbols, err := ExtractGoExports(codeDir)
	if err != nil {
		// If no Go files in directory, code symbols is empty.
		codeSymbols = nil
	}

	report := &Report{
		ReadmePath:    readmePath,
		PackageDir:    codeDir,
		ReadmeSymbols: readmeSymbols,
		CodeSymbols:   codeSymbols,
	}

	codeSet := make(map[string]bool)
	for _, s := range codeSymbols {
		codeSet[s] = true
		// Also index the bare function name without receiver.
		if idx := strings.Index(s, "."); idx > 0 {
			codeSet[s[idx+1:]] = true
		}
	}

	readmeSet := make(map[string]bool)
	for _, s := range readmeSymbols {
		readmeSet[s] = true
	}

	// Stale: in README but not in code.
	for _, s := range readmeSymbols {
		if !codeSet[s] {
			report.Findings = append(report.Findings, Finding{
				Kind:       StaleReference,
				Symbol:     s,
				SourceFile: readmePath,
				Detail:     fmt.Sprintf("symbol %q referenced in README but not found in code exports", s),
			})
			report.StaleCount++
		}
	}

	// Undocumented: in code but not in README.
	for _, s := range codeSymbols {
		bare := s
		if idx := strings.Index(s, "."); idx > 0 {
			bare = s[idx+1:]
		}
		if !readmeSet[s] && !readmeSet[bare] {
			report.Findings = append(report.Findings, Finding{
				Kind:       Undocumented,
				Symbol:     s,
				SourceFile: codeDir,
				Detail:     fmt.Sprintf("exported symbol %q not mentioned in README", s),
			})
			report.UndocumentedCount++
		}
	}

	// Stale package refs: check subdirectory references.
	baseDir := filepath.Dir(readmePath)
	for _, pkg := range pkgPaths {
		// Only check relative package references (not full k8s.io/... paths).
		if !strings.HasPrefix(pkg, "k8s.io/") {
			if !CheckSubdirExists(baseDir, pkg) {
				report.Findings = append(report.Findings, Finding{
					Kind:       StalePackageRef,
					Symbol:     pkg,
					SourceFile: readmePath,
					Detail:     fmt.Sprintf("package path %q referenced in README but directory not found", pkg),
				})
				report.StalePackageCount++
			}
		}
	}

	return report, nil
}

// DetectMultiple runs drift detection on multiple README files.
func DetectMultiple(targets []Target) ([]*Report, error) {
	var reports []*Report
	for _, t := range targets {
		report, err := Detect(t.ReadmePath, t.CodeDir)
		if err != nil {
			return nil, fmt.Errorf("detect %s: %w", t.ReadmePath, err)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// Target specifies a README and its associated code directory.
type Target struct {
	ReadmePath string
	CodeDir    string // if empty, defaults to README's directory
}

// receiverTypeName extracts the type name from a method receiver expression.
func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	default:
		return ""
	}
}

func isGoIdentChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
}

// isAllCaps returns true if the string is all uppercase letters (like "NOT", "NO").
func isAllCaps(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// FormatReport formats a Report as a Markdown section.
func FormatReport(r *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n", r.ReadmePath)
	fmt.Fprintf(&b, "- **Code directory**: `%s`\n", r.PackageDir)
	fmt.Fprintf(&b, "- **README symbols found**: %d\n", len(r.ReadmeSymbols))
	fmt.Fprintf(&b, "- **Code exports found**: %d\n", len(r.CodeSymbols))
	fmt.Fprintf(&b, "- **Stale references**: %d\n", r.StaleCount)
	fmt.Fprintf(&b, "- **Undocumented exports**: %d\n", r.UndocumentedCount)
	fmt.Fprintf(&b, "- **Stale package refs**: %d\n\n", r.StalePackageCount)

	if len(r.Findings) == 0 {
		fmt.Fprintf(&b, "No drift detected.\n\n")
		return b.String()
	}

	// Group by kind.
	var stale, undoc, stalePkg []Finding
	for _, f := range r.Findings {
		switch f.Kind {
		case StaleReference:
			stale = append(stale, f)
		case Undocumented:
			undoc = append(undoc, f)
		case StalePackageRef:
			stalePkg = append(stalePkg, f)
		}
	}

	if len(stale) > 0 {
		fmt.Fprintf(&b, "#### Stale References (in README, not in code)\n\n")
		for _, f := range stale {
			fmt.Fprintf(&b, "- `%s`\n", f.Symbol)
		}
		b.WriteString("\n")
	}

	if len(undoc) > 0 {
		fmt.Fprintf(&b, "#### Undocumented Exports (in code, not in README)\n\n")
		limit := len(undoc)
		if limit > 50 {
			limit = 50
		}
		for _, f := range undoc[:limit] {
			fmt.Fprintf(&b, "- `%s`\n", f.Symbol)
		}
		if len(undoc) > 50 {
			fmt.Fprintf(&b, "- ... and %d more\n", len(undoc)-50)
		}
		b.WriteString("\n")
	}

	if len(stalePkg) > 0 {
		fmt.Fprintf(&b, "#### Stale Package References\n\n")
		for _, f := range stalePkg {
			fmt.Fprintf(&b, "- `%s`\n", f.Symbol)
		}
		b.WriteString("\n")
	}

	return b.String()
}
