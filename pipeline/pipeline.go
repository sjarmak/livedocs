// Package pipeline orchestrates diff-triggered continuous maintenance.
// It detects changed files via git diff, checks cache hits, runs extractors
// on changed files only, stores claims in the DB, and marks deleted files.
package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/live-docs/live_docs/cache"
	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
	"github.com/live-docs/live_docs/gitdiff"
)

// Config holds the dependencies for a Pipeline.
type Config struct {
	Repo     string              // repository identifier, e.g. "kubernetes/kubernetes"
	RepoDir  string              // absolute path to the git repo root
	Cache    cache.Store         // content-hash cache
	ClaimsDB *db.ClaimsDB        // claims database
	Registry *extractor.Registry // extractor registry
}

// Pipeline orchestrates incremental extraction triggered by git diffs.
type Pipeline struct {
	repo     string
	repoDir  string
	cache    cache.Store
	claimsDB *db.ClaimsDB
	registry *extractor.Registry
}

// Result summarises a single pipeline run.
type Result struct {
	FilesChanged   int           // total non-deleted files in the diff
	FilesExtracted int           // files that were actually extracted (cache misses)
	FilesDeleted   int           // files tombstoned
	FilesSkipped   int           // files with no registered extractor
	CacheHits      int           // files skipped due to cache hit
	ClaimsStored   int           // total claims inserted
	Duration       time.Duration // wall-clock time
	Errors         []FileError   // non-fatal per-file errors
}

// FileError records a non-fatal error for a single file.
type FileError struct {
	Path string
	Err  error
}

// New creates a Pipeline from the given Config.
func New(cfg Config) *Pipeline {
	return &Pipeline{
		repo:     cfg.Repo,
		repoDir:  cfg.RepoDir,
		cache:    cfg.Cache,
		claimsDB: cfg.ClaimsDB,
		registry: cfg.Registry,
	}
}

// Run executes the pipeline for changes between fromCommit and toCommit.
// It returns a Result summarising what was processed.
func (p *Pipeline) Run(ctx context.Context, fromCommit, toCommit string) (Result, error) {
	start := time.Now()
	var result Result

	// 1. Get the diff.
	changes, err := gitdiff.DiffBetween(p.repoDir, fromCommit, toCommit)
	if err != nil {
		return result, fmt.Errorf("pipeline: diff: %w", err)
	}

	// 2. Handle deleted files.
	for _, path := range gitdiff.DeletedPaths(changes) {
		if err := p.markDeleted(path); err != nil {
			result.Errors = append(result.Errors, FileError{Path: path, Err: err})
		}
		result.FilesDeleted++
	}

	// 3. Process changed files (added, modified, renamed, copied).
	changedPaths := gitdiff.ChangedPaths(changes)
	result.FilesChanged = len(changedPaths)

	for _, relPath := range changedPaths {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		extracted, claims, err := p.processFile(ctx, relPath)
		if err != nil {
			// Check if it's a "no extractor" error — that's a skip, not an error.
			var langErr *extractor.LanguageNotRegisteredError
			if errors.As(err, &langErr) {
				result.FilesSkipped++
				continue
			}
			result.Errors = append(result.Errors, FileError{Path: relPath, Err: err})
			continue
		}

		if !extracted {
			result.CacheHits++
			continue
		}

		result.FilesExtracted++
		result.ClaimsStored += claims
	}

	result.Duration = time.Since(start)
	return result, nil
}

