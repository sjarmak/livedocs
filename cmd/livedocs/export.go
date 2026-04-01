package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/audit"
)

var (
	exportFormat string
	exportOutput string
)

var exportCmd = &cobra.Command{
	Use:   "export [path]",
	Short: "Export documentation audit report",
	Long: `Generate a compliance audit report of documentation freshness.

Supported formats:
  audit-json   Machine-readable JSON (default)
  audit-md     Human-readable Markdown

The report traces documentation claims to commits and records freshness
status at a point in time. Suitable for SOC 2 / ISO 27001 evidence.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}

		report, err := audit.Generate(path, time.Now())
		if err != nil {
			return fmt.Errorf("generate audit report: %w", err)
		}

		w := cmd.OutOrStdout()
		if exportOutput != "" {
			f, err := os.Create(exportOutput)
			if err != nil {
				return fmt.Errorf("create output file: %w", err)
			}
			defer f.Close()
			w = f
		}

		switch exportFormat {
		case "audit", "audit-json":
			if err := audit.WriteJSON(w, report); err != nil {
				return fmt.Errorf("write JSON: %w", err)
			}
		case "audit-md":
			if err := audit.WriteMarkdown(w, report); err != nil {
				return fmt.Errorf("write markdown: %w", err)
			}
		default:
			return fmt.Errorf("unknown format %q: use \"audit-json\" or \"audit-md\"", exportFormat)
		}

		return nil
	},
}

func init() {
	exportCmd.Flags().StringVar(&exportFormat, "format", "audit-json",
		"output format: audit-json or audit-md")
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "",
		"write output to file (default: stdout)")
}
