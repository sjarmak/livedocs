package main

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/drift"
)

var verifyClaimsCmd = &cobra.Command{
	Use:   "verify-claims [path]",
	Short: "Verify claims in a SQLite DB against current source code",
	Long: `Check claims stored in a claims database against the actual source code.

Exits 0 if all claims match, exits 1 if drift is detected.
Output is one-line-per-issue for CI parsability.

Flags:
  --staleness       Report per-claim staleness scores
  --canary           Sample 50 random claims, re-verify, exit non-zero if >2% stale
  --check-existing   Scan README.md files for claims that contradict current code`,
	Args: cobra.MaximumNArgs(1),
	RunE: runVerifyClaims,
}

func init() {
	verifyClaimsCmd.Flags().String("db", "", "path to claims SQLite database (default: <dirname>.claims.db)")
	verifyClaimsCmd.Flags().Bool("staleness", false, "report per-claim staleness scores")
	verifyClaimsCmd.Flags().Bool("canary", false, "sample 50 random claims and re-verify; exit non-zero if >2% stale")
	verifyClaimsCmd.Flags().Bool("check-existing", false, "scan README.md files for contradicting claims")
}

func runVerifyClaims(cmd *cobra.Command, args []string) error {
	defer resetCmdFlags(cmd)

	dbFlag := mustGetString(cmd, "db")
	staleness := mustGetBool(cmd, "staleness")
	canary := mustGetBool(cmd, "canary")
	checkEx := mustGetBool(cmd, "check-existing")

	repoPath := "."
	if len(args) > 0 {
		repoPath = args[0]
	}

	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	dbPath, err := resolveClaimsDBPath(absRepo, dbFlag)
	if err != nil {
		return err
	}

	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		return fmt.Errorf("open claims db: %w", err)
	}
	defer cdb.Close()

	out := cmd.OutOrStdout()

	if checkEx {
		return runCheckExisting(cdb, absRepo, out)
	}

	if canary {
		return runCanary(cdb, absRepo, out)
	}

	if staleness {
		return runStaleness(cdb, absRepo, out)
	}

	return runBasicVerify(cdb, absRepo, out)
}

// resolveClaimsDBPath determines the database file path. If dbFlag is set,
// use it directly. Otherwise, derive from the repo directory name.
func resolveClaimsDBPath(absRepo, dbFlag string) (string, error) {
	if dbFlag != "" {
		if _, err := os.Stat(dbFlag); err != nil {
			return "", fmt.Errorf("claims db not found: %w", err)
		}
		return dbFlag, nil
	}

	dirName := filepath.Base(absRepo)
	candidate := dirName + ".claims.db"
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}

	// Also check in the repo directory itself.
	candidate2 := filepath.Join(absRepo, dirName+".claims.db")
	if _, err := os.Stat(candidate2); err == nil {
		return candidate2, nil
	}

	return "", fmt.Errorf("claims db not found: tried %q and %q (use --db flag)", candidate, candidate2)
}

// claimWithSymbol bundles a DB claim with its symbol for output formatting.
type claimWithSymbol struct {
	claim  db.Claim
	symbol db.Symbol
}

// getAllClaimsWithSymbols fetches all claims joined with their symbol info.
func getAllClaimsWithSymbols(cdb *db.ClaimsDB) ([]claimWithSymbol, error) {
	paths, err := cdb.ListDistinctImportPaths(100000)
	if err != nil {
		return nil, fmt.Errorf("list import paths: %w", err)
	}

	var results []claimWithSymbol
	for _, ip := range paths {
		swc, err := cdb.GetStructuralClaimsByImportPath(ip)
		if err != nil {
			return nil, fmt.Errorf("get claims for %s: %w", ip, err)
		}
		for _, sc := range swc {
			for _, cl := range sc.Claims {
				results = append(results, claimWithSymbol{claim: cl, symbol: sc.Symbol})
			}
		}
		// Also get semantic claims via symbol-level query.
		symbols, err := cdb.ListSymbolsByImportPath(ip)
		if err != nil {
			continue
		}
		for _, sym := range symbols {
			claims, err := cdb.GetClaimsBySubject(sym.ID)
			if err != nil {
				continue
			}
			for _, cl := range claims {
				if cl.ClaimTier == "semantic" {
					results = append(results, claimWithSymbol{claim: cl, symbol: sym})
				}
			}
		}
	}

	return results, nil
}

