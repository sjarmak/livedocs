package renderer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/live-docs/live_docs/db"
)

// LoadPackageData queries the claims database and assembles a PackageData
// for the given import path. It reads symbols and claims, then groups them
// into the sections needed for Tier 1 rendering.
func LoadPackageData(cdb *db.ClaimsDB, importPath string) (*PackageData, error) {
	symbols, err := cdb.ListSymbolsByImportPath(importPath)
	if err != nil {
		return nil, fmt.Errorf("list symbols for %s: %w", importPath, err)
	}
	if len(symbols) == 0 {
		return nil, fmt.Errorf("no symbols found for import path %s", importPath)
	}

	pd := &PackageData{
		ImportPath: importPath,
		Repo:       symbols[0].Repo,
		Language:   symbols[0].Language,
	}

	// Collect all claims keyed by symbol ID for efficient lookup.
	claimsBySymbol := make(map[int64][]db.Claim)
	for _, sym := range symbols {
		claims, err := cdb.GetClaimsBySubject(sym.ID)
		if err != nil {
			return nil, fmt.Errorf("get claims for symbol %s: %w", sym.SymbolName, err)
		}
		claimsBySymbol[sym.ID] = claims
	}

	// Extract sections from claims.
	pd.Interfaces = extractInterfaces(symbols, claimsBySymbol)
	pd.InterfaceSatisfactions = extractSatisfactions(symbols, claimsBySymbol)
	pd.ForwardDeps = extractForwardDeps(symbols, claimsBySymbol)
	pd.ReverseDeps, pd.ReverseDepCount = extractReverseDeps(symbols, claimsBySymbol)
	pd.FunctionCategories = extractFunctionCategories(symbols, claimsBySymbol)
	pd.TypesByCategory = extractTypeCategories(symbols, claimsBySymbol)
	pd.SourceFileCount, pd.TestFileCount, pd.TestFiles = extractFileMetadata(symbols, claimsBySymbol)
	pd.SemanticEnrichmentDate = latestSemanticTimestamp(claimsBySymbol)

	return pd, nil
}

// extractInterfaces finds symbols of kind "type" that have "has_kind" claims
// with object_text "interface", then collects their methods via "encloses" and
// implementations via reverse "implements" lookups.
func extractInterfaces(symbols []db.Symbol, claimsBySymbol map[int64][]db.Claim) []InterfaceInfo {
	var interfaces []InterfaceInfo

	for _, sym := range symbols {
		if sym.Kind != "type" || sym.Visibility != "public" {
			continue
		}
		claims := claimsBySymbol[sym.ID]
		isInterface := false
		for _, cl := range claims {
			if cl.Predicate == "has_kind" && cl.ObjectText == "interface" {
				isInterface = true
				break
			}
		}
		if !isInterface {
			continue
		}

		info := InterfaceInfo{Name: sym.SymbolName}

		// Collect methods from "encloses" claims.
		for _, cl := range claims {
			if cl.Predicate == "encloses" && cl.ObjectText != "" {
				info.Methods = append(info.Methods, cl.ObjectText)
			}
		}

		// Collect implementations: scan all symbols for "implements" pointing to this interface.
		for _, otherSym := range symbols {
			for _, cl := range claimsBySymbol[otherSym.ID] {
				if cl.Predicate == "implements" && cl.ObjectText == sym.SymbolName {
					info.Implementations = append(info.Implementations, otherSym.SymbolName)
				}
			}
		}
		sort.Strings(info.Implementations)

		interfaces = append(interfaces, info)
	}

	sort.Slice(interfaces, func(i, j int) bool {
		return interfaces[i].Name < interfaces[j].Name
	})
	return interfaces
}

// extractSatisfactions groups "implements" claims by concrete type.
func extractSatisfactions(symbols []db.Symbol, claimsBySymbol map[int64][]db.Claim) []Satisfaction {
	satMap := make(map[string][]string) // concrete type -> interfaces

	for _, sym := range symbols {
		if sym.Visibility != "public" {
			continue
		}
		for _, cl := range claimsBySymbol[sym.ID] {
			if cl.Predicate == "implements" && cl.ObjectText != "" {
				satMap[sym.SymbolName] = append(satMap[sym.SymbolName], cl.ObjectText)
			}
		}
	}

	var sats []Satisfaction
	for typ, ifaces := range satMap {
		sort.Strings(ifaces)
		sats = append(sats, Satisfaction{ConcreteType: typ, Interfaces: ifaces})
	}
	sort.Slice(sats, func(i, j int) bool {
		return sats[i].ConcreteType < sats[j].ConcreteType
	})
	return sats
}

// extractForwardDeps collects "imports" claims from all symbols in the package.
func extractForwardDeps(symbols []db.Symbol, claimsBySymbol map[int64][]db.Claim) []Dependency {
	seen := make(map[string]string) // import path -> annotation

	for _, sym := range symbols {
		for _, cl := range claimsBySymbol[sym.ID] {
			if cl.Predicate == "imports" && cl.ObjectText != "" {
				// Use the first annotation we find; later ones are redundant.
				if _, ok := seen[cl.ObjectText]; !ok {
					// ObjectName holds the annotation if present.
					seen[cl.ObjectText] = ""
				}
			}
		}
	}

	var deps []Dependency
	for ip, ann := range seen {
		deps = append(deps, Dependency{ImportPath: ip, Annotation: ann})
	}
	sort.Slice(deps, func(i, j int) bool {
		return deps[i].ImportPath < deps[j].ImportPath
	})
	return deps
}

