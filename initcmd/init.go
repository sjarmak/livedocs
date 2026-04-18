// Package initcmd implements the livedocs init workflow: scaffold configuration,
// detect languages, and run the first full extraction pass.
package initcmd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sjarmak/livedocs/cache"
	"github.com/sjarmak/livedocs/config"
	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
	"github.com/sjarmak/livedocs/extractor/lang"
	"github.com/sjarmak/livedocs/extractor/treesitter"
)

// Result summarises the outcome of a livedocs init run.
type Result struct {
	ConfigCreated  bool          // true if .livedocs.yaml was created (vs already existed)
	DirCreated     bool          // true if .livedocs/ was created
	Languages      []string      // detected or configured languages
	FilesScanned   int           // total files walked
	FilesExtracted int           // files that produced claims
	FilesSkipped   int           // files with no extractor
	ClaimsStored   int           // total claims inserted
	Errors         []FileError   // non-fatal per-file errors
	Duration       time.Duration // wall-clock time
}

// FileError records a non-fatal error for a single file.
type FileError struct {
	Path string
	Err  error
}

// skipDirs are directories to skip during file discovery.
var skipDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	"_output":      true,
	".livedocs":    true,
	"testdata":     true,
}

// Options configures init behavior.
type Options struct {
	// RepoRoot is the absolute path to the repository root.
	RepoRoot string

	// Writer receives progress output. Nil disables output.
	Writer io.Writer

	// Force overwrites an existing .livedocs.yaml if true.
	Force bool
}

// Run executes the full init workflow: scaffold config, detect languages,
// create DB, walk files, extract claims.
func Run(ctx context.Context, opts Options) (Result, error) {
	start := time.Now()
	var result Result

	repoRoot := opts.RepoRoot
	if repoRoot == "" {
		return result, fmt.Errorf("init: repo root is required")
	}

	// 1. Load or create config.
	cfgPath := config.ConfigPath(repoRoot)
	cfg, created, err := loadOrCreateConfig(cfgPath, repoRoot, opts.Force)
	if err != nil {
		return result, fmt.Errorf("init: config: %w", err)
	}
	result.ConfigCreated = created

	// 2. Create .livedocs/ directory.
	dirPath := config.DirPath(repoRoot)
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return result, fmt.Errorf("init: create dir %s: %w", dirPath, err)
	}
	result.DirCreated = true

	// 3. Set up extractor registry.
	langRegistry := lang.NewRegistry()
	tsExtractor := treesitter.New(langRegistry)

	registry := extractor.NewRegistry()
	for _, langName := range langRegistry.AllLanguages() {
		langCfg, ok := langRegistry.LookupByLanguage(langName)
		if !ok {
			continue
		}
		registry.Register(extractor.LanguageConfig{
			Language:          langCfg.Language,
			Extensions:        langCfg.Extensions,
			TreeSitterGrammar: langCfg.GrammarName,
			FastExtractor:     tsExtractor,
		})
	}

	// 4. Open claims DB and cache.
	claimsDBPath := filepath.Join(repoRoot, cfg.ClaimsDB)
	if err := os.MkdirAll(filepath.Dir(claimsDBPath), 0755); err != nil {
		return result, fmt.Errorf("init: create claims db dir: %w", err)
	}
	claimsDB, err := db.OpenClaimsDB(claimsDBPath)
	if err != nil {
		return result, fmt.Errorf("init: open claims db: %w", err)
	}
	defer claimsDB.Close()

	if err := claimsDB.CreateSchema(); err != nil {
		return result, fmt.Errorf("init: create schema: %w", err)
	}

	cacheDBPath := filepath.Join(repoRoot, cfg.CacheDB)
	cacheStore, err := cache.NewSQLiteStore(cacheDBPath, config.DefaultCacheCapBytes)
	if err != nil {
		return result, fmt.Errorf("init: open cache: %w", err)
	}
	defer cacheStore.Close()

	// 5. Detect repo name.
	repoName := cfg.Repo
	if repoName == "" {
		repoName = detectRepoName(repoRoot)
	}

	// 6. Walk and extract.
	excludeSet := buildExcludeSet(cfg.Exclude)

	files, err := discoverFiles(repoRoot, excludeSet, registry)
	if err != nil {
		return result, fmt.Errorf("init: discover files: %w", err)
	}
	result.FilesScanned = len(files)

	// Detect languages from discovered files.
	langSet := make(map[string]bool)
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		langCfg := registry.LookupByExtension(ext)
		if langCfg != nil {
			langSet[langCfg.Language] = true
		}
	}
	for l := range langSet {
		result.Languages = append(result.Languages, l)
	}

	// If config specifies languages, filter to those only.
	var langFilter map[string]bool
	if len(cfg.Languages) > 0 {
		langFilter = make(map[string]bool, len(cfg.Languages))
		for _, l := range cfg.Languages {
			langFilter[l] = true
		}
		result.Languages = cfg.Languages
	}

	logf(opts.Writer, "Scanning %d files...\n", len(files))

	for _, relPath := range files {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		// If language filter is set, check it.
		if langFilter != nil {
			ext := strings.ToLower(filepath.Ext(relPath))
			langCfg := registry.LookupByExtension(ext)
			if langCfg == nil || !langFilter[langCfg.Language] {
				result.FilesSkipped++
				continue
			}
		}

		claims, err := extractAndStore(ctx, repoRoot, relPath, repoName, registry, claimsDB, cacheStore)
		if err != nil {
			var langErr *extractor.LanguageNotRegisteredError
			if errors.As(err, &langErr) {
				result.FilesSkipped++
				continue
			}
			result.Errors = append(result.Errors, FileError{Path: relPath, Err: err})
			continue
		}

		result.FilesExtracted++
		result.ClaimsStored += claims
	}

	result.Duration = time.Since(start)

	logf(opts.Writer, "Done: %d files extracted, %d claims stored, %d skipped, %d errors (%s)\n",
		result.FilesExtracted, result.ClaimsStored, result.FilesSkipped, len(result.Errors), result.Duration.Truncate(time.Millisecond))

	return result, nil
}

