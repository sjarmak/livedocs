// Package pipeline orchestrates diff-triggered continuous maintenance.
// It detects changed files via git diff, checks cache hits, runs extractors
// on changed files only, stores claims in the DB, and marks deleted files.
package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sjarmak/livedocs/cache"
	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
	"github.com/sjarmak/livedocs/gitdiff"
)

// Config holds the dependencies for a Pipeline.
type Config struct {
	Repo     string              // repository identifier, e.g. "kubernetes/kubernetes"
	RepoDir  string              // absolute path to the git repo root
	Cache    cache.Store         // content-hash cache
	ClaimsDB *db.ClaimsDB        // claims database
	Registry *extractor.Registry // extractor registry

	// FileSource, when non-nil, replaces local filesystem access and git diff
	// with the provided implementation. This enables extraction from remote
	// sources (e.g. GitHub API) without cloning.
	FileSource FileSource
}

// Pipeline orchestrates incremental extraction triggered by git diffs.
type Pipeline struct {
	repo       string
	repoDir    string
	cache      cache.Store
	claimsDB   *db.ClaimsDB
	registry   *extractor.Registry
	fileSource FileSource
}

// Result summarises a single pipeline run.
type Result struct {
	FilesChanged     int           // total non-deleted files in the diff
	FilesExtracted   int           // files that were actually extracted (cache misses)
	FilesDeleted     int           // files tombstoned
	FilesSkipped     int           // files with no registered extractor
	CacheHits        int           // files skipped due to cache hit
	ClaimsStored     int           // total claims inserted
	ReverseDepFiles  int           // files added via reverse-dependency lookup (not in original diff)
	ChangedPaths     []string      // relative paths of all non-deleted changed files
	Duration         time.Duration // wall-clock time
	Errors           []FileError   // non-fatal per-file errors
	StalenessWarning bool          // true when HEAD changed but diff returned zero files
}

// FileError records a non-fatal error for a single file.
type FileError struct {
	Path string
	Err  error
}

// New creates a Pipeline from the given Config.
func New(cfg Config) *Pipeline {
	return &Pipeline{
		repo:       cfg.Repo,
		repoDir:    cfg.RepoDir,
		cache:      cfg.Cache,
		claimsDB:   cfg.ClaimsDB,
		registry:   cfg.Registry,
		fileSource: cfg.FileSource,
	}
}

