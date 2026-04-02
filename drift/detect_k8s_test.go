package drift

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectKubernetesREADMEs runs drift detection against real k8s READMEs
// and writes the results to validation/drift_detection_results.md.
// This is an integration test; skip if k8s repos not available.
func TestDetectKubernetesREADMEs(t *testing.T) {
	k8sBase := filepath.Join(os.Getenv("HOME"), "kubernetes", "kubernetes", "staging", "src", "k8s.io")
	klogBase := filepath.Join(os.Getenv("HOME"), "kubernetes", "kubernetes", "vendor", "k8s.io", "klog", "v2")

	if _, err := os.Stat(k8sBase); err != nil {
		t.Skipf("kubernetes staging area not found at %s", k8sBase)
	}

	targets := []Target{
		{
			ReadmePath: filepath.Join(k8sBase, "client-go", "README.md"),
			CodeDir:    filepath.Join(k8sBase, "client-go"),
		},
		{
			ReadmePath: filepath.Join(k8sBase, "apimachinery", "README.md"),
			CodeDir:    filepath.Join(k8sBase, "apimachinery"),
		},
		{
			ReadmePath: filepath.Join(k8sBase, "apimachinery", "pkg", "api", "validate", "README.md"),
			CodeDir:    filepath.Join(k8sBase, "apimachinery", "pkg", "api", "validate"),
		},
	}

	// Add klog if available.
	if _, err := os.Stat(filepath.Join(klogBase, "README.md")); err == nil {
		targets = append(targets, Target{
			ReadmePath: filepath.Join(klogBase, "README.md"),
			CodeDir:    klogBase,
		})
	}

	// Also scan a broader set of READMEs in staging.
	additionalREADMEs := []string{
		"apiserver/README.md",
		"cli-runtime/README.md",
		"code-generator/README.md",
		"component-base/README.md",
	}
	for _, rel := range additionalREADMEs {
		readmePath := filepath.Join(k8sBase, rel)
		if _, err := os.Stat(readmePath); err == nil {
			codeDir := filepath.Dir(readmePath)
			targets = append(targets, Target{
				ReadmePath: readmePath,
				CodeDir:    codeDir,
			})
		}
	}

	// Verify target files exist.
	var validTargets []Target
	for _, tgt := range targets {
		if _, err := os.Stat(tgt.ReadmePath); err != nil {
			t.Logf("SKIP: %s not found", tgt.ReadmePath)
			continue
		}
		validTargets = append(validTargets, tgt)
	}

	if len(validTargets) == 0 {
		t.Skip("no valid targets found")
	}

	reports, err := DetectMultiple(validTargets)
	if err != nil {
		t.Fatal(err)
	}

	// Build the results markdown.
	var b strings.Builder
	b.WriteString("# Drift Detection Results\n\n")
	b.WriteString("Comparison of existing Kubernetes README files against actual code exports.\n\n")
	b.WriteString(fmt.Sprintf("**Date**: 2026-03-31\n"))
	b.WriteString(fmt.Sprintf("**READMEs analyzed**: %d\n\n", len(reports)))

	totalStale := 0
	totalUndoc := 0
	totalStalePkg := 0
	for _, r := range reports {
		totalStale += r.StaleCount
		totalUndoc += r.UndocumentedCount
		totalStalePkg += r.StalePackageCount
	}

	b.WriteString("## Summary\n\n")
	b.WriteString(fmt.Sprintf("| Metric | Count |\n"))
	b.WriteString(fmt.Sprintf("|--------|-------|\n"))
	b.WriteString(fmt.Sprintf("| READMEs analyzed | %d |\n", len(reports)))
	b.WriteString(fmt.Sprintf("| Stale symbol references | %d |\n", totalStale))
	b.WriteString(fmt.Sprintf("| Undocumented exports | %d |\n", totalUndoc))
	b.WriteString(fmt.Sprintf("| Stale package references | %d |\n\n", totalStalePkg))

	b.WriteString("## Per-README Reports\n\n")
	for _, r := range reports {
		b.WriteString(FormatReport(r))
	}

	b.WriteString("## Methodology\n\n")
	b.WriteString("Drift detection compares:\n")
	b.WriteString("1. **README symbols**: Backtick-quoted identifiers and PascalCase words extracted from Markdown\n")
	b.WriteString("2. **Code exports**: Exported Go symbols (functions, types, constants, variables) parsed via go/ast\n")
	b.WriteString("3. **Package paths**: Directory references in the README checked against actual subdirectories\n\n")
	b.WriteString("Common English words matching PascalCase (e.g., 'The', 'This', 'Example') are filtered out.\n")
	b.WriteString("Test files (*_test.go) are excluded from code export analysis.\n")

	// Write results file.
	outDir := filepath.Join(os.Getenv("HOME"), "live_docs", "validation")
	os.MkdirAll(outDir, 0755)
	outPath := filepath.Join(outDir, "drift_detection_results.md")
	err = os.WriteFile(outPath, []byte(b.String()), 0644)
	if err != nil {
		t.Fatalf("write results: %v", err)
	}

	t.Logf("Results written to %s", outPath)
	t.Logf("Total stale: %d, undocumented: %d, stale packages: %d", totalStale, totalUndoc, totalStalePkg)

	// Print summary for each README.
	for _, r := range reports {
		t.Logf("  %s: stale=%d undoc=%d stale_pkg=%d readme_syms=%d code_exports=%d",
			r.ReadmePath, r.StaleCount, r.UndocumentedCount, r.StalePackageCount,
			len(r.ReadmeSymbols), len(r.CodeSymbols))
	}
}
