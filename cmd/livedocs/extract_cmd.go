package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/cache"
	"github.com/live-docs/live_docs/config"
	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
	"github.com/live-docs/live_docs/extractor/goextractor"
	"github.com/live-docs/live_docs/extractor/lang"
	"github.com/live-docs/live_docs/extractor/treesitter"
	"github.com/live-docs/live_docs/extractor/tribal"
	"github.com/live-docs/live_docs/gitdiff"
	"github.com/live-docs/live_docs/pipeline"
	"github.com/live-docs/live_docs/semantic"
	"github.com/live-docs/live_docs/sourcegraph"
)

var (
	extractSource                 string
	extractRepo                   string
	extractOutput                 string
	extractTier2                  bool
	extractTribal                 string
	extractDataDir                string
	extractFromRev                string
	extractToRev                  string
	extractConfirm                bool
	extractConcurrency            int
	extractAcceptUnknownGhVersion bool
	extractForceRemine            bool
	extractMaxFiles               int
)

// newLLMClient creates the LLM client for semantic extraction.
// Overridable in tests to inject a mock.
var newLLMClient = func(apiKey string) (semantic.LLMClient, error) {
	return semantic.NewAnthropicClient(apiKey)
}

// confidenceThreshold is the minimum confidence for semantic claims.
const confidenceThreshold = 0.7

