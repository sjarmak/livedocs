// Package goextractor implements a deep Go extractor using go/packages, go/types,
// and go/doc. It produces compiler-grade claims including type resolution,
// interface satisfaction, and full function signatures.
//
// This extractor emits all 10 structural predicates (both tree-sitter-safe and
// deep-only). It is designed for initial indexing and nightly validation runs,
// not per-commit hot paths.
package goextractor

import (
	"context"
	"fmt"
	"go/ast"
	"go/doc"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/live-docs/live_docs/extractor"
	"github.com/live-docs/live_docs/scip"
	"golang.org/x/tools/go/packages"
)

const (
	extractorName    = "go-deep"
	extractorVersion = "0.1.0"
)

// GoDeepExtractor extracts compiler-grade claims from Go source code using
// go/packages for loading, go/types for type resolution, and go/doc for
// documentation extraction.
type GoDeepExtractor struct {
	// Repo is the repository identifier (e.g., "kubernetes/kubernetes").
	Repo string

	// ModulePath override. If empty, detected from go/packages.
	ModulePath string

	// ModuleVersion override. If empty, defaults to "".
	ModuleVersion string
}

// Name returns the stable extractor identifier.
func (e *GoDeepExtractor) Name() string { return extractorName }

// Version returns the extractor version string.
func (e *GoDeepExtractor) Version() string { return extractorVersion }

// Extract loads Go packages at the given directory path and returns claims for
// all symbols found. The lang parameter must be "go".
func (e *GoDeepExtractor) Extract(ctx context.Context, path string, lang string) ([]extractor.Claim, error) {
	if lang != "go" {
		return nil, fmt.Errorf("goextractor: unsupported language %q, expected \"go\"", lang)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("goextractor: resolve path %q: %w", path, err)
	}

	pkgs, err := e.loadPackages(ctx, absPath)
	if err != nil {
		return nil, fmt.Errorf("goextractor: load packages at %q: %w", absPath, err)
	}

	now := time.Now().UTC()
	var claims []extractor.Claim

	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}
		pkgClaims := e.extractPackage(pkg, now)
		claims = append(claims, pkgClaims...)
	}

	// Extract interface implementations across all loaded packages.
	implClaims := e.extractImplements(pkgs, now)
	claims = append(claims, implClaims...)

	return claims, nil
}

// loadPackages uses go/packages to load all Go packages matching the given path.
// If dir is a file path, its parent directory is used as the working directory.
func (e *GoDeepExtractor) loadPackages(ctx context.Context, dir string) ([]*packages.Package, error) {
	// Ensure dir is a directory, not a file.
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}

	cfg := &packages.Config{
		Mode: packages.NeedTypes |
			packages.NeedDeps |
			packages.NeedSyntax |
			packages.NeedName |
			packages.NeedFiles |
			packages.NeedImports |
			packages.NeedTypesInfo |
			packages.NeedModule,
		Dir:     dir,
		Context: ctx,
		Tests:   true,
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("packages.Load: %w", err)
	}

	// When Tests=true, go/packages loads both the regular and test variant
	// of each package. The test variant (ID contains "[") includes both regular
	// and test symbols. We prefer the test variant to capture test symbols,
	// and deduplicate by package path.
	byPath := make(map[string]*packages.Package)
	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}
		existing, ok := byPath[pkg.PkgPath]
		if !ok {
			byPath[pkg.PkgPath] = pkg
			continue
		}
		// Prefer the variant with more syntax files (the test variant).
		if len(pkg.Syntax) > len(existing.Syntax) {
			byPath[pkg.PkgPath] = pkg
		}
	}

	result := make([]*packages.Package, 0, len(byPath))
	for _, pkg := range byPath {
		result = append(result, pkg)
	}

	return result, nil
}

