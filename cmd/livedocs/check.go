package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/check"
	"github.com/live-docs/live_docs/drift"
	"github.com/live-docs/live_docs/semantic"
	"github.com/live-docs/live_docs/sourcegraph"
)

var (
	checkFormat         string
	checkUpdateManifest bool
	checkManifest       bool
	checkSemantic       bool
	checkCrossRepo      bool
	checkDocMap         string
	checkDocsDir        string
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

		// Mode: cross-repo semantic check
		if checkCrossRepo {
			return runCrossRepoCheck(cmd, path)
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
	checkCmd.Flags().BoolVar(&checkSemantic, "semantic", false, "run semantic drift detection via Sourcegraph deepsearch")
	checkCmd.Flags().BoolVar(&checkCrossRepo, "cross-repo", false, "run cross-repo semantic drift detection using doc-map")
	checkCmd.Flags().StringVar(&checkDocMap, "doc-map", "", "path to doc-map.yaml (required with --cross-repo)")
	checkCmd.Flags().StringVar(&checkDocsDir, "docs-dir", "", "directory containing documentation files (for --cross-repo)")
}

func runFullCheck(cmd *cobra.Command, path string) error {
	result, err := check.Run(cmd.Context(), path)
	if err != nil {
		return fmt.Errorf("check failed: %w", err)
	}

	// Optional semantic pass: validate README descriptions against code intent.
	if checkSemantic {
		runSemanticPass(cmd, result, path)
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

// runSemanticPass creates a SourcegraphClient and SemanticChecker, then runs
// semantic validation on each report's README. Findings are appended to the
// result. On any setup error the pass is skipped with a warning.
func runSemanticPass(cmd *cobra.Command, result *check.Result, path string) {
	sgClient, err := sourcegraph.NewSourcegraphClient()
	if err != nil {
		log.Printf("semantic: skipping semantic check: %v", err)
		return
	}
	defer sgClient.Close()

	checker := drift.NewSemanticChecker(sgClient)
	ctx := cmd.Context()

	for _, report := range result.Reports {
		findings, err := checker.Check(ctx, report.ReadmePath, report.PackageDir, path)
		if err != nil {
			log.Printf("semantic: error checking %s: %v", report.ReadmePath, err)
			continue
		}
		if len(findings) > 0 {
			report.Findings = append(report.Findings, findings...)
			result.HasDrift = true
		}
	}
}

// runCrossRepoCheck runs cross-repo semantic drift detection using a doc-map
// to validate documentation against code in remote repositories.
func runCrossRepoCheck(cmd *cobra.Command, _ string) error {
	if checkDocMap == "" {
		return fmt.Errorf("--doc-map is required with --cross-repo")
	}

	docMap, err := drift.LoadDocMap(checkDocMap)
	if err != nil {
		return err
	}

	// Create Sourcegraph MCP client for code search.
	sgClient, err := sourcegraph.NewSourcegraphClient()
	if err != nil {
		return fmt.Errorf("create sourcegraph client: %w", err)
	}
	defer sgClient.Close()

	// Create LLM client for verification.
	// Priority: claude CLI (uses OAuth) > ANTHROPIC_API_KEY > Sourcegraph deepsearch.
	llm, llmSource := resolveLLMClient(sgClient)
	log.Printf("cross-repo: using %s for LLM verification", llmSource)

	searcher := &sourcegraphCodeSearcher{caller: sgClient}
	checker := drift.NewCrossRepoChecker(llm, searcher, docMap)

	ctx := cmd.Context()
	reports, err := checker.CheckAllDocs(ctx, checkDocsDir)
	if err != nil {
		return fmt.Errorf("cross-repo check: %w", err)
	}

	out := cmd.OutOrStdout()
	hasDrift := false

	switch checkFormat {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reports); err != nil {
			return fmt.Errorf("encode JSON: %w", err)
		}
	case "text":
		for _, r := range reports {
			fmt.Fprint(out, drift.FormatCrossRepoReport(r))
		}
	default:
		return fmt.Errorf("unknown format %q", checkFormat)
	}

	for _, r := range reports {
		if r.Stale > 0 {
			hasDrift = true
			break
		}
	}
	if hasDrift {
		totalStale := 0
		for _, r := range reports {
			totalStale += r.Stale
		}
		return fmt.Errorf("cross-repo drift detected: %d stale section(s) across %d doc(s)", totalStale, len(reports))
	}
	return nil
}

// sourcegraphCodeSearcher adapts the Sourcegraph MCP client to the
// drift.CodeSearcher interface.
type sourcegraphCodeSearcher struct {
	caller sourcegraph.MCPCaller
}

func (s *sourcegraphCodeSearcher) Search(ctx context.Context, repo, query string) (string, error) {
	return s.caller.CallTool(ctx, "keyword_search", map[string]any{
		"repos": repo,
		"query": query,
	})
}

// resolveLLMClient picks the best available LLM client for verification.
// Priority: claude CLI (uses existing OAuth) > ANTHROPIC_API_KEY > Sourcegraph deepsearch.
func resolveLLMClient(sgFallback semantic.LLMClient) (semantic.LLMClient, string) {
	// 1. Claude CLI — uses OAuth, no extra API key needed.
	if cli, err := semantic.NewClaudeCLIClient("haiku"); err == nil {
		return cli, "claude CLI (OAuth)"
	}

	// 2. Direct Anthropic API — explicit API key.
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		if client, err := semantic.NewAnthropicClient(apiKey); err == nil {
			return client, "Anthropic API (ANTHROPIC_API_KEY)"
		}
	}

	// 3. Sourcegraph deepsearch — always available if SRC_ACCESS_TOKEN is set.
	return sgFallback, "Sourcegraph deepsearch"
}
