package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/anchor"
	"github.com/live-docs/live_docs/cache"
	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/drift"
	"github.com/live-docs/live_docs/extractor"
	"github.com/live-docs/live_docs/extractor/goextractor"
	"github.com/live-docs/live_docs/gitdiff"
	"github.com/live-docs/live_docs/pipeline"
)

var diffFormat string

var diffCmd = &cobra.Command{
	Use:   "diff <from-commit> <to-commit> [repo-path]",
	Short: "Show documentation impact of code changes between two commits",
	Long: `Runs the full live docs pipeline:

1. Detects changed files between two commits (git diff)
2. Extracts symbols from changed files (tree-sitter + go/packages)
3. Finds markdown docs that reference changed symbols
4. Reports which docs are now stale and which symbols are undocumented

This is the core "code changed → docs affected" flow.`,
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		fromCommit := args[0]
		toCommit := args[1]
		repoPath := "."
		if len(args) > 2 {
			repoPath = args[2]
		}

		absRepo, err := filepath.Abs(repoPath)
		if err != nil {
			return fmt.Errorf("resolve repo path: %w", err)
		}

		return runDiff(cmd, absRepo, fromCommit, toCommit)
	},
}

func init() {
	diffCmd.Flags().StringVar(&diffFormat, "format", "text", "output format: text or json")
}

// DiffReport is the output of the diff command.
type DiffReport struct {
	FromCommit     string        `json:"from_commit"`
	ToCommit       string        `json:"to_commit"`
	FilesChanged   int           `json:"files_changed"`
	FilesExtracted int           `json:"files_extracted"`
	FilesDeleted   int           `json:"files_deleted"`
	CacheHits      int           `json:"cache_hits"`
	ClaimsStored   int           `json:"claims_stored"`
	Duration       string        `json:"duration"`
	ChangedFiles   []string      `json:"changed_files"`
	DeletedFiles   []string      `json:"deleted_files"`
	AffectedDocs   []AffectedDoc `json:"affected_docs"`
	Errors         []string      `json:"errors,omitempty"`
}

// AffectedDoc describes a documentation file impacted by code changes.
type AffectedDoc struct {
	Path                string   `json:"path"`
	StaleRefs           []string `json:"stale_refs,omitempty"`
	UndocumentedExports []string `json:"undocumented_exports,omitempty"`
	StalePackages       []string `json:"stale_packages,omitempty"`
}

func runDiff(cmd *cobra.Command, repoDir, fromCommit, toCommit string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	// 1. Get the diff to know which files changed.
	changes, err := gitdiff.DiffBetween(repoDir, fromCommit, toCommit)
	if err != nil {
		return fmt.Errorf("git diff: %w", err)
	}

	changedPaths := gitdiff.ChangedPaths(changes)
	deletedPaths := gitdiff.DeletedPaths(changes)

	if len(changedPaths) == 0 && len(deletedPaths) == 0 {
		fmt.Fprintln(out, "No changes between commits.")
		return nil
	}

	// 2. Set up the pipeline infrastructure.
	repoName := filepath.Base(repoDir)

	// Use in-memory SQLite for claims and cache (ephemeral per run).
	claimsDB, err := db.OpenClaimsDB(":memory:")
	if err != nil {
		return fmt.Errorf("open claims db: %w", err)
	}
	defer claimsDB.Close()

	if err := claimsDB.CreateSchema(); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	cacheStore, err := cache.NewSQLiteStore(":memory:", 2*1024*1024*1024)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	defer cacheStore.Close()

	// 3. Register extractors.
	registry := extractor.NewRegistry()
	goDeep := &goextractor.GoDeepExtractor{Repo: repoName}
	registry.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		DeepExtractor: goDeep,
	})

	// 4. Run the pipeline.
	p := pipeline.New(pipeline.Config{
		Repo:     repoName,
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: registry,
	})

	result, err := p.Run(ctx, fromCommit, toCommit)
	if err != nil {
		return fmt.Errorf("pipeline: %w", err)
	}

	// 5. Build anchor index from extracted claims and detect stale anchors.
	var anchorIndex *anchor.Index
	if result.ClaimsStored > 0 {
		anchorIndex = anchor.NewIndex()
		// Get all claims from the DB and build anchors.
		for _, changedFile := range changedPaths {
			claims, err := claimsDB.GetClaimsByFile(changedFile)
			if err != nil {
				continue
			}
			for _, cl := range claims {
				anchorIndex.Add(anchor.Anchor{
					ClaimID:   cl.ID,
					File:      cl.SourceFile,
					StartLine: cl.SourceLine,
					EndLine:   cl.SourceLine,
				})
			}
		}
	}

	// 6. Find documentation files affected by the changed code.
	//    Look for markdown files near changed directories.
	affectedDocs := findAffectedDocs(repoDir, changedPaths, deletedPaths)

	// 7. Build the report.
	report := DiffReport{
		FromCommit:     fromCommit,
		ToCommit:       toCommit,
		FilesChanged:   result.FilesChanged,
		FilesExtracted: result.FilesExtracted,
		FilesDeleted:   result.FilesDeleted,
		CacheHits:      result.CacheHits,
		ClaimsStored:   result.ClaimsStored,
		Duration:       result.Duration.String(),
		ChangedFiles:   changedPaths,
		DeletedFiles:   deletedPaths,
		AffectedDocs:   affectedDocs,
	}
	for _, e := range result.Errors {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", e.Path, e.Err))
	}

	// 8. Output.
	switch diffFormat {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case "text":
		return formatDiffText(out, report)
	default:
		return fmt.Errorf("unknown format %q", diffFormat)
	}
}