// extractPackage extracts claims from a single loaded package.
func (e *GoDeepExtractor) extractPackage(pkg *packages.Package, now time.Time) []extractor.Claim {
	var claims []extractor.Claim

	importPath := pkg.PkgPath
	modulePath := e.modulePath(pkg)
	moduleVersion := e.ModuleVersion

	// Detect test and generated files BEFORE doc extraction, because
	// doc.NewFromFiles modifies the AST by re-associating comments,
	// which breaks ast.IsGenerated checks.
	testFiles := make(map[string]bool)
	generatedFiles := make(map[string]bool)

	for _, f := range pkg.GoFiles {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_test.go") {
			testFiles[f] = true
		}
	}
	for _, syn := range pkg.Syntax {
		fname := pkg.Fset.File(syn.Pos()).Name()
		base := filepath.Base(fname)
		if strings.HasSuffix(base, "_test.go") {
			testFiles[fname] = true
		}
		if isGeneratedFile(syn) {
			generatedFiles[fname] = true
		}
	}

	// Build go/doc package for documentation extraction.
	// Note: doc.NewFromFiles modifies the AST in place (re-associates comments).
	var docPkg *doc.Package
	if len(pkg.Syntax) > 0 {
		var err error
		docPkg, err = doc.NewFromFiles(pkg.Fset, pkg.Syntax, importPath, doc.AllDecls|doc.AllMethods)
		if err != nil {
			// Non-fatal: proceed without doc extraction.
			docPkg = nil
		}
	}

	// Build maps for doc lookup.
	typeDocs := make(map[string]string)
	funcDocs := make(map[string]string)
	methodDocs := make(map[string]map[string]string) // type -> method -> doc
	if docPkg != nil {
		for _, t := range docPkg.Types {
			typeDocs[t.Name] = t.Doc
			methodDocs[t.Name] = make(map[string]string)
			for _, m := range t.Methods {
				methodDocs[t.Name][m.Name] = m.Doc
			}
			for _, f := range t.Funcs {
				funcDocs[f.Name] = f.Doc
			}
		}
		for _, f := range docPkg.Funcs {
			funcDocs[f.Name] = f.Doc
		}
	}

	// Iterate all scope objects in the package.
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if obj == nil {
			continue
		}

		pos := pkg.Fset.Position(obj.Pos())
		sourceFile := pos.Filename
		sourceLine := pos.Line

		kind := objectKind(obj)
		vis := goVisibility(name)
		scipSym := scip.FormatGoSymbol(modulePath, moduleVersion, importPath, name, goKindToSCIP(kind), "")
		base := claimBase(e.Repo, importPath, name, "go", kind, vis, sourceFile, sourceLine, scipSym, now)

		// defines
		c := base
		c.Predicate = extractor.PredicateDefines
		claims = append(claims, c)

		// has_kind
		c = base
		c.Predicate = extractor.PredicateHasKind
		c.ObjectText = string(kind)
		claims = append(claims, c)

		// has_signature (for funcs)
		if fn, ok := obj.(*types.Func); ok {
			sig := fn.Type().(*types.Signature)
			c = base
			c.Predicate = extractor.PredicateHasSignature
			c.ObjectText = sig.String()
			claims = append(claims, c)
		}

		// exports (for exported names)
		if vis == extractor.VisibilityPublic {
			c = base
			c.Predicate = extractor.PredicateExports
			claims = append(claims, c)
		}

		// has_doc
		if docText := lookupDoc(name, kind, typeDocs, funcDocs); docText != "" {
			c = base
			c.Predicate = extractor.PredicateHasDoc
			c.ObjectText = strings.TrimSpace(docText)
			c.Confidence = 0.85
			claims = append(claims, c)
		}

		// is_test
		if testFiles[sourceFile] {
			c = base
			c.Predicate = extractor.PredicateIsTest
			claims = append(claims, c)
		}

		// is_generated
		if generatedFiles[sourceFile] {
			c = base
			c.Predicate = extractor.PredicateIsGenerated
			claims = append(claims, c)
		}

		// encloses (package encloses symbol)
		enc := claimBase(e.Repo, importPath, importPath, "go", extractor.KindModule, extractor.VisibilityPublic, sourceFile, sourceLine, "", now)
		enc.Predicate = extractor.PredicateEncloses
		enc.ObjectName = name
		claims = append(claims, enc)
	}

	// Extract methods on named types.
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}

		for i := 0; i < named.NumMethods(); i++ {
			m := named.Method(i)
			mPos := pkg.Fset.Position(m.Pos())
			mVis := goVisibility(m.Name())
			mSCIP := scip.FormatGoSymbol(modulePath, moduleVersion, importPath, m.Name(), scip.KindMethod, name)
			mName := name + "." + m.Name()
			sig := m.Type().(*types.Signature)
			base := claimBase(e.Repo, importPath, mName, "go", extractor.KindMethod, mVis, mPos.Filename, mPos.Line, mSCIP, now)

			// defines
			c := base
			c.Predicate = extractor.PredicateDefines
			claims = append(claims, c)

			// has_kind
			c = base
			c.Predicate = extractor.PredicateHasKind
			c.ObjectText = string(extractor.KindMethod)
			claims = append(claims, c)

			// has_signature
			c = base
			c.Predicate = extractor.PredicateHasSignature
			c.ObjectText = sig.String()
			claims = append(claims, c)

			// has_doc
			if md, ok := methodDocs[name]; ok {
				if docText, ok := md[m.Name()]; ok && docText != "" {
					c = base
					c.Predicate = extractor.PredicateHasDoc
					c.ObjectText = strings.TrimSpace(docText)
					c.Confidence = 0.85
					claims = append(claims, c)
				}
			}

			// exports
			if mVis == extractor.VisibilityPublic {
				c = base
				c.Predicate = extractor.PredicateExports
				claims = append(claims, c)
			}
		}
	}

	// imports claims
	for impPath := range pkg.Imports {
		c := claimBase(e.Repo, importPath, importPath, "go", extractor.KindModule, extractor.VisibilityPublic, firstFile(pkg), 0, "", now)
		c.Predicate = extractor.PredicateImports
		c.ObjectName = impPath
		claims = append(claims, c)
	}

	return claims
}

