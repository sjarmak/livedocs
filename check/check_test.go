package check

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeFixture creates a temp directory with Go files and a README for testing.
func makeFixture(t *testing.T, readme string, goFiles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0644); err != nil {
		t.Fatal(err)
	}
	for name, content := range goFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFindMarkdownFiles(t *testing.T) {
	dir := t.TempDir()

	// Create nested structure.
	os.MkdirAll(filepath.Join(dir, "pkg", "sub"), 0755)
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.MkdirAll(filepath.Join(dir, "vendor"), 0755)
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0755)

	// Create markdown files.
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Root"), 0644)
	os.WriteFile(filepath.Join(dir, "pkg", "README.md"), []byte("# Pkg"), 0644)
	os.WriteFile(filepath.Join(dir, "pkg", "sub", "DESIGN.md"), []byte("# Design"), 0644)

	// These should be skipped.
	os.WriteFile(filepath.Join(dir, ".git", "README.md"), []byte("# Git"), 0644)
	os.WriteFile(filepath.Join(dir, "vendor", "README.md"), []byte("# Vendor"), 0644)
	os.WriteFile(filepath.Join(dir, "node_modules", "README.md"), []byte("# Node"), 0644)

	files, err := FindMarkdownFiles(dir)
	if err != nil {
		t.Fatalf("FindMarkdownFiles: %v", err)
	}

	if len(files) != 3 {
		t.Errorf("expected 3 markdown files, got %d: %v", len(files), files)
	}

	// Verify skipped dirs are not included.
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f)
		if rel == filepath.Join(".git", "README.md") || rel == filepath.Join("vendor", "README.md") || rel == filepath.Join("node_modules", "README.md") {
			t.Errorf("should have skipped %s", rel)
		}
	}
}

func TestDiscoverTargets(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Root"), 0644)
	os.WriteFile(filepath.Join(dir, "pkg", "README.md"), []byte("# Pkg"), 0644)

	targets, err := DiscoverTargets(dir)
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}

	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}

	for _, tgt := range targets {
		if tgt.CodeDir == "" {
			t.Errorf("target %s has empty CodeDir", tgt.ReadmePath)
		}
		// CodeDir should be the directory containing the README.
		expectedDir := filepath.Dir(tgt.ReadmePath)
		if tgt.CodeDir != expectedDir {
			t.Errorf("target %s: CodeDir=%s, want %s", tgt.ReadmePath, tgt.CodeDir, expectedDir)
		}
	}
}

func TestRun_NoDrift(t *testing.T) {
	readme := "# Package\n\nThis package provides `MyFunc` for processing.\n"
	goCode := map[string]string{
		"mycode.go": "package mypkg\n\nfunc MyFunc() {}\n",
	}
	dir := makeFixture(t, readme, goCode)

	result, err := Run(context.Background(), dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.HasDrift {
		// Check what drift was found.
		for _, r := range result.Reports {
			for _, f := range r.Findings {
				t.Logf("  finding: %s %s: %s", f.Kind, f.Symbol, f.Detail)
			}
		}
		t.Errorf("expected no drift, but HasDrift=true (stale=%d, undoc=%d)",
			result.TotalStale, result.TotalUndocumented)
	}
}

func TestRun_WithDrift(t *testing.T) {
	// README references RemovedFunc which doesn't exist in code.
	readme := "# MyPkg\n\nUse `RemovedFunc` and `ExistingFunc` to do things.\n"
	goCode := map[string]string{
		"mycode.go": "package mypkg\n\nfunc ExistingFunc() {}\n\nfunc AnotherExport() {}\n",
	}
	dir := makeFixture(t, readme, goCode)

	result, err := Run(context.Background(), dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !result.HasDrift {
		t.Fatal("expected drift, got HasDrift=false")
	}
	if result.TotalStale < 1 {
		t.Errorf("expected at least 1 stale reference, got %d", result.TotalStale)
	}
}

func TestResult_JSON(t *testing.T) {
	result := &Result{
		HasDrift:           true,
		TotalStale:         2,
		TotalUndocumented:  1,
		TotalStalePackages: 0,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Result
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.HasDrift != result.HasDrift {
		t.Errorf("HasDrift: got %v, want %v", decoded.HasDrift, result.HasDrift)
	}
	if decoded.TotalStale != result.TotalStale {
		t.Errorf("TotalStale: got %d, want %d", decoded.TotalStale, result.TotalStale)
	}
}

// --- Manifest tests ---

func TestLoadManifest_Valid(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".livedocs"), 0755)
	content := `entries:
  - source: "pkg/*.go"
    docs:
      - "pkg/README.md"
  - source: "cmd/*.go"
    docs:
      - "cmd/README.md"
      - "CONTRIBUTING.md"
`
	os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(content), 0644)

	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m.Entries))
	}
	if m.Entries[0].Source != "pkg/*.go" {
		t.Errorf("entry 0 source: got %q, want %q", m.Entries[0].Source, "pkg/*.go")
	}
	if len(m.Entries[1].Docs) != 2 {
		t.Errorf("entry 1 docs: got %d, want 2", len(m.Entries[1].Docs))
	}
}