// findAffectedDocs searches for markdown documentation files in or near the
// directories containing changed code files, then runs drift detection on them.
func findAffectedDocs(repoDir string, changedPaths, deletedPaths []string) []AffectedDoc {
	// Collect unique directories that contain changed files.
	dirs := make(map[string]bool)
	for _, p := range changedPaths {
		dir := filepath.Dir(p)
		dirs[dir] = true
		// Also check parent dir (docs often live one level up).
		parent := filepath.Dir(dir)
		if parent != "." && parent != "" {
			dirs[parent] = true
		}
	}
	for _, p := range deletedPaths {
		dirs[filepath.Dir(p)] = true
	}

	// Find markdown files in those directories.
	var results []AffectedDoc
	seenDocs := make(map[string]bool)

	for dir := range dirs {
		absDir := filepath.Join(repoDir, dir)
		entries, err := os.ReadDir(absDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			lower := strings.ToLower(name)
			if !strings.HasSuffix(lower, ".md") {
				continue
			}
			// Focus on documentation files, not changelogs etc.
			if lower == "changelog.md" || lower == "changes.md" || lower == "history.md" {
				continue
			}

			docPath := filepath.Join(dir, name)
			if seenDocs[docPath] {
				continue
			}
			seenDocs[docPath] = true

			// Run drift detection on this doc vs its code directory.
			absDocPath := filepath.Join(repoDir, docPath)
			codeDir := filepath.Join(repoDir, dir)
			finding, driftErr := drift.Detect(absDocPath, codeDir)
			if driftErr != nil || finding == nil {
				continue
			}
			if finding.StaleCount > 0 || finding.UndocumentedCount > 0 || finding.StalePackageCount > 0 {
				doc := AffectedDoc{
					Path: docPath,
				}
				for _, f := range finding.Findings {
					switch f.Kind {
					case drift.StaleReference:
						doc.StaleRefs = append(doc.StaleRefs, f.Symbol)
					case drift.Undocumented:
						doc.UndocumentedExports = append(doc.UndocumentedExports, f.Symbol)
					case drift.StalePackageRef:
						doc.StalePackages = append(doc.StalePackages, f.Symbol)
					}
				}
				results = append(results, doc)
			}
		}
	}

	return results
}

func formatDiffText(out io.Writer, r DiffReport) error {
	fmt.Fprintf(out, "# Live Docs Diff: %s..%s\n\n", short(r.FromCommit), short(r.ToCommit))

	fmt.Fprintf(out, "## Pipeline Summary\n\n")
	fmt.Fprintf(out, "- **Files changed**: %d\n", r.FilesChanged)
	fmt.Fprintf(out, "- **Files extracted**: %d (symbols parsed)\n", r.FilesExtracted)
	fmt.Fprintf(out, "- **Files deleted**: %d\n", r.FilesDeleted)
	fmt.Fprintf(out, "- **Cache hits**: %d\n", r.CacheHits)
	fmt.Fprintf(out, "- **Claims stored**: %d\n", r.ClaimsStored)
	fmt.Fprintf(out, "- **Duration**: %s\n", r.Duration)

	if len(r.ChangedFiles) > 0 {
		fmt.Fprintf(out, "\n## Changed Files (%d)\n\n", len(r.ChangedFiles))
		for _, f := range r.ChangedFiles {
			fmt.Fprintf(out, "- `%s`\n", f)
		}
	}

	if len(r.DeletedFiles) > 0 {
		fmt.Fprintf(out, "\n## Deleted Files (%d)\n\n", len(r.DeletedFiles))
		for _, f := range r.DeletedFiles {
			fmt.Fprintf(out, "- `%s`\n", f)
		}
	}

	if len(r.AffectedDocs) > 0 {
		fmt.Fprintf(out, "\n## Affected Documentation (%d files)\n\n", len(r.AffectedDocs))
		for _, doc := range r.AffectedDocs {
			fmt.Fprintf(out, "### %s\n\n", doc.Path)
			if len(doc.StaleRefs) > 0 {
				fmt.Fprintf(out, "**Stale references** (in doc, not in code):\n")
				for _, ref := range doc.StaleRefs {
					fmt.Fprintf(out, "- `%s`\n", ref)
				}
			}
			if len(doc.UndocumentedExports) > 0 {
				fmt.Fprintf(out, "**Undocumented exports** (in code, not in doc):\n")
				limit := len(doc.UndocumentedExports)
				if limit > 20 {
					limit = 20
				}
				for _, exp := range doc.UndocumentedExports[:limit] {
					fmt.Fprintf(out, "- `%s`\n", exp)
				}
				if len(doc.UndocumentedExports) > 20 {
					fmt.Fprintf(out, "- ... and %d more\n", len(doc.UndocumentedExports)-20)
				}
			}
			if len(doc.StalePackages) > 0 {
				fmt.Fprintf(out, "**Stale package references**:\n")
				for _, pkg := range doc.StalePackages {
					fmt.Fprintf(out, "- `%s`\n", pkg)
				}
			}
			fmt.Fprintln(out)
		}
	} else {
		fmt.Fprintf(out, "\n## Affected Documentation\n\nNo documentation files affected by these changes.\n")
	}

	if len(r.Errors) > 0 {
		fmt.Fprintf(out, "\n## Errors (%d)\n\n", len(r.Errors))
		for _, e := range r.Errors {
			fmt.Fprintf(out, "- %s\n", e)
		}
	}

	return nil
}

func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}
