// Package validation benchmarks tree-sitter Go bindings:
//
//  1. smacker/go-tree-sitter   — CGO-based (baseline)
//  2. odvcencio/gotreesitter   — pure Go runtime (no-CGO alternative, 206 grammars)
//
// malivvan/tree-sitter (wazero WASM) was evaluated but only ships C/C++ grammars
// at v0.0.1. It lacks Go support and is not benchmarked.
//
// Measures: parse time, memory allocations, correctness (node count, function extraction).
// Test corpus: real Kubernetes Go files (52-8529 lines).
package validation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	// CGO baseline
	sitter "github.com/smacker/go-tree-sitter"
	sittergo "github.com/smacker/go-tree-sitter/golang"

	// Pure Go
	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// testFiles returns the list of sample Go files for benchmarking.
func testFiles(t testing.TB) []string {
	t.Helper()
	dir := filepath.Join("testdata")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading tier1_samples: %v", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	if len(files) == 0 {
		t.Fatal("no .go files found in tier1_samples")
	}
	return files
}

func readFile(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

// --- CGO baseline (smacker/go-tree-sitter) ---

func parseCGO(src []byte) (*sitter.Tree, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(sittergo.GetLanguage())
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, fmt.Errorf("cgo parse: %w", err)
	}
	return tree, nil
}

func countNodesCGO(node *sitter.Node) int {
	count := 1
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil {
			count += countNodesCGO(child)
		}
	}
	return count
}

func extractFunctionsCGO(node *sitter.Node, src []byte) []string {
	var fns []string
	if node.Type() == "function_declaration" {
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child != nil && child.Type() == "identifier" {
				fns = append(fns, child.Content(src))
				break
			}
		}
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil {
			fns = append(fns, extractFunctionsCGO(child, src)...)
		}
	}
	return fns
}

// --- Pure Go (odvcencio/gotreesitter) ---

var goLang = grammars.GoLanguage()

func parsePureGo(src []byte) (*gts.Tree, error) {
	parser := gts.NewParser(goLang)
	tree, err := parser.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("pure-go parse: %w", err)
	}
	return tree, nil
}

func countNodesPureGo(node *gts.Node) int {
	count := 1
	for i := 0; i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child != nil {
			count += countNodesPureGo(child)
		}
	}
	return count
}

func extractFunctionsPureGo(node *gts.Node, src []byte, lang *gts.Language) []string {
	var fns []string
	if node.Type(lang) == "function_declaration" {
		for i := 0; i < node.ChildCount(); i++ {
			child := node.Child(i)
			if child != nil && child.Type(lang) == "identifier" {
				fns = append(fns, child.Text(src))
				break
			}
		}
	}
	for i := 0; i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child != nil {
			fns = append(fns, extractFunctionsPureGo(child, src, lang)...)
		}
	}
	return fns
}

// --- Benchmarks ---

func BenchmarkCGO(b *testing.B) {
	files := testFiles(b)
	for _, f := range files {
		src := readFile(b, f)
		name := filepath.Base(f)
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			for b.Loop() {
				tree, err := parseCGO(src)
				if err != nil {
					b.Fatal(err)
				}
				_ = tree
			}
		})
	}
}

func BenchmarkPureGo(b *testing.B) {
	files := testFiles(b)
	for _, f := range files {
		src := readFile(b, f)
		name := filepath.Base(f)
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			for b.Loop() {
				tree, err := parsePureGo(src)
				if err != nil {
					b.Fatal(err)
				}
				_ = tree
			}
		})
	}
}

// --- Correctness Tests ---

