package scip

import (
	"testing"
)

func TestFormatSymbol(t *testing.T) {
	tests := []struct {
		name   string
		symbol Symbol
		want   string
	}{
		{
			name: "go package-level function",
			symbol: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/api",
				Version: "v0.28.0",
				Descriptors: []Descriptor{
					{Name: "core", Suffix: SuffixNamespace},
					{Name: "v1", Suffix: SuffixNamespace},
					{Name: "NewPod", Suffix: SuffixMethod},
				},
			},
			want: "scip-go gomod k8s.io/api v0.28.0 core/v1/NewPod().",
		},
		{
			name: "go type",
			symbol: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/api",
				Version: "v0.28.0",
				Descriptors: []Descriptor{
					{Name: "core", Suffix: SuffixNamespace},
					{Name: "v1", Suffix: SuffixNamespace},
					{Name: "Pod", Suffix: SuffixType},
				},
			},
			want: "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#",
		},
		{
			name: "go method on type",
			symbol: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/client-go",
				Version: "v0.28.0",
				Descriptors: []Descriptor{
					{Name: "informers", Suffix: SuffixNamespace},
					{Name: "SharedInformerFactory", Suffix: SuffixType},
					{Name: "Start", Suffix: SuffixMethod},
				},
			},
			want: "scip-go gomod k8s.io/client-go v0.28.0 informers/SharedInformerFactory#Start().",
		},
		{
			name: "go package-level variable",
			symbol: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/api",
				Version: "v0.28.0",
				Descriptors: []Descriptor{
					{Name: "core", Suffix: SuffixNamespace},
					{Name: "v1", Suffix: SuffixNamespace},
					{Name: "SchemeGroupVersion", Suffix: SuffixTerm},
				},
			},
			want: "scip-go gomod k8s.io/api v0.28.0 core/v1/SchemeGroupVersion.",
		},
		{
			name: "go constant",
			symbol: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/api",
				Version: "v0.28.0",
				Descriptors: []Descriptor{
					{Name: "core", Suffix: SuffixNamespace},
					{Name: "v1", Suffix: SuffixNamespace},
					{Name: "PodRunning", Suffix: SuffixTerm},
				},
			},
			want: "scip-go gomod k8s.io/api v0.28.0 core/v1/PodRunning.",
		},
		{
			name: "empty version",
			symbol: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/api",
				Version: "",
				Descriptors: []Descriptor{
					{Name: "core", Suffix: SuffixNamespace},
					{Name: "Pod", Suffix: SuffixType},
				},
			},
			want: "scip-go gomod k8s.io/api  core/Pod#",
		},
		{
			name: "single descriptor",
			symbol: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "fmt",
				Version: "go1.26.1",
				Descriptors: []Descriptor{
					{Name: "Println", Suffix: SuffixMethod},
				},
			},
			want: "scip-go gomod fmt go1.26.1 Println().",
		},
		{
			name: "descriptor with backtick-escaped name",
			symbol: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "example.com/pkg",
				Version: "v1.0.0",
				Descriptors: []Descriptor{
					{Name: "my package", Suffix: SuffixNamespace},
					{Name: "MyType", Suffix: SuffixType},
				},
			},
			want: "scip-go gomod example.com/pkg v1.0.0 `my package`/MyType#",
		},
		{
			name: "descriptor name with backtick needs escaping",
			symbol: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "example.com/pkg",
				Version: "v1.0.0",
				Descriptors: []Descriptor{
					{Name: "has`tick", Suffix: SuffixTerm},
				},
			},
			want: "scip-go gomod example.com/pkg v1.0.0 `has``tick`.",
		},
		{
			name: "typescript symbol",
			symbol: Symbol{
				Scheme:  "scip-typescript",
				Manager: "npm",
				Package: "@kubernetes/client-node",
				Version: "0.20.0",
				Descriptors: []Descriptor{
					{Name: "KubeConfig", Suffix: SuffixType},
					{Name: "loadFromDefault", Suffix: SuffixMethod},
				},
			},
			want: "scip-typescript npm @kubernetes/client-node 0.20.0 KubeConfig#loadFromDefault().",
		},
		{
			name: "no descriptors",
			symbol: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/api",
				Version: "v0.28.0",
			},
			want: "scip-go gomod k8s.io/api v0.28.0 ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.symbol.Format()
			if got != tt.want {
				t.Errorf("Format() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseSymbol(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Symbol
		wantErr bool
	}{
		{
			name:  "go function",
			input: "scip-go gomod k8s.io/api v0.28.0 core/v1/NewPod().",
			want: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/api",
				Version: "v0.28.0",
				Descriptors: []Descriptor{
					{Name: "core", Suffix: SuffixNamespace},
					{Name: "v1", Suffix: SuffixNamespace},
					{Name: "NewPod", Suffix: SuffixMethod},
				},
			},
		},
		{
			name:  "go type",
			input: "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#",
			want: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/api",
				Version: "v0.28.0",
				Descriptors: []Descriptor{
					{Name: "core", Suffix: SuffixNamespace},
					{Name: "v1", Suffix: SuffixNamespace},
					{Name: "Pod", Suffix: SuffixType},
				},
			},
		},
		{
			name:  "go term",
			input: "scip-go gomod k8s.io/api v0.28.0 core/v1/SchemeGroupVersion.",
			want: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/api",
				Version: "v0.28.0",
				Descriptors: []Descriptor{
					{Name: "core", Suffix: SuffixNamespace},
					{Name: "v1", Suffix: SuffixNamespace},
					{Name: "SchemeGroupVersion", Suffix: SuffixTerm},
				},
			},
		},
		{
			name:  "method on type",
			input: "scip-go gomod k8s.io/client-go v0.28.0 informers/SharedInformerFactory#Start().",
			want: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/client-go",
				Version: "v0.28.0",
				Descriptors: []Descriptor{
					{Name: "informers", Suffix: SuffixNamespace},
					{Name: "SharedInformerFactory", Suffix: SuffixType},
					{Name: "Start", Suffix: SuffixMethod},
				},
			},
		},
		{
			name:  "empty version",
			input: "scip-go gomod k8s.io/api  core/Pod#",
			want: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/api",
				Version: "",
				Descriptors: []Descriptor{
					{Name: "core", Suffix: SuffixNamespace},
					{Name: "Pod", Suffix: SuffixType},
				},
			},
		},
		{
			name:  "backtick-escaped name",
			input: "scip-go gomod example.com/pkg v1.0.0 `my package`/MyType#",
			want: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "example.com/pkg",
				Version: "v1.0.0",
				Descriptors: []Descriptor{
					{Name: "my package", Suffix: SuffixNamespace},
					{Name: "MyType", Suffix: SuffixType},
				},
			},
		},
		{
			name:  "escaped backtick in name",
			input: "scip-go gomod example.com/pkg v1.0.0 `has``tick`.",
			want: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "example.com/pkg",
				Version: "v1.0.0",
				Descriptors: []Descriptor{
					{Name: "has`tick", Suffix: SuffixTerm},
				},
			},
		},
		{
			name:    "too few components",
			input:   "scip-go gomod",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:  "no descriptors",
			input: "scip-go gomod k8s.io/api v0.28.0 ",
			want: Symbol{
				Scheme:  "scip-go",
				Manager: "gomod",
				Package: "k8s.io/api",
				Version: "v0.28.0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSymbol(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseSymbol() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Scheme != tt.want.Scheme {
				t.Errorf("Scheme = %q, want %q", got.Scheme, tt.want.Scheme)
			}
			if got.Manager != tt.want.Manager {
				t.Errorf("Manager = %q, want %q", got.Manager, tt.want.Manager)
			}
			if got.Package != tt.want.Package {
				t.Errorf("Package = %q, want %q", got.Package, tt.want.Package)
			}
			if got.Version != tt.want.Version {
				t.Errorf("Version = %q, want %q", got.Version, tt.want.Version)
			}
			if len(got.Descriptors) != len(tt.want.Descriptors) {
				t.Fatalf("len(Descriptors) = %d, want %d", len(got.Descriptors), len(tt.want.Descriptors))
			}
			for i, d := range got.Descriptors {
				if d != tt.want.Descriptors[i] {
					t.Errorf("Descriptors[%d] = %+v, want %+v", i, d, tt.want.Descriptors[i])
				}
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	symbols := []Symbol{
		{
			Scheme:  "scip-go",
			Manager: "gomod",
			Package: "k8s.io/api",
			Version: "v0.28.0",
			Descriptors: []Descriptor{
				{Name: "core", Suffix: SuffixNamespace},
				{Name: "v1", Suffix: SuffixNamespace},
				{Name: "Pod", Suffix: SuffixType},
				{Name: "Spec", Suffix: SuffixTerm},
			},
		},
		{
			Scheme:  "scip-go",
			Manager: "gomod",
			Package: "k8s.io/client-go",
			Version: "v0.28.0",
			Descriptors: []Descriptor{
				{Name: "informers", Suffix: SuffixNamespace},
				{Name: "SharedInformerFactory", Suffix: SuffixType},
				{Name: "Start", Suffix: SuffixMethod},
			},
		},
		{
			Scheme:  "scip-go",
			Manager: "gomod",
			Package: "example.com/pkg",
			Version: "v1.0.0",
			Descriptors: []Descriptor{
				{Name: "my package", Suffix: SuffixNamespace},
				{Name: "has`tick", Suffix: SuffixTerm},
			},
		},
	}

	for _, sym := range symbols {
		t.Run(sym.Format(), func(t *testing.T) {
			formatted := sym.Format()
			parsed, err := ParseSymbol(formatted)
			if err != nil {
				t.Fatalf("ParseSymbol(%q) error: %v", formatted, err)
			}
			reFormatted := parsed.Format()
			if reFormatted != formatted {
				t.Errorf("round-trip failed: %q -> %q", formatted, reFormatted)
			}
		})
	}
}

func TestFormatGoSymbol(t *testing.T) {
	tests := []struct {
		name       string
		modulePath string
		version    string
		pkgPath    string
		symbolName string
		kind       SymbolKind
		ownerType  string
		want       string
	}{
		{
			name:       "package-level type",
			modulePath: "k8s.io/api",
			version:    "v0.28.0",
			pkgPath:    "k8s.io/api/core/v1",
			symbolName: "Pod",
			kind:       KindType,
			want:       "scip-go gomod k8s.io/api v0.28.0 core/v1/Pod#",
		},
		{
			name:       "package-level function",
			modulePath: "k8s.io/client-go",
			version:    "v0.28.0",
			pkgPath:    "k8s.io/client-go/informers",
			symbolName: "NewSharedInformerFactory",
			kind:       KindFunc,
			want:       "scip-go gomod k8s.io/client-go v0.28.0 informers/NewSharedInformerFactory().",
		},
		{
			name:       "method on type",
			modulePath: "k8s.io/client-go",
			version:    "v0.28.0",
			pkgPath:    "k8s.io/client-go/informers",
			symbolName: "Start",
			kind:       KindMethod,
			ownerType:  "SharedInformerFactory",
			want:       "scip-go gomod k8s.io/client-go v0.28.0 informers/SharedInformerFactory#Start().",
		},
		{
			name:       "package-level var",
			modulePath: "k8s.io/api",
			version:    "v0.28.0",
			pkgPath:    "k8s.io/api/core/v1",
			symbolName: "SchemeGroupVersion",
			kind:       KindVar,
			want:       "scip-go gomod k8s.io/api v0.28.0 core/v1/SchemeGroupVersion.",
		},
		{
			name:       "stdlib package",
			modulePath: "std",
			version:    "go1.26.1",
			pkgPath:    "fmt",
			symbolName: "Println",
			kind:       KindFunc,
			want:       "scip-go gomod std go1.26.1 fmt/Println().",
		},
		{
			name:       "package same as module",
			modulePath: "github.com/pkg/errors",
			version:    "v0.9.1",
			pkgPath:    "github.com/pkg/errors",
			symbolName: "New",
			kind:       KindFunc,
			want:       "scip-go gomod github.com/pkg/errors v0.9.1 New().",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatGoSymbol(tt.modulePath, tt.version, tt.pkgPath, tt.symbolName, tt.kind, tt.ownerType)
			if got != tt.want {
				t.Errorf("FormatGoSymbol() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNeedsEscaping(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"simple", false},
		{"CamelCase", false},
		{"has space", true},
		{"has/slash", true},
		{"has.dot", true},
		{"has#hash", true},
		{"has(paren", true},
		{"has)paren", true},
		{"has`tick", true},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := needsEscaping(tt.name)
			if got != tt.want {
				t.Errorf("needsEscaping(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
