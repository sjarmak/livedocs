// Package renderer transforms claims from the database into Tier 1 Markdown
// documentation. It generates cross-package structural content that go doc
// cannot provide: interface satisfaction maps, dependency graphs, reverse
// dependency indexes, and function categorization by purpose.
package renderer

// PackageData holds the aggregated claims for a single Go package, organized
// into the sections that appear in the rendered Markdown output.
type PackageData struct {
	ImportPath string
	Repo       string
	Language   string

	// SourceFileCount is the number of non-test source files.
	SourceFileCount int
	// TestFileCount is the number of test source files.
	TestFileCount int

	// Interfaces lists exported interfaces with their methods and known implementations.
	Interfaces []InterfaceInfo

	// InterfaceSatisfactions maps concrete types to the interfaces they implement.
	InterfaceSatisfactions []Satisfaction

	// ForwardDeps lists k8s.io import paths this package depends on, with annotation.
	ForwardDeps []Dependency

	// ReverseDeps lists packages that import this package.
	ReverseDeps []ReverseDep

	// ReverseDepCount is the total count of reverse dependencies (may exceed len(ReverseDeps)).
	ReverseDepCount int

	// FunctionCategories groups exported functions by purpose.
	FunctionCategories []FunctionCategory

	// TypesByCategory groups exported types by purpose/kind.
	TypesByCategory []TypeCategory

	// TestFiles lists test file names.
	TestFiles []string

	// SemanticEnrichmentDate holds the most recent LastVerified timestamp
	// from semantic-tier claims for this package. Empty if no semantic claims exist.
	SemanticEnrichmentDate string
}

// InterfaceInfo describes an exported interface.
type InterfaceInfo struct {
	Name            string
	Methods         []string
	Implementations []string
}

// Satisfaction records that a concrete type implements an interface.
type Satisfaction struct {
	ConcreteType string
	Interfaces   []string
}

// Dependency is a forward dependency with an optional annotation.
type Dependency struct {
	ImportPath string
	Annotation string // e.g. "Object type for list/watch"
}

// ReverseDep is a package that imports this package.
type ReverseDep struct {
	ImportPath string
	File       string // optional: specific file within the importer
}

// FunctionCategory groups functions by purpose.
type FunctionCategory struct {
	Name      string
	Functions []string
}

// TypeCategory groups types by purpose.
type TypeCategory struct {
	Name  string
	Types []string
}