// loadOrCreateConfig loads existing config or creates a new one.
// Returns the config, whether it was created, and any error.
func loadOrCreateConfig(cfgPath, repoRoot string, force bool) (config.Config, bool, error) {
	if !force {
		if _, err := os.Stat(cfgPath); err == nil {
			cfg, err := config.Load(cfgPath)
			return cfg, false, err
		}
	}

	// Auto-detect languages from file extensions in repo.
	detected := detectLanguages(repoRoot)

	content := config.DefaultYAML(detected)
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		return config.Config{}, false, fmt.Errorf("write config: %w", err)
	}

	cfg, err := config.Load(cfgPath)
	return cfg, true, err
}

// detectLanguages scans the repo root (1 level deep for speed) and returns
// detected language names based on file extensions.
func detectLanguages(repoRoot string) []string {
	langRegistry := lang.NewRegistry()
	seen := make(map[string]bool)

	// Walk top-level and one level deep for a quick sample.
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() {
			if skipDirs[entry.Name()] {
				continue
			}
			subEntries, err := os.ReadDir(filepath.Join(repoRoot, entry.Name()))
			if err != nil {
				continue
			}
			for _, sub := range subEntries {
				if sub.IsDir() {
					continue
				}
				ext := strings.ToLower(filepath.Ext(sub.Name()))
				if cfg, ok := langRegistry.LookupByExtension(ext); ok {
					seen[cfg.Language] = true
				}
			}
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if cfg, ok := langRegistry.LookupByExtension(ext); ok {
			seen[cfg.Language] = true
		}
	}

	languages := make([]string, 0, len(seen))
	for l := range seen {
		languages = append(languages, l)
	}
	return languages
}

// detectRepoName tries to determine a repo name from the directory name.
func detectRepoName(repoRoot string) string {
	return filepath.Base(repoRoot)
}