// Run executes the pipeline for changes between fromCommit and toCommit.
// It returns a Result summarising what was processed.
func (p *Pipeline) Run(ctx context.Context, fromCommit, toCommit string) (Result, error) {
	start := time.Now()
	var result Result

	// 1. Get the diff.
	var (
		changes []gitdiff.FileChange
		err     error
	)
	if p.fileSource != nil {
		changes, err = p.fileSource.DiffBetween(ctx, p.repo, fromCommit, toCommit)
	} else {
		changes, err = gitdiff.DiffBetween(p.repoDir, fromCommit, toCommit)
	}
	if err != nil {
		return result, fmt.Errorf("pipeline: diff: %w", err)
	}

	// Staleness check: if revisions differ but diff returned nothing,
	// the compare may have returned incomplete data.
	if len(changes) == 0 && fromCommit != toCommit && fromCommit != "" && toCommit != "" {
		log.Printf("WARNING: repo %s HEAD changed (%s..%s) but diff returned zero files — possible incomplete compare data", p.repo, fromCommit, toCommit)
		result.StalenessWarning = true
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

	// 3a. Expand extraction set with reverse dependencies — files that import
	// symbols from the changed files. This prevents incremental drift where
	// cross-file relationships (imports, implements) become stale.
	revDeps, err := reverseDepPaths(p.claimsDB, changedPaths)
	if err != nil {
		// Non-fatal: log and continue with original changed paths.
		log.Printf("WARNING: reverse-dep lookup failed: %v", err)
	} else if len(revDeps) > 0 {
		result.ReverseDepFiles = len(revDeps)
		changedPaths = append(changedPaths, revDeps...)
	}

	result.FilesChanged = len(changedPaths)
	result.ChangedPaths = changedPaths

	// When the FileSource supports batch reading, pre-read all eligible
	// files concurrently to avoid serial network round-trips.
	var preRead map[string][]byte
	if br, ok := p.fileSource.(BatchReader); ok && len(changedPaths) > 0 {
		var toRead []string
		for _, rp := range changedPaths {
			if extractor.IsGenerated(rp) {
				continue
			}
			ext := strings.ToLower(filepath.Ext(rp))
			if p.registry.LookupByExtension(ext) != nil {
				toRead = append(toRead, rp)
			}
		}
		if len(toRead) > 0 {
			log.Printf("batch-reading %d files concurrently", len(toRead))
			batchResults := br.BatchReadFiles(ctx, p.repo, "", toRead)
			preRead = make(map[string][]byte, len(batchResults))
			for _, r := range batchResults {
				if r.Err == nil {
					preRead[r.Path] = r.Content
				}
			}
		}
	}

	for _, relPath := range changedPaths {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		// Skip generated files before any I/O or extraction.
		if extractor.IsGenerated(relPath) {
			result.FilesSkipped++
			continue
		}

		extracted, claims, err := p.processFileWithContent(ctx, relPath, preRead)
		if err != nil {
			// Check if it's a "no extractor" error — that's a skip, not an error.
			var langErr *extractor.LanguageNotRegisteredError
			if errors.As(err, &langErr) {
				result.FilesSkipped++
				continue
			}
			// Extractor requires local filesystem (e.g. go/packages) — skip with warning.
			if errors.Is(err, extractor.ErrRequiresLocalFS) {
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
	return p.processFileWithContent(ctx, relPath, nil)
}

// processFileWithContent is like processFile but accepts an optional pre-read
// content map. If the file's content is found in preRead, it is used directly;
// otherwise the file is read from the FileSource or local filesystem.
func (p *Pipeline) processFileWithContent(ctx context.Context, relPath string, preRead map[string][]byte) (bool, int, error) {
	// Read file and compute content hash.
	var content []byte
	var err error
	if preRead != nil {
		if c, ok := preRead[relPath]; ok {
			content = c
		}
	}
	if content == nil {
		if p.fileSource != nil {
			content, err = p.fileSource.ReadFile(ctx, p.repo, "", relPath)
		} else {
			absPath := filepath.Join(p.repoDir, relPath)
			content, err = os.ReadFile(absPath)
		}
		if err != nil {
			return false, 0, fmt.Errorf("read %s: %w", relPath, err)
		}
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
	var claims []extractor.Claim
	if p.fileSource != nil {
		claims, err = p.registry.ExtractFileBytes(ctx, content, relPath)
	} else {
		absPath := filepath.Join(p.repoDir, relPath)
		claims, err = p.registry.ExtractFile(ctx, absPath)
	}
	if err != nil {
		return false, 0, fmt.Errorf("extract %s: %w", relPath, err)
	}

	// Wrap delete-old + store-new + update-cache + update-source-files in a
	// single transaction so a crash mid-extraction leaves the DB consistent.
	stored := 0
	txErr := p.claimsDB.RunInTransaction(func() error {
		// Delete old claims for this file before inserting new ones (idempotent re-import).
		if err := p.claimsDB.DeleteClaimsByExtractorAndFile(ex.Name(), relPath); err != nil {
			return fmt.Errorf("delete old claims for %s: %w", relPath, err)
		}

		// Store claims.
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
				return fmt.Errorf("upsert symbol for %s: %w", relPath, err)
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
				return fmt.Errorf("insert claim for %s: %w", relPath, err)
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
			return fmt.Errorf("cache put for %s: %w", relPath, err)
		}

		// Update source_files in claims DB.
		_, err := p.claimsDB.UpsertSourceFile(db.SourceFile{
			Repo:             p.repo,
			RelativePath:     relPath,
			ContentHash:      contentHash,
			ExtractorVersion: extractorVersion,
			GrammarVersion:   grammarVersion,
			LastIndexed:      db.Now(),
		})
		if err != nil {
			return fmt.Errorf("upsert source file for %s: %w", relPath, err)
		}

		return nil
	})
	if txErr != nil {
		return false, 0, txErr
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
