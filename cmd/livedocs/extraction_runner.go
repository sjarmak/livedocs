package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sjarmak/livedocs/cache"
	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
	"github.com/sjarmak/livedocs/extractor/lang"
	"github.com/sjarmak/livedocs/extractor/treesitter"
	"github.com/sjarmak/livedocs/gitdiff"
	"github.com/sjarmak/livedocs/pipeline"
)

// extractionRunner implements mcpserver.ExtractionRunner by delegating to
// Sourcegraph MCP for commit lookups and the extraction pipeline for indexing.
type extractionRunner struct {
	sgClient    pipeline.MCPCaller
	dataDir     string
	concurrency int
}

// newExtractionRunner creates an extractionRunner.
// sgClient is used for Sourcegraph MCP calls (commit_search, read_file, etc.).
// dataDir is the directory where per-repo .claims.db files are stored.
// concurrency controls the max concurrent MCP calls during extraction.
func newExtractionRunner(sgClient pipeline.MCPCaller, dataDir string, concurrency int) *extractionRunner {
	if concurrency < 1 {
		concurrency = 10
	}
	return &extractionRunner{
		sgClient:    sgClient,
		dataDir:     dataDir,
		concurrency: concurrency,
	}
}

// commitSHAPattern matches a 40-character hex SHA.
var commitSHAPattern = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

// RemoteHeadCommit returns the latest commit SHA for the given repo by calling
// the Sourcegraph commit_search MCP tool.
func (r *extractionRunner) RemoteHeadCommit(ctx context.Context, repo string) (string, error) {
	result, err := r.sgClient.CallTool(ctx, "commit_search", map[string]any{
		"repos": []string{repo},
		"count": 1,
	})
	if err != nil {
		return "", fmt.Errorf("commit_search for %s: %w", repo, err)
	}

	// Parse the first 40-char hex SHA from the response.
	sha := commitSHAPattern.FindString(result)
	if sha == "" {
		return "", fmt.Errorf("commit_search for %s: no commit SHA found in response", repo)
	}
	return sha, nil
}

// RunExtraction runs a full or incremental extraction for the repo.
// If no claims DB exists, it performs a full extraction via Sourcegraph.
// If a claims DB exists, it performs an incremental extraction using the
// stored commit SHA as the base revision.
func (r *extractionRunner) RunExtraction(ctx context.Context, repo, importPath string) error {
	repoName := repoNameFromPath(repo)
	dbPath := filepath.Join(r.dataDir, repoName+".claims.db")

	_, statErr := os.Stat(dbPath)
	dbExists := statErr == nil

	if dbExists {
		return r.runIncrementalExtraction(ctx, repo, repoName, dbPath, importPath)
	}
	return r.runFullExtraction(ctx, repo, repoName, dbPath)
}

// repoNameFromPath extracts the last path component from a repo identifier.
// e.g. "github.com/org/repo" -> "repo"
func repoNameFromPath(repo string) string {
	if idx := strings.LastIndex(repo, "/"); idx >= 0 {
		return repo[idx+1:]
	}
	return repo
}

// runFullExtraction performs a full extraction by listing all files from
// Sourcegraph and running the pipeline.
func (r *extractionRunner) runFullExtraction(ctx context.Context, repo, repoName, dbPath string) error {
	log.Printf("extraction-runner: starting full extraction for %s", repo)
	start := time.Now()

	// Create file source.
	fileSource, err := pipeline.NewSourcegraphFileSource(r.sgClient, sgToolLister{}, pipeline.WithConcurrency(r.concurrency))
	if err != nil {
		return fmt.Errorf("create sourcegraph file source: %w", err)
	}

	// List all files.
	files, err := fileSource.ListFiles(ctx, repo, "", "*")
	if err != nil {
		return fmt.Errorf("list files for %s: %w", repo, err)
	}

	// Build synthetic FileChanges (all added).
	changes := make([]gitdiff.FileChange, len(files))
	for i, f := range files {
		changes[i] = gitdiff.FileChange{Status: gitdiff.StatusAdded, Path: f}
	}

	// Get the current HEAD commit for metadata.
	headSHA, err := r.RemoteHeadCommit(ctx, repo)
	if err != nil {
		log.Printf("extraction-runner: warning: could not get HEAD commit for %s: %v", repo, err)
		headSHA = ""
	}

	// Run extraction pipeline and store results.
	result, err := r.runPipeline(ctx, repo, repoName, dbPath, &prefetchedDiffSource{inner: fileSource, changes: changes}, "", "", headSHA)
	if err != nil {
		return err
	}

	log.Printf("extraction-runner: full extraction for %s completed in %s (files: %d, claims: %d, errors: %d)",
		repo, result.Duration.Round(time.Millisecond), result.FilesExtracted, result.ClaimsStored, len(result.Errors))
	log.Printf("extraction-runner: total wall time including file listing: %s", time.Since(start).Round(time.Millisecond))
	return nil
}

