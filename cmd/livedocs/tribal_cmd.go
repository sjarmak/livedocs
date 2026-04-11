package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/db"
)

var tribalCmd = &cobra.Command{
	Use:   "tribal",
	Short: "Tribal knowledge management commands",
	Long:  "Subcommands for inspecting and managing tribal knowledge facts in claims databases.",
}

var tribalStatusCmd = &cobra.Command{
	Use:   "status <db-path>",
	Short: "Show tribal knowledge fact counts by kind",
	Long:  "Opens a claims database and reports the number of active tribal facts grouped by kind.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath := args[0]

		claimsDB, err := db.OpenClaimsDB(dbPath)
		if err != nil {
			return fmt.Errorf("open claims db: %w", err)
		}
		defer claimsDB.Close()

		counts, err := claimsDB.CountTribalFactsByKind()
		if err != nil {
			return fmt.Errorf("count tribal facts: %w", err)
		}

		out := cmd.OutOrStdout()

		if len(counts) == 0 {
			fmt.Fprintf(out, "No tribal facts found in %s\n", dbPath)
			return nil
		}

		fmt.Fprintf(out, "## Tribal Knowledge Status\n\n")
		fmt.Fprintf(out, "Database: %s\n\n", dbPath)

		// Sort kinds for deterministic output.
		kinds := make([]string, 0, len(counts))
		for k := range counts {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)

		total := 0
		for _, kind := range kinds {
			count := counts[kind]
			fmt.Fprintf(out, "- **%s**: %d\n", kind, count)
			total += count
		}
		fmt.Fprintf(out, "- **total**: %d\n", total)

		return nil
	},
}

func init() {
	tribalCmd.AddCommand(tribalStatusCmd)
}