// buildExcludeSet creates a fast-lookup set from exclude patterns.
func buildExcludeSet(patterns []string) map[string]bool {
	set := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		set[p] = true
	}
	return set
}

// discoverFiles walks the repo and returns relative paths of extractable files.
func discoverFiles(repoRoot string, excludeSet map[string]bool, registry *extractor.Registry) ([]string, error) {
	var files []string
	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return err
		}
		if info.IsDir() {
			if excludeSet[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if registry.LookupByExtension(ext) == nil {
			return nil // not a recognized file type
		}

		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

// extractAndStore extracts claims from a single file and stores them in the DB.
// Returns the number of claims stored.
func extractAndStore(
	ctx context.Context,
	repoRoot, relPath, repoName string,
	registry *extractor.Registry,
	claimsDB *db.ClaimsDB,
	cacheStore cache.Store,
) (int, error) {
	absPath := filepath.Join(repoRoot, relPath)

	// Read file and compute hash.
	content, err := os.ReadFile(absPath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", relPath, err)
	}
	contentHash := fmt.Sprintf("%x", sha256.Sum256(content))

	// Determine extractor info for cache key.
	ext := strings.ToLower(filepath.Ext(relPath))
	cfg := registry.LookupByExtension(ext)
	if cfg == nil {
		return 0, &extractor.LanguageNotRegisteredError{Key: ext}
	}

	ex := cfg.FastExtractor
	if cfg.DeepExtractor != nil {
		ex = cfg.DeepExtractor
	}
	if ex == nil {
		return 0, &extractor.LanguageNotRegisteredError{Key: cfg.Language}
	}

	extractorVersion := ex.Version()
	grammarVersion := cfg.TreeSitterGrammar

	// Cache check.
	hit, err := cacheStore.Hit(repoName, relPath, contentHash, extractorVersion, grammarVersion)
	if err != nil {
		return 0, fmt.Errorf("cache hit check %s: %w", relPath, err)
	}
	if hit {
		return 0, nil
	}

	// Extract claims.
	claims, err := registry.ExtractFile(ctx, absPath)
	if err != nil {
		return 0, fmt.Errorf("extract %s: %w", relPath, err)
	}

	// Delete old claims for this file before inserting new ones.
	if err := claimsDB.DeleteClaimsByExtractorAndFile(ex.Name(), relPath); err != nil {
		return 0, fmt.Errorf("delete old claims for %s: %w", relPath, err)
	}

	// Store claims.
	stored := 0
	for _, claim := range claims {
		if claim.SubjectRepo == "" {
			claim.SubjectRepo = repoName
		}
		if claim.SubjectImportPath == "" {
			claim.SubjectImportPath = relPath
		}

		symID, err := claimsDB.UpsertSymbol(db.Symbol{
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
			return stored, fmt.Errorf("upsert symbol for %s: %w", relPath, err)
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
			return stored, fmt.Errorf("insert claim for %s: %w", relPath, err)
		}
		stored++
	}

	// Update cache.
	if err := cacheStore.Put(cache.Entry{
		Repo:             repoName,
		RelativePath:     relPath,
		ContentHash:      contentHash,
		ExtractorVersion: extractorVersion,
		GrammarVersion:   grammarVersion,
		LastIndexed:      time.Now(),
		SizeBytes:        int64(len(content)),
	}); err != nil {
		return stored, fmt.Errorf("cache put for %s: %w", relPath, err)
	}

	// Update source_files.
	_, err = claimsDB.UpsertSourceFile(db.SourceFile{
		Repo:             repoName,
		RelativePath:     relPath,
		ContentHash:      contentHash,
		ExtractorVersion: extractorVersion,
		GrammarVersion:   grammarVersion,
		LastIndexed:      db.Now(),
	})
	if err != nil {
		return stored, fmt.Errorf("upsert source file for %s: %w", relPath, err)
	}

	return stored, nil
}

func logf(w io.Writer, format string, args ...interface{}) {
	if w != nil {
		fmt.Fprintf(w, format, args...)
	}
}