// runBasicVerify checks every claim against the source file at the claimed location.
func runBasicVerify(cdb *db.ClaimsDB, repoDir string, out io.Writer) error {
	all, err := getAllClaimsWithSymbols(cdb)
	if err != nil {
		return err
	}

	if len(all) == 0 {
		fmt.Fprintln(out, "OK: no claims in database")
		return nil
	}

	driftCount := 0
	for _, cws := range all {
		issue := verifyOneClaim(cws, repoDir)
		if issue != "" {
			fmt.Fprintln(out, issue)
			driftCount++
		}
	}

	if driftCount > 0 {
		fmt.Fprintf(out, "\n%d claim(s) drifted out of %d total\n", driftCount, len(all))
		return fmt.Errorf("drift detected: %d claim(s)", driftCount)
	}

	fmt.Fprintf(out, "OK: %d claim(s) verified\n", len(all))
	return nil
}

// verifyOneClaim checks a single claim against source code and returns a
// DRIFT line if the claim is broken, or empty string if OK.
func verifyOneClaim(cws claimWithSymbol, repoDir string) string {
	cl := cws.claim
	sym := cws.symbol

	sourceFile := cl.SourceFile
	if !filepath.IsAbs(sourceFile) {
		sourceFile = filepath.Join(repoDir, sourceFile)
	}

	info, err := os.Stat(sourceFile)
	if err != nil {
		// Source file deleted — HIGH severity.
		return formatDriftWithSeverity(SeverityHigh, cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName, "source file not found")
	}

	// For "defines" predicate: check that the file exists and has content around the claimed line.
	if cl.Predicate == "defines" {
		if cl.SourceLine > 0 {
			content, err := os.ReadFile(sourceFile)
			if err != nil {
				return formatDriftWithSeverity(SeverityHigh, cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName, "cannot read source file")
			}
			lines := strings.Split(string(content), "\n")
			if cl.SourceLine > len(lines) {
				// Line count mismatch suggests symbol moved — MEDIUM severity.
				return formatDriftWithSeverity(SeverityMedium, cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName,
					fmt.Sprintf("source file has only %d lines, claim references line %d", len(lines), cl.SourceLine))
			}
		}
	}

	// For "has_signature" predicate: check file exists and hasn't been deleted.
	// Full signature re-parsing is expensive; check file modification time as proxy.
	if cl.Predicate == "has_signature" {
		if info.Size() == 0 {
			return formatDriftWithSeverity(SeverityHigh, cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName, "source file is empty")
		}
	}

	// Staleness check: if last_verified is older than source file modification time.
	lastVerified, err := time.Parse(time.RFC3339, cl.LastVerified)
	if err == nil && info.ModTime().After(lastVerified) {
		// Source modified after verification — LOW severity (informational).
		return formatDriftWithSeverity(SeverityLow, cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName,
			fmt.Sprintf("source modified %s after last verification %s",
				info.ModTime().Format(time.RFC3339), cl.LastVerified))
	}

	return ""
}

// DriftSeverity classifies the severity of a drift finding.
type DriftSeverity string

const (
	// SeverityHigh indicates a symbol was deleted or its source file no longer exists.
	SeverityHigh DriftSeverity = "HIGH"
	// SeverityMedium indicates a symbol was renamed or moved to a different import path.
	SeverityMedium DriftSeverity = "MEDIUM"
	// SeverityLow indicates a minor informational mismatch (e.g., staleness).
	SeverityLow DriftSeverity = "LOW"
)