func TestLoadManifest_Missing(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for missing manifest, got nil")
	}
}

func TestLoadManifest_Malformed(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".livedocs"), 0755)
	os.WriteFile(filepath.Join(dir, ManifestFileName), []byte("not: [valid: yaml: {{"), 0644)

	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for malformed manifest, got nil")
	}
}

func TestSaveManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := &Manifest{
		Entries: []ManifestEntry{
			{Source: "src/*.go", Docs: []string{"src/README.md"}},
			{Source: "lib/*.py", Docs: []string{"lib/DESIGN.md", "README.md"}},
		},
	}

	if err := SaveManifest(dir, original); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	loaded, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	if len(loaded.Entries) != len(original.Entries) {
		t.Fatalf("entry count: got %d, want %d", len(loaded.Entries), len(original.Entries))
	}
	for i, entry := range loaded.Entries {
		if entry.Source != original.Entries[i].Source {
			t.Errorf("entry %d source: got %q, want %q", i, entry.Source, original.Entries[i].Source)
		}
		if len(entry.Docs) != len(original.Entries[i].Docs) {
			t.Errorf("entry %d docs count: got %d, want %d", i, len(entry.Docs), len(original.Entries[i].Docs))
		}
	}
}

func TestSaveManifest_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{Entries: []ManifestEntry{{Source: "*.go", Docs: []string{"README.md"}}}}

	if err := SaveManifest(dir, m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	// Verify .livedocs dir was created.
	info, err := os.Stat(filepath.Join(dir, ".livedocs"))
	if err != nil {
		t.Fatalf("stat .livedocs: %v", err)
	}
	if !info.IsDir() {
		t.Error(".livedocs is not a directory")
	}
}

