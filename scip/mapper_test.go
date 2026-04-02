package scip

import (
	"testing"

	scipb "github.com/scip-code/scip/bindings/go/scip"
)

func TestDecomposeSymbol_GoType(t *testing.T) {
	// Example: "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#"
	// scheme="scip-go", manager="gomod", package="k8s.io/api", version="v0.28.0"
	// descriptors: core/ (namespace), v1/ (namespace), Pod# (type)
	result, err := DecomposeSymbol("scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#")
	if err != nil {
		t.Fatalf("decompose: %v", err)
	}
	if result.ImportPath != "k8s.io/api/core/v1" {
		t.Errorf("import_path: got %q, want %q", result.ImportPath, "k8s.io/api/core/v1")
	}
	if result.SymbolName != "Pod" {
		t.Errorf("symbol_name: got %q, want %q", result.SymbolName, "Pod")
	}
	if result.Kind != "type" {
		t.Errorf("kind: got %q, want %q", result.Kind, "type")
	}
}

func TestDecomposeSymbol_GoFunc(t *testing.T) {
	result, err := DecomposeSymbol("scip-go gomod k8s.io/client-go v0.28.0 tools/cache/NewInformer().")
	if err != nil {
		t.Fatalf("decompose: %v", err)
	}
	if result.ImportPath != "k8s.io/client-go/tools/cache" {
		t.Errorf("import_path: got %q, want %q", result.ImportPath, "k8s.io/client-go/tools/cache")
	}
	if result.SymbolName != "NewInformer" {
		t.Errorf("symbol_name: got %q, want %q", result.SymbolName, "NewInformer")
	}
	if result.Kind != "func" {
		t.Errorf("kind: got %q, want %q", result.Kind, "func")
	}
}

func TestDecomposeSymbol_GoVar(t *testing.T) {
	result, err := DecomposeSymbol("scip-go gomod k8s.io/api v0.28.0 core/v1/SchemeGroupVersion.")
	if err != nil {
		t.Fatalf("decompose: %v", err)
	}
	if result.ImportPath != "k8s.io/api/core/v1" {
		t.Errorf("import_path: got %q, want %q", result.ImportPath, "k8s.io/api/core/v1")
	}
	if result.SymbolName != "SchemeGroupVersion" {
		t.Errorf("symbol_name: got %q, want %q", result.SymbolName, "SchemeGroupVersion")
	}
	if result.Kind != "var" {
		t.Errorf("kind: got %q, want %q", result.Kind, "var")
	}
}

func TestDecomposeSymbol_Empty(t *testing.T) {
	_, err := DecomposeSymbol("")
	if err == nil {
		t.Error("expected error for empty symbol")
	}
}

func TestDecomposeSymbol_Local(t *testing.T) {
	_, err := DecomposeSymbol("local 42")
	if err == nil {
		t.Error("expected error for local symbol")
	}
}

func TestDecomposeSymbol_Invalid(t *testing.T) {
	_, err := DecomposeSymbol("not a valid symbol")
	if err == nil {
		t.Error("expected error for invalid symbol")
	}
}

func TestMapSCIPKind(t *testing.T) {
	tests := []struct {
		kind scipb.SymbolInformation_Kind
		want string
	}{
		{scipb.SymbolInformation_Class, "type"},
		{scipb.SymbolInformation_Interface, "type"},
		{scipb.SymbolInformation_Struct, "type"},
		{scipb.SymbolInformation_Enum, "type"},
		{scipb.SymbolInformation_Function, "func"},
		{scipb.SymbolInformation_Method, "func"},
		{scipb.SymbolInformation_Constructor, "func"},
		{scipb.SymbolInformation_Constant, "const"},
		{scipb.SymbolInformation_EnumMember, "const"},
		{scipb.SymbolInformation_Variable, "var"},
		{scipb.SymbolInformation_Field, "var"},
		{scipb.SymbolInformation_Module, "module"},
		{scipb.SymbolInformation_Namespace, "module"},
		{scipb.SymbolInformation_Package, "module"},
		{scipb.SymbolInformation_UnspecifiedKind, "unknown"},
	}
	for _, tt := range tests {
		got := MapSCIPKind(tt.kind)
		if got != tt.want {
			t.Errorf("MapSCIPKind(%v) = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestMapVisibility(t *testing.T) {
	tests := []struct {
		name, lang, want string
	}{
		{"Pod", "go", "public"},
		{"pod", "go", "internal"},
		{"_private", "go", "internal"},
		{"MyClass", "typescript", "public"},
		{"myFunc", "python", "public"},
	}
	for _, tt := range tests {
		got := MapVisibility(tt.name, tt.lang)
		if got != tt.want {
			t.Errorf("MapVisibility(%q, %q) = %q, want %q", tt.name, tt.lang, got, tt.want)
		}
	}
}

func TestSymbolKey(t *testing.T) {
	got := SymbolKey("k8s.io/api/core/v1", "Pod")
	want := "k8s.io/api/core/v1.Pod"
	if got != want {
		t.Errorf("SymbolKey() = %q, want %q", got, want)
	}
}
