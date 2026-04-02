package main

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/drift"
)

var (
	verifyClaimsDB        string
	verifyClaimsStaleness bool
	verifyClaimsCanary    bool
	verifyClaimsCheckEx   bool
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
	verifyClaimsCmd.Flags().StringVar(&verifyClaimsDB, "db", "", "path to claims SQLite database (default: <dirname>.claims.db)")
	verifyClaimsCmd.Flags().BoolVar(&verifyClaimsStaleness, "staleness", false, "report per-claim staleness scores")
	verifyClaimsCmd.Flags().BoolVar(&verifyClaimsCanary, "canary", false, "sample 50 random claims and re-verify; exit non-zero if >2% stale")
	verifyClaimsCmd.Flags().BoolVar(&verifyClaimsCheckEx, "check-existing", false, "scan README.md files for contradicting claims")
}

func runVerifyClaims(cmd *cobra.Command, args []string) error {
	repoPath := "."
	if len(args) > 0 {
		repoPath = args[0]
	}

	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	dbPath, err := resolveClaimsDBPath(absRepo, verifyClaimsDB)
	if err != nil {
		return err
	}

	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		return fmt.Errorf("open claims db: %w", err)
	}
	defer cdb.Close()

	out := cmd.OutOrStdout()

	if verifyClaimsCheckEx {
		return runCheckExisting(cdb, absRepo, out)
	}

	if verifyClaimsCanary {
		return runCanary(cdb, absRepo, out)
	}

	if verifyClaimsStaleness {
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
		return formatDrift(cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName, "source file not found")
	}

	// For "defines" predicate: check that the file exists and has content around the claimed line.
	if cl.Predicate == "defines" {
		if cl.SourceLine > 0 {
			content, err := os.ReadFile(sourceFile)
			if err != nil {
				return formatDrift(cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName, "cannot read source file")
			}
			lines := strings.Split(string(content), "\n")
			if cl.SourceLine > len(lines) {
				return formatDrift(cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName,
					fmt.Sprintf("source file has only %d lines, claim references line %d", len(lines), cl.SourceLine))
			}
		}
	}

	// For "has_signature" predicate: check file exists and hasn't been deleted.
	// Full signature re-parsing is expensive; check file modification time as proxy.
	if cl.Predicate == "has_signature" {
		if info.Size() == 0 {
			return formatDrift(cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName, "source file is empty")
		}
	}

	// Staleness check: if last_verified is older than source file modification time.
	lastVerified, err := time.Parse(time.RFC3339, cl.LastVerified)
	if err == nil && info.ModTime().After(lastVerified) {
		return formatDrift(cl.SourceFile, cl.SourceLine, cl.Predicate, sym.SymbolName,
			fmt.Sprintf("source modified %s after last verification %s",
				info.ModTime().Format(time.RFC3339), cl.LastVerified))
	}

	return ""
}

// formatDrift produces a single CI-parseable drift line.
func formatDrift(file string, line int, predicate, subject, detail string) string {
	return fmt.Sprintf("DRIFT: %s:%d: %s %s — %s", file, line, predicate, subject, detail)
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
				// Symbol in README not found in claims DB at all.
				fmt.Fprintf(out, "DRIFT: %s:0: readme_ref %s — symbol referenced in README but not found in claims DB\n",
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
							fmt.Fprintf(out, "DRIFT: %s:0: readme_ref %s — README references symbol but source file %s no longer exists\n",
								relReadme, sym, cl.SourceFile)
							driftCount++
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
