package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
	"github.com/live-docs/live_docs/sourcegraph"
)

var (
	enrichDataDir         string
	enrichBudget          int
	enrichMaxSymbols      int
	enrichIncludeInternal bool
	enrichForce           bool
	enrichDryRun          bool
	enrichVerify          bool
)

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
number of router calls without contacting Sourcegraph.`,
	RunE: runEnrich,
}

func init() {
	enrichCmd.Flags().StringVar(&enrichDataDir, "data-dir", "", "directory containing per-repo .claims.db files (required)")
	enrichCmd.Flags().IntVar(&enrichBudget, "budget", 100, "maximum number of Sourcegraph router calls")
	enrichCmd.Flags().IntVar(&enrichMaxSymbols, "max-symbols", 200, "maximum number of symbols to enrich")
	enrichCmd.Flags().BoolVar(&enrichIncludeInternal, "include-internal", false, "include internal-visibility symbols")
	enrichCmd.Flags().BoolVar(&enrichForce, "force", false, "re-enrich all symbols, ignoring content-hash cache")
	enrichCmd.Flags().BoolVar(&enrichDryRun, "dry-run", false, "list symbols and estimated cost without calling Sourcegraph")
	enrichCmd.Flags().BoolVar(&enrichVerify, "verify", false, "verify enriched claims after completion")
	_ = enrichCmd.MarkFlagRequired("data-dir")
}

func runEnrich(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()

	// Check for SRC_ACCESS_TOKEN unless dry-run.
	if !enrichDryRun && os.Getenv("SRC_ACCESS_TOKEN") == "" {
		fmt.Fprintln(out, "SRC_ACCESS_TOKEN is not set. Set it to a Sourcegraph access token to enable enrichment.")
		return nil
	}

	// Discover claims databases.
	pattern := filepath.Join(enrichDataDir, "*.claims.db")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob claims DBs: %w", err)
	}
	if len(matches) == 0 {
		fmt.Fprintf(out, "No .claims.db files found in %s\n", enrichDataDir)
		return nil
	}

	// For non-dry-run, create the Sourcegraph client once.
	var sgClient *sourcegraph.SourcegraphClient
	if !enrichDryRun {
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

		if enrichDryRun {
			summary, err := runEnrichDryRun(cmd, cdb, repoName)
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
				Budget:          enrichBudget,
				MaxSymbols:      enrichMaxSymbols,
				IncludeInternal: enrichIncludeInternal,
				Force:           enrichForce,
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
	if enrichDryRun {
		fmt.Fprintf(out, "  Candidate symbols: %d\n", totalSummary.SymbolsSkipped)
		fmt.Fprintf(out, "  Estimated calls:   %d (up to budget %d)\n",
			min(totalSummary.SymbolsSkipped*4, enrichBudget), enrichBudget)
		fmt.Fprintf(out, "  Mode:              dry-run (no Sourcegraph calls made)\n")
	} else {
		fmt.Fprintf(out, "  Symbols enriched: %d\n", totalSummary.SymbolsEnriched)
		fmt.Fprintf(out, "  Symbols skipped:  %d\n", totalSummary.SymbolsSkipped)
		fmt.Fprintf(out, "  Calls made:       %d\n", totalSummary.CallsMade)
	}
	fmt.Fprintf(out, "  Elapsed time:     %s\n", totalSummary.ElapsedTime.Round(time.Millisecond))

	return nil
}

// runEnrichDryRun lists candidate symbols for a single repo without calling
// the router. Returns a summary with SymbolsSkipped set to the candidate count.
func runEnrichDryRun(cmd *cobra.Command, cdb *db.ClaimsDB, repoName string) (sourcegraph.EnrichmentSummary, error) {
	out := cmd.OutOrStdout()
	var summary sourcegraph.EnrichmentSummary

	// Create a no-op enricher just to select symbols. We pass a nil router
	// because dry-run never calls Route. We need a non-nil router to satisfy
	// NewEnricher, so use a stub.
	enricher, err := sourcegraph.NewEnricher(cdb, &dryRunRouter{})
	if err != nil {
		return summary, err
	}

	symbols, err := enricher.SelectSymbols(enrichIncludeInternal, enrichMaxSymbols)
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