// runIncrementalExtraction performs an incremental extraction using the stored
// commit SHA as the base and the remote HEAD as the target.
func (r *extractionRunner) runIncrementalExtraction(ctx context.Context, repo, repoName, dbPath, importPath string) error {
	log.Printf("extraction-runner: starting incremental extraction for %s", repo)

	// Read current metadata to get the last indexed commit.
	existingDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		return fmt.Errorf("open existing claims DB for %s: %w", repo, err)
	}
	meta, err := existingDB.GetExtractionMeta()
	existingDB.Close()
	if err != nil {
		return fmt.Errorf("get extraction meta for %s: %w", repo, err)
	}

	fromRev := meta.CommitSHA
	if fromRev == "" {
		// No commit SHA recorded — fall back to full extraction.
		log.Printf("extraction-runner: no commit SHA in DB for %s, falling back to full extraction", repo)
		return r.runFullExtraction(ctx, repo, repoName, dbPath)
	}

	// Get the remote HEAD.
	toRev, err := r.RemoteHeadCommit(ctx, repo)
	if err != nil {
		return fmt.Errorf("get remote HEAD for %s: %w", repo, err)
	}

	if fromRev == toRev {
		log.Printf("extraction-runner: %s is already up-to-date at %s", repo, fromRev)
		return nil
	}

	// Create file source for incremental extraction.
	fileSource, err := pipeline.NewSourcegraphFileSource(r.sgClient, sgToolLister{}, pipeline.WithConcurrency(r.concurrency))
	if err != nil {
		return fmt.Errorf("create sourcegraph file source: %w", err)
	}

	result, err := r.runPipeline(ctx, repo, repoName, dbPath, fileSource, fromRev, toRev, toRev)
	if err != nil {
		return err
	}

	log.Printf("extraction-runner: incremental extraction for %s completed in %s (files: %d, claims: %d, errors: %d)",
		repo, result.Duration.Round(time.Millisecond), result.FilesExtracted, result.ClaimsStored, len(result.Errors))
	return nil
}

// runPipeline sets up the extraction pipeline, runs it, and atomically replaces
// the claims DB file. The headSHA is stored in extraction metadata.
func (r *extractionRunner) runPipeline(
	ctx context.Context,
	repo, repoName, dbPath string,
	fileSource pipeline.FileSource,
	fromRev, toRev, headSHA string,
) (pipeline.Result, error) {
	// Ensure data-dir exists.
	if err := os.MkdirAll(r.dataDir, 0o755); err != nil {
		return pipeline.Result{}, fmt.Errorf("create data-dir: %w", err)
	}

	// Use atomic file replacement.
	tmpFile, err := os.CreateTemp(r.dataDir, repoName+".claims.db.tmp.*")
	if err != nil {
		return pipeline.Result{}, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	// Open claims DB.
	claimsDB, err := db.OpenClaimsDB(tmpPath)
	if err != nil {
		return pipeline.Result{}, fmt.Errorf("open claims db: %w", err)
	}
	defer claimsDB.Close()

	if err := claimsDB.CreateSchema(); err != nil {
		return pipeline.Result{}, fmt.Errorf("create schema: %w", err)
	}

	// Open in-memory cache.
	cacheStore, err := cache.NewSQLiteStore(":memory:", 2*1024*1024*1024)
	if err != nil {
		return pipeline.Result{}, fmt.Errorf("open cache: %w", err)
	}
	defer cacheStore.Close()

	// Set up extractor registry (tree-sitter only for remote extraction).
	registry := buildRemoteRegistry()

	// Run the pipeline.
	p := pipeline.New(pipeline.Config{
		Repo:       repo,
		RepoDir:    "",
		Cache:      cacheStore,
		ClaimsDB:   claimsDB,
		Registry:   registry,
		FileSource: fileSource,
	})

	result, err := p.Run(ctx, fromRev, toRev)
	if err != nil {
		return pipeline.Result{}, fmt.Errorf("pipeline run for %s: %w", repo, err)
	}

	// Store extraction metadata with commit SHA.
	if err := claimsDB.SetExtractionMeta(db.ExtractionMeta{
		CommitSHA:   headSHA,
		ExtractedAt: db.Now(),
		RepoRoot:    repo,
	}); err != nil {
		return pipeline.Result{}, fmt.Errorf("set extraction meta for %s: %w", repo, err)
	}

	// Close DB before rename so all data is flushed.
	claimsDB.Close()

	// Atomically replace the output file.
	if err := os.Rename(tmpPath, dbPath); err != nil {
		return pipeline.Result{}, fmt.Errorf("atomic rename %s -> %s: %w", tmpPath, dbPath, err)
	}
	success = true

	return result, nil
}

// buildRemoteRegistry creates an extractor registry suitable for remote
// extraction (tree-sitter only; Go deep extractor requires local FS).
func buildRemoteRegistry() *extractor.Registry {
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

	return registry
}
