// Package mcpserver staleness.go implements lazy staleness checking for MCP
// query paths. When a tool handler detects that source files have changed on
// disk since last extraction, it can trigger single-file re-extraction before
// returning the response.
package mcpserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
)

// StalenessChecker checks whether source files are stale relative to stored
// content hashes and can trigger single-file re-extraction. It is safe for
// concurrent use.
type StalenessChecker struct {
	repoRoots map[string]string   // repo name -> absolute path to repo root
	registry  *extractor.Registry // extractor registry for re-extraction
	mu        sync.RWMutex        // protects repoRoots
}

// NewStalenessChecker creates a StalenessChecker with the given repo root mappings
// and extractor registry. If registry is nil, re-extraction is disabled (check-only mode).
func NewStalenessChecker(repoRoots map[string]string, registry *extractor.Registry) *StalenessChecker {
	roots := make(map[string]string, len(repoRoots))
	for k, v := range repoRoots {
		roots[k] = v
	}
	return &StalenessChecker{
		repoRoots: roots,
		registry:  registry,
	}
}

// StaleFile describes a single source file that has changed on disk.
type StaleFile struct {
	RelativePath string
	StoredHash   string
	CurrentHash  string
	RepoName     string
}

// CheckPackageStaleness checks whether any source files for the given import
// path have changed on disk since last extraction. Returns the list of stale
// files. If the repo root is not configured, returns nil (no-op).
func (sc *StalenessChecker) CheckPackageStaleness(cdb *db.ClaimsDB, repoName, importPath string) []StaleFile {
	sc.mu.RLock()
	repoRoot, ok := sc.repoRoots[repoName]
	sc.mu.RUnlock()
	if !ok {
		return nil
	}

	sourceFiles, err := cdb.GetSourceFilesByImportPath(importPath)
	if err != nil || len(sourceFiles) == 0 {
		return nil
	}

	var stale []StaleFile
	for _, sf := range sourceFiles {
		absPath := filepath.Join(repoRoot, sf.RelativePath)
		content, err := os.ReadFile(absPath)
		if err != nil {
			// File might have been deleted or be unreadable — skip.
			continue
		}
		currentHash := fmt.Sprintf("%x", sha256.Sum256(content))
		if currentHash != sf.ContentHash {
			stale = append(stale, StaleFile{
				RelativePath: sf.RelativePath,
				StoredHash:   sf.ContentHash,
				CurrentHash:  currentHash,
				RepoName:     repoName,
			})
		}
	}
	return stale
}

// RefreshStaleFiles re-extracts the given stale files and updates the claims DB.
// This is best-effort: errors are collected but do not stop processing. Returns
// the number of files successfully re-extracted and any errors encountered.
func (sc *StalenessChecker) RefreshStaleFiles(ctx context.Context, cdb *db.ClaimsDB, staleFiles []StaleFile) (int, []error) {
	if sc.registry == nil || len(staleFiles) == 0 {
		return 0, nil
	}

	var refreshed int
	var errs []error

	for _, sf := range staleFiles {
		sc.mu.RLock()
		repoRoot, ok := sc.repoRoots[sf.RepoName]
		sc.mu.RUnlock()
		if !ok {
			continue
		}

		err := sc.reExtractFile(ctx, cdb, sf.RepoName, repoRoot, sf.RelativePath)
		if err != nil {
			errs = append(errs, fmt.Errorf("re-extract %s: %w", sf.RelativePath, err))
			continue
		}
		refreshed++
	}

	return refreshed, errs
}

// reExtractFile re-extracts a single file using the pipeline's processFile logic.
// It reads the file, extracts claims, and stores them in a transaction.
func (sc *StalenessChecker) reExtractFile(ctx context.Context, cdb *db.ClaimsDB, repoName, repoRoot, relPath string) error {
	absPath := filepath.Join(repoRoot, relPath)

	// Read file and compute content hash.
	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", relPath, err)
	}
	contentHash := fmt.Sprintf("%x", sha256.Sum256(content))

	// Determine extractor.
	ext := strings.ToLower(filepath.Ext(relPath))
	cfg := sc.registry.LookupByExtension(ext)
	if cfg == nil {
		return fmt.Errorf("no extractor for extension %s", ext)
	}

	ex := cfg.DeepExtractor
	if ex == nil {
		ex = cfg.FastExtractor
	}
	if ex == nil {
		return fmt.Errorf("no extractor implementation for %s", cfg.Language)
	}

	// Extract claims.
	claims, err := sc.registry.ExtractFile(ctx, absPath)
	if err != nil {
		return fmt.Errorf("extract %s: %w", relPath, err)
	}

	// Store in a transaction.
	return cdb.RunInTransaction(func() error {
		// Delete old claims for this file.
		if err := cdb.DeleteClaimsByExtractorAndFile(ex.Name(), relPath); err != nil {
			return fmt.Errorf("delete old claims for %s: %w", relPath, err)
		}

		// Store new claims.
		for _, claim := range claims {
			if claim.SubjectRepo == "" {
				claim.SubjectRepo = repoName
			}
			if claim.SubjectImportPath == "" {
				claim.SubjectImportPath = relPath
			}

			symID, err := cdb.UpsertSymbol(db.Symbol{
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

			_, err = cdb.InsertClaim(db.Claim{
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
		}

		// Update source_files record.
		_, err := cdb.UpsertSourceFile(db.SourceFile{
			Repo:             repoName,
			RelativePath:     relPath,
			ContentHash:      contentHash,
			ExtractorVersion: ex.Version(),
			GrammarVersion:   cfg.TreeSitterGrammar,
			LastIndexed:      db.Now(),
		})
		if err != nil {
			return fmt.Errorf("upsert source file for %s: %w", relPath, err)
		}

		return nil
	})
}

// RepoRoot returns the configured root directory for a repo, or "" if not configured.
func (sc *StalenessChecker) RepoRoot(repoName string) string {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.repoRoots[repoName]
}

// HasRepoRoot reports whether a repo root is configured for the given repo name.
func (sc *StalenessChecker) HasRepoRoot(repoName string) bool {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	_, ok := sc.repoRoots[repoName]
	return ok
}

// stalenessWarning formats a warning message about stale files that could not
// be refreshed.
func stalenessWarning(staleFiles []StaleFile, refreshed int, errs []error) string {
	if len(staleFiles) == 0 {
		return ""
	}

	total := len(staleFiles)
	if refreshed == total {
		return fmt.Sprintf("> **Note:** %d file(s) were re-extracted on-the-fly for freshness.\n\n", refreshed)
	}

	failed := total - refreshed
	msg := fmt.Sprintf("> **Warning:** %d file(s) have changed on disk since last extraction.", total)
	if refreshed > 0 {
		msg += fmt.Sprintf(" %d re-extracted successfully, %d failed.", refreshed, failed)
	}
	if len(errs) > 0 {
		msg += " Errors: " + errs[0].Error()
	}
	msg += "\n\n"
	return msg
}
