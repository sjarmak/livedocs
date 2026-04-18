// Package check implements zero-config documentation drift detection.
//
// It discovers markdown files in a repository, pairs them with their
// adjacent code directories, and runs drift detection to find stale
// symbol references, undocumented exports, and broken package paths.
package check

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sjarmak/livedocs/aicontext"
	"github.com/sjarmak/livedocs/drift"
)

// skipDirs are directories to skip during markdown file discovery.
var skipDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	"_output":      true,
	".livedocs":    true,
	// Go convention for test fixtures; unlikely to contain real docs.
	"testdata": true,
}

// defaultDocPatterns are filename patterns that identify documentation files.
// Only files matching these patterns are scanned by default (not all *.md).
var defaultDocPatterns = []string{
	"README.md",
	"README.*.md", // README.zh.md, etc.
	"CLAUDE.md",
	"AGENTS.md",
	"CONTRIBUTING.md",
	"CHANGELOG.md",
	"SECURITY.md",
	"CODE_OF_CONDUCT.md",
	"ARCHITECTURE.md",
}

// FindOptions configures how documentation files are discovered.
type FindOptions struct {
	// AllMarkdown scans all *.md files (legacy behavior).
	AllMarkdown bool
	// IncludePatterns overrides defaultDocPatterns when non-empty.
	IncludePatterns []string
	// ExcludePatterns excludes files/dirs matching these patterns.
	// Patterns ending in "/" match directory prefixes.
	ExcludePatterns []string
}

// FindDocFiles discovers documentation files under root using sensible defaults.
// By default only files matching defaultDocPatterns are returned.
// A .livedocsignore file in root provides additional exclude patterns.
func FindDocFiles(root string, opts FindOptions) ([]string, error) {
	// Load .livedocsignore if present.
	ignorePatterns := loadIgnoreFile(root)
	allExcludes := make([]string, 0, len(ignorePatterns)+len(opts.ExcludePatterns))
	allExcludes = append(allExcludes, ignorePatterns...)
	allExcludes = append(allExcludes, opts.ExcludePatterns...)

	// Determine which filename patterns to match.
	patterns := defaultDocPatterns
	if len(opts.IncludePatterns) > 0 {
		patterns = opts.IncludePatterns
	}

	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return err
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			// Check exclude patterns for directory prefixes.
			rel, relErr := filepath.Rel(root, path)
			if relErr == nil && isExcludedPath(rel, allExcludes) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			return nil
		}

		// Check path-based excludes.
		rel, relErr := filepath.Rel(root, path)
		if relErr == nil && isExcludedPath(rel, allExcludes) {
			return nil
		}

		// If AllMarkdown, include all .md files (legacy behavior).
		if opts.AllMarkdown {
			files = append(files, path)
			return nil
		}

		// Match against doc patterns.
		name := info.Name()
		for _, p := range patterns {
			if matched, _ := filepath.Match(p, name); matched {
				files = append(files, path)
				return nil
			}
		}
		return nil
	})
	return files, err
}

// loadIgnoreFile reads .livedocsignore from root and returns parsed patterns.
func loadIgnoreFile(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, ".livedocsignore"))
	if err != nil {
		return nil
	}
	return parseIgnorePatterns(string(data))
}