// formatDriftWithSeverity produces a single CI-parseable drift line with severity.
func formatDriftWithSeverity(severity DriftSeverity, file string, line int, predicate, subject, detail string) string {
	return fmt.Sprintf("DRIFT [%s]: %s:%d: %s %s — %s", severity, file, line, predicate, subject, detail)
}

// formatDrift produces a single CI-parseable drift line (defaults to LOW severity for backward compat).
func formatDrift(file string, line int, predicate, subject, detail string) string {
	return formatDriftWithSeverity(SeverityLow, file, line, predicate, subject, detail)
}

// runStaleness reports per-claim staleness information.
func runStaleness(cdb *db.ClaimsDB, repoDir string, out io.Writer) error {
	all, err := getAllClaimsWithSymbols(cdb)
	if err != nil {
		return err
	}

	if len(all) == 0 {
		fmt.Fprintln(out, "OK: no claims in database")
		return nil
	}

	staleCount := 0
	for _, cws := range all {
		cl := cws.claim
		sym := cws.symbol

		sourceFile := cl.SourceFile
		if !filepath.IsAbs(sourceFile) {
			sourceFile = filepath.Join(repoDir, sourceFile)
		}

		lastVerified, parseErr := time.Parse(time.RFC3339, cl.LastVerified)
		if parseErr != nil {
			fmt.Fprintf(out, "STALE: %s:%d: %s %s — cannot parse last_verified: %s\n",
				cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName, cl.LastVerified)
			staleCount++
			continue
		}

		info, err := os.Stat(sourceFile)
		if err != nil {
			fmt.Fprintf(out, "STALE: %s:%d: %s %s — source file not found (age: unknown)\n",
				cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName)
			staleCount++
			continue
		}

		age := time.Since(lastVerified)
		if info.ModTime().After(lastVerified) {
			staleDuration := info.ModTime().Sub(lastVerified)
			fmt.Fprintf(out, "STALE: %s:%d: %s %s — stale for %s (verified %s, modified %s)\n",
				cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName,
				staleDuration.Round(time.Second),
				cl.LastVerified, info.ModTime().Format(time.RFC3339))
			staleCount++
		} else {
			fmt.Fprintf(out, "FRESH: %s:%d: %s %s — verified %s ago\n",
				cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName,
				age.Round(time.Second))
		}
	}

	if staleCount > 0 {
		fmt.Fprintf(out, "\n%d stale claim(s) out of %d total\n", staleCount, len(all))
		return fmt.Errorf("staleness detected: %d claim(s)", staleCount)
	}

	fmt.Fprintf(out, "\nOK: %d claim(s), all fresh\n", len(all))
	return nil
}

// runCanary samples 50 random claims and re-verifies them.
// Exits non-zero if more than 2% are stale.
func runCanary(cdb *db.ClaimsDB, repoDir string, out io.Writer) error {
	all, err := getAllClaimsWithSymbols(cdb)
	if err != nil {
		return err
	}

	if len(all) == 0 {
		fmt.Fprintln(out, "OK: no claims in database")
		return nil
	}

	sampleSize := 50
	if sampleSize > len(all) {
		sampleSize = len(all)
	}

	// Fisher-Yates shuffle for random sampling.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	shuffled := make([]claimWithSymbol, len(all))
	copy(shuffled, all)
	for i := len(shuffled) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	sample := shuffled[:sampleSize]

	staleCount := 0
	for _, cws := range sample {
		issue := verifyOneClaim(cws, repoDir)
		if issue != "" {
			fmt.Fprintln(out, issue)
			staleCount++
		}
	}

	stalePercent := float64(staleCount) / float64(sampleSize) * 100.0
	fmt.Fprintf(out, "\nCanary: %d/%d sampled claims stale (%.1f%%)\n", staleCount, sampleSize, stalePercent)

	if stalePercent > 2.0 {
		return fmt.Errorf("canary failed: %.1f%% stale (threshold: 2%%)", stalePercent)
	}

	fmt.Fprintln(out, "OK: canary passed")
	return nil
}