// extractImplements detects interface satisfaction across all loaded packages.
// For each concrete type T and each interface I in any loaded package, if
// T implements I, an "implements" claim is emitted.
func (e *GoDeepExtractor) extractImplements(pkgs []*packages.Package, now time.Time) []extractor.Claim {
	// Collect all named interfaces and concrete types.
	type namedIface struct {
		importPath string
		name       string
		iface      *types.Interface
	}
	type namedType struct {
		importPath string
		name       string
		typ        types.Type
		pos        token.Position
		modulePath string
	}

	var ifaces []namedIface
	var concretes []namedType

	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}
		scope := pkg.Types.Scope()
		modPath := e.modulePath(pkg)
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			tn, ok := obj.(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			iface, isIface := named.Underlying().(*types.Interface)
			if isIface {
				ifaces = append(ifaces, namedIface{
					importPath: pkg.PkgPath,
					name:       name,
					iface:      iface,
				})
			} else {
				concretes = append(concretes, namedType{
					importPath: pkg.PkgPath,
					name:       name,
					typ:        named,
					pos:        pkg.Fset.Position(obj.Pos()),
					modulePath: modPath,
				})
			}
		}
	}

	var claims []extractor.Claim
	for _, ct := range concretes {
		for _, iface := range ifaces {
			// Skip trivial empty interfaces.
			if iface.iface.NumMethods() == 0 {
				continue
			}
			// Check both T and *T.
			if types.Implements(ct.typ, iface.iface) || types.Implements(types.NewPointer(ct.typ), iface.iface) {
				claims = append(claims, extractor.Claim{
					SubjectRepo:       e.Repo,
					SubjectImportPath: ct.importPath,
					SubjectName:       ct.name,
					Language:          "go",
					Kind:              extractor.KindType,
					Visibility:        goVisibility(ct.name),
					Predicate:         extractor.PredicateImplements,
					ObjectName:        iface.importPath + "." + iface.name,
					SourceFile:        ct.pos.Filename,
					SourceLine:        ct.pos.Line,
					Confidence:        1.0,
					ClaimTier:         extractor.TierStructural,
					Extractor:         extractorName,
					ExtractorVersion:  extractorVersion,
					SCIPSymbol: scip.FormatGoSymbol(
						ct.modulePath, e.ModuleVersion, ct.importPath, ct.name, scip.KindType, "",
					),
					LastVerified: now,
				})
			}
		}
	}

	return claims
}

