// Package scip provides a SCIP protobuf index importer that populates the claims DB.
// It parses index.scip files using the sourcegraph/scip Go bindings and maps
// SCIP symbols to composite keys (repo + import_path + symbol_name).
package scip

import (
	"fmt"
	"strings"

	scipb "github.com/scip-code/scip/bindings/go/scip"
)

// SymbolDecomposition holds the parts of a SCIP symbol string decomposed
// into the composite key fields used by the claims DB.
type SymbolDecomposition struct {
	ImportPath string // e.g., "k8s.io/api/core/v1"
	SymbolName string // e.g., "Pod"
	Kind       string // e.g., "type", "func", "const", "var"
}

// DecomposeSymbol parses a SCIP symbol string and extracts the import_path
// and symbol_name components for the composite primary key.
//
// SCIP symbol format: "<scheme> <manager> <package-name> <version> <descriptors...>"
// Example: "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#"
func DecomposeSymbol(symbolStr string) (SymbolDecomposition, error) {
	if symbolStr == "" {
		return SymbolDecomposition{}, fmt.Errorf("empty symbol string")
	}
	if strings.HasPrefix(symbolStr, "local ") {
		return SymbolDecomposition{}, fmt.Errorf("local symbols cannot be decomposed: %s", symbolStr)
	}

	sym, err := scipb.ParseSymbol(symbolStr)
	if err != nil {
		return SymbolDecomposition{}, fmt.Errorf("parse SCIP symbol %q: %w", symbolStr, err)
	}

	pkg := sym.GetPackage()
	if pkg == nil {
		return SymbolDecomposition{}, fmt.Errorf("no package in symbol: %s", symbolStr)
	}

	descriptors := sym.GetDescriptors()
	if len(descriptors) == 0 {
		return SymbolDecomposition{}, fmt.Errorf("no descriptors in symbol: %s", symbolStr)
	}

	// Build import_path from package name + namespace descriptors.
	// Example: package="k8s.io/api", namespaces=["core/", "v1/"] -> "k8s.io/api/core/v1"
	importPath := pkg.GetName()
	var symbolName string
	var kind string

	for _, d := range descriptors {
		switch d.GetSuffix() {
		case scipb.Descriptor_Namespace:
			// Namespace/Package descriptors extend the import path.
			// Note: Descriptor_Package has the same value as Descriptor_Namespace.
			importPath = importPath + "/" + d.GetName()
		case scipb.Descriptor_Type:
			symbolName = d.GetName()
			kind = "type"
		case scipb.Descriptor_Method:
			symbolName = d.GetName()
			kind = "func"
		case scipb.Descriptor_Term:
			symbolName = d.GetName()
			kind = "var"
		case scipb.Descriptor_Macro:
			symbolName = d.GetName()
			kind = "const"
		case scipb.Descriptor_Parameter:
			// Parameters are not top-level symbols; skip.
			continue
		case scipb.Descriptor_TypeParameter:
			// Type parameters are not top-level symbols; skip.
			continue
		default:
			symbolName = d.GetName()
			kind = "unknown"
		}
	}

	if symbolName == "" {
		return SymbolDecomposition{}, fmt.Errorf("no symbol name found in descriptors: %s", symbolStr)
	}

	// Clean up trailing slashes from import path construction.
	importPath = strings.TrimRight(importPath, "/")

	return SymbolDecomposition{
		ImportPath: importPath,
		SymbolName: symbolName,
		Kind:       kind,
	}, nil
}

// MapSCIPKind maps a SCIP SymbolInformation_Kind to a claims DB kind string.
func MapSCIPKind(k scipb.SymbolInformation_Kind) string {
	switch k {
	case scipb.SymbolInformation_Class,
		scipb.SymbolInformation_Interface,
		scipb.SymbolInformation_Enum,
		scipb.SymbolInformation_Struct:
		return "type"
	case scipb.SymbolInformation_Function,
		scipb.SymbolInformation_Method,
		scipb.SymbolInformation_Constructor,
		scipb.SymbolInformation_AbstractMethod:
		return "func"
	case scipb.SymbolInformation_Constant,
		scipb.SymbolInformation_EnumMember:
		return "const"
	case scipb.SymbolInformation_Variable,
		scipb.SymbolInformation_Field,
		scipb.SymbolInformation_Property:
		return "var"
	case scipb.SymbolInformation_Module,
		scipb.SymbolInformation_Namespace,
		scipb.SymbolInformation_Package:
		return "module"
	case scipb.SymbolInformation_TypeParameter:
		return "type"
	default:
		return "unknown"
	}
}

// MapVisibility determines visibility from a SCIP symbol string.
// For Go, exported names start with uppercase. For other languages,
// we default to "public" since SCIP does not directly encode visibility.
func MapVisibility(symbolName, language string) string {
	if language == "go" && len(symbolName) > 0 {
		first := rune(symbolName[0])
		if first >= 'a' && first <= 'z' || first == '_' {
			return "internal"
		}
		return "public"
	}
	// Default: public (SCIP does not encode visibility directly).
	return "public"
}

// SymbolKey builds the cross-repo xref key: import_path + "." + symbol_name.
func SymbolKey(importPath, symbolName string) string {
	return importPath + "." + symbolName
}
