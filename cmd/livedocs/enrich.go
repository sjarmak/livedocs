package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
	"github.com/sjarmak/livedocs/sourcegraph"
)

// estimatedCostPerCall is the assumed cost per Sourcegraph router call
// for cold-cache cost estimation in --initial mode.
const estimatedCostPerCall = 0.003

var enrichCmd = &cobra.Command{
	Use:   "enrich",
	Short: "Enrich claims with semantic context from Sourcegraph",
	Long: `Run the enrichment pipeline over all claims databases in --data-dir.

For each .claims.db file, selects high-value symbols, queries Sourcegraph
for semantic context, and stores the resulting claims. Cost controls limit
total router calls (--budget) and symbol count (--max-symbols).

Requires SRC_ACCESS_TOKEN environment variable. Without it the command
prints an informational message and exits successfully.

Use --dry-run to preview which symbols would be enriched and the estimated
number of router calls without contacting Sourcegraph.

Use --initial for full-corpus enrichment (sets budget and max-symbols to
unlimited). Requires --confirm to proceed; without it, prints a cost
estimate and exits.`,
	RunE: runEnrich,
}

func init() {
	enrichCmd.Flags().String("data-dir", "", "directory containing per-repo .claims.db files (required)")
	enrichCmd.Flags().Int("budget", 100, "maximum number of Sourcegraph router calls")
	enrichCmd.Flags().Int("max-symbols", 200, "maximum number of symbols to enrich")
	enrichCmd.Flags().Bool("include-internal", false, "include internal-visibility symbols")
	enrichCmd.Flags().Bool("force", false, "re-enrich all symbols, ignoring content-hash cache")
	enrichCmd.Flags().Bool("dry-run", false, "list symbols and estimated cost without calling Sourcegraph")
	enrichCmd.Flags().Bool("verify", false, "verify enriched claims after completion")
	enrichCmd.Flags().Bool("initial", false, "full-corpus enrichment (unlimited budget and max-symbols)")
	enrichCmd.Flags().Bool("confirm", false, "confirm cold-cache enrichment (required with --initial)")
	_ = enrichCmd.MarkFlagRequired("data-dir")
}

func runEnrich(cmd *cobra.Command, args []string) error {
	defer resetCmdFlags(cmd)

	dataDir, _ := cmd.Flags().GetString("data-dir")
	budget, _ := cmd.Flags().GetInt("budget")
	maxSymbols, _ := cmd.Flags().GetInt("max-symbols")
	includeInternal, _ := cmd.Flags().GetBool("include-internal")
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	initial, _ := cmd.Flags().GetBool("initial")
	confirm, _ := cmd.Flags().GetBool("confirm")

	out := cmd.OutOrStdout()

	// --initial overrides budget and max-symbols to unlimited.
	if initial {
		budget = 0
		maxSymbols = 0
	}

	// Check for SRC_ACCESS_TOKEN unless dry-run or initial-without-confirm.
	needsToken := !dryRun && !(initial && !confirm)
	if needsToken && os.Getenv("SRC_ACCESS_TOKEN") == "" {
		fmt.Fprintln(out, "SRC_ACCESS_TOKEN is not set. Set it to a Sourcegraph access token to enable enrichment.")
		return nil
	}

	// Discover claims databases.
	pattern := filepath.Join(dataDir, "*.claims.db")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob claims DBs: %w", err)
	}
	if len(matches) == 0 {
		fmt.Fprintf(out, "No .claims.db files found in %s\n", dataDir)
		return nil
	}

	// --initial without --confirm: print cost estimate and exit.
	if initial && !confirm {
		return runInitialCostEstimate(cmd, matches, includeInternal)
	}

	// For non-dry-run, create the Sourcegraph client once.
	var sgClient *sourcegraph.SourcegraphClient
	if !dryRun {
		sgClient, err = sourcegraph.NewSourcegraphClient()
		if err != nil {
			return fmt.Errorf("create sourcegraph client: %w", err)
		}
		defer sgClient.Close()
	}

	start := time.Now()
	var totalSummary sourcegraph.EnrichmentSummary

	for _, match := range matches {
		base := filepath.Base(match)
		repoName := strings.TrimSuffix(base, ".claims.db")
		if repoName == "" {
			continue
		}

		cdb, err := db.OpenClaimsDB(match)
		if err != nil {
			fmt.Fprintf(out, "warning: open %s: %v (skipping)\n", base, err)
			continue
		}

		if dryRun {
			summary, err := runEnrichDryRun(cmd, cdb, repoName, includeInternal, maxSymbols)
			cdb.Close()
			if err != nil {
				fmt.Fprintf(out, "warning: dry-run %s: %v (skipping)\n", repoName, err)
				continue
			}
			totalSummary.SymbolsSkipped += summary.SymbolsSkipped
		} else {
			router := sourcegraph.NewDefaultRouter(sgClient)
			enricher, err := sourcegraph.NewEnricher(cdb, router)
			if err != nil {
				cdb.Close()
				return fmt.Errorf("create enricher for %s: %w", repoName, err)
			}

			opts := sourcegraph.EnrichOpts{
				Budget:          budget,
				MaxSymbols:      maxSymbols,
				IncludeInternal: includeInternal,
				Force:           force,
			}

			summary, err := enricher.Run(cmd.Context(), opts)
			cdb.Close()
			if err != nil {
				fmt.Fprintf(out, "warning: enrich %s: %v\n", repoName, err)
				continue
			}

			totalSummary.SymbolsEnriched += summary.SymbolsEnriched
			totalSummary.SymbolsSkipped += summary.SymbolsSkipped
			totalSummary.CallsMade += summary.CallsMade
		}
	}

	totalSummary.ElapsedTime = time.Since(start)

	// Print summary.
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Enrichment Summary")
	fmt.Fprintln(out, "==================")
	if dryRun {
		fmt.Fprintf(out, "  Candidate symbols: %d\n", totalSummary.SymbolsSkipped)
		fmt.Fprintf(out, "  Estimated calls:   %d (up to budget %d)\n",
			min(totalSummary.SymbolsSkipped*4, budget), budget)
		fmt.Fprintf(out, "  Mode:              dry-run (no Sourcegraph calls made)\n")
	} else {
		fmt.Fprintf(out, "  Symbols enriched: %d\n", totalSummary.SymbolsEnriched)
		fmt.Fprintf(out, "  Symbols skipped:  %d\n", totalSummary.SymbolsSkipped)
		fmt.Fprintf(out, "  Calls made:       %d\n", totalSummary.CallsMade)
	}
	fmt.Fprintf(out, "  Elapsed time:     %s\n", totalSummary.ElapsedTime.Round(time.Millisecond))

	return nil
}

