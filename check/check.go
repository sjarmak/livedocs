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

	"github.com/live-docs/live_docs/aicontext"
	"github.com/live-docs/live_docs/drift"
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
	mdFiles, err := FindMarkdownFiles(root)
	if err != nil {
		return nil, fmt.Errorf("find markdown files: %w", err)
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

// Run executes drift detection on all markdown files under path.
func Run(ctx context.Context, path string) (*Result, error) {
	targets, err := DiscoverTargets(path)
	if err != nil {
		return nil, fmt.Errorf("discover targets: %w", err)
	}

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
