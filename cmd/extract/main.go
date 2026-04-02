// Package main runs full extraction against kubernetes/kubernetes.
//
// It loads all Go packages using the Go deep extractor (single ./... load),
// runs tree-sitter on all .go files for comparison, stores all claims in a
// per-repo SQLite DB, and reports metrics.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
	"github.com/live-docs/live_docs/extractor/goextractor"
	"github.com/live-docs/live_docs/extractor/lang"
	"github.com/live-docs/live_docs/extractor/treesitter"
)

const repoName = "kubernetes/kubernetes"

func main() {
	k8sDir := os.Getenv("K8S_DIR")
	if k8sDir == "" {
		k8sDir = os.ExpandEnv("$HOME/kubernetes/kubernetes")
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/home/ds/live_docs/validation/kubernetes_kubernetes.db"
	}

	fmt.Printf("=== Full Kubernetes Monorepo Extraction ===\n")
	fmt.Printf("Target: %s\n", k8sDir)
	fmt.Printf("DB: %s\n", dbPath)
	fmt.Printf("Go version: %s\n", runtime.Version())
	fmt.Printf("GOMAXPROCS: %d\n", runtime.GOMAXPROCS(0))
	fmt.Printf("Time: %s\n\n", time.Now().Format(time.RFC3339))

	// Open claims DB (fresh each run).
	os.Remove(dbPath)
	claimsDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		fatal("open claims DB: %v", err)
	}
	defer claimsDB.Close()

	if err := claimsDB.CreateSchema(); err != nil {
		fatal("create schema: %v", err)
	}

	// ================================================================
	// Phase 1: Go deep extraction (single ./... load)
	// ================================================================
	fmt.Println("--- Phase 1: Go Deep Extraction ---")
	fmt.Println("Loading all packages via go/packages (./...) ...")
	deepStart := time.Now()

	ext := &goextractor.GoDeepExtractor{Repo: repoName}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	deepClaimsList, err := ext.Extract(ctx, k8sDir, "go")
	if err != nil {
		fatal("deep extraction: %v", err)
	}

	deepLoadDuration := time.Since(deepStart)
	fmt.Printf("Extraction produced %d claims in %s\n", len(deepClaimsList), deepLoadDuration.Round(time.Second))
	fmt.Printf("Peak RSS after load: %d MB\n", readRSSMB())

	// Store deep claims in DB.
	fmt.Println("Storing deep claims in DB...")
	storeStart := time.Now()
	deepStored, deepStoreErrors := storeClaims(claimsDB, deepClaimsList, repoName)
	storeDuration := time.Since(storeStart)
	fmt.Printf("Stored %d deep claims (%d errors) in %s\n\n", deepStored, deepStoreErrors, storeDuration.Round(time.Second))

	// Free deep claims memory.
	deepClaimsList = nil
	runtime.GC()

	deepTotalDuration := time.Since(deepStart)

	// ================================================================
	// Phase 2: Tree-sitter extraction on all .go files
	// ================================================================
	fmt.Println("--- Phase 2: Tree-sitter Extraction ---")
	tsStart := time.Now()

	langRegistry := lang.NewRegistry()
	tsExtractor := treesitter.New(langRegistry)

	goFiles, err := findGoFiles(k8sDir)
	if err != nil {
		fatal("find go files: %v", err)
	}
	fmt.Printf("Total .go files found: %d\n", len(goFiles))

	tsTotalClaims := 0
	tsStoredClaims := 0
	tsErrors := 0
	tsFilesProcessed := 0

	for _, f := range goFiles {
		claims, err := tsExtractor.Extract(context.Background(), f, "go")
		if err != nil {
			tsErrors++
			continue
		}
		tsTotalClaims += len(claims)

		rel, _ := filepath.Rel(k8sDir, f)
		stored, storeErrs := storeClaimsWithDefaults(claimsDB, claims, repoName, rel)
		tsStoredClaims += stored
		tsErrors += storeErrs
		tsFilesProcessed++

		if tsFilesProcessed%5000 == 0 {
			fmt.Printf("  [%d/%d files] claims=%d errors=%d elapsed=%s\n",
				tsFilesProcessed, len(goFiles), tsStoredClaims, tsErrors,
				time.Since(tsStart).Round(time.Second))
		}
	}

	tsDuration := time.Since(tsStart)
	fmt.Printf("\nTree-sitter extraction complete:\n")
	fmt.Printf("  Files: %d\n", tsFilesProcessed)
	fmt.Printf("  Claims extracted: %d\n", tsTotalClaims)
	fmt.Printf("  Claims stored: %d\n", tsStoredClaims)
	fmt.Printf("  Errors: %d\n", tsErrors)
	fmt.Printf("  Duration: %s\n\n", tsDuration.Round(time.Second))

	// ================================================================
	// Phase 3: Report
	// ================================================================
	fmt.Println("=== Summary ===")
	fmt.Printf("Go deep:     %d claims in %s\n", deepStored, deepTotalDuration.Round(time.Second))
	fmt.Printf("Tree-sitter: %d claims from %d files in %s\n", tsStoredClaims, tsFilesProcessed, tsDuration.Round(time.Second))
	fmt.Printf("Total:       %d claims\n", deepStored+tsStoredClaims)
	fmt.Printf("Peak RSS:    %d MB\n", readRSSMB())
	fmt.Printf("DB size:     %s\n", humanSize(fileSize(dbPath)))

	// Predicate breakdown.
	fmt.Println("\n--- Claims by Predicate ---")
	predicates := []string{
		"defines", "imports", "exports", "has_doc", "is_test", "is_generated",
		"has_kind", "implements", "has_signature", "encloses",
	}
	for _, pred := range predicates {
		count := countPredicate(claimsDB, pred)
		if count > 0 {
			fmt.Printf("  %-16s %d\n", pred, count)
		}
	}

	// Extractor breakdown.
	fmt.Println("\n--- Claims by Extractor ---")
	extractors := countByExtractor(claimsDB)
	for _, kv := range extractors {
		fmt.Printf("  %-20s %d\n", kv.key, kv.count)
	}

	fmt.Println("\n=== Extraction Complete ===")
}

