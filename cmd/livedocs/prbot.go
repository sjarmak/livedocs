package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/gitdiff"
	"github.com/live-docs/live_docs/prbot"
)

var (
	prbotDiffFile string
	prbotDBPath   string
	prbotRadius   int
	prbotFormat   string
)

var prbotCmd = &cobra.Command{
	Use:   "prbot",
	Short: "Analyze PR diff for documentation impact",
	Long: `Analyze a PR diff against the claims database to find invalidated
documentation claims. Outputs a markdown comment suitable for posting
on a GitHub PR.

In CLI mode, provide a diff file (git diff --name-status output) and
a claims database path. The command prints the PR comment body to stdout.

As a GitHub App webhook handler, this logic is invoked automatically
on pull_request events.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		changes, err := loadChanges(prbotDiffFile)
		if err != nil {
			return fmt.Errorf("load diff: %w", err)
		}

		claims, err := loadClaims(prbotDBPath)
		if err != nil {
			return fmt.Errorf("load claims: %w", err)
		}

		report := prbot.Analyze(changes, claims, prbotRadius)

		out := cmd.OutOrStdout()
		switch prbotFormat {
		case "markdown":
			fmt.Fprint(out, prbot.FormatComment(report))
		case "json":
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if err := enc.Encode(report); err != nil {
				return fmt.Errorf("encode JSON: %w", err)
			}
		default:
			return fmt.Errorf("unknown format %q: use \"markdown\" or \"json\"", prbotFormat)
		}

		return nil
	},
}

func init() {
	prbotCmd.Flags().StringVar(&prbotDiffFile, "diff-file", "", "path to git diff --name-status output file (required)")
	prbotCmd.Flags().StringVar(&prbotDBPath, "db", ".livedocs/claims.db", "path to claims database")
	prbotCmd.Flags().IntVar(&prbotRadius, "radius", 5, "line radius for anchor matching")
	prbotCmd.Flags().StringVar(&prbotFormat, "format", "markdown", "output format: markdown or json")
	_ = prbotCmd.MarkFlagRequired("diff-file")
}

// loadChanges reads a git diff --name-status file and parses it.
func loadChanges(path string) ([]gitdiff.FileChange, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read diff file %s: %w", path, err)
	}
	return gitdiff.ParseNameStatus(string(data))
}

// loadClaims opens the claims database and loads all claims. It also
// builds an anchor index using BuildFromClaims internally.
func loadClaims(dbPath string) ([]db.Claim, error) {
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open claims db: %w", err)
	}
	defer cdb.Close()

	return loadAllClaims(cdb)
}

// loadAllClaims retrieves all claims from the database by querying
// all distinct source files and collecting their claims.
func loadAllClaims(cdb *db.ClaimsDB) ([]db.Claim, error) {
	// Query all claims by fetching all distinct import paths and their claims.
	// For the PR bot, we need all claims to check against the diff.
	paths, err := cdb.ListDistinctImportPaths(10000)
	if err != nil {
		return nil, fmt.Errorf("list import paths: %w", err)
	}

	var allClaims []db.Claim
	seen := make(map[int64]bool)
	for _, ip := range paths {
		swcs, err := cdb.GetStructuralClaimsByImportPath(ip)
		if err != nil {
			return nil, fmt.Errorf("get claims for %s: %w", ip, err)
		}
		for _, swc := range swcs {
			for _, cl := range swc.Claims {
				if !seen[cl.ID] {
					seen[cl.ID] = true
					allClaims = append(allClaims, cl)
				}
			}
		}
	}

	// Also get semantic claims via predicates that are common.
	for _, pred := range []string{"purpose", "usage_pattern", "complexity", "stability"} {
		semClaims, err := cdb.GetClaimsByPredicate(pred)
		if err != nil {
			return nil, fmt.Errorf("get %s claims: %w", pred, err)
		}
		for _, cl := range semClaims {
			if !seen[cl.ID] {
				seen[cl.ID] = true
				allClaims = append(allClaims, cl)
			}
		}
	}

	return allClaims, nil
}
