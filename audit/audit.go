// Package audit generates compliance audit reports of documentation freshness.
//
// Reports are designed to serve as evidence artifacts for SOC 2 / ISO 27001
// audits, tracing every documentation claim back to its source commit and
// recording freshness status at a specific point in time.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/live-docs/live_docs/check"
	"github.com/live-docs/live_docs/drift"
)

// ReportVersion is the schema version for audit reports. Bump when the
// JSON structure changes in a backwards-incompatible way.
const ReportVersion = "1.0.0"

// Report is the top-level compliance audit report.
type Report struct {
	Version     string      `json:"version"`
	GeneratedAt time.Time   `json:"generated_at"`
	Repo        RepoInfo    `json:"repo"`
	Summary     Summary     `json:"summary"`
	Files       []FileAudit `json:"files"`
}

// RepoInfo captures repository metadata at report generation time.
type RepoInfo struct {
	Path      string `json:"path"`
	CommitSHA string `json:"commit_sha"`
	Branch    string `json:"branch"`
	Remote    string `json:"remote,omitempty"`
}

// Summary provides aggregate freshness metrics.
type Summary struct {
	TotalFiles         int     `json:"total_files"`
	FilesWithDrift     int     `json:"files_with_drift"`
	TotalStale         int     `json:"total_stale"`
	TotalUndocumented  int     `json:"total_undocumented"`
	TotalStalePackages int     `json:"total_stale_packages"`
	FreshnessPercent   float64 `json:"freshness_percent"`
}

// FileAudit represents the audit state of a single documentation file.
type FileAudit struct {
	Path              string    `json:"path"`
	CodeDir           string    `json:"code_dir"`
	StaleCount        int       `json:"stale_count"`
	UndocumentedCount int       `json:"undocumented_count"`
	StalePackageCount int       `json:"stale_package_count"`
	IsFresh           bool      `json:"is_fresh"`
	Findings          []Finding `json:"findings,omitempty"`
}

// Finding is a single audit finding with commit tracing.
type Finding struct {
	Kind       string `json:"kind"`
	Symbol     string `json:"symbol"`
	SourceFile string `json:"source_file"`
	Detail     string `json:"detail"`
}

// Generate produces an audit report for the repository at the given path.
func Generate(path string, now time.Time) (*Report, error) {
	repoInfo := resolveRepoInfo(path)

	result, err := check.Run(context.Background(), path)
	if err != nil {
		return nil, fmt.Errorf("run check: %w", err)
	}

	report := &Report{
		Version:     ReportVersion,
		GeneratedAt: now.UTC(),
		Repo:        repoInfo,
	}

	report.Files = buildFileAudits(result.Reports)
	report.Summary = buildSummary(result, report.Files)

	return report, nil
}

func buildFileAudits(reports []*drift.Report) []FileAudit {
	audits := make([]FileAudit, 0, len(reports))
	for _, r := range reports {
		fa := FileAudit{
			Path:              r.ReadmePath,
			CodeDir:           r.PackageDir,
			StaleCount:        r.StaleCount,
			UndocumentedCount: r.UndocumentedCount,
			StalePackageCount: r.StalePackageCount,
			IsFresh:           r.StaleCount == 0 && r.StalePackageCount == 0,
		}
		fa.Findings = make([]Finding, 0, len(r.Findings))
		for _, f := range r.Findings {
			fa.Findings = append(fa.Findings, Finding{
				Kind:       string(f.Kind),
				Symbol:     f.Symbol,
				SourceFile: f.SourceFile,
				Detail:     f.Detail,
			})
		}
		audits = append(audits, fa)
	}
	return audits
}