// extractReverseDeps collects "exports" claims with predicate "exports" where
// object_text contains the importing package path. We use a convention:
// a claim with predicate "exports" and object_text = "reverse_dep:<import_path>"
// records a reverse dependency.
func extractReverseDeps(symbols []db.Symbol, claimsBySymbol map[int64][]db.Claim) ([]ReverseDep, int) {
	seen := make(map[string]bool)
	var deps []ReverseDep

	for _, sym := range symbols {
		for _, cl := range claimsBySymbol[sym.ID] {
			if cl.Predicate == "exports" && strings.HasPrefix(cl.ObjectText, "reverse_dep:") {
				ip := strings.TrimPrefix(cl.ObjectText, "reverse_dep:")
				if !seen[ip] {
					seen[ip] = true
					deps = append(deps, ReverseDep{ImportPath: ip})
				}
			}
		}
	}

	sort.Slice(deps, func(i, j int) bool {
		return deps[i].ImportPath < deps[j].ImportPath
	})
	return deps, len(deps)
}

// extractFunctionCategories groups functions by their "has_kind" claim's
// object_text value, which encodes the category (e.g. "constructor", "utility").
// Functions without a has_kind claim go into an "Other" category.
func extractFunctionCategories(symbols []db.Symbol, claimsBySymbol map[int64][]db.Claim) []FunctionCategory {
	cats := make(map[string][]string)

	for _, sym := range symbols {
		if sym.Kind != "func" || sym.Visibility != "public" {
			continue
		}
		// Skip test, benchmark, and example functions — they inflate output
		// and are not useful for codebase onboarding.
		if isTestFunction(sym.SymbolName) {
			continue
		}
		category := ""
		for _, cl := range claimsBySymbol[sym.ID] {
			if cl.Predicate == "has_kind" && cl.ObjectText != "" {
				category = cl.ObjectText
				break
			}
		}
		if category == "" {
			category = "Other"
		}
		cats[category] = append(cats[category], sym.SymbolName)
	}

	var result []FunctionCategory
	for name, fns := range cats {
		sort.Strings(fns)
		result = append(result, FunctionCategory{Name: name, Functions: fns})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// extractTypeCategories groups non-interface types by their "has_kind" claim.
func extractTypeCategories(symbols []db.Symbol, claimsBySymbol map[int64][]db.Claim) []TypeCategory {
	cats := make(map[string][]string)

	for _, sym := range symbols {
		if sym.Kind != "type" || sym.Visibility != "public" {
			continue
		}
		// Skip interfaces — they have their own section.
		isInterface := false
		category := ""
		for _, cl := range claimsBySymbol[sym.ID] {
			if cl.Predicate == "has_kind" {
				if cl.ObjectText == "interface" {
					isInterface = true
					break
				}
				category = cl.ObjectText
			}
		}
		if isInterface {
			continue
		}
		if category == "" {
			category = "Other"
		}
		cats[category] = append(cats[category], sym.SymbolName)
	}

	var result []TypeCategory
	for name, types := range cats {
		sort.Strings(types)
		result = append(result, TypeCategory{Name: name, Types: types})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// extractFileMetadata counts source/test files and collects test file names
// from "is_test" and "defines" claims.
func extractFileMetadata(symbols []db.Symbol, claimsBySymbol map[int64][]db.Claim) (sourceCount, testCount int, testFiles []string) {
	sourceFiles := make(map[string]bool)
	testFileSet := make(map[string]bool)

	for _, sym := range symbols {
		for _, cl := range claimsBySymbol[sym.ID] {
			if cl.Predicate == "is_test" && cl.ObjectText == "true" {
				testFileSet[cl.SourceFile] = true
			}
			if cl.SourceFile != "" {
				sourceFiles[cl.SourceFile] = true
			}
		}
	}

	// Source files = all files minus test files.
	for f := range sourceFiles {
		if testFileSet[f] || strings.HasSuffix(f, "_test.go") {
			testCount++
		} else {
			sourceCount++
		}
	}

	for f := range testFileSet {
		testFiles = append(testFiles, f)
	}
	sort.Strings(testFiles)
	return sourceCount, testCount, testFiles
}

// latestSemanticTimestamp returns the most recent LastVerified value among
// all semantic-tier claims, or "" if none exist.
func latestSemanticTimestamp(claimsBySymbol map[int64][]db.Claim) string {
	var latest string
	for _, claims := range claimsBySymbol {
		for _, cl := range claims {
			if cl.ClaimTier == "semantic" && cl.LastVerified > latest {
				latest = cl.LastVerified
			}
		}
	}
	return latest
}

// isTestFunction returns true for Go test, benchmark, and example functions.
func isTestFunction(name string) bool {
	return strings.HasPrefix(name, "Test") ||
		strings.HasPrefix(name, "Benchmark") ||
		strings.HasPrefix(name, "Example")
}
