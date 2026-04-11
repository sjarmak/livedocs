package main

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestTribalFlagRegistered(t *testing.T) {
	f := extractCmd.Flags().Lookup("tribal")
	if f == nil {
		t.Fatal("extract command missing --tribal flag")
	}
	if f.NoOptDefVal != "deterministic" {
		t.Errorf("expected --tribal NoOptDefVal=deterministic, got %s", f.NoOptDefVal)
	}
}

func TestTribalLLMReturnsError(t *testing.T) {
	resetExtractFlags()

	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "llm-test.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"extract", "--tribal=llm", "--repo", "llm-repo", "--output", outputDB, repoDir})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for --tribal=llm")
	}
	if !strings.Contains(err.Error(), "LLM tribal extraction requires explicit config opt-in") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestTribalStatusCommandRegistered(t *testing.T) {
	registered := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		registered[cmd.Name()] = true
	}
	if !registered["tribal"] {
		t.Error("tribal subcommand not registered on root command")
	}

	// Check that 'status' is a child of 'tribal'.
	var hasStatus bool
	for _, cmd := range tribalCmd.Commands() {
		if cmd.Name() == "status" {
			hasStatus = true
		}
	}
	if !hasStatus {
		t.Error("status subcommand not registered on tribal command")
	}
}

func TestTribalStatusEmptyDB(t *testing.T) {
	resetExtractFlags()

	// Create a minimal claims DB with tribal schema but no facts.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "empty.claims.db")

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	_, err = sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS symbols (id INTEGER PRIMARY KEY, repo TEXT, import_path TEXT, symbol_name TEXT, language TEXT, kind TEXT, visibility TEXT, display_name TEXT, scip_symbol TEXT);
		CREATE TABLE IF NOT EXISTS claims (id INTEGER PRIMARY KEY, subject_id INTEGER, predicate TEXT, object_text TEXT, object_id INTEGER, source_file TEXT, source_line INTEGER, confidence REAL, claim_tier TEXT, extractor TEXT, extractor_version TEXT, last_verified TEXT);
		CREATE TABLE IF NOT EXISTS tribal_facts (
			id INTEGER PRIMARY KEY, subject_id INTEGER NOT NULL,
			kind TEXT NOT NULL, body TEXT NOT NULL, source_quote TEXT NOT NULL,
			confidence REAL NOT NULL, corroboration INTEGER NOT NULL DEFAULT 1,
			extractor TEXT NOT NULL, extractor_version TEXT NOT NULL, model TEXT,
			staleness_hash TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL, last_verified TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS tribal_evidence (
			id INTEGER PRIMARY KEY, fact_id INTEGER NOT NULL,
			source_type TEXT NOT NULL, source_ref TEXT NOT NULL,
			author TEXT, authored_at TEXT, content_hash TEXT NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	sqlDB.Close()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"tribal", "status", dbPath})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tribal status failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "No tribal facts found") {
		t.Errorf("expected 'No tribal facts found' message, got: %q", out)
	}
}

// gitInDir runs a git command in the given directory.
func gitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("git %v: %s (err: %v)", args, string(out), err)
	}
}

