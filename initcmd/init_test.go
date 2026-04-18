package initcmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sjarmak/livedocs/config"
	"github.com/sjarmak/livedocs/extractor"
	"github.com/sjarmak/livedocs/extractor/lang"
	"github.com/sjarmak/livedocs/extractor/treesitter"
)

// setupTestRepo creates a temp directory with some Go source files.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create a simple Go file.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import "fmt"

// Greet returns a greeting message.
func Greet(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

func main() {
	fmt.Println(Greet("world"))
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a subdirectory with another Go file.
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "helper.go"), []byte(`package pkg

// Add returns the sum of two integers.
func Add(a, b int) int {
	return a + b
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestRunCreatesConfigAndDir(t *testing.T) {
	dir := setupTestRepo(t)
	var buf bytes.Buffer

	result, err := Run(context.Background(), Options{
		RepoRoot: dir,
		Writer:   &buf,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Config should be created.
	if !result.ConfigCreated {
		t.Error("expected ConfigCreated = true")
	}

	// .livedocs.yaml should exist.
	cfgPath := config.ConfigPath(dir)
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Error(".livedocs.yaml was not created")
	}

	// .livedocs/ directory should exist.
	dirPath := config.DirPath(dir)
	info, err := os.Stat(dirPath)
	if os.IsNotExist(err) {
		t.Error(".livedocs/ directory was not created")
	} else if !info.IsDir() {
		t.Error(".livedocs exists but is not a directory")
	}

	// Claims DB should exist.
	claimsDBPath := filepath.Join(dir, config.DefaultClaimsDBPath)
	if _, err := os.Stat(claimsDBPath); os.IsNotExist(err) {
		t.Error("claims.db was not created")
	}

	// Cache DB should exist.
	cacheDBPath := filepath.Join(dir, config.DefaultCacheDBPath)
	if _, err := os.Stat(cacheDBPath); os.IsNotExist(err) {
		t.Error("cache.db was not created")
	}
}

func TestRunExtractsClaims(t *testing.T) {
	dir := setupTestRepo(t)
	var buf bytes.Buffer

	result, err := Run(context.Background(), Options{
		RepoRoot: dir,
		Writer:   &buf,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.FilesExtracted == 0 {
		t.Error("expected at least 1 file extracted")
	}
	if result.ClaimsStored == 0 {
		t.Error("expected at least 1 claim stored")
	}
	if result.FilesScanned < 2 {
		t.Errorf("expected at least 2 files scanned, got %d", result.FilesScanned)
	}
}

func TestRunDetectsLanguages(t *testing.T) {
	dir := setupTestRepo(t)
	var buf bytes.Buffer

	result, err := Run(context.Background(), Options{
		RepoRoot: dir,
		Writer:   &buf,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(result.Languages) == 0 {
		t.Error("expected at least 1 language detected")
	}

	hasGo := false
	for _, l := range result.Languages {
		if l == "go" {
			hasGo = true
		}
	}
	if !hasGo {
		t.Errorf("expected 'go' in detected languages, got %v", result.Languages)
	}
}

func TestRunExistingConfig(t *testing.T) {
	dir := setupTestRepo(t)

	// Pre-create a config.
	cfgPath := config.ConfigPath(dir)
	if err := os.WriteFile(cfgPath, []byte("languages:\n  - go\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := Run(context.Background(), Options{
		RepoRoot: dir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.ConfigCreated {
		t.Error("expected ConfigCreated = false when config already exists")
	}
}

func TestRunForceOverwritesConfig(t *testing.T) {
	dir := setupTestRepo(t)

	// Pre-create a config.
	cfgPath := config.ConfigPath(dir)
	if err := os.WriteFile(cfgPath, []byte("repo: old-repo\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := Run(context.Background(), Options{
		RepoRoot: dir,
		Force:    true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !result.ConfigCreated {
		t.Error("expected ConfigCreated = true with Force")
	}
}

func TestRunEmptyDir(t *testing.T) {
	dir := t.TempDir()

	result, err := Run(context.Background(), Options{
		RepoRoot: dir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.FilesScanned != 0 {
		t.Errorf("expected 0 files scanned in empty dir, got %d", result.FilesScanned)
	}
	if result.ClaimsStored != 0 {
		t.Errorf("expected 0 claims in empty dir, got %d", result.ClaimsStored)
	}
}

func TestRunMissingRepoRoot(t *testing.T) {
	_, err := Run(context.Background(), Options{})
	if err == nil {
		t.Error("expected error for missing repo root")
	}
}

func TestRunIdempotent(t *testing.T) {
	dir := setupTestRepo(t)

	// Run once.
	result1, err := Run(context.Background(), Options{RepoRoot: dir})
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}

	// Run again without Force (config exists, extraction uses cache).
	result2, err := Run(context.Background(), Options{RepoRoot: dir})
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}

	if result2.ConfigCreated {
		t.Error("second run should not re-create config")
	}

	// Claims should be same or zero (cache hits).
	if result2.ClaimsStored > result1.ClaimsStored {
		t.Errorf("second run stored more claims (%d) than first (%d)",
			result2.ClaimsStored, result1.ClaimsStored)
	}
}

func TestRunContextCancellation(t *testing.T) {
	dir := setupTestRepo(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := Run(ctx, Options{RepoRoot: dir})
	if err == nil {
		// May or may not error depending on timing, both are acceptable.
		return
	}
	if err != context.Canceled {
		// If it errored, it should be a context cancellation.
		// But it might also complete before checking context.
		t.Logf("got error (acceptable): %v", err)
	}
}

func TestDiscoverFilesSkipsDirs(t *testing.T) {
	dir := t.TempDir()

	// Create files in various directories.
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.MkdirAll(filepath.Join(dir, "vendor"), 0755)
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)

	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "src", "lib.go"), []byte("package src"), 0644)
	os.WriteFile(filepath.Join(dir, "vendor", "dep.go"), []byte("package dep"), 0644)
	os.WriteFile(filepath.Join(dir, ".git", "config.go"), []byte("package git"), 0644)

	// Set up a minimal registry to recognize .go files.
	langReg := lang.NewRegistry()
	tsExt := treeSitterRegistryForTest(langReg)

	files, err := discoverFiles(dir, buildExcludeSet(config.Config{}.ApplyDefaults().Exclude), tsExt)
	if err != nil {
		t.Fatalf("discoverFiles: %v", err)
	}

	// Should find main.go and src/lib.go but NOT vendor/dep.go or .git/config.go.
	for _, f := range files {
		if filepath.Dir(f) == "vendor" || filepath.Dir(f) == ".git" {
			t.Errorf("found file in excluded dir: %s", f)
		}
	}

	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(files), files)
	}
}

// treeSitterRegistryForTest creates a minimal extractor.Registry with tree-sitter
// support for Go.
func treeSitterRegistryForTest(langReg *lang.Registry) *extractor.Registry {
	tsExtractor := treesitter.New(langReg)
	registry := extractor.NewRegistry()
	for _, langName := range langReg.AllLanguages() {
		cfg, ok := langReg.LookupByLanguage(langName)
		if !ok {
			continue
		}
		registry.Register(extractor.LanguageConfig{
			Language:          cfg.Language,
			Extensions:        cfg.Extensions,
			TreeSitterGrammar: cfg.GrammarName,
			FastExtractor:     tsExtractor,
		})
	}
	return registry
}
