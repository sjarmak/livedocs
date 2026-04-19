package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sjarmak/livedocs/mcpserver"
	"github.com/sjarmak/livedocs/renderer"
)

var contextCmd = &cobra.Command{
	Use:   "context <repo> [package]",
	Short: "Generate claims-backed context documentation",
	Long: `Generate Markdown documentation from claims databases.

With two arguments (repo + package), renders documentation for a single
package and prints it to stdout.

With one argument (repo only), generates .livedocs/CONTEXT.md files for
every package found in the repository's claims database.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		defer resetCmdFlags(cmd)

		repoName := args[0]

		dataDir, _ := cmd.Flags().GetString("data-dir")

		pool := mcpserver.NewDBPool(dataDir, mcpserver.DefaultMaxOpenDBs)
		defer pool.Close()

		cdb, err := pool.Open(repoName)
		if err != nil {
			return fmt.Errorf("open repo %s: %w", repoName, err)
		}

		if len(args) == 2 {
			// Single package mode: render to stdout.
			pkgPath := args[1]
			pd, err := renderer.LoadPackageData(cdb, pkgPath)
			if err != nil {
				return fmt.Errorf("load package data for %s: %w", pkgPath, err)
			}
			md := renderer.RenderMarkdown(pd)
			fmt.Fprint(cmd.OutOrStdout(), md)
			return nil
		}

		// Repo-wide mode: generate CONTEXT.md for each package.
		const maxPaths = 10000
		paths, err := cdb.ListDistinctImportPaths(maxPaths)
		if err != nil {
			return fmt.Errorf("list import paths: %w", err)
		}
		if len(paths) == 0 {
			return fmt.Errorf("no import paths found in repo %s", repoName)
		}

		outDir := filepath.Join(".livedocs", repoName)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("create output directory %s: %w", outDir, err)
		}

		var generated int
		for _, pkgPath := range paths {
			pd, err := renderer.LoadPackageData(cdb, pkgPath)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: skipping %s: %v\n", pkgPath, err)
				continue
			}
			md := renderer.RenderMarkdown(pd)

			outFile := filepath.Join(outDir, "CONTEXT.md")
			if len(paths) > 1 {
				// Use a sanitized package path as subdirectory.
				pkgDir := filepath.Join(outDir, sanitizePath(pkgPath))
				if err := os.MkdirAll(pkgDir, 0o755); err != nil {
					return fmt.Errorf("create directory %s: %w", pkgDir, err)
				}
				outFile = filepath.Join(pkgDir, "CONTEXT.md")
			}

			if err := os.WriteFile(outFile, []byte(md), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", outFile, err)
			}
			generated++
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Generated %d CONTEXT.md files in %s\n", generated, outDir)
		return nil
	},
}

// sanitizePath converts an import path to a safe filesystem path by replacing
// characters that might cause issues.
func sanitizePath(importPath string) string {
	// Import paths use forward slashes; filepath.FromSlash handles OS conversion.
	return filepath.FromSlash(importPath)
}

func init() {
	contextCmd.Flags().String("data-dir", "data/claims/", "directory containing per-repo .claims.db files")
}