var extractCmd = &cobra.Command{
	Use:   "extract [path]",
	Short: "Extract claims from a repository into a SQLite database",
	Long: `Walks all source files in the given repository path and extracts
structural claims using language-specific extractors:

  - Go files (.go): deep extractor using go/packages and go/types
  - TypeScript files (.ts, .tsx): tree-sitter extractor
  - Python files (.py): tree-sitter extractor
  - Shell files (.sh): tree-sitter extractor

Creates a per-repo SQLite database containing symbols and claims.

Use --source clone --repo <url> to shallow-clone a remote repository
before extraction. The clone is cleaned up after extraction completes.

Use --source sourcegraph --repo <repo> --data-dir <dir> to extract claims
from a remote repository via Sourcegraph MCP. Requires SRC_ACCESS_TOKEN.
Supports --from-rev and --to-rev for incremental extraction. Without
revision flags, estimates cost and requires --confirm to proceed.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch extractSource {
		case "", "local":
			return runExtractLocal(cmd, args)
		case "clone":
			return runExtractClone(cmd)
		case "sourcegraph":
			return runExtractSourcegraph(cmd)
		default:
			return fmt.Errorf("unknown --source value: %q (valid: local, clone, sourcegraph)", extractSource)
		}
	},
}

func runExtractLocal(cmd *cobra.Command, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("local extraction requires a path argument")
	}
	repoPath := args[0]
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("resolve repo path: %w", err)
	}

	repoName := extractRepo
	if repoName == "" {
		repoName = filepath.Base(absRepo)
	}

	return runExtract(cmd.Context(), cmd, absRepo, repoName, extractOutput)
}

func runExtractClone(cmd *cobra.Command) error {
	if extractRepo == "" {
		return fmt.Errorf("--repo is required when --source clone is used")
	}

	repoURL := extractRepo
	repoName := repoNameFromURL(repoURL)

	tmpDir, err := os.MkdirTemp("", "livedocs-clone-*")
	if err != nil {
		return fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	fmt.Fprintf(cmd.OutOrStdout(), "Cloning %s (depth=1) into temp directory...\n", repoURL)

	gitCmd := exec.CommandContext(cmd.Context(), "git", "clone", "--depth=1", repoURL, tmpDir)
	gitCmd.Stdout = cmd.OutOrStdout()
	gitCmd.Stderr = cmd.ErrOrStderr()
	if err := gitCmd.Run(); err != nil {
		return fmt.Errorf("git clone --depth=1 %s: %w", repoURL, err)
	}

	return runExtract(cmd.Context(), cmd, tmpDir, repoName, extractOutput)
}

// repoNameFromURL extracts a repository name from a URL.
// e.g. "https://github.com/org/repo.git" -> "repo"
func repoNameFromURL(rawURL string) string {
	// Strip trailing slashes and .git suffix.
	name := strings.TrimRight(rawURL, "/")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSuffix(name, ".git")
	if name == "" {
		return "unknown"
	}
	return name
}

func init() {
	extractCmd.Flags().StringVar(&extractSource, "source", "", "extraction source: local (default), clone, sourcegraph")
	extractCmd.Flags().StringVar(&extractRepo, "repo", "", "repository name or URL (URL when --source clone)")
	extractCmd.Flags().StringVarP(&extractOutput, "output", "o", "", "output SQLite file path (default: <repo>.claims.db)")
	extractCmd.Flags().BoolVar(&extractTier2, "tier2", false, "generate Tier 2 semantic claims via LLM (requires ANTHROPIC_API_KEY)")
	tribalFlag := extractCmd.Flags().VarPF(&tribalFlagValue{val: &extractTribal}, "tribal", "", "tribal knowledge extraction mode: deterministic (default when flag present), llm")
	tribalFlag.NoOptDefVal = "deterministic"
	extractCmd.Flags().StringVar(&extractDataDir, "data-dir", "", "directory for output .claims.db (used with --source sourcegraph)")
	extractCmd.Flags().StringVar(&extractFromRev, "from-rev", "", "start revision for incremental extraction (used with --source sourcegraph)")
	extractCmd.Flags().StringVar(&extractToRev, "to-rev", "", "end revision for incremental extraction (used with --source sourcegraph)")
	extractCmd.Flags().BoolVar(&extractConfirm, "confirm", false, "confirm full extraction after cost estimate (used with --source sourcegraph)")
	extractCmd.Flags().IntVar(&extractConcurrency, "concurrency", 10, "max concurrent MCP calls (used with --source sourcegraph)")
	extractCmd.Flags().BoolVar(&extractAcceptUnknownGhVersion, "accept-unknown-gh-version", false, "accept a gh CLI version not in the advisory allowlist (used with --tribal=llm)")
	extractCmd.Flags().BoolVar(&extractForceRemine, "force-remine", false, "clear the stored PR cursor and re-mine every file from scratch (used with --tribal=llm)")
	extractCmd.Flags().IntVar(&extractMaxFiles, "max-files", 0, "override tribal.max_files_per_run: cap files processed by tribal mining per invocation (0 = use config default)")
}

// rankedFilesTrace is a package-level hook used by tests to observe which
// files the tribal mining pipeline selects. When non-nil, both tribal
// extraction paths invoke it with the ordered slice of relative paths
// they are about to process. It is never set in production code.
var rankedFilesTrace func([]string)

// skipDirs contains directory names to skip during file walking.
var skipDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	"_output":      true,
	"_build":       true,
	".cache":       true,
}

// resolveMaxFilesForTribal returns the cap on files processed by the tribal
// mining loop per invocation. If --max-files was set to a positive value it
// takes precedence; otherwise we fall back to cfg.Tribal.MaxFilesPerRun from
// the repo's .livedocs.yaml (which defaults to config.DefaultTribalMaxFilesPerRun).
func resolveMaxFilesForTribal(repoDir string) int {
	if extractMaxFiles > 0 {
		return extractMaxFiles
	}
	cfg, err := config.Load(config.ConfigPath(repoDir))
	if err != nil || cfg.Tribal.MaxFilesPerRun <= 0 {
		return config.DefaultTribalMaxFilesPerRun
	}
	return cfg.Tribal.MaxFilesPerRun
}

// seedSourceFilesForTribal walks repoDir and upserts a minimal source_files
// row for every candidate file the tribal extractors can process. This lets
// ClaimsDB.RankFilesForMining see every candidate even on a fresh DB that
// hasn't been through a full structural extraction pass. It does NOT touch
// claims or symbols — structural extraction (earlier phases of runExtract)
// is responsible for those.
func seedSourceFilesForTribal(ctx context.Context, claimsDB *db.ClaimsDB, repoDir, repoName string) error {
	return filepath.WalkDir(repoDir, func(path string, d os.DirEntry, wErr error) error {
		if wErr != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go", ".ts", ".tsx", ".py", ".sh":
			// supported
		default:
			return nil
		}
		relPath, relErr := filepath.Rel(repoDir, path)
		if relErr != nil {
			relPath = path
		}
		// Skip paths excluded by glob rules so the ranker never even
		// sees them. (The SQL layer also filters them, but skipping
		// here avoids inserting rows we'd only filter out later.)
		if db.ShouldSkipForMining(relPath) {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		contentHash := fmt.Sprintf("%x", sha256.Sum256(content))
		_, _ = claimsDB.UpsertSourceFile(db.SourceFile{
			Repo:             repoName,
			RelativePath:     relPath,
			ContentHash:      contentHash,
			ExtractorVersion: "tribal-seed",
			LastIndexed:      db.Now(),
		})
		return nil
	})
}

func runExtract(ctx context.Context, cmd *cobra.Command, repoDir, repoName, outputPath string) error {
	out := cmd.OutOrStdout()
	start := time.Now()

	// Determine output path.
	if outputPath == "" {
		outputPath = repoName + ".claims.db"
	}

	// Use atomic file replacement: extract into a temp file, then rename.
	// This prevents MCP readers from seeing an empty/missing DB during extraction.
	outputDir := filepath.Dir(outputPath)
	if outputDir == "" {
		outputDir = "."
	}
	tmpFile, err := os.CreateTemp(outputDir, filepath.Base(outputPath)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close() // Close so OpenClaimsDB can open it

	// Clean up temp file on failure; on success we rename it.
	extractOK := false
	defer func() {
		if !extractOK {
			os.Remove(tmpPath)
		}
	}()

	// Open claims DB at temp path.
	claimsDB, err := db.OpenClaimsDB(tmpPath)
	if err != nil {
		return fmt.Errorf("open claims db: %w", err)
	}
	defer claimsDB.Close()

	if err := claimsDB.CreateSchema(); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// Open in-memory cache.
	cacheStore, err := cache.NewSQLiteStore(":memory:", 2*1024*1024*1024)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	defer cacheStore.Close()

	// Set up extractor registry.
	registry := extractor.NewRegistry()

	// Register Go deep extractor.
	goDeep := &goextractor.GoDeepExtractor{Repo: repoName}
	registry.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		DeepExtractor: goDeep,
	})

	// Register tree-sitter extractor for non-Go languages.
	langRegistry := lang.NewRegistry()
	tsExtractor := treesitter.New(langRegistry)

	registry.Register(extractor.LanguageConfig{
		Language:          "typescript",
		Extensions:        []string{".ts", ".tsx"},
		TreeSitterGrammar: "tree-sitter-typescript",
		FastExtractor:     tsExtractor,
	})
	registry.Register(extractor.LanguageConfig{
		Language:          "python",
		Extensions:        []string{".py"},
		TreeSitterGrammar: "tree-sitter-python",
		FastExtractor:     tsExtractor,
	})
	registry.Register(extractor.LanguageConfig{
		Language:          "shell",
		Extensions:        []string{".sh"},
		TreeSitterGrammar: "tree-sitter-bash",
		FastExtractor:     tsExtractor,
	})

	var totalSymbols, totalClaims int
	var totalFiles, extractedFiles, skippedFiles, errorCount int

	// Phase 1: Run Go deep extractor on the whole repo.
	fmt.Fprintf(out, "Extracting Go symbols from %s...\n", repoDir)
	goClaims, goErr := goDeep.Extract(ctx, repoDir, "go")
	if goErr != nil {
		fmt.Fprintf(out, "Go deep extractor warning: %v\n", goErr)
	} else {
		stored, err := storeClaims(claimsDB, repoName, goClaims)
		if err != nil {
			return fmt.Errorf("store Go claims: %w", err)
		}
		totalClaims += stored
		fmt.Fprintf(out, "Go deep extractor: %d claims stored\n", stored)
	}

	// Phase 2: Walk non-Go files for tree-sitter extraction.
	fmt.Fprintf(out, "Walking repository for non-Go files...\n")
	err = filepath.WalkDir(repoDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip inaccessible entries
		}

		// Skip excluded directories.
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))

		// Skip Go files (handled by deep extractor) and unregistered extensions.
		if ext == ".go" {
			return nil
		}

		cfg := registry.LookupByExtension(ext)
		if cfg == nil {
			return nil
		}

		totalFiles++

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Extract claims.
		claims, extractErr := cfg.FastExtractor.Extract(ctx, path, cfg.Language)
		if extractErr != nil {
			errorCount++
			return nil // non-fatal
		}

		if len(claims) == 0 {
			skippedFiles++
			return nil
		}

		// Compute relative path for storage.
		relPath, relErr := filepath.Rel(repoDir, path)
		if relErr != nil {
			relPath = path
		}

		// Fill in repo and import path, then store.
		for i := range claims {
			if claims[i].SubjectRepo == "" {
				claims[i].SubjectRepo = repoName
			}
			if claims[i].SubjectImportPath == "" {
				claims[i].SubjectImportPath = relPath
			}
			claims[i].SourceFile = relPath
		}

		stored, storeErr := storeClaims(claimsDB, repoName, claims)
		if storeErr != nil {
			errorCount++
			return nil
		}

		totalClaims += stored
		extractedFiles++

		// Update source file record.
		content, readErr := os.ReadFile(path)
		if readErr == nil {
			contentHash := fmt.Sprintf("%x", sha256.Sum256(content))
			_, _ = claimsDB.UpsertSourceFile(db.SourceFile{
				Repo:             repoName,
				RelativePath:     relPath,
				ContentHash:      contentHash,
				ExtractorVersion: cfg.FastExtractor.Version(),
				GrammarVersion:   cfg.TreeSitterGrammar,
				LastIndexed:      db.Now(),
			})
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("walk repo: %w", err)
	}

	// Phase 3: Tier 2 semantic extraction (if requested).
	var semanticStored int
	var semanticFiltered int64
	if extractTier2 {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("ANTHROPIC_API_KEY environment variable is required for --tier2 semantic extraction")
		}

		fmt.Fprintf(out, "Running Tier 2 semantic extraction...\n")

		client, clientErr := newLLMClient(apiKey)
		if clientErr != nil {
			return fmt.Errorf("create LLM client: %w", clientErr)
		}

		gen, genErr := semantic.NewGenerator(claimsDB, client, repoName)
		if genErr != nil {
			return fmt.Errorf("create semantic generator: %w", genErr)
		}

		batchResult, batchErr := gen.GenerateBatchFromDB(ctx, -1)
		if batchErr != nil {
			return fmt.Errorf("semantic batch generation: %w", batchErr)
		}
		semanticStored = batchResult.TotalClaims

		// Confidence gate: remove semantic claims below threshold.
		deleted, delErr := claimsDB.DeleteLowConfidenceSemanticClaims(confidenceThreshold)
		if delErr != nil {
			return fmt.Errorf("confidence gate: %w", delErr)
		}
		semanticFiltered = deleted

		// Security filter: remove claims with sensitive content from semantic tier.
		sensitiveDeleted, sensErr := claimsDB.DeleteSensitiveClaims()
		if sensErr != nil {
			return fmt.Errorf("sensitive content filter: %w", sensErr)
		}
		semanticFiltered += sensitiveDeleted

		semanticStored -= int(semanticFiltered)

		fmt.Fprintf(out, "Semantic claims: %d stored, %d filtered (confidence < %.1f or sensitive)\n",
			semanticStored, semanticFiltered, confidenceThreshold)
	}

	// Phase 4: Tribal knowledge extraction (if requested).
	if extractTribal != "" {
		// Resolve the files-per-run cap from --max-files flag or config default.
		maxFiles := resolveMaxFilesForTribal(repoDir)

		switch extractTribal {
		case "deterministic":
			fmt.Fprintf(out, "Running tribal knowledge extraction (deterministic)...\n")
			if err := runTribalExtraction(ctx, out, claimsDB, repoDir, repoName, maxFiles); err != nil {
				return fmt.Errorf("tribal extraction: %w", err)
			}
		case "llm":
			// LLM mode is additive: run deterministic extractors first, then PR comment mining.
			fmt.Fprintf(out, "Running tribal knowledge extraction (deterministic)...\n")
			if err := runTribalExtraction(ctx, out, claimsDB, repoDir, repoName, maxFiles); err != nil {
				return fmt.Errorf("tribal extraction: %w", err)
			}
			fmt.Fprintf(out, "Running tribal knowledge extraction (LLM)...\n")
			if err := runLLMTribalExtraction(ctx, out, claimsDB, repoDir, repoName, maxFiles); err != nil {
				return fmt.Errorf("LLM tribal extraction: %w", err)
			}
		default:
			return fmt.Errorf("unknown --tribal value: %q (valid: deterministic, llm)", extractTribal)
		}
	}

	// Count symbols in DB.
	totalSymbols = countSymbols(claimsDB)

	duration := time.Since(start)

	// Print summary.
	fmt.Fprintf(out, "\n## Extract Summary\n\n")
	fmt.Fprintf(out, "- **Repository**: %s\n", repoName)
	fmt.Fprintf(out, "- **Path**: %s\n", repoDir)
	fmt.Fprintf(out, "- **Output**: %s\n", outputPath)
	fmt.Fprintf(out, "- **Symbols**: %d\n", totalSymbols)
	fmt.Fprintf(out, "- **Claims**: %d\n", totalClaims)
	fmt.Fprintf(out, "- **Non-Go files extracted**: %d\n", extractedFiles)
	fmt.Fprintf(out, "- **Files skipped**: %d\n", skippedFiles)
	fmt.Fprintf(out, "- **Errors**: %d\n", errorCount)
	if extractTier2 {
		fmt.Fprintf(out, "- **Semantic claims stored**: %d\n", semanticStored)
		fmt.Fprintf(out, "- **Semantic claims filtered**: %d\n", semanticFiltered)
	}
	fmt.Fprintf(out, "- **Duration**: %s\n", duration.Round(time.Millisecond))

	// Store extraction metadata with repo root path.
	if err := claimsDB.SetExtractionMeta(db.ExtractionMeta{
		ExtractedAt: db.Now(),
		RepoRoot:    repoDir,
	}); err != nil {
		return fmt.Errorf("set extraction meta: %w", err)
	}

	// Close DB before rename so all data is flushed.
	claimsDB.Close()

	// Atomically replace the output file.
	if err := os.Rename(tmpPath, outputPath); err != nil {
		return fmt.Errorf("atomic rename %s -> %s: %w", tmpPath, outputPath, err)
	}
	extractOK = true

	return nil
}

// sgToolLister satisfies pipeline.ToolLister for the Sourcegraph MCP server.
// This returns a hardcoded list because the MCP subprocess does not support
// tool enumeration via the current client. Actual tool availability is
// validated on first use — if a tool is missing, CallTool returns an error.
type sgToolLister struct{}

func (sgToolLister) ListTools(_ context.Context) ([]string, error) {
	return []string{"read_file", "list_files", "compare_revisions"}, nil
}

// sgCostPerCall is the estimated cost per Sourcegraph MCP call for cost estimation.
const sgCostPerCall = 0.003

// sgSecondsPerCall is the estimated wall-clock time per MCP call.
const sgSecondsPerCall = 0.5

func runExtractSourcegraph(cmd *cobra.Command) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	// Validate required inputs.
	if os.Getenv("SRC_ACCESS_TOKEN") == "" {
		return fmt.Errorf("SRC_ACCESS_TOKEN environment variable is required for --source sourcegraph")
	}
	if extractRepo == "" {
		return fmt.Errorf("--repo is required when --source sourcegraph is used")
	}
	if extractDataDir == "" {
		return fmt.Errorf("--data-dir is required when --source sourcegraph is used")
	}

	// Derive repo name for the DB file (last path component).
	repoName := extractRepo
	if idx := strings.LastIndex(repoName, "/"); idx >= 0 {
		repoName = repoName[idx+1:]
	}

	// Create Sourcegraph MCP client.
	sgClient, err := sourcegraph.NewSourcegraphClient()
	if err != nil {
		return fmt.Errorf("create sourcegraph client: %w", err)
	}
	defer sgClient.Close()

	// Create SourcegraphFileSource with concurrency control.
	fileSource, err := pipeline.NewSourcegraphFileSource(sgClient, sgToolLister{}, pipeline.WithConcurrency(extractConcurrency))
	if err != nil {
		return fmt.Errorf("create sourcegraph file source: %w", err)
	}

	// Determine extraction mode: incremental (--from-rev/--to-rev) or full.
	isIncremental := extractFromRev != "" || extractToRev != ""

	var changes []gitdiff.FileChange

	if !isIncremental {
		// Full extraction: list all files, estimate cost, require --confirm.
		fmt.Fprintf(out, "Listing files in %s...\n", extractRepo)
		files, err := fileSource.ListFiles(ctx, extractRepo, "", "*")
		if err != nil {
			return fmt.Errorf("list files: %w", err)
		}

		fileCount := len(files)
		estimatedCalls := fileCount // 1 read_file call per file
		estimatedCost := float64(estimatedCalls) * sgCostPerCall
		estimatedTime := float64(estimatedCalls) * sgSecondsPerCall / float64(extractConcurrency)

		fmt.Fprintf(out, "\nFull Extraction Cost Estimate\n")
		fmt.Fprintf(out, "============================\n")
		fmt.Fprintf(out, "  Repository:      %s\n", extractRepo)
		fmt.Fprintf(out, "  Files:           %d\n", fileCount)
		fmt.Fprintf(out, "  MCP calls:       %d\n", estimatedCalls)
		fmt.Fprintf(out, "  Concurrency:     %d\n", extractConcurrency)
		fmt.Fprintf(out, "  Estimated cost:  $%.2f\n", estimatedCost)
		fmt.Fprintf(out, "  Estimated time:  %.0fs\n", estimatedTime)

		if !extractConfirm {
			fmt.Fprintf(out, "\nRun with --confirm to proceed with extraction.\n")
			return nil
		}

		// Build synthetic FileChanges (all added).
		changes = make([]gitdiff.FileChange, len(files))
		for i, f := range files {
			changes[i] = gitdiff.FileChange{Status: gitdiff.StatusAdded, Path: f}
		}
	}

	// Set up output path.
	outputPath := filepath.Join(extractDataDir, repoName+".claims.db")

	// Ensure data-dir exists.
	if err := os.MkdirAll(extractDataDir, 0o755); err != nil {
		return fmt.Errorf("create data-dir: %w", err)
	}

	// Use atomic file replacement.
	tmpFile, err := os.CreateTemp(extractDataDir, repoName+".claims.db.tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	extractOK := false
	defer func() {
		if !extractOK {
			os.Remove(tmpPath)
		}
	}()

	// Open claims DB.
	claimsDB, err := db.OpenClaimsDB(tmpPath)
	if err != nil {
		return fmt.Errorf("open claims db: %w", err)
	}
	defer claimsDB.Close()

	if err := claimsDB.CreateSchema(); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// Open in-memory cache.
	cacheStore, err := cache.NewSQLiteStore(":memory:", 2*1024*1024*1024)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	defer cacheStore.Close()

	// Set up extractor registry (tree-sitter only; Go deep extractor requires local FS).
	registry := extractor.NewRegistry()
	langRegistry := lang.NewRegistry()
	tsExtractor := treesitter.New(langRegistry)

	registry.Register(extractor.LanguageConfig{
		Language:          "go",
		Extensions:        []string{".go"},
		FastExtractor:     tsExtractor,
		TreeSitterGrammar: "tree-sitter-go",
	})
	registry.Register(extractor.LanguageConfig{
		Language:          "typescript",
		Extensions:        []string{".ts", ".tsx"},
		TreeSitterGrammar: "tree-sitter-typescript",
		FastExtractor:     tsExtractor,
	})
	registry.Register(extractor.LanguageConfig{
		Language:          "python",
		Extensions:        []string{".py"},
		TreeSitterGrammar: "tree-sitter-python",
		FastExtractor:     tsExtractor,
	})
	registry.Register(extractor.LanguageConfig{
		Language:          "shell",
		Extensions:        []string{".sh"},
		TreeSitterGrammar: "tree-sitter-bash",
		FastExtractor:     tsExtractor,
	})

	// Run the pipeline.
	start := time.Now()
	p := pipeline.New(pipeline.Config{
		Repo:       extractRepo,
		RepoDir:    "",
		Cache:      cacheStore,
		ClaimsDB:   claimsDB,
		Registry:   registry,
		FileSource: fileSource,
	})

	// For full extraction, wrap the FileSource so DiffBetween returns the
	// pre-fetched file list instead of making a second compare_revisions call.
	if !isIncremental {
		p = pipeline.New(pipeline.Config{
			Repo:       extractRepo,
			RepoDir:    "",
			Cache:      cacheStore,
			ClaimsDB:   claimsDB,
			Registry:   registry,
			FileSource: &prefetchedDiffSource{inner: fileSource, changes: changes},
		})
	}

	var result pipeline.Result
	if isIncremental {
		fromRev := extractFromRev
		toRev := extractToRev
		if toRev == "" {
			toRev = "HEAD"
		}
		fmt.Fprintf(out, "Running incremental extraction %s..%s\n", fromRev, toRev)
		result, err = p.Run(ctx, fromRev, toRev)
	} else {
		fmt.Fprintf(out, "\nRunning full extraction (%d files)...\n", len(changes))
		result, err = p.Run(ctx, "", "")
	}
	if err != nil {
		return fmt.Errorf("pipeline run: %w", err)
	}

	duration := time.Since(start)

	// Print summary.
	fmt.Fprintf(out, "\n## Sourcegraph Extract Summary\n\n")
	fmt.Fprintf(out, "- **Repository**: %s\n", extractRepo)
	fmt.Fprintf(out, "- **Output**: %s\n", outputPath)
	fmt.Fprintf(out, "- **Files changed**: %d\n", result.FilesChanged)
	fmt.Fprintf(out, "- **Files extracted**: %d\n", result.FilesExtracted)
	fmt.Fprintf(out, "- **Files skipped**: %d\n", result.FilesSkipped)
	fmt.Fprintf(out, "- **Files deleted**: %d\n", result.FilesDeleted)
	fmt.Fprintf(out, "- **Cache hits**: %d\n", result.CacheHits)
	fmt.Fprintf(out, "- **Claims stored**: %d\n", result.ClaimsStored)
	fmt.Fprintf(out, "- **Errors**: %d\n", len(result.Errors))
	fmt.Fprintf(out, "- **Duration**: %s\n", duration.Round(time.Millisecond))

	for _, fe := range result.Errors {
		fmt.Fprintf(out, "  - %s: %v\n", fe.Path, fe.Err)
	}

	// Store extraction metadata.
	if err := claimsDB.SetExtractionMeta(db.ExtractionMeta{
		ExtractedAt: db.Now(),
		RepoRoot:    extractRepo,
	}); err != nil {
		return fmt.Errorf("set extraction meta: %w", err)
	}

	// Close DB before rename.
	claimsDB.Close()

	if err := os.Rename(tmpPath, outputPath); err != nil {
		return fmt.Errorf("atomic rename %s -> %s: %w", tmpPath, outputPath, err)
	}
	extractOK = true

	return nil
}

// storeClaims upserts symbols and inserts claims from a slice of extractor claims.
// Claims with sensitive content in ObjectText are filtered out before storage.
// Returns the number of claims stored.
func storeClaims(claimsDB *db.ClaimsDB, repoName string, claims []extractor.Claim) (int, error) {
	// Filter out claims containing sensitive content.
	claims = extractor.FilterSensitiveClaims(claims)

	stored := 0
	err := claimsDB.RunInTransaction(func() error {
		for _, claim := range claims {
			if claim.SubjectRepo == "" {
				claim.SubjectRepo = repoName
			}

			vis := string(claim.Visibility)
			if vis == "" {
				vis = "public"
			}

			kind := string(claim.Kind)
			if kind == "" {
				kind = "var"
			}

			symID, err := claimsDB.UpsertSymbol(db.Symbol{
				Repo:        claim.SubjectRepo,
				ImportPath:  claim.SubjectImportPath,
				SymbolName:  claim.SubjectName,
				Language:    claim.Language,
				Kind:        kind,
				Visibility:  vis,
				DisplayName: claim.SubjectName,
				SCIPSymbol:  claim.SCIPSymbol,
			})
			if err != nil {
				return fmt.Errorf("upsert symbol %s: %w", claim.SubjectName, err)
			}

			_, err = claimsDB.InsertClaim(db.Claim{
				SubjectID:        symID,
				Predicate:        string(claim.Predicate),
				ObjectText:       claim.ObjectText,
				SourceFile:       claim.SourceFile,
				SourceLine:       claim.SourceLine,
				Confidence:       claim.Confidence,
				ClaimTier:        string(claim.ClaimTier),
				Extractor:        claim.Extractor,
				ExtractorVersion: claim.ExtractorVersion,
				LastVerified:     db.Now(),
			})
			if err != nil {
				return fmt.Errorf("insert claim for %s: %w", claim.SubjectName, err)
			}
			stored++
		}
		return nil
	})
	return stored, err
}

// prefetchedDiffSource wraps a FileSource and overrides DiffBetween to return
// a pre-fetched list of file changes. This avoids a redundant compare_revisions
// call when the file list has already been fetched via ListFiles for cost estimation.
type prefetchedDiffSource struct {
	inner   pipeline.FileSource
	changes []gitdiff.FileChange
}

func (p *prefetchedDiffSource) ReadFile(ctx context.Context, repo, revision, path string) ([]byte, error) {
	return p.inner.ReadFile(ctx, repo, revision, path)
}

func (p *prefetchedDiffSource) ListFiles(ctx context.Context, repo, revision, pattern string) ([]string, error) {
	return p.inner.ListFiles(ctx, repo, revision, pattern)
}

func (p *prefetchedDiffSource) DiffBetween(_ context.Context, _, _, _ string) ([]gitdiff.FileChange, error) {
	return p.changes, nil
}

// countSymbols returns the total number of symbols in the database.
func countSymbols(claimsDB *db.ClaimsDB) int {
	// Use the DB's exported methods to count.
	// Since there's no direct Count method, search with wildcard.
	symbols, err := claimsDB.SearchSymbolsByName("%")
	if err != nil {
		return 0
	}
	return len(symbols)
}

// tribalFlagValue implements pflag.Value for the --tribal flag.
type tribalFlagValue struct {
	val *string
}

func (t *tribalFlagValue) String() string {
	if t.val == nil {
		return ""
	}
	return *t.val
}

func (t *tribalFlagValue) Set(s string) error {
	switch s {
	case "deterministic", "llm":
		*t.val = s
		return nil
	default:
		return fmt.Errorf("invalid --tribal value: %q (valid: deterministic, llm)", s)
	}
}

func (t *tribalFlagValue) Type() string { return "string" }

// runTribalExtraction runs all Phase 1 deterministic tribal extractors on the
// extracted repository and inserts facts into the claims database.
//
// File selection uses ClaimsDB.RankFilesForMining to prioritize never-mined
// and high-surface files, capped at maxFiles. Source_files are seeded from
// disk first so fresh DBs have a ranking target.
func runTribalExtraction(ctx context.Context, out io.Writer, claimsDB *db.ClaimsDB, repoDir, repoName string, maxFiles int) error {
	// Ensure tribal schema exists.
	if err := claimsDB.CreateTribalSchema(); err != nil {
		return fmt.Errorf("create tribal schema: %w", err)
	}

	var ownershipCount, rationaleCount, markerCount, blameCount int

	// 1. Codeowners extraction.
	n, err := tribal.ExtractCodeowners(claimsDB, repoDir, repoName)
	if err != nil {
		fmt.Fprintf(out, "Tribal: codeowners warning: %v\n", err)
	} else {
		ownershipCount += n
	}

	// 2. Seed source_files so the ranker has a complete view of candidate
	// files on a fresh DB, then call RankFilesForMining to prioritize.
	if err := seedSourceFilesForTribal(ctx, claimsDB, repoDir, repoName); err != nil {
		fmt.Fprintf(out, "Tribal: seed source_files warning: %v\n", err)
	}
	rankedFiles, rankErr := claimsDB.RankFilesForMining(repoName, maxFiles)
	if rankErr != nil {
		return fmt.Errorf("rank files for tribal mining: %w", rankErr)
	}
	fmt.Fprintf(out, "Tribal: ranked %d files for mining (cap %d)\n", len(rankedFiles), maxFiles)
	for i, p := range rankedFiles {
		fmt.Fprintf(out, "  [%d] %s\n", i+1, p)
	}
	if rankedFilesTrace != nil {
		rankedFilesTrace(append([]string(nil), rankedFiles...))
	}

	blameExt := &tribal.BlameExtractor{}
	rationaleExt := &tribal.CommitRationaleExtractor{RepoDir: repoDir}
	markerExt := &tribal.MarkerExtractor{}

	for _, relPath := range rankedFiles {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Second-layer glob defense (redundant with SQL but cheap).
		if db.ShouldSkipForMining(relPath) {
			continue
		}

		// Filter extensions the deterministic extractors actually handle.
		ext := strings.ToLower(filepath.Ext(relPath))
		switch ext {
		case ".go", ".ts", ".tsx", ".py", ".sh":
			// supported
		default:
			continue
		}

		absPath := filepath.Join(repoDir, relPath)

		// Upsert a file-level symbol for tribal facts.
		fileSymID, upErr := claimsDB.UpsertSymbol(db.Symbol{
			Repo:       repoName,
			ImportPath: relPath,
			SymbolName: relPath,
			Language:   "file",
			Kind:       "file",
			Visibility: "public",
		})
		if upErr != nil {
			continue // skip this file
		}

		// 3. Blame extraction: use file-level symbol spanning all lines.
		content, readErr := os.ReadFile(absPath)
		if readErr != nil {
			continue
		}
		lineCount := 1
		for _, b := range content {
			if b == '\n' {
				lineCount++
			}
		}

		symRanges := []tribal.SymbolRange{{
			SymbolID:  fileSymID,
			StartLine: 1,
			EndLine:   lineCount,
		}}
		blameFacts, blameErr := blameExt.ExtractForFile(ctx, repoDir, relPath, symRanges)
		if blameErr == nil {
			for _, fact := range blameFacts {
				if _, _, insertErr := claimsDB.UpsertTribalFact(fact, fact.Evidence); insertErr == nil {
					blameCount++
				}
			}
		}

		// 4. Commit rationale extraction.
		fact, evidence, ratErr := rationaleExt.ExtractForFile(ctx, relPath, fileSymID)
		if ratErr == nil && fact != nil {
			if _, _, insertErr := claimsDB.UpsertTribalFact(*fact, evidence); insertErr == nil {
				rationaleCount++
			}
		}

		// 5. Inline marker extraction.
		markerFacts, markerErr := markerExt.ExtractFromFile(relPath, content)
		if markerErr == nil {
			for i := range markerFacts {
				markerFacts[i].SubjectID = fileSymID
				if _, _, insertErr := claimsDB.UpsertTribalFact(markerFacts[i], markerFacts[i].Evidence); insertErr == nil {
					markerCount++
				}
			}
		}
	}

	fmt.Fprintf(out, "\n## Tribal Knowledge Summary\n\n")
	fmt.Fprintf(out, "- **Ownership facts (codeowners)**: %d\n", ownershipCount)
	fmt.Fprintf(out, "- **Ownership facts (blame)**: %d\n", blameCount)
	fmt.Fprintf(out, "- **Rationale facts**: %d\n", rationaleCount)
	fmt.Fprintf(out, "- **Marker facts (todo/quirk)**: %d\n", markerCount)
	fmt.Fprintf(out, "- **Total tribal facts**: %d\n", ownershipCount+blameCount+rationaleCount+markerCount)

	return nil
}

// runLLMTribalExtraction runs the LLM-based PR comment miner on the repository.
// It validates config opt-in, API key presence, and git remote parsing, then
// selects files via ClaimsDB.RankFilesForMining (capped at maxFiles) before
// classifying PR comments via LLM.
func runLLMTribalExtraction(ctx context.Context, out io.Writer, claimsDB *db.ClaimsDB, repoDir, repoName string, maxFiles int) error {
	// 1. Load config and check opt-in gate.
	cfg, err := config.Load(config.ConfigPath(repoDir))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Tribal.LLMEnabled {
		return fmt.Errorf("LLM tribal extraction requires tribal.llm_enabled: true in .livedocs.yaml")
	}

	// 2. Parse git remote for owner/repo (needed by PR comment miner).
	owner, repo, ok := getGitRemoteOwnerRepo(ctx, repoDir)
	if !ok {
		fmt.Fprintf(out, "Warning: could not parse git remote origin; skipping LLM tribal extraction\n")
		return nil
	}

	// 3. gh CLI version preflight.
	ghVersion, ghErr := tribal.CheckGhVersion(ctx, nil, extractAcceptUnknownGhVersion)
	if ghErr != nil {
		return fmt.Errorf("gh CLI preflight: %w (rerun with --accept-unknown-gh-version to bypass)", ghErr)
	}
	fmt.Fprintf(out, "gh CLI version: %s\n", ghVersion)
	minerVersion := "gh-" + ghVersion

	// 4. Optional force-remine: clear all stored PR cursors for this repo.
	if extractForceRemine {
		if err := claimsDB.ClearPRIDSet(repoName); err != nil {
			return fmt.Errorf("force-remine: clear pr id set: %w", err)
		}
		fmt.Fprintf(out, "force-remine: cleared PR cursors for %s\n", repoName)
	}

	// 5. Create LLM client: prefer Claude CLI (OAuth) over API key.
	var client semantic.LLMClient
	cliClient, cliErr := semantic.NewClaudeCLIClient(cfg.Tribal.Model)
	if cliErr == nil {
		client = cliClient
		fmt.Fprintf(out, "Using Claude CLI (OAuth) for LLM tribal extraction\n")
	} else {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("LLM tribal extraction requires either 'claude' CLI on PATH (OAuth) or ANTHROPIC_API_KEY env var")
		}
		apiClient, apiErr := semantic.NewAnthropicClient(apiKey, semantic.WithModel(cfg.Tribal.Model))
		if apiErr != nil {
			return fmt.Errorf("create LLM client: %w", apiErr)
		}
		client = apiClient
		fmt.Fprintf(out, "Using Anthropic API for LLM tribal extraction\n")
	}

	// 6. Create PR comment miner and wrap in TribalMiningService.
	// The service owns cursor management, budget enforcement, error
	// propagation, and fact upsert — callers never touch PRCommentMiner,
	// DailyBudget, or cursor columns directly (premortem F7 mitigation).
	miner := &tribal.PRCommentMiner{
		RepoOwner:   owner,
		RepoName:    repo,
		Client:      client,
		Model:       cfg.Tribal.Model,
		DailyBudget: cfg.Tribal.DailyBudget,
	}
	svc := tribal.NewTribalMiningService(claimsDB, miner, repoName,
		tribal.WithMinerVersion(minerVersion))

	// 7. Select files via the ranker. source_files is seeded by
	// runTribalExtraction (always invoked in the LLM path), so the ranker
	// has a complete candidate set.
	rankedFiles, rankErr := claimsDB.RankFilesForMining(repoName, maxFiles)
	if rankErr != nil {
		return fmt.Errorf("rank files for LLM tribal mining: %w", rankErr)
	}
	fmt.Fprintf(out, "LLM tribal: ranked %d files for mining (cap %d)\n", len(rankedFiles), maxFiles)
	for i, p := range rankedFiles {
		fmt.Fprintf(out, "  [%d] %s\n", i+1, p)
	}
	if rankedFilesTrace != nil {
		rankedFilesTrace(append([]string(nil), rankedFiles...))
	}

	var rationaleCount, invariantCount, quirkCount int

mineLoop:
	for _, relPath := range rankedFiles {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Second-layer glob defense.
		if db.ShouldSkipForMining(relPath) {
			continue
		}

		// Filter supported extensions.
		ext := strings.ToLower(filepath.Ext(relPath))
		switch ext {
		case ".go", ".ts", ".tsx", ".py", ".sh":
			// supported
		default:
			continue
		}

		// Mine via the service — it handles symbol upsert, cursor,
		// budget, error propagation, and fact upsert atomically.
		result, mineErr := svc.MineFile(ctx, relPath, tribal.TriggerBatchSchedule)
		if mineErr != nil {
			var me *tribal.MiningError
			if errors.As(mineErr, &me) && me.Code == "budget_exceeded" {
				fmt.Fprintf(out, "LLM tribal: daily budget reached, stopping extraction\n")
				break mineLoop
			}
			// Cursor regressions and other per-file errors are non-fatal.
			continue
		}

		if result != nil {
			for _, fact := range result.Facts {
				switch fact.Kind {
				case "rationale":
					rationaleCount++
				case "invariant":
					invariantCount++
				case "quirk":
					quirkCount++
				}
			}
		}
	}

	// 8. Print LLM tribal summary.
	total := rationaleCount + invariantCount + quirkCount
	fmt.Fprintf(out, "\n## LLM Tribal Knowledge Summary (PR Comments)\n\n")
	fmt.Fprintf(out, "- **Rationale facts**: %d\n", rationaleCount)
	fmt.Fprintf(out, "- **Invariant facts**: %d\n", invariantCount)
	fmt.Fprintf(out, "- **Quirk facts**: %d\n", quirkCount)
	fmt.Fprintf(out, "- **Total LLM tribal facts**: %d\n", total)

	return nil
}

// getGitRemoteOwnerRepo extracts the GitHub owner and repo name from the
// git remote "origin" URL of the repository at repoDir.
func getGitRemoteOwnerRepo(ctx context.Context, repoDir string) (owner, name string, ok bool) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	return parseGitRemoteURL(strings.TrimSpace(string(output)))
}

// parseGitRemoteURL extracts owner and repo name from a git remote URL.
// Handles HTTPS (https://github.com/owner/repo.git) and SSH (git@github.com:owner/repo.git).
func parseGitRemoteURL(rawURL string) (owner, name string, ok bool) {
	rawURL = strings.TrimSpace(rawURL)
	rawURL = strings.TrimSuffix(rawURL, ".git")

	// SSH format: git@github.com:owner/repo
	if strings.Contains(rawURL, ":") && strings.Contains(rawURL, "@") {
		// Find the part after the colon.
		colonIdx := strings.LastIndex(rawURL, ":")
		path := rawURL[colonIdx+1:]
		parts := strings.Split(path, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-2], parts[len(parts)-1], true
		}
		return "", "", false
	}

	// HTTPS format: https://github.com/owner/repo
	// Strip scheme if present.
	if idx := strings.Index(rawURL, "://"); idx >= 0 {
		rawURL = rawURL[idx+3:]
	}
	parts := strings.Split(rawURL, "/")
	// Expect at least: host/owner/repo
	if len(parts) >= 3 {
		return parts[len(parts)-2], parts[len(parts)-1], true
	}

	return "", "", false
}
