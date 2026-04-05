package renderer

import (
	"fmt"
	"strings"
	"time"
)

// RenderMarkdown produces Tier 1 Markdown documentation from a PackageData.
// The output focuses on cross-package structural claims that go doc cannot
// provide: interface satisfaction, dependency graphs, reverse dependencies,
// and function categorization.
func RenderMarkdown(pd *PackageData) string {
	var b strings.Builder

	renderHeader(&b, pd)
	renderInterfaces(&b, pd)
	renderImplements(&b, pd)
	renderUsedBy(&b, pd)
	renderCrossPackageReferences(&b, pd)
	renderFunctionCategories(&b, pd)
	renderTypeCategories(&b, pd)
	renderTestCoverage(&b, pd)
	renderSemanticEnrichment(&b, pd)

	return b.String()
}

func renderHeader(b *strings.Builder, pd *PackageData) {
	fmt.Fprintf(b, "# %s\n\n", pd.ImportPath)
	fmt.Fprintf(b, "> Tier 1 — Claims-backed structural documentation\n\n")
	fmt.Fprintf(b, "**Import path:** `%s`\n", pd.ImportPath)
	if pd.SourceFileCount > 0 || pd.TestFileCount > 0 {
		fmt.Fprintf(b, "**Source files:** %d (non-test)", pd.SourceFileCount)
		if pd.TestFileCount > 0 {
			fmt.Fprintf(b, ", %d (test)", pd.TestFileCount)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "**Language:** %s\n", pd.Language)
	b.WriteString("\n")
}

func renderInterfaces(b *strings.Builder, pd *PackageData) {
	if len(pd.Interfaces) == 0 {
		return
	}

	fmt.Fprintf(b, "## Exported Interfaces (%d)\n\n", len(pd.Interfaces))
	b.WriteString("| Interface | Methods | Key Implementations |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, iface := range pd.Interfaces {
		methods := strings.Join(iface.Methods, ", ")
		impls := strings.Join(iface.Implementations, ", ")
		if impls == "" {
			impls = "—"
		}
		fmt.Fprintf(b, "| `%s` | %s | %s |\n", iface.Name, methods, impls)
	}
	b.WriteString("\n")
}

func renderImplements(b *strings.Builder, pd *PackageData) {
	if len(pd.InterfaceSatisfactions) == 0 {
		return
	}

	b.WriteString("## Implements\n\n")
	for _, sat := range pd.InterfaceSatisfactions {
		fmt.Fprintf(b, "- `%s` implements %s\n", sat.ConcreteType, formatInterfaceList(sat.Interfaces))
	}
	b.WriteString("\n")
}

func formatInterfaceList(ifaces []string) string {
	quoted := make([]string, len(ifaces))
	for i, iface := range ifaces {
		quoted[i] = "`" + iface + "`"
	}
	if len(quoted) <= 2 {
		return strings.Join(quoted, ", ")
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + ", " + quoted[len(quoted)-1]
}

func renderUsedBy(b *strings.Builder, pd *PackageData) {
	if len(pd.ReverseDeps) == 0 && pd.ReverseDepCount == 0 {
		return
	}

	b.WriteString("## Used By\n\n")

	count := pd.ReverseDepCount
	if count == 0 {
		count = len(pd.ReverseDeps)
	}
	fmt.Fprintf(b, "%d packages depend on this package:\n\n", count)
	for _, dep := range pd.ReverseDeps {
		if dep.File != "" {
			fmt.Fprintf(b, "- `%s` (%s)\n", dep.ImportPath, dep.File)
		} else {
			fmt.Fprintf(b, "- `%s`\n", dep.ImportPath)
		}
	}
	b.WriteString("\n")
}

func renderCrossPackageReferences(b *strings.Builder, pd *PackageData) {
	if len(pd.ForwardDeps) == 0 {
		return
	}

	b.WriteString("## Cross-Package References\n\n")

	b.WriteString("**Direct dependencies:**\n\n")
	for _, dep := range pd.ForwardDeps {
		if dep.Annotation != "" {
			fmt.Fprintf(b, "- `%s` — %s\n", dep.ImportPath, dep.Annotation)
		} else {
			fmt.Fprintf(b, "- `%s`\n", dep.ImportPath)
		}
	}
	b.WriteString("\n")
}

func renderFunctionCategories(b *strings.Builder, pd *PackageData) {
	if len(pd.FunctionCategories) == 0 {
		return
	}

	b.WriteString("## Exported Functions by Category\n\n")
	for _, cat := range pd.FunctionCategories {
		fmt.Fprintf(b, "**%s (%d):** %s\n\n", cat.Name, len(cat.Functions), strings.Join(cat.Functions, ", "))
	}
}

func renderTypeCategories(b *strings.Builder, pd *PackageData) {
	if len(pd.TypesByCategory) == 0 {
		return
	}

	b.WriteString("## Exported Types by Category\n\n")
	for _, cat := range pd.TypesByCategory {
		fmt.Fprintf(b, "**%s:** %s\n\n", cat.Name, strings.Join(cat.Types, ", "))
	}
}

func renderTestCoverage(b *strings.Builder, pd *PackageData) {
	if len(pd.TestFiles) == 0 {
		return
	}

	fmt.Fprintf(b, "## Test Coverage\n\n")
	// Extract base names for readability.
	names := make([]string, len(pd.TestFiles))
	for i, f := range pd.TestFiles {
		// Use the file as-is since it may already be a base name.
		names[i] = f
	}
	fmt.Fprintf(b, "%d test files: %s\n", len(names), strings.Join(names, ", "))
}

func renderSemanticEnrichment(b *strings.Builder, pd *PackageData) {
	if pd.SemanticEnrichmentDate == "" {
		return
	}

	b.WriteString("\n---\n\n")
	b.WriteString("## Semantic Enrichment\n\n")

	// Format the timestamp for display. Fall back to raw string if parsing fails.
	display := pd.SemanticEnrichmentDate
	if t, err := time.Parse(time.RFC3339, pd.SemanticEnrichmentDate); err == nil {
		display = t.Format("2006-01-02 15:04 UTC")
	}
	fmt.Fprintf(b, "> *Last enriched: %s*\n", display)
	fmt.Fprintf(b, ">\n")
	fmt.Fprintf(b, "> Semantic claims below are AI-generated snapshots and may be stale.\n")
}