func TestGenerateManifest(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg"), 0755)

	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Root"), 0644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "pkg", "README.md"), []byte("# Pkg"), 0644)
	os.WriteFile(filepath.Join(dir, "pkg", "handler.go"), []byte("package pkg"), 0644)
	os.WriteFile(filepath.Join(dir, "pkg", "handler_test.go"), []byte("package pkg"), 0644)

	m, err := GenerateManifest(dir)
	if err != nil {
		t.Fatalf("GenerateManifest: %v", err)
	}

	if len(m.Entries) == 0 {
		t.Fatal("expected entries, got 0")
	}

	// Verify that pkg/*.go → pkg/README.md mapping exists.
	found := false
	for _, entry := range m.Entries {
		if entry.Source == "pkg/*.go" {
			for _, doc := range entry.Docs {
				if doc == "pkg/README.md" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected pkg/*.go → pkg/README.md mapping")
		for _, e := range m.Entries {
			t.Logf("  %s → %v", e.Source, e.Docs)
		}
	}
}

func TestAffectedDocs_GlobMatch(t *testing.T) {
	m := &Manifest{
		Entries: []ManifestEntry{
			{Source: "pkg/*.go", Docs: []string{"pkg/README.md"}},
			{Source: "cmd/*.go", Docs: []string{"cmd/README.md", "CONTRIBUTING.md"}},
			{Source: "*.yaml", Docs: []string{"CONFIG.md"}},
		},
	}

	tests := []struct {
		name    string
		changed []string
		want    []string
	}{
		{
			name:    "single match",
			changed: []string{"pkg/handler.go"},
			want:    []string{"pkg/README.md"},
		},
		{
			name:    "multiple docs from one source",
			changed: []string{"cmd/main.go"},
			want:    []string{"CONTRIBUTING.md", "cmd/README.md"},
		},
		{
			name:    "no match",
			changed: []string{"internal/foo.rs", "docs/guide.txt"},
			want:    nil,
		},
		{
			name:    "root level glob",
			changed: []string{"config.yaml"},
			want:    []string{"CONFIG.md"},
		},
		{
			name:    "multiple changed files",
			changed: []string{"pkg/a.go", "cmd/b.go"},
			want:    []string{"CONTRIBUTING.md", "cmd/README.md", "pkg/README.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.AffectedDocs(tt.changed)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d]=%q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestAffectedDocs_EmptyManifest(t *testing.T) {
	m := &Manifest{}
	got := m.AffectedDocs([]string{"pkg/handler.go"})
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
}

func TestRunManifestWithFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".livedocs"), 0755)
	content := `entries:
  - source: "pkg/*.go"
    docs:
      - "pkg/README.md"
  - source: "cmd/*.go"
    docs:
      - "cmd/README.md"
`
	os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(content), 0644)

	result, err := RunManifestWithFiles(context.Background(), dir, []string{"pkg/handler.go", "unrelated.txt"})
	if err != nil {
		t.Fatalf("RunManifestWithFiles: %v", err)
	}

	if !result.HasAffected {
		t.Fatal("expected HasAffected=true")
	}
	if len(result.AffectedDocs) != 1 {
		t.Fatalf("expected 1 affected doc, got %d: %v", len(result.AffectedDocs), result.AffectedDocs)
	}
	if result.AffectedDocs[0] != "pkg/README.md" {
		t.Errorf("expected pkg/README.md, got %s", result.AffectedDocs[0])
	}
}

func TestRunManifestWithFiles_NoAffected(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".livedocs"), 0755)
	content := `entries:
  - source: "pkg/*.go"
    docs:
      - "pkg/README.md"
`
	os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(content), 0644)

	result, err := RunManifestWithFiles(context.Background(), dir, []string{"unrelated.txt"})
	if err != nil {
		t.Fatalf("RunManifestWithFiles: %v", err)
	}

	if result.HasAffected {
		t.Fatal("expected HasAffected=false")
	}
	if len(result.AffectedDocs) != 0 {
		t.Errorf("expected 0 affected docs, got %v", result.AffectedDocs)
	}
}

func TestManifestNoSQLite(t *testing.T) {
	// Verify manifest-based check works without any SQLite database.
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".livedocs"), 0755)

	// No .db files anywhere in the tree.
	content := `entries:
  - source: "*.go"
    docs:
      - "README.md"
`
	os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(content), 0644)

	result, err := RunManifestWithFiles(context.Background(), dir, []string{"main.go"})
	if err != nil {
		t.Fatalf("RunManifestWithFiles: %v", err)
	}

	// Verify it worked without needing any database.
	if !result.HasAffected {
		t.Error("expected affected docs")
	}

	// Verify no .db files were created.
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if filepath.Ext(path) == ".db" || filepath.Ext(path) == ".sqlite" {
			t.Errorf("SQLite file found: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestManifestPerformance_1000Files(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".livedocs"), 0755)

	// Create a manifest with 100 glob patterns.
	m := &Manifest{}
	for i := 0; i < 100; i++ {
		m.Entries = append(m.Entries, ManifestEntry{
			Source: fmt.Sprintf("pkg%d/*.go", i),
			Docs:   []string{fmt.Sprintf("pkg%d/README.md", i)},
		})
	}
	if err := SaveManifest(dir, m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	// Generate 1000+ changed files.
	changedFiles := make([]string, 1200)
	for i := 0; i < 1200; i++ {
		pkgIdx := i % 100
		changedFiles[i] = fmt.Sprintf("pkg%d/file%d.go", pkgIdx, i)
	}

	start := time.Now()
	result, err := RunManifestWithFiles(context.Background(), dir, changedFiles)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunManifestWithFiles: %v", err)
	}

	if elapsed > 2*time.Second {
		t.Errorf("manifest check took %v, must complete in <2s", elapsed)
	}

	if !result.HasAffected {
		t.Error("expected affected docs with 1200 changed files")
	}

	t.Logf("Performance: %d changed files, %d affected docs, %v elapsed",
		len(changedFiles), len(result.AffectedDocs), elapsed)
}

func TestFormatManifestResult(t *testing.T) {
	r := &ManifestResult{
		AffectedDocs: []string{"pkg/README.md", "cmd/README.md"},
		ChangedFiles: []string{"pkg/handler.go", "cmd/main.go"},
		HasAffected:  true,
	}

	output := FormatManifestResult(r)
	if output == "" {
		t.Fatal("expected non-empty output")
	}
	if !contains(output, "pkg/README.md") {
		t.Error("output should contain pkg/README.md")
	}
	if !contains(output, "Changed files: 2") {
		t.Error("output should contain changed files count")
	}
	if !contains(output, "Affected docs: 2") {
		t.Error("output should contain affected docs count")
	}
}

func TestFormatManifestResult_NoAffected(t *testing.T) {
	r := &ManifestResult{
		ChangedFiles: []string{"unrelated.txt"},
	}
	output := FormatManifestResult(r)
	if !contains(output, "No documentation affected") {
		t.Error("output should indicate no affected docs")
	}
}

func TestParseLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a\nb\nc\n", 3},
		{"  a  \n\n  b  \n", 2},
		{"\n\n\n", 0},
	}
	for _, tt := range tests {
		got := parseLines(tt.input)
		if len(got) != tt.want {
			t.Errorf("parseLines(%q): got %d lines, want %d", tt.input, len(got), tt.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
