package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/check"
)

var (
	checkFormat         string
	checkUpdateManifest bool
	checkManifest       bool
)

var checkCmd = &cobra.Command{
	Use:   "check [path]",
	Short: "Detect documentation drift",
	Long: `Scan a repository for stale, missing, or inaccurate documentation.
Returns exit code 1 if drift is found (CI-friendly).

Modes:
  default          Full drift detection (symbol-level, may be slow on large repos)
  --manifest       Stateless manifest-based check (fast, no SQLite, post-commit hook friendly)
  --update-manifest  Generate or update the .livedocs/manifest file`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}

		// Mode: update manifest
		if checkUpdateManifest {
			return runUpdateManifest(cmd, path)
		}

		// Mode: manifest-based stateless check
		if checkManifest {
			return runManifestCheck(cmd, path)
		}

		// Default mode: full drift detection
		return runFullCheck(cmd, path)
	},
}

func init() {
	checkCmd.Flags().StringVar(&checkFormat, "format", "text", "output format: text or json")
	checkCmd.Flags().BoolVar(&checkUpdateManifest, "update-manifest", false, "generate or update the .livedocs/manifest file")
	checkCmd.Flags().BoolVar(&checkManifest, "manifest", false, "use fast manifest-based check (no SQLite)")
}

func runFullCheck(cmd *cobra.Command, path string) error {
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
}

func runManifestCheck(cmd *cobra.Command, path string) error {
	result, err := check.RunManifest(cmd.Context(), path)
	if err != nil {
		return fmt.Errorf("manifest check failed: %w", err)
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
		fmt.Fprint(out, check.FormatManifestResult(result))
	default:
		return fmt.Errorf("unknown format %q: use \"text\" or \"json\"", checkFormat)
	}

	if result.HasAffected {
		return fmt.Errorf("documentation may need updating: %d doc(s) affected",
			len(result.AffectedDocs))
	}
	return nil
}

func runUpdateManifest(cmd *cobra.Command, path string) error {
	manifest, err := check.GenerateManifest(path)
	if err != nil {
		return fmt.Errorf("generate manifest: %w", err)
	}

	if err := check.SaveManifest(path, manifest); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Manifest updated: %d entries written to %s\n",
		len(manifest.Entries), check.ManifestFileName)
	return nil
}