// modulePath returns the Go module path for the package.
func (e *GoDeepExtractor) modulePath(pkg *packages.Package) string {
	if e.ModulePath != "" {
		return e.ModulePath
	}
	if pkg.Module != nil {
		return pkg.Module.Path
	}
	// Fallback: use the first path component as module path.
	return pkg.PkgPath
}

// objectKind maps a go/types.Object to an extractor.SymbolKind.
func objectKind(obj types.Object) extractor.SymbolKind {
	switch obj.(type) {
	case *types.TypeName:
		return extractor.KindType
	case *types.Func:
		return extractor.KindFunc
	case *types.Const:
		return extractor.KindConst
	case *types.Var:
		return extractor.KindVar
	default:
		return extractor.KindVar
	}
}

// goVisibility determines Go visibility from the symbol name.
func goVisibility(name string) extractor.Visibility {
	if len(name) == 0 {
		return extractor.VisibilityPrivate
	}
	if unicode.IsUpper(rune(name[0])) {
		return extractor.VisibilityPublic
	}
	return extractor.VisibilityInternal
}

// goKindToSCIP maps extractor.SymbolKind to scip.SymbolKind for SCIP symbol generation.
func goKindToSCIP(k extractor.SymbolKind) scip.SymbolKind {
	switch k {
	case extractor.KindType:
		return scip.KindType
	case extractor.KindFunc:
		return scip.KindFunc
	case extractor.KindMethod:
		return scip.KindMethod
	case extractor.KindConst:
		return scip.KindConst
	case extractor.KindVar:
		return scip.KindVar
	default:
		return scip.KindVar
	}
}

// claimBase returns a partially filled Claim with common fields.
// Callers set Predicate, ObjectText, ObjectName, and override Confidence as needed.
func claimBase(repo, importPath, name, lang string, kind extractor.SymbolKind, vis extractor.Visibility, sourceFile string, sourceLine int, scipSym string, now time.Time) extractor.Claim {
	return extractor.Claim{
		SubjectRepo:       repo,
		SubjectImportPath: importPath,
		SubjectName:       name,
		Language:          lang,
		Kind:              kind,
		Visibility:        vis,
		SourceFile:        sourceFile,
		SourceLine:        sourceLine,
		Confidence:        1.0,
		ClaimTier:         extractor.TierStructural,
		Extractor:         extractorName,
		ExtractorVersion:  extractorVersion,
		SCIPSymbol:        scipSym,
		LastVerified:      now,
	}
}

// lookupDoc finds the doc string for a symbol.
func lookupDoc(name string, kind extractor.SymbolKind, typeDocs, funcDocs map[string]string) string {
	switch kind {
	case extractor.KindType:
		return typeDocs[name]
	case extractor.KindFunc:
		return funcDocs[name]
	default:
		return ""
	}
}

// firstFile returns the first Go file in the package, or empty string.
func firstFile(pkg *packages.Package) string {
	if len(pkg.GoFiles) > 0 {
		return pkg.GoFiles[0]
	}
	return ""
}

// isGeneratedFile uses the stdlib ast.IsGenerated which checks for the standard
// "Code generated ... DO NOT EDIT." header comment per the Go convention.
func isGeneratedFile(f *ast.File) bool {
	return ast.IsGenerated(f)
}