// runCheckExisting scans README.md files in the repo and compares referenced
// symbols against claims in the database to find contradictions.
func runCheckExisting(cdb *db.ClaimsDB, repoDir string, out io.Writer) error {
	readmes, err := findReadmes(repoDir)
	if err != nil {
		return fmt.Errorf("find readmes: %w", err)
	}

	if len(readmes) == 0 {
		fmt.Fprintln(out, "OK: no README.md files found")
		return nil
	}

	driftCount := 0
	for _, readme := range readmes {
		content, err := os.ReadFile(readme)
		if err != nil {
			continue
		}

		symbols, _ := drift.ExtractReadmeSymbols(string(content))
		relReadme, _ := filepath.Rel(repoDir, readme)

		// For each symbol mentioned in the README, check if any claims
		// in the DB contradict its existence or status.
		for _, sym := range symbols {
			matches, err := cdb.SearchSymbolsByName(sym)
			if err != nil {
				continue
			}

			if len(matches) == 0 {
				// Symbol in README not found in claims DB at all — HIGH severity.
				fmt.Fprintf(out, "DRIFT [HIGH]: %s:0: readme_ref %s — symbol referenced in README but not found in claims DB\n",
					relReadme, sym)
				driftCount++
				continue
			}

			// Check if any matching symbol has a claim indicating deletion or invalidity.
			for _, match := range matches {
				claims, err := cdb.GetClaimsBySubject(match.ID)
				if err != nil {
					continue
				}
				for _, cl := range claims {
					if cl.Predicate == "defines" {
						sourceFile := cl.SourceFile
						if !filepath.IsAbs(sourceFile) {
							sourceFile = filepath.Join(repoDir, sourceFile)
						}
						if _, err := os.Stat(sourceFile); err != nil {
							// Source file deleted but referenced in README — HIGH severity.
							fmt.Fprintf(out, "DRIFT [HIGH]: %s:0: readme_ref %s — README references symbol but source file %s no longer exists\n",
								relReadme, sym, cl.SourceFile)
							driftCount++
						}
					}
				}

				// Check if symbol exists but in a different import path than expected.
				// If the README references it and the DB has it under a different path,
				// that's a MEDIUM severity (possibly renamed/moved).
				if match.ImportPath != "" {
					allByName, err := cdb.SearchSymbolsByName(sym)
					if err == nil && len(allByName) > 1 {
						// Multiple import paths for the same symbol name suggests a move.
						pathSet := make(map[string]bool)
						for _, s := range allByName {
							pathSet[s.ImportPath] = true
						}
						if len(pathSet) > 1 {
							paths := make([]string, 0, len(pathSet))
							for p := range pathSet {
								paths = append(paths, p)
							}
							sort.Strings(paths)
							fmt.Fprintf(out, "DRIFT [MEDIUM]: %s:0: readme_ref %s — symbol found in multiple import paths: %s\n",
								relReadme, sym, strings.Join(paths, ", "))
							driftCount++
							break // Only report once per symbol.
						}
					}
				}
			}
		}
	}

	if driftCount > 0 {
		fmt.Fprintf(out, "\n%d contradiction(s) found between README files and claims DB\n", driftCount)
		return fmt.Errorf("check-existing found %d contradiction(s)", driftCount)
	}

	fmt.Fprintf(out, "OK: %d README file(s) checked, no contradictions\n", len(readmes))
	return nil
}

// findReadmes walks the repo directory for README.md files.
func findReadmes(dir string) ([]string, error) {
	var readmes []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible dirs
		}
		// Skip hidden directories and vendor.
		base := filepath.Base(path)
		if info.IsDir() && (strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules") {
			return filepath.SkipDir
		}
		if !info.IsDir() && strings.EqualFold(base, "README.md") {
			readmes = append(readmes, path)
		}
		return nil
	})
	return readmes, err
}
