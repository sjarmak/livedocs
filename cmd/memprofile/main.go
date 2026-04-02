// Package main profiles go/packages memory usage on kubernetes/kubernetes.
//
// It loads increasing subsets (50, 100, 200, 500 packages) with
// NeedTypes|NeedDeps|NeedSyntax and reports RSS at each checkpoint.
// This validates whether the go/packages approach stays within the
// 8GB memory budget for the live docs extraction layer.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
)

// defaultCheckpoints defines the package counts to measure at.
// The last entry is clamped to the actual number of packages available.
var defaultCheckpoints = []int{50, 100, 200, 500, 1000}

func main() {
	k8sDir := os.Getenv("K8S_DIR")
	if k8sDir == "" {
		k8sDir = os.ExpandEnv("$HOME/kubernetes/kubernetes")
	}

	fmt.Printf("=== go/packages Memory Profile ===\n")
	fmt.Printf("Target: %s\n", k8sDir)
	fmt.Printf("Go version: %s\n", runtime.Version())
	fmt.Printf("GOMAXPROCS: %d\n", runtime.GOMAXPROCS(0))
	fmt.Printf("Time: %s\n\n", time.Now().Format(time.RFC3339))

	// List all packages first.
	allPkgs, err := listPackages(k8sDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR listing packages: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Total packages available: %d\n\n", len(allPkgs))

	// Always include the full package count as the final checkpoint.
	checkpoints := append(defaultCheckpoints, len(allPkgs))

	// Run profiling at each checkpoint.
	for _, count := range checkpoints {
		if count > len(allPkgs) {
			count = len(allPkgs)
		}
		subset := allPkgs[:count]

		fmt.Printf("--- Loading %d packages ---\n", count)
		rssBeforeMB := readRSSMB()
		fmt.Printf("  RSS before load: %d MB\n", rssBeforeMB)

		start := time.Now()
		pkgs, loadErr := loadPackages(k8sDir, subset)
		elapsed := time.Since(start)

		// Force GC and read stats.
		runtime.GC()
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)

		rssAfterMB := readRSSMB()

		fmt.Printf("  Packages loaded: %d\n", len(pkgs))
		if loadErr != nil {
			fmt.Printf("  Load error: %v\n", loadErr)
		}
		errCount := countErrors(pkgs)
		fmt.Printf("  Packages with errors: %d\n", errCount)
		// Diagnostic: check how many packages have types, syntax, etc.
		typesLoaded := 0
		syntaxLoaded := 0
		depsLoaded := 0
		for _, pkg := range pkgs {
			if pkg.Types != nil {
				typesLoaded++
			}
			if len(pkg.Syntax) > 0 {
				syntaxLoaded++
			}
			if len(pkg.Imports) > 0 {
				depsLoaded++
			}
		}
		fmt.Printf("  Types loaded: %d/%d\n", typesLoaded, len(pkgs))
		fmt.Printf("  Syntax loaded: %d/%d\n", syntaxLoaded, len(pkgs))
		fmt.Printf("  Deps loaded: %d/%d\n", depsLoaded, len(pkgs))
		if errCount > 0 && len(pkgs) > 0 {
			// Print first few errors for diagnosis.
			shown := 0
			for _, pkg := range pkgs {
				if shown >= 3 {
					break
				}
				for _, e := range pkg.Errors {
					if shown >= 3 {
						break
					}
					fmt.Printf("  Sample error [%s]: %s\n", pkg.PkgPath, e)
					shown++
				}
			}
		}
		fmt.Printf("  Time: %s\n", elapsed.Round(time.Millisecond))
		fmt.Printf("  RSS after load: %d MB\n", rssAfterMB)
		fmt.Printf("  RSS delta: %d MB\n", rssAfterMB-rssBeforeMB)
		fmt.Printf("  HeapAlloc: %d MB\n", memStats.HeapAlloc/(1024*1024))
		fmt.Printf("  HeapSys: %d MB\n", memStats.HeapSys/(1024*1024))
		fmt.Printf("  TotalAlloc: %d MB\n", memStats.TotalAlloc/(1024*1024))
		fmt.Printf("  NumGC: %d\n", memStats.NumGC)
		fmt.Printf("\n")

		// Release references to allow GC before next checkpoint.
		pkgs = nil
		runtime.GC()
	}

	fmt.Println("=== Profile Complete ===")
}

// listPackages uses `go list ./...` to enumerate packages in the given directory.
func listPackages(dir string) ([]string, error) {
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w\nstderr: %s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result, nil
}

// loadPackages uses go/packages to load the given package patterns with full
// type information, dependencies, and syntax.
func loadPackages(dir string, patterns []string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedTypes |
			packages.NeedDeps |
			packages.NeedSyntax |
			packages.NeedName |
			packages.NeedFiles |
			packages.NeedImports |
			packages.NeedTypesInfo,
		Dir:   dir,
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	return pkgs, err
}

// countErrors returns the number of packages with non-empty Errors.
func countErrors(pkgs []*packages.Package) int {
	count := 0
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			count++
		}
	}
	return count
}

// readRSSMB reads the current process RSS from /proc/self/status.
func readRSSMB() int64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, parseErr := strconv.ParseInt(fields[1], 10, 64)
				if parseErr == nil {
					return kb / 1024
				}
			}
		}
	}
	return -1
}