// parseIgnorePatterns parses gitignore-style patterns from content.
// Blank lines and lines starting with # are skipped.
func parseIgnorePatterns(content string) []string {
	var patterns []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "..") {
			continue
		}
		cleanPattern := strings.TrimSuffix(line, "/")
		if _, err := filepath.Match(cleanPattern, "test"); err != nil {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// isExcludedPath checks whether relPath matches any exclude pattern.
// Patterns ending in "/" match directory prefixes.
// Other patterns are matched against the filename using filepath.Match.
func isExcludedPath(relPath string, excludes []string) bool {
	for _, pattern := range excludes {
		if strings.HasSuffix(pattern, "/") {
			// Directory prefix match.
			dirPrefix := strings.TrimSuffix(pattern, "/")
			if relPath == dirPrefix || strings.HasPrefix(relPath, dirPrefix+string(filepath.Separator)) {
				return true
			}
			continue
		}
		if strings.ContainsRune(pattern, '/') {
			matched, _ := filepath.Match(pattern, relPath)
			if matched {
				return true
			}
		} else {
			name := filepath.Base(relPath)
			matched, _ := filepath.Match(pattern, name)
			if matched {
				return true
			}
		}
	}
	return false
}

// Result aggregates drift detection results across all markdown files.
type Result struct {
	HasDrift           bool              `json:"has_drift"`
	TotalStale         int               `json:"total_stale"`
	TotalUndocumented  int               `json:"total_undocumented"`
	TotalStalePackages int               `json:"total_stale_packages"`
	Reports            []*drift.Report   `json:"reports"`
	AIContext          *aicontext.Report `json:"ai_context,omitempty"`
}

// FindMarkdownFiles walks root and returns all *.md file paths,
// skipping common non-source directories.
func FindMarkdownFiles(root string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if info != nil && info.IsDir() {
				// Skip unreadable directories but continue walking.
				return filepath.SkipDir
			}
			return err
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// DiscoverTargets finds markdown files and pairs each with its directory
// as the code directory for drift detection.
func DiscoverTargets(root string) ([]drift.Target, error) {
	return DiscoverTargetsWithOptions(root, FindOptions{})
}

// DiscoverTargetsWithOptions finds doc files using the given options and pairs
// each with its directory as the code directory for drift detection.
func DiscoverTargetsWithOptions(root string, opts FindOptions) ([]drift.Target, error) {
	mdFiles, err := FindDocFiles(root, opts)
	if err != nil {
		return nil, fmt.Errorf("find doc files: %w", err)
	}

	targets := make([]drift.Target, len(mdFiles))
	for i, f := range mdFiles {
		targets[i] = drift.Target{
			ReadmePath: f,
			CodeDir:    filepath.Dir(f),
		}
	}
	return targets, nil
}

// RunWithOptions executes drift detection with the given find options.
func RunWithOptions(ctx context.Context, path string, opts FindOptions) (*Result, error) {
	targets, err := DiscoverTargetsWithOptions(path, opts)
	if err != nil {
		return nil, fmt.Errorf("discover targets: %w", err)
	}
	return runWithTargets(ctx, path, targets)
}

// Run executes drift detection on doc files under path using default patterns.
func Run(ctx context.Context, path string) (*Result, error) {
	targets, err := DiscoverTargets(path)
	if err != nil {
		return nil, fmt.Errorf("discover targets: %w", err)
	}
	return runWithTargets(ctx, path, targets)
}

// runWithTargets runs drift detection on the given targets.
func runWithTargets(ctx context.Context, path string, targets []drift.Target) (*Result, error) {
	if len(targets) == 0 {
		// No markdown files, but still check AI context files.
		aiReport, aiErr := aicontext.Check(path)
		if aiErr != nil {
			aiReport = &aicontext.Report{Root: path}
		}
		return &Result{
			AIContext: aiReport,
			HasDrift:  aiReport.HasDrift(),
		}, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	reports, err := drift.DetectMultiple(targets)
	if err != nil {
		return nil, fmt.Errorf("detect drift: %w", err)
	}

	result := &Result{Reports: reports}
	for _, r := range reports {
		result.TotalStale += r.StaleCount
		result.TotalUndocumented += r.UndocumentedCount
		result.TotalStalePackages += r.StalePackageCount
	}

	// Check AI context files (CLAUDE.md, AGENTS.md, .cursorrules, etc.).
	aiReport, err := aicontext.Check(path)
	if err != nil {
		// Non-fatal: AI context checking is best-effort.
		aiReport = &aicontext.Report{Root: path}
	}
	result.AIContext = aiReport

	// HasDrift triggers exit code 1 only for high-confidence structural issues
	// (stale references, broken package paths, stale AI context references).
	// Undocumented exports are informational.
	result.HasDrift = result.TotalStale > 0 || result.TotalStalePackages > 0 || aiReport.HasDrift()

	return result, nil
}

// FormatText formats a Result as human-readable text.
func FormatText(result *Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Drift Report\n\n")

	for _, r := range result.Reports {
		b.WriteString(drift.FormatReport(r))
	}

	// Include AI context report if present.
	if result.AIContext != nil && len(result.AIContext.Files) > 0 {
		b.WriteString(aicontext.FormatReport(result.AIContext))
	}

	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "**Total**: %d stale, %d undocumented, %d stale packages\n",
		result.TotalStale, result.TotalUndocumented, result.TotalStalePackages)
	if result.AIContext != nil && result.AIContext.StaleCount > 0 {
		fmt.Fprintf(&b, "**AI Context**: %d stale reference(s) in %d file(s)\n",
			result.AIContext.StaleCount, len(result.AIContext.Files))
	}

	if result.HasDrift {
		fmt.Fprintf(&b, "\nDrift detected.\n")
	} else {
		fmt.Fprintf(&b, "\nNo stale references found.\n")
	}

	return b.String()
}
