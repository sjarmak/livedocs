package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/live-docs/live_docs/db"
)

// resetExportFlags resets global flag state to avoid leaking between tests.
func resetExportFlags() {
	exportFormat = "audit-json"
	exportOutput = ""
	exportRepo = ""
	exportDB = ""
}

func TestExportCommandRegistered(t *testing.T) {
	registered := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		registered[cmd.Name()] = true
	}
	if !registered["export"] {
		t.Error("export subcommand not registered on root command")
	}
}

func TestExportCommandFlags(t *testing.T) {
	if exportCmd.Flags().Lookup("format") == nil {
		t.Error("export command missing --format flag")
	}
	if exportCmd.Flags().Lookup("repo") == nil {
		t.Error("export command missing --repo flag")
	}
	if exportCmd.Flags().Lookup("output") == nil {
		t.Error("export command missing --output flag")
	}
	if exportCmd.Flags().Lookup("db") == nil {
		t.Error("export command missing --db flag")
	}
}

func TestExportMarkdownRequiresRepo(t *testing.T) {
	resetExportFlags()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"export", "--format", "markdown", "/tmp"})
	err := rootCmd.Execute()
	if err == nil {
		t.Error("expected error when --repo is not provided for markdown format")
	}
	if err != nil && !strings.Contains(err.Error(), "--repo is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExportMarkdownFromDB(t *testing.T) {
	resetExportFlags()

	// Create a temporary directory structure with go.mod.
	repoDir := t.TempDir()
	subPkg := filepath.Join(repoDir, "tools", "cache")
	if err := os.MkdirAll(subPkg, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write go.mod at repo root.
	goMod := filepath.Join(repoDir, "go.mod")
	if err := os.WriteFile(goMod, []byte("module k8s.io/client-go\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// Create and seed a claims DB.
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "client-go.claims.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	importPath := "k8s.io/client-go/tools/cache"
	repo := "client-go"

	// Seed interface.
	storeID := mustUpsertSymbolCmd(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "Store",
		Language: "go", Kind: "type", Visibility: "public",
	})
	mustInsertClaimCmd(t, cdb, newStructuralClaimCmd(storeID, "has_kind", "interface", "store.go"))
	mustInsertClaimCmd(t, cdb, newStructuralClaimCmd(storeID, "encloses", "Add", "store.go"))
	mustInsertClaimCmd(t, cdb, newStructuralClaimCmd(storeID, "encloses", "Delete", "store.go"))

	// Seed concrete type implementing the interface.
	deltaID := mustUpsertSymbolCmd(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "DeltaFIFO",
		Language: "go", Kind: "type", Visibility: "public",
	})
	mustInsertClaimCmd(t, cdb, newStructuralClaimCmd(deltaID, "has_kind", "struct", "delta_fifo.go"))
	mustInsertClaimCmd(t, cdb, newStructuralClaimCmd(deltaID, "implements", "Store", "delta_fifo.go"))

	// Seed forward dependencies (cross-package references).
	pkgID := mustUpsertSymbolCmd(t, cdb, db.Symbol{
		Repo: repo, ImportPath: importPath, SymbolName: "_package_",
		Language: "go", Kind: "module", Visibility: "public",
	})
	mustInsertClaimCmd(t, cdb, newStructuralClaimCmd(pkgID, "imports", "k8s.io/apimachinery/pkg/runtime", "store.go"))
	mustInsertClaimCmd(t, cdb, newStructuralClaimCmd(pkgID, "imports", "k8s.io/klog/v2", "store.go"))

	// Seed reverse dependencies (used by).
	mustInsertClaimCmd(t, cdb, newStructuralClaimCmd(pkgID, "exports", "reverse_dep:k8s.io/kubernetes/pkg/kubelet", "store.go"))
	mustInsertClaimCmd(t, cdb, newStructuralClaimCmd(pkgID, "exports", "reverse_dep:k8s.io/kubernetes/cmd/kube-controller-manager", "store.go"))

	cdb.Close()

	// Run the export command.
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{
		"export",
		"--format", "markdown",
		"--repo", repo,
		"--db", dbPath,
		subPkg,
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("export command failed: %v", err)
	}

	md := buf.String()

	// Verify required sections.
	checks := []struct {
		name    string
		content string
	}{
		{"title", "# k8s.io/client-go/tools/cache"},
		{"tier 1 marker", "Tier 1"},
		{"implements section", "## Implements"},
		{"DeltaFIFO implements Store", "`DeltaFIFO` implements `Store`"},
		{"used by section", "## Used By"},
		{"reverse dep kubelet", "`k8s.io/kubernetes/pkg/kubelet`"},
		{"reverse dep controller-manager", "`k8s.io/kubernetes/cmd/kube-controller-manager`"},
		{"cross-package section", "## Cross-Package References"},
		{"forward dep runtime", "`k8s.io/apimachinery/pkg/runtime`"},
		{"forward dep klog", "`k8s.io/klog/v2`"},
		{"interface section", "## Exported Interfaces"},
		{"Store interface", "`Store`"},
	}

	for _, check := range checks {
		if !strings.Contains(md, check.content) {
			t.Errorf("missing %s: expected output to contain %q\n\nFull output:\n%s", check.name, check.content, md)
		}
	}
}

func TestExportMarkdownToFile(t *testing.T) {
	resetExportFlags()

	// Set up the same way as TestExportMarkdownFromDB but write to a file.
	repoDir := t.TempDir()
	goMod := filepath.Join(repoDir, "go.mod")
	if err := os.WriteFile(goMod, []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "test.claims.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	symID := mustUpsertSymbolCmd(t, cdb, db.Symbol{
		Repo: "test-repo", ImportPath: "example.com/test", SymbolName: "Hello",
		Language: "go", Kind: "func", Visibility: "public",
	})
	mustInsertClaimCmd(t, cdb, newStructuralClaimCmd(symID, "defines", "Hello", "main.go"))
	cdb.Close()

	outFile := filepath.Join(t.TempDir(), "output.md")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{
		"export",
		"--format", "markdown",
		"--repo", "test-repo",
		"--db", dbPath,
		"--output", outFile,
		repoDir,
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("export command failed: %v", err)
	}

	content, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}

	if !strings.Contains(string(content), "# example.com/test") {
		t.Errorf("output file missing expected content, got:\n%s", string(content))
	}
}

func TestExportUnknownFormat(t *testing.T) {
	resetExportFlags()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"export", "--format", "unknown", "/tmp"})
	err := rootCmd.Execute()
	if err == nil {
		t.Error("expected error for unknown format")
	}
}

// Test helpers — local to export_test.go to avoid conflicts with extract_cmd_test.go.

func mustUpsertSymbolCmd(t *testing.T, cdb *db.ClaimsDB, s db.Symbol) int64 {
	t.Helper()
	id, err := cdb.UpsertSymbol(s)
	if err != nil {
		t.Fatalf("upsert symbol %s: %v", s.SymbolName, err)
	}
	return id
}

func mustInsertClaimCmd(t *testing.T, cdb *db.ClaimsDB, cl db.Claim) {
	t.Helper()
	_, err := cdb.InsertClaim(cl)
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}
}

func newStructuralClaimCmd(subjectID int64, predicate, objectText, sourceFile string) db.Claim {
	return db.Claim{
		SubjectID:        subjectID,
		Predicate:        predicate,
		ObjectText:       objectText,
		SourceFile:       sourceFile,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "go-deep",
		ExtractorVersion: "0.1.0",
		LastVerified:     db.Now(),
	}
}
