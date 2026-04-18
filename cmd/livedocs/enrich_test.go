package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
)

func TestEnrichCmd_NoToken(t *testing.T) {
	// Without SRC_ACCESS_TOKEN, should print message and exit 0.
	t.Setenv("SRC_ACCESS_TOKEN", "")

	dir := t.TempDir()
	seedEnrichDB(t, dir, "testrepo")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"enrich", "--data-dir", dir})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "SRC_ACCESS_TOKEN") {
		t.Errorf("expected token warning, got: %s", output)
	}
}

func TestEnrichCmd_DryRun(t *testing.T) {
	// Dry-run should list symbols and estimated cost without needing a token.
	t.Setenv("SRC_ACCESS_TOKEN", "")

	dir := t.TempDir()
	seedEnrichDB(t, dir, "myrepo")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"enrich", "--data-dir", dir, "--dry-run"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "myrepo") {
		t.Errorf("expected repo name in output, got: %s", output)
	}
	if !strings.Contains(output, "candidate symbol") {
		t.Errorf("expected candidate symbols mention, got: %s", output)
	}
	if !strings.Contains(output, "Enrichment Summary") {
		t.Errorf("expected summary header, got: %s", output)
	}
	if !strings.Contains(output, "dry-run") {
		t.Errorf("expected dry-run mode indicator, got: %s", output)
	}
}

func TestEnrichCmd_DryRunListsSymbols(t *testing.T) {
	t.Setenv("SRC_ACCESS_TOKEN", "")

	dir := t.TempDir()
	seedEnrichDB(t, dir, "symbolrepo")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"enrich", "--data-dir", dir, "--dry-run"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "MyFunc") {
		t.Errorf("expected symbol MyFunc in dry-run output, got: %s", output)
	}
}

func TestEnrichCmd_NoDBFiles(t *testing.T) {
	t.Setenv("SRC_ACCESS_TOKEN", "test-token")

	dir := t.TempDir()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"enrich", "--data-dir", dir})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No .claims.db files found") {
		t.Errorf("expected no-files message, got: %s", output)
	}
}

func TestEnrichCmd_RequiresDataDir(t *testing.T) {
	// Verify the flag is marked required by checking the flag definition.
	f := enrichCmd.Flags().Lookup("data-dir")
	if f == nil {
		t.Fatal("--data-dir flag not found")
	}
	ann := f.Annotations
	if ann == nil {
		t.Fatal("--data-dir has no annotations (not marked required)")
	}
	if _, ok := ann["cobra_annotation_bash_completion_one_required_flag"]; !ok {
		t.Error("--data-dir not marked as required")
	}
}

func TestEnrichCmd_DryRunWithMaxSymbols(t *testing.T) {
	t.Setenv("SRC_ACCESS_TOKEN", "")

	dir := t.TempDir()
	seedEnrichDBMultiple(t, dir, "bigrepo", 10)

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"enrich", "--data-dir", dir, "--dry-run", "--max-symbols", "3"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "3 candidate symbols") {
		t.Errorf("expected 3 candidate symbols, got: %s", output)
	}
}

// seedEnrichDB creates a claims database with a single public function symbol.
func seedEnrichDB(t *testing.T, dir, repoName string) {
	t.Helper()
	dbPath := filepath.Join(dir, repoName+".claims.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open claims DB: %v", err)
	}
	defer cdb.Close()

	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	_, err = cdb.UpsertSymbol(db.Symbol{
		Repo:       repoName,
		ImportPath: repoName + "/pkg",
		SymbolName: "MyFunc",
		Language:   "go",
		Kind:       string(extractor.KindFunc),
		Visibility: string(extractor.VisibilityPublic),
	})
	if err != nil {
		t.Fatalf("insert symbol: %v", err)
	}
}

// seedEnrichDBMultiple creates a claims database with n public function symbols.
func seedEnrichDBMultiple(t *testing.T, dir, repoName string, n int) {
	t.Helper()
	dbPath := filepath.Join(dir, repoName+".claims.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open claims DB: %v", err)
	}
	defer cdb.Close()

	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	for i := 0; i < n; i++ {
		_, err = cdb.UpsertSymbol(db.Symbol{
			Repo:       repoName,
			ImportPath: repoName + "/pkg",
			SymbolName: "Func" + string(rune('A'+i)),
			Language:   "go",
			Kind:       string(extractor.KindFunc),
			Visibility: string(extractor.VisibilityPublic),
		})
		if err != nil {
			t.Fatalf("insert symbol %d: %v", i, err)
		}
	}
}
