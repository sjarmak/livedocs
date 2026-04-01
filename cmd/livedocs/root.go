package main

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:          "livedocs",
	Short:        "Live documentation for codebases",
	Long:         "livedocs keeps repository documentation automatically up to date with every commit.",
	SilenceUsage: true,
}

func init() {
	rootCmd.AddCommand(checkCmd)
	rootCmd.AddCommand(exportCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(prbotCmd)
	rootCmd.AddCommand(versionCmd)
}
