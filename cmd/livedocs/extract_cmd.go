package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/cache"
	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
	"github.com/live-docs/live_docs/extractor/goextractor"
	"github.com/live-docs/live_docs/extractor/lang"
	"github.com/live-docs/live_docs/extractor/treesitter"
	"github.com/live-docs/live_docs/semantic"
)

var (
	extractRepo   string
	extractOutput string
	extractTier2  bool
)

// newLLMClient creates the LLM client for semantic extraction.
// Overridable in tests to inject a mock.
var newLLMClient = func(apiKey string) (semantic.LLMClient, error) {
	return semantic.NewAnthropicClient(apiKey)
}

// confidenceThreshold is the minimum confidence for semantic claims.
const confidenceThreshold = 0.7

var extractCmd = &cobra.Command{
	Use:   "extract <path>",
	Short: "Extract claims from a repository into a SQLite database",
	Long: `Walks all source files in the given repository path and extracts
structural claims using language-specific extractors:

  - Go files (.go): deep extractor using go/packages and go/types
  - TypeScript files (.ts, .tsx): tree-sitter extractor
  - Python files (.py): tree-sitter extractor
  - Shell files (.sh): tree-sitter extractor

Creates a per-repo SQLite database containing symbols and claims.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoPath := args[0]
		absRepo, err := filepath.Abs(repoPath)
		if err != nil {
			return fmt.Errorf("resolve repo path: %w", err)
		}

		if extractRepo == "" {
			extractRepo = filepath.Base(absRepo)
		}

		return runExtract(cmd.Context(), cmd, absRepo, extractRepo, extractOutput)
	},
}

func init() {
	extractCmd.Flags().StringVar(&extractRepo, "repo", "", "repository name (default: directory basename)")
	extractCmd.Flags().StringVarP(&extractOutput, "output", "o", "", "output SQLite file path (default: <repo>.claims.db)")
	extractCmd.Flags().BoolVar(&extractTier2, "tier2", false, "generate Tier 2 semantic claims via LLM (requires ANTHROPIC_API_KEY)")
}

// skipDirs contains directory names to skip during file walking.
var skipDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	"_output":      true,
	"_build":       true,
	".cache":       true,
}

func runExtract(ctx context.Context, cmd *cobra.Command, repoDir, repoName, outputPath string) error {
	out := cmd.OutOrStdout()
	start := time.Now()

	// Determine output path.
	if outputPath == "" {
		outputPath = repoName + ".claims.db"
	}

	// Remove existing DB to start fresh.
	_ = os.Remove(outputPath)

	// Open on-disk claims DB.
	claimsDB, err := db.OpenClaimsDB(outputPath)
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

	return nil
}

// storeClaims upserts symbols and inserts claims from a slice of extractor claims.
// Claims with sensitive content in ObjectText are filtered out before storage.
// Returns the number of claims stored.
func storeClaims(claimsDB *db.ClaimsDB, repoName string, claims []extractor.Claim) (int, error) {
	// Filter out claims containing sensitive content.
	claims = extractor.FilterSensitiveClaims(claims)

	stored := 0
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
			return stored, fmt.Errorf("upsert symbol %s: %w", claim.SubjectName, err)
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
			return stored, fmt.Errorf("insert claim for %s: %w", claim.SubjectName, err)
		}
		stored++
	}
	return stored, nil
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
