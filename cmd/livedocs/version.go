package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Set via ldflags at build time (e.g. by GoReleaser).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "livedocs %s (commit: %s, built: %s)\n", version, commit, date)
	},
}