func buildSummary(result *check.Result, files []FileAudit) Summary {
	driftCount := 0
	for _, f := range files {
		if !f.IsFresh {
			driftCount++
		}
	}

	freshness := 100.0
	if len(files) > 0 {
		freshness = float64(len(files)-driftCount) / float64(len(files)) * 100
	}

	return Summary{
		TotalFiles:         len(files),
		FilesWithDrift:     driftCount,
		TotalStale:         result.TotalStale,
		TotalUndocumented:  result.TotalUndocumented,
		TotalStalePackages: result.TotalStalePackages,
		FreshnessPercent:   freshness,
	}
}

// WriteJSON writes the report as indented JSON to the given writer.
func WriteJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteMarkdown writes the report as a human-readable markdown document.
func WriteMarkdown(w io.Writer, r *Report) error {
	var b strings.Builder

	fmt.Fprintf(&b, "# Documentation Audit Report\n\n")
	fmt.Fprintf(&b, "**Generated**: %s\n\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "## Repository\n\n")
	fmt.Fprintf(&b, "| Field | Value |\n")
	fmt.Fprintf(&b, "|-------|-------|\n")
	fmt.Fprintf(&b, "| Path | `%s` |\n", r.Repo.Path)
	fmt.Fprintf(&b, "| Commit | `%s` |\n", r.Repo.CommitSHA)
	fmt.Fprintf(&b, "| Branch | `%s` |\n", r.Repo.Branch)
	if r.Repo.Remote != "" {
		fmt.Fprintf(&b, "| Remote | `%s` |\n", r.Repo.Remote)
	}

	fmt.Fprintf(&b, "\n## Summary\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n")
	fmt.Fprintf(&b, "|--------|-------|\n")
	fmt.Fprintf(&b, "| Total doc files | %d |\n", r.Summary.TotalFiles)
	fmt.Fprintf(&b, "| Files with drift | %d |\n", r.Summary.FilesWithDrift)
	fmt.Fprintf(&b, "| Stale references | %d |\n", r.Summary.TotalStale)
	fmt.Fprintf(&b, "| Undocumented exports | %d |\n", r.Summary.TotalUndocumented)
	fmt.Fprintf(&b, "| Stale package refs | %d |\n", r.Summary.TotalStalePackages)
	fmt.Fprintf(&b, "| Freshness | %.1f%% |\n", r.Summary.FreshnessPercent)

	if len(r.Files) > 0 {
		fmt.Fprintf(&b, "\n## File Details\n\n")
		for _, f := range r.Files {
			status := "FRESH"
			if !f.IsFresh {
				status = "DRIFT"
			}
			fmt.Fprintf(&b, "### %s [%s]\n\n", f.Path, status)
			fmt.Fprintf(&b, "- Code directory: `%s`\n", f.CodeDir)
			fmt.Fprintf(&b, "- Stale: %d | Undocumented: %d | Stale packages: %d\n\n",
				f.StaleCount, f.UndocumentedCount, f.StalePackageCount)

			if len(f.Findings) > 0 {
				fmt.Fprintf(&b, "| Kind | Symbol | Detail |\n")
				fmt.Fprintf(&b, "|------|--------|--------|\n")
				for _, finding := range f.Findings {
					fmt.Fprintf(&b, "| %s | `%s` | %s |\n",
						finding.Kind, finding.Symbol, finding.Detail)
				}
				b.WriteString("\n")
			}
		}
	}

	fmt.Fprintf(&b, "---\n\n")
	fmt.Fprintf(&b, "*Report version: %s*\n", r.Version)

	_, err := io.WriteString(w, b.String())
	return err
}

// resolveRepoInfo attempts to read git metadata from the given path.
// Returns partial info if git commands fail (non-git repos are valid).
func resolveRepoInfo(path string) RepoInfo {
	info := RepoInfo{Path: path}

	if sha, err := gitCommand(path, "rev-parse", "HEAD"); err == nil {
		info.CommitSHA = sha
	}
	if branch, err := gitCommand(path, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		info.Branch = branch
	}
	if remote, err := gitCommand(path, "config", "--get", "remote.origin.url"); err == nil {
		info.Remote = remote
	}

	return info
}

func gitCommand(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
