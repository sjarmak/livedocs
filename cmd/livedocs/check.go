package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/check"
)

var checkFormat string

var checkCmd = &cobra.Command{
	Use:   "check [path]",
	Short: "Detect documentation drift",
	Long:  "Scan a repository for stale, missing, or inaccurate documentation. Returns exit code 1 if drift is found (CI-friendly).",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}

		result, err := check.Run(cmd.Context(), path)
		if err != nil {
			return fmt.Errorf("check failed: %w", err)
		}

		out := cmd.OutOrStdout()
		switch checkFormat {
		case "json":
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if err := enc.Encode(result); err != nil {
				return fmt.Errorf("encode JSON: %w", err)
			}
		case "text":
			fmt.Fprint(out, check.FormatText(result))
		default:
			return fmt.Errorf("unknown format %q: use \"text\" or \"json\"", checkFormat)
		}

		if result.HasDrift {
			return fmt.Errorf("drift detected: %d stale references, %d stale packages",
				result.TotalStale, result.TotalStalePackages)
		}
		return nil
	},
}

func init() {
	checkCmd.Flags().StringVar(&checkFormat, "format", "text", "output format: text or json")
}
