package check

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