// processFile handles a single changed file: hash, cache check, extract, store.
// Returns (true, claimCount, nil) if extraction happened, (false, 0, nil) on cache hit.
func (p *Pipeline) processFile(ctx context.Context, relPath string) (bool, int, error) {
	absPath := filepath.Join(p.repoDir, relPath)

	// Read file and compute content hash.
	content, err := os.ReadFile(absPath)
	if err != nil {
		return false, 0, fmt.Errorf("read %s: %w", relPath, err)
	}
	contentHash := fmt.Sprintf("%x", sha256.Sum256(content))

	// Determine extractor version for cache key.
	ext := strings.ToLower(filepath.Ext(relPath))
	cfg := p.registry.LookupByExtension(ext)
	if cfg == nil {
		return false, 0, &extractor.LanguageNotRegisteredError{Key: ext}
	}

	ex := cfg.DeepExtractor
	if ex == nil {
		ex = cfg.FastExtractor
	}
	if ex == nil {
		return false, 0, &extractor.LanguageNotRegisteredError{Key: cfg.Language}
	}

	extractorVersion := ex.Version()
	grammarVersion := cfg.TreeSitterGrammar // used as grammar version in cache key

	// Cache check.
	hit, err := p.cache.Hit(p.repo, relPath, contentHash, extractorVersion, grammarVersion)
	if err != nil {
		return false, 0, fmt.Errorf("cache hit check for %s: %w", relPath, err)
	}
	if hit {
		return false, 0, nil
	}

	// Extract claims.
	claims, err := p.registry.ExtractFile(ctx, absPath)
	if err != nil {
		return false, 0, fmt.Errorf("extract %s: %w", relPath, err)
	}

	// Delete old claims for this file before inserting new ones (idempotent re-import).
	if err := p.claimsDB.DeleteClaimsByExtractorAndFile(ex.Name(), relPath); err != nil {
		return false, 0, fmt.Errorf("delete old claims for %s: %w", relPath, err)
	}

	// Store claims.
	stored := 0
	for _, claim := range claims {
		// Fill in repo if the extractor left it empty.
		if claim.SubjectRepo == "" {
			claim.SubjectRepo = p.repo
		}
		if claim.SubjectImportPath == "" {
			claim.SubjectImportPath = relPath
		}

		symID, err := p.claimsDB.UpsertSymbol(db.Symbol{
			Repo:        claim.SubjectRepo,
			ImportPath:  claim.SubjectImportPath,
			SymbolName:  claim.SubjectName,
			Language:    claim.Language,
			Kind:        string(claim.Kind),
			Visibility:  string(claim.Visibility),
			DisplayName: claim.SubjectName,
			SCIPSymbol:  claim.SCIPSymbol,
		})
		if err != nil {
			return false, stored, fmt.Errorf("upsert symbol for %s: %w", relPath, err)
		}

		_, err = p.claimsDB.InsertClaim(db.Claim{
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
			return false, stored, fmt.Errorf("insert claim for %s: %w", relPath, err)
		}
		stored++
	}

	// Update cache.
	if err := p.cache.Put(cache.Entry{
		Repo:             p.repo,
		RelativePath:     relPath,
		ContentHash:      contentHash,
		ExtractorVersion: extractorVersion,
		GrammarVersion:   grammarVersion,
		LastIndexed:      time.Now(),
		SizeBytes:        int64(len(content)),
	}); err != nil {
		return true, stored, fmt.Errorf("cache put for %s: %w", relPath, err)
	}

	// Update source_files in claims DB.
	_, err = p.claimsDB.UpsertSourceFile(db.SourceFile{
		Repo:             p.repo,
		RelativePath:     relPath,
		ContentHash:      contentHash,
		ExtractorVersion: extractorVersion,
		GrammarVersion:   grammarVersion,
		LastIndexed:      db.Now(),
	})
	if err != nil {
		return true, stored, fmt.Errorf("upsert source file for %s: %w", relPath, err)
	}

	return true, stored, nil
}

// markDeleted tombstones a file in both the cache and claims DB.
func (p *Pipeline) markDeleted(relPath string) error {
	// Tombstone in cache (no-op if not present).
	if err := p.cache.MarkDeleted(p.repo, relPath); err != nil {
		return fmt.Errorf("cache mark deleted %s: %w", relPath, err)
	}

	// Tombstone in claims DB (ignore "not found" since the file may not
	// have been indexed yet).
	err := p.claimsDB.MarkFileDeleted(p.repo, relPath)
	if err != nil && !strings.Contains(err.Error(), "not found") {
		return fmt.Errorf("db mark deleted %s: %w", relPath, err)
	}

	return nil
}