func TestExtractWithTribalDeterministic(t *testing.T) {
	resetExtractFlags()

	// Create a test repo with CODEOWNERS and a file with TODO markers.
	repoDir := t.TempDir()

	// Initialize a git repo so blame/rationale work.
	gitInDir(t, repoDir, "init")
	gitInDir(t, repoDir, "config", "user.email", "test@test.com")
	gitInDir(t, repoDir, "config", "user.name", "Test User")

	// Write CODEOWNERS.
	if err := os.WriteFile(filepath.Join(repoDir, "CODEOWNERS"), []byte("*.go @team-go\n*.py @team-py\n"), 0644); err != nil {
		t.Fatalf("write CODEOWNERS: %v", err)
	}

	// Write a Go file with TODO markers.
	goFile := filepath.Join(repoDir, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main

import "fmt"

// TODO: refactor this to be more modular
// HACK: workaround for upstream bug #123
func Hello() {
	fmt.Println("hello")
}

func main() {
	Hello()
}
`), 0644); err != nil {
		t.Fatalf("write go file: %v", err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// Commit so blame and rationale extractors have data.
	gitInDir(t, repoDir, "add", "-A")
	gitInDir(t, repoDir, "commit", "-m", "feat: initial commit with hello function and CODEOWNERS")

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "tribal-test.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"extract", "--tribal=deterministic", "--repo", "tribal-repo", "--output", outputDB, repoDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("extract --tribal=deterministic failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Tribal Knowledge Summary") {
		t.Error("output missing Tribal Knowledge Summary")
	}

	// Open the DB and verify tribal facts exist.
	sqlDB, err := sql.Open("sqlite", outputDB)
	if err != nil {
		t.Fatalf("open output db: %v", err)
	}
	defer sqlDB.Close()

	// Check that tribal_facts table exists and has rows.
	var totalFacts int
	err = sqlDB.QueryRow("SELECT COUNT(*) FROM tribal_facts").Scan(&totalFacts)
	if err != nil {
		t.Fatalf("query tribal_facts: %v", err)
	}
	if totalFacts == 0 {
		t.Error("expected at least 1 tribal fact, got 0")
	}

	// Check for ownership facts (from CODEOWNERS).
	var ownershipCount int
	err = sqlDB.QueryRow("SELECT COUNT(*) FROM tribal_facts WHERE kind = 'ownership'").Scan(&ownershipCount)
	if err != nil {
		t.Fatalf("query ownership: %v", err)
	}
	if ownershipCount == 0 {
		t.Error("expected >= 1 ownership fact from CODEOWNERS")
	}

	// Check for todo/quirk facts (from inline markers).
	var markerCount int
	err = sqlDB.QueryRow("SELECT COUNT(*) FROM tribal_facts WHERE kind IN ('todo', 'quirk')").Scan(&markerCount)
	if err != nil {
		t.Fatalf("query markers: %v", err)
	}
	if markerCount == 0 {
		t.Error("expected >= 1 todo/quirk fact from inline markers")
	}

	// Check that all facts have model=NULL (deterministic).
	var nonNullModel int
	err = sqlDB.QueryRow("SELECT COUNT(*) FROM tribal_facts WHERE model IS NOT NULL").Scan(&nonNullModel)
	if err != nil {
		t.Fatalf("query non-null model: %v", err)
	}
	if nonNullModel != 0 {
		t.Errorf("expected all facts to have model=NULL, found %d with non-NULL model", nonNullModel)
	}

	// Check that every fact has >= 1 evidence row.
	var factsWithoutEvidence int
	err = sqlDB.QueryRow(`
		SELECT COUNT(*) FROM tribal_facts tf
		WHERE NOT EXISTS (SELECT 1 FROM tribal_evidence te WHERE te.fact_id = tf.id)
	`).Scan(&factsWithoutEvidence)
	if err != nil {
		t.Fatalf("query facts without evidence: %v", err)
	}
	if factsWithoutEvidence != 0 {
		t.Errorf("expected all facts to have evidence, found %d without", factsWithoutEvidence)
	}

	// Now test tribal status command on this DB.
	buf2 := new(bytes.Buffer)
	rootCmd.SetOut(buf2)
	rootCmd.SetArgs([]string{"tribal", "status", outputDB})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tribal status failed: %v", err)
	}

	statusOut := buf2.String()
	if !strings.Contains(statusOut, "Tribal Knowledge Status") {
		t.Errorf("tribal status output missing header, got: %q", statusOut)
	}
	if !strings.Contains(statusOut, "ownership") {
		t.Errorf("tribal status missing 'ownership' kind, got: %q", statusOut)
	}
}