// runInitialCostEstimate counts symbols across all DBs and prints
// a cost/time estimate for --initial mode, then exits without enriching.
func runInitialCostEstimate(cmd *cobra.Command, matches []string, includeInternal bool) error {
	out := cmd.OutOrStdout()
	totalSymbols := 0

	for _, match := range matches {
		base := filepath.Base(match)
		cdb, err := db.OpenClaimsDB(match)
		if err != nil {
			fmt.Fprintf(out, "warning: open %s: %v (skipping)\n", base, err)
			continue
		}

		enricher, err := sourcegraph.NewEnricher(cdb, &dryRunRouter{})
		if err != nil {
			cdb.Close()
			continue
		}

		symbols, err := enricher.SelectSymbols(includeInternal, 0)
		cdb.Close()
		if err != nil {
			continue
		}
		totalSymbols += len(symbols)
	}

	estimatedCalls := totalSymbols * len(sourcegraph.DefaultPredicates())
	estimatedCost := float64(estimatedCalls) * estimatedCostPerCall
	estimatedMinutes := float64(estimatedCalls) * 2.0 / 60.0 // ~2s per call

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Initial Enrichment Cost Estimate")
	fmt.Fprintln(out, "================================")
	fmt.Fprintf(out, "  Total symbols:     %d\n", totalSymbols)
	fmt.Fprintf(out, "  Estimated calls:   %d\n", estimatedCalls)
	fmt.Fprintf(out, "  Estimated cost:    $%.2f\n", estimatedCost)
	fmt.Fprintf(out, "  Estimated time:    %.0f minutes\n", estimatedMinutes)
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Run with --confirm to proceed with enrichment.")

	return nil
}

// runEnrichDryRun lists candidate symbols for a single repo without calling
// the router. Returns a summary with SymbolsSkipped set to the candidate count.
func runEnrichDryRun(cmd *cobra.Command, cdb *db.ClaimsDB, repoName string, includeInternal bool, maxSymbols int) (sourcegraph.EnrichmentSummary, error) {
	out := cmd.OutOrStdout()
	var summary sourcegraph.EnrichmentSummary

	// Create a no-op enricher just to select symbols. We pass a nil router
	// because dry-run never calls Route. We need a non-nil router to satisfy
	// NewEnricher, so use a stub.
	enricher, err := sourcegraph.NewEnricher(cdb, &dryRunRouter{})
	if err != nil {
		return summary, err
	}

	symbols, err := enricher.SelectSymbols(includeInternal, maxSymbols)
	if err != nil {
		return summary, fmt.Errorf("select symbols: %w", err)
	}

	fmt.Fprintf(out, "\n[%s] %d candidate symbols:\n", repoName, len(symbols))
	for _, sym := range symbols {
		fmt.Fprintf(out, "  %s %s (%s)\n", sym.Kind, sym.SymbolName, sym.ImportPath)
	}

	summary.SymbolsSkipped = len(symbols)
	return summary, nil
}

// dryRunRouter is a stub PredicateRouter that satisfies NewEnricher but is
// never called during dry-run.
type dryRunRouter struct{}

func (d *dryRunRouter) Route(_ context.Context, _ extractor.Predicate, _ sourcegraph.SymbolContext) (string, error) {
	return "", fmt.Errorf("dryRunRouter: should not be called")
}