// storeClaims stores claims that already have SubjectRepo and SubjectImportPath filled in.
func storeClaims(claimsDB *db.ClaimsDB, claims []extractor.Claim, repoName string) (int, int) {
	stored := 0
	errors := 0
	for _, claim := range claims {
		if claim.SubjectRepo == "" {
			claim.SubjectRepo = repoName
		}

		symID, err := claimsDB.UpsertSymbol(db.Symbol{
			Repo:        claim.SubjectRepo,
			ImportPath:  claim.SubjectImportPath,
			SymbolName:  claim.SubjectName,
			Language:    claim.Language,
			Kind:        string(claim.Kind),
			Visibility:  string(claim.Visibility),
			DisplayName: claim.SubjectName,
			SCIPSymbol:  claim.SCIPSymbol,
		})
		if err != nil {
			errors++
			continue
		}

		_, err = claimsDB.InsertClaim(db.Claim{
			SubjectID:        symID,
			Predicate:        string(claim.Predicate),
			ObjectText:       claim.ObjectText,
			SourceFile:       claim.SourceFile,
			SourceLine:       claim.SourceLine,
			Confidence:       claim.Confidence,
			ClaimTier:        string(claim.ClaimTier),
			Extractor:        claim.Extractor,
			ExtractorVersion: claim.ExtractorVersion,
			LastVerified:     db.Now(),
		})
		if err != nil {
			errors++
			continue
		}
		stored++
	}
	return stored, errors
}

// storeClaimsWithDefaults stores claims, filling in repo and import path defaults.
func storeClaimsWithDefaults(claimsDB *db.ClaimsDB, claims []extractor.Claim, repoName, relPath string) (int, int) {
	stored := 0
	errors := 0
	for _, claim := range claims {
		if claim.SubjectRepo == "" {
			claim.SubjectRepo = repoName
		}
		if claim.SubjectImportPath == "" {
			claim.SubjectImportPath = relPath
		}

		symID, err := claimsDB.UpsertSymbol(db.Symbol{
			Repo:        claim.SubjectRepo,
			ImportPath:  claim.SubjectImportPath,
			SymbolName:  claim.SubjectName,
			Language:    claim.Language,
			Kind:        string(claim.Kind),
			Visibility:  string(claim.Visibility),
			DisplayName: claim.SubjectName,
			SCIPSymbol:  claim.SCIPSymbol,
		})
		if err != nil {
			errors++
			continue
		}

		_, err = claimsDB.InsertClaim(db.Claim{
			SubjectID:        symID,
			Predicate:        string(claim.Predicate),
			ObjectText:       claim.ObjectText,
			SourceFile:       claim.SourceFile,
			SourceLine:       claim.SourceLine,
			Confidence:       claim.Confidence,
			ClaimTier:        string(claim.ClaimTier),
			Extractor:        claim.Extractor,
			ExtractorVersion: claim.ExtractorVersion,
			LastVerified:     db.Now(),
		})
		if err != nil {
			errors++
			continue
		}
		stored++
	}
	return stored, errors
}

// findGoFiles walks the repo and returns all .go file paths.
func findGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "_output" {
				return filepath.SkipDir
			}
		}
		if !info.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// countPredicate counts claims with a given predicate.
func countPredicate(claimsDB *db.ClaimsDB, predicate string) int {
	claims, err := claimsDB.GetClaimsByPredicate(predicate)
	if err != nil {
		return 0
	}
	return len(claims)
}

type kv struct {
	key   string
	count int
}

// countByExtractor counts claims grouped by extractor name.
func countByExtractor(claimsDB *db.ClaimsDB) []kv {
	counts := make(map[string]int)
	predicates := []string{
		"defines", "imports", "exports", "has_doc", "is_test", "is_generated",
		"has_kind", "implements", "has_signature", "encloses",
	}
	for _, pred := range predicates {
		claims, err := claimsDB.GetClaimsByPredicate(pred)
		if err != nil {
			continue
		}
		for _, cl := range claims {
			counts[cl.Extractor]++
		}
	}
	var result []kv
	for k, v := range counts {
		result = append(result, kv{k, v})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].count > result[j].count })
	return result
}

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

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func humanSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}