func TestCorrectnessComparison(t *testing.T) {
	files := testFiles(t)
	for _, f := range files {
		src := readFile(t, f)
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			// Parse with both
			cgoTree, err := parseCGO(src)
			if err != nil {
				t.Fatalf("CGO parse failed: %v", err)
			}
			pureTree, err := parsePureGo(src)
			if err != nil {
				t.Fatalf("PureGo parse failed: %v", err)
			}

			// Count nodes
			cgoNodes := countNodesCGO(cgoTree.RootNode())
			pureNodes := countNodesPureGo(pureTree.RootNode())

			t.Logf("  Node counts — CGO: %d, PureGo: %d", cgoNodes, pureNodes)

			// Extract functions
			cgoFns := extractFunctionsCGO(cgoTree.RootNode(), src)
			pureFns := extractFunctionsPureGo(pureTree.RootNode(), src, goLang)

			t.Logf("  Functions — CGO: %d, PureGo: %d", len(cgoFns), len(pureFns))

			// Compare function lists (CGO is the reference)
			if len(cgoFns) != len(pureFns) {
				t.Errorf("PureGo function count mismatch: CGO=%d PureGo=%d", len(cgoFns), len(pureFns))
				// Show first few differences
				cgoSet := make(map[string]bool)
				for _, fn := range cgoFns {
					cgoSet[fn] = true
				}
				pureSet := make(map[string]bool)
				for _, fn := range pureFns {
					pureSet[fn] = true
				}
				for fn := range cgoSet {
					if !pureSet[fn] {
						t.Logf("  CGO has %q, PureGo does not", fn)
					}
				}
				for fn := range pureSet {
					if !cgoSet[fn] {
						t.Logf("  PureGo has %q, CGO does not", fn)
					}
				}
			}

			// Compare actual function names
			cgoSet := make(map[string]bool)
			for _, fn := range cgoFns {
				cgoSet[fn] = true
			}
			for _, fn := range pureFns {
				if !cgoSet[fn] {
					t.Errorf("PureGo found function %q not in CGO results", fn)
				}
			}
		})
	}
}

// --- Wall-Clock Timing and Memory Report ---

func TestParseTimingReport(t *testing.T) {
	files := testFiles(t)

	type result struct {
		file      string
		lines     int
		bytes     int
		cgoDur    time.Duration
		pureDur   time.Duration
		cgoAlloc  uint64
		pureAlloc uint64
	}

	var results []result

	for _, f := range files {
		src := readFile(t, f)
		lines := strings.Count(string(src), "\n")
		name := filepath.Base(f)

		var r result
		r.file = name
		r.lines = lines
		r.bytes = len(src)

		const warmup = 3
		const runs = 10

		// CGO timing
		for i := 0; i < warmup; i++ {
			parseCGO(src)
		}
		var cgoTotal time.Duration
		var cgoMem uint64
		for i := 0; i < runs; i++ {
			var m1, m2 runtime.MemStats
			runtime.ReadMemStats(&m1)
			start := time.Now()
			parseCGO(src)
			cgoTotal += time.Since(start)
			runtime.ReadMemStats(&m2)
			cgoMem += m2.TotalAlloc - m1.TotalAlloc
		}
		r.cgoDur = cgoTotal / time.Duration(runs)
		r.cgoAlloc = cgoMem / uint64(runs)

		// PureGo timing
		for i := 0; i < warmup; i++ {
			parsePureGo(src)
		}
		var pureTotal time.Duration
		var pureMem uint64
		for i := 0; i < runs; i++ {
			var m1, m2 runtime.MemStats
			runtime.ReadMemStats(&m1)
			start := time.Now()
			parsePureGo(src)
			pureTotal += time.Since(start)
			runtime.ReadMemStats(&m2)
			pureMem += m2.TotalAlloc - m1.TotalAlloc
		}
		r.pureDur = pureTotal / time.Duration(runs)
		r.pureAlloc = pureMem / uint64(runs)

		results = append(results, r)
	}

	// Print report
	t.Log("")
	t.Log("=== PARSE TIMING REPORT (avg of 10 runs, 3 warmup) ===")
	t.Log("")
	t.Logf("%-20s %6s %8s | %12s %12s | %10s %10s",
		"File", "Lines", "Bytes", "CGO", "PureGo", "CGO mem", "Pure mem")
	t.Logf("%-20s %6s %8s | %12s %12s | %10s %10s",
		"----", "-----", "-----", "---", "------", "-------", "--------")

	for _, r := range results {
		ratio := float64(r.pureDur) / float64(r.cgoDur)
		t.Logf("%-20s %6d %8d | %12s %12s | %8d KB %8d KB  (%.1fx)",
			r.file, r.lines, r.bytes,
			r.cgoDur, r.pureDur,
			r.cgoAlloc/1024, r.pureAlloc/1024,
			ratio)
	}

	t.Log("")
	t.Log("=== VERDICT CRITERIA: no-CGO must parse <500ms per file ===")
	for _, r := range results {
		if r.pureDur > 500*time.Millisecond {
			t.Errorf("FAIL: %s purego parse took %s (>500ms)", r.file, r.pureDur)
		} else {
			t.Logf("PASS: %s purego parse took %s (<500ms)", r.file, r.pureDur)
		}
	}
}
