package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/initcmd"
)

var (
	initForce bool
	initHook  bool
)

var initCmd = &cobra.Command{
	Use:   "init [path]",
	Short: "Initialize livedocs in a repository",
	Long:  "Scaffold a .livedocs.yaml configuration file and run the first extraction pass.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}

		absPath, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve path: %w", err)
		}

		info, err := os.Stat(absPath)
		if err != nil {
			return fmt.Errorf("stat %s: %w", absPath, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", absPath)
		}

		result, err := initcmd.Run(cmd.Context(), initcmd.Options{
			RepoRoot: absPath,
			Writer:   cmd.OutOrStdout(),
			Force:    initForce,
		})
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		if result.ConfigCreated {
			fmt.Fprintln(out, "Created .livedocs.yaml")
		} else {
			fmt.Fprintln(out, "Using existing .livedocs.yaml")
		}
		if result.DirCreated {
			fmt.Fprintln(out, "Created .livedocs/ directory")
		}

		fmt.Fprintf(out, "\nSummary:\n")
		fmt.Fprintf(out, "  Languages:  %v\n", result.Languages)
		fmt.Fprintf(out, "  Files:      %d scanned, %d extracted, %d skipped\n",
			result.FilesScanned, result.FilesExtracted, result.FilesSkipped)
		fmt.Fprintf(out, "  Claims:     %d stored\n", result.ClaimsStored)
		if len(result.Errors) > 0 {
			fmt.Fprintf(out, "  Errors:     %d (non-fatal)\n", len(result.Errors))
		}

		// Print enrichment guidance when SRC_ACCESS_TOKEN is not set.
		if os.Getenv("SRC_ACCESS_TOKEN") == "" {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "To add semantic context:")
			fmt.Fprintf(out, "  export SRC_ACCESS_TOKEN=<your-token> && livedocs enrich --data-dir .livedocs/ --initial\n")
		}

		// Install post-commit hook if requested.
		if initHook {
			installed, err := initcmd.InstallPostCommitHook(absPath)
			if err != nil {
				return fmt.Errorf("install hook: %w", err)
			}
			if installed {
				fmt.Fprintln(out, "\nInstalled git post-commit hook for livedocs extract")
			} else {
				fmt.Fprintln(out, "\nGit post-commit hook already contains livedocs trigger")
			}
		}

		return nil
	},
}

func init() {
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite existing .livedocs.yaml")
	initCmd.Flags().BoolVar(&initHook, "hook", false, "install git post-commit hook for automatic extraction")
}
