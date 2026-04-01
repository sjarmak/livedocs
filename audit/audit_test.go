package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/live-docs/live_docs/check"
)

func TestGenerate_EmptyRepo(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	report, err := Generate(dir, now)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if report.Version != ReportVersion {
		t.Errorf("version = %q, want %q", report.Version, ReportVersion)
	}
	if !report.GeneratedAt.Equal(now) {
		t.Errorf("generated_at = %v, want %v", report.GeneratedAt, now)
	}
	if report.Summary.TotalFiles != 0 {
		t.Errorf("total_files = %d, want 0", report.Summary.TotalFiles)
	}
	if report.Summary.FreshnessPercent != 100.0 {
		t.Errorf("freshness = %f, want 100.0", report.Summary.FreshnessPercent)
	}
}

func TestGenerate_WithMarkdownAndGoFiles(t *testing.T) {
	dir := t.TempDir()

	// Create a Go file with an exported symbol.
	goContent := `package example

// Foo is an exported function.
func Foo() {}

// Bar is another exported function.
func Bar() {}
`
	if err := os.WriteFile(filepath.Join(dir, "example.go"), []byte(goContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a README that references Foo but not Bar, and references a stale symbol.
	mdContent := "# Example\n\nUse `Foo` for things.\n\nSee also `Baz` for other things.\n"
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(mdContent), 0644); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	report, err := Generate(dir, now)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if report.Summary.TotalFiles != 1 {
		t.Errorf("total_files = %d, want 1", report.Summary.TotalFiles)
	}
	if report.Summary.TotalStale < 1 {
		t.Errorf("total_stale = %d, want >= 1 (Baz is stale)", report.Summary.TotalStale)
	}
	if report.Summary.TotalUndocumented < 1 {
		t.Errorf("total_undocumented = %d, want >= 1 (Bar is undocumented)", report.Summary.TotalUndocumented)
	}

	if len(report.Files) != 1 {
		t.Fatalf("files count = %d, want 1", len(report.Files))
	}
	fa := report.Files[0]
	if fa.IsFresh {
		t.Error("expected file to not be fresh (has stale references)")
	}
	if len(fa.Findings) == 0 {
		t.Error("expected findings, got none")
	}
}

func TestWriteJSON_RoundTrip(t *testing.T) {
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	report := &Report{
		Version:     ReportVersion,
		GeneratedAt: now,
		Repo: RepoInfo{
			Path:      "/tmp/repo",
			CommitSHA: "abc123",
			Branch:    "main",
		},
		Summary: Summary{
			TotalFiles:       2,
			FilesWithDrift:   1,
			TotalStale:       3,
			FreshnessPercent: 50.0,
		},
		Files: []FileAudit{
			{
				Path:       "README.md",
				CodeDir:    ".",
				StaleCount: 3,
				IsFresh:    false,
				Findings: []Finding{
					{Kind: "stale", Symbol: "OldFunc", Detail: "not found"},
				},
			},
			{
				Path:    "docs/guide.md",
				CodeDir: "docs",
				IsFresh: true,
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteJSON(&buf, report); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	var decoded Report
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Version != ReportVersion {
		t.Errorf("version = %q, want %q", decoded.Version, ReportVersion)
	}
	if decoded.Repo.CommitSHA != "abc123" {
		t.Errorf("commit_sha = %q, want %q", decoded.Repo.CommitSHA, "abc123")
	}
	if decoded.Summary.TotalFiles != 2 {
		t.Errorf("total_files = %d, want 2", decoded.Summary.TotalFiles)
	}
	if len(decoded.Files) != 2 {
		t.Fatalf("files count = %d, want 2", len(decoded.Files))
	}
	if decoded.Files[0].Findings[0].Symbol != "OldFunc" {
		t.Errorf("finding symbol = %q, want %q", decoded.Files[0].Findings[0].Symbol, "OldFunc")
	}
}

func TestWriteMarkdown_ContainsExpectedSections(t *testing.T) {
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	report := &Report{
		Version:     ReportVersion,
		GeneratedAt: now,
		Repo: RepoInfo{
			Path:      "/tmp/repo",
			CommitSHA: "abc123",
			Branch:    "main",
			Remote:    "git@github.com:org/repo.git",
		},
		Summary: Summary{
			TotalFiles:       1,
			FilesWithDrift:   1,
			TotalStale:       2,
			FreshnessPercent: 0.0,
		},
		Files: []FileAudit{
			{
				Path:       "README.md",
				CodeDir:    ".",
				StaleCount: 2,
				IsFresh:    false,
				Findings: []Finding{
					{Kind: "stale", Symbol: "Gone", Detail: "not in code"},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, report); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}

	md := buf.String()

	for _, want := range []string{
		"# Documentation Audit Report",
		"abc123",
		"main",
		"git@github.com:org/repo.git",
		"Freshness",
		"0.0%",
		"## File Details",
		"README.md [DRIFT]",
		"`Gone`",
		"Report version: 1.0.0",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}

func TestWriteMarkdown_FreshFile(t *testing.T) {
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	report := &Report{
		Version:     ReportVersion,
		GeneratedAt: now,
		Repo:        RepoInfo{Path: "/tmp/repo"},
		Summary: Summary{
			TotalFiles:       1,
			FreshnessPercent: 100.0,
		},
		Files: []FileAudit{
			{
				Path:    "README.md",
				CodeDir: ".",
				IsFresh: true,
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, report); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}

	md := buf.String()
	if !strings.Contains(md, "[FRESH]") {
		t.Error("expected [FRESH] status for fresh file")
	}
	if !strings.Contains(md, "100.0%") {
		t.Error("expected 100.0% freshness")
	}
}

func TestBuildSummary_Freshness(t *testing.T) {
	tests := []struct {
		name      string
		files     []FileAudit
		wantPct   float64
		wantDrift int
	}{
		{
			name:      "all fresh",
			files:     []FileAudit{{IsFresh: true}, {IsFresh: true}},
			wantPct:   100.0,
			wantDrift: 0,
		},
		{
			name:      "half drifted",
			files:     []FileAudit{{IsFresh: true}, {IsFresh: false}},
			wantPct:   50.0,
			wantDrift: 1,
		},
		{
			name:      "all drifted",
			files:     []FileAudit{{IsFresh: false}, {IsFresh: false}},
			wantPct:   0.0,
			wantDrift: 2,
		},
		{
			name:      "no files",
			files:     nil,
			wantPct:   100.0,
			wantDrift: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a minimal check.Result since buildSummary reads aggregate counts from it.
			result := &check.Result{}
			summary := buildSummary(result, tt.files)

			if summary.FreshnessPercent != tt.wantPct {
				t.Errorf("freshness = %f, want %f", summary.FreshnessPercent, tt.wantPct)
			}
			if summary.FilesWithDrift != tt.wantDrift {
				t.Errorf("files_with_drift = %d, want %d", summary.FilesWithDrift, tt.wantDrift)
			}
		})
	}
}
