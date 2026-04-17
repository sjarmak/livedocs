package main

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/live-docs/live_docs/db"
	"github.com/spf13/pflag"
	_ "modernc.org/sqlite"
)

func TestValidateDBPath(t *testing.T) {
	tests := []struct {
		name    string
		dbPath  string
		dataDir string
		wantErr string
	}{
		{
			name:   "valid path",
			dbPath: "repo.claims.db",
		},
		{
			name:   "valid absolute path",
			dbPath: "/data/repos/my-repo.claims.db",
		},
		{
			name:    "empty path",
			dbPath:  "",
			wantErr: "must not be empty",
		},
		{
			name:    "wrong suffix .db",
			dbPath:  "repo.db",
			wantErr: "must end with .claims.db",
		},
		{
			name:    "wrong suffix .sqlite",
			dbPath:  "repo.sqlite",
			wantErr: "must end with .claims.db",
		},
		{
			name:    "no suffix",
			dbPath:  "/etc/passwd",
			wantErr: "must end with .claims.db",
		},
		{
			name:    "directory traversal no suffix",
			dbPath:  "../../../etc/passwd",
			wantErr: "must end with .claims.db",
		},
		{
			name:    "suffix embedded but not at end",
			dbPath:  "repo.claims.db.bak",
			wantErr: "must end with .claims.db",
		},
		{
			name:    "data-dir traversal",
			dbPath:  "../secret.claims.db",
			dataDir: "/data/repos",
			wantErr: "outside data directory",
		},
		{
			name:    "data-dir valid",
			dbPath:  "/data/repos/my-repo.claims.db",
			dataDir: "/data/repos",
		},
		{
			name:    "data-dir subdirectory valid",
			dbPath:  "/data/repos/org/my-repo.claims.db",
			dataDir: "/data/repos",
		},
		{
			name:    "data-dir sibling rejected",
			dbPath:  "/data/other/my-repo.claims.db",
			dataDir: "/data/repos",
			wantErr: "outside data directory",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDBPath(tc.dbPath, tc.dataDir)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestTribalCLIRejectsInvalidDBPath(t *testing.T) {
	resetExtractFlags()

	commands := []struct {
		name string
		args []string
	}{
		{
			name: "correct rejects bad suffix",
			args: []string{"tribal", "correct", "--db", "/tmp/evil.db", "--fact-id", "1", "--body", "b", "--reason", "r"},
		},
		{
			name: "supersede rejects bad suffix",
			args: []string{"tribal", "supersede", "--db", "/tmp/evil.db", "--fact-id", "1", "--body", "b", "--reason", "r"},
		},
		{
			name: "delete rejects bad suffix",
			args: []string{"tribal", "delete", "--db", "/tmp/evil.db", "--fact-id", "1", "--reason", "r"},
		},
		{
			name: "status rejects bad suffix",
			args: []string{"tribal", "status", "/tmp/evil.db"},
		},
	}

	for _, tc := range commands {
		t.Run(tc.name, func(t *testing.T) {
			resetTribalCorrectionFlags()
			buf := new(bytes.Buffer)
			rootCmd.SetOut(buf)
			rootCmd.SetErr(buf)
			rootCmd.SetArgs(tc.args)
			err := rootCmd.Execute()
			if err == nil {
				t.Fatal("expected validation error for invalid db path")
			}
			if !strings.Contains(err.Error(), ".claims.db") {
				t.Errorf("expected error about .claims.db suffix, got: %v", err)
			}
		})
	}
}

func TestTribalCLIAcceptsValidDBPath(t *testing.T) {
	resetExtractFlags()
	dbPath := createTribalTestDB(t)

	// correct should work with a valid .claims.db path
	resetTribalCorrectionFlags()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{
		"tribal", "correct",
		"--db", dbPath,
		"--fact-id", "1",
		"--body", "validated body",
		"--reason", "validation test",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tribal correct with valid path failed: %v", err)
	}
	if !strings.Contains(buf.String(), "Corrected fact 1") {
		t.Errorf("expected success output, got: %q", buf.String())
	}
}

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

	// Initialize a git repo so deterministic extractors can run first.
	gitInDir(t, repoDir, "init")
	gitInDir(t, repoDir, "config", "user.email", "test@test.com")
	gitInDir(t, repoDir, "config", "user.name", "Test User")
	gitInDir(t, repoDir, "add", "-A")
	gitInDir(t, repoDir, "commit", "-m", "init")

	outDir := t.TempDir()
	outputDB := filepath.Join(outDir, "llm-test.claims.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"extract", "--tribal=llm", "--repo", "llm-repo", "--output", outputDB, repoDir})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for --tribal=llm without config opt-in")
	}
	// Now that Phase 2 is implemented, --tribal=llm without llm_enabled returns a config error.
	if !strings.Contains(err.Error(), "llm_enabled") {
		t.Errorf("expected error mentioning llm_enabled, got: %v", err)
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

// createTribalTestDB creates a temp claims DB with tribal schema and a single
// active fact (id=1). Returns the DB path. The fact has subject_id=1,
// kind='invariant', body='original body'.
func createTribalTestDB(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.claims.db")

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	_, err = sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS symbols (
			id INTEGER PRIMARY KEY, repo TEXT, import_path TEXT,
			symbol_name TEXT, language TEXT, kind TEXT, visibility TEXT,
			display_name TEXT, scip_symbol TEXT
		);
		INSERT INTO symbols (id, repo, import_path, symbol_name, language, kind, visibility)
		VALUES (1, 'test-repo', 'pkg/foo', 'Foo', 'go', 'function', 'public');

		CREATE TABLE IF NOT EXISTS claims (
			id INTEGER PRIMARY KEY, subject_id INTEGER, predicate TEXT,
			object_text TEXT, object_id INTEGER, source_file TEXT,
			source_line INTEGER, confidence REAL, claim_tier TEXT,
			extractor TEXT, extractor_version TEXT, last_verified TEXT
		);

		CREATE TABLE IF NOT EXISTS tribal_facts (
			id INTEGER PRIMARY KEY, subject_id INTEGER NOT NULL,
			kind TEXT NOT NULL CHECK(kind IN ('ownership','rationale','invariant','quirk','todo','deprecation')),
			body TEXT NOT NULL, source_quote TEXT NOT NULL,
			confidence REAL NOT NULL, corroboration INTEGER NOT NULL DEFAULT 1,
			extractor TEXT NOT NULL, extractor_version TEXT NOT NULL, model TEXT,
			staleness_hash TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stale','quarantined','superseded','deleted')),
			created_at TEXT NOT NULL, last_verified TEXT NOT NULL,
			cluster_key TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS tribal_evidence (
			id INTEGER PRIMARY KEY, fact_id INTEGER NOT NULL,
			source_type TEXT NOT NULL CHECK(source_type IN ('blame','commit_msg','pr_comment','codeowners','inline_marker','runbook','correction')),
			source_ref TEXT NOT NULL, author TEXT, authored_at TEXT,
			content_hash TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS tribal_corrections (
			id INTEGER PRIMARY KEY, fact_id INTEGER NOT NULL,
			action TEXT NOT NULL CHECK(action IN ('correct','delete','supersede')),
			new_body TEXT, reason TEXT NOT NULL, actor TEXT NOT NULL,
			created_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS source_files (
			id INTEGER PRIMARY KEY, repo TEXT NOT NULL, relative_path TEXT NOT NULL,
			content_hash TEXT NOT NULL, extractor_version TEXT NOT NULL,
			extracted_at TEXT NOT NULL, last_pr_id_set BLOB, pr_miner_version TEXT DEFAULT ''
		);

		INSERT INTO tribal_facts (id, subject_id, kind, body, source_quote,
			confidence, corroboration, extractor, extractor_version, staleness_hash,
			status, created_at, last_verified, cluster_key)
		VALUES (1, 1, 'invariant', 'original body', 'quote here',
			1.0, 1, 'test', 'v1', 'hash123',
			'active', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z', '');

		INSERT INTO tribal_evidence (id, fact_id, source_type, source_ref, content_hash)
		VALUES (1, 1, 'correction', 'test-ref', 'abc123');
	`)
	if err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	return dbPath
}

func TestTribalCorrectionCLICorrect(t *testing.T) {
	resetExtractFlags()
	dbPath := createTribalTestDB(t)

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{
		"tribal", "correct",
		"--db", dbPath,
		"--fact-id", "1",
		"--body", "corrected body text",
		"--reason", "the original was wrong",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tribal correct failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Corrected fact 1") {
		t.Errorf("expected success message, got: %q", out)
	}

	// Verify the correction row was inserted.
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	var action, newBody, reason string
	err = sqlDB.QueryRow("SELECT action, new_body, reason FROM tribal_corrections WHERE fact_id = 1").
		Scan(&action, &newBody, &reason)
	if err != nil {
		t.Fatalf("query correction: %v", err)
	}
	if action != "correct" {
		t.Errorf("expected action 'correct', got %q", action)
	}
	if newBody != "corrected body text" {
		t.Errorf("expected new_body 'corrected body text', got %q", newBody)
	}

	// Verify the new replacement fact was created.
	var newFactCount int
	err = sqlDB.QueryRow("SELECT COUNT(*) FROM tribal_facts WHERE body = 'corrected body text'").
		Scan(&newFactCount)
	if err != nil {
		t.Fatalf("query new fact: %v", err)
	}
	if newFactCount != 1 {
		t.Errorf("expected 1 replacement fact, got %d", newFactCount)
	}
}

func TestTribalCorrectionCLISupersede(t *testing.T) {
	resetExtractFlags()
	dbPath := createTribalTestDB(t)

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{
		"tribal", "supersede",
		"--db", dbPath,
		"--fact-id", "1",
		"--body", "superseded body text",
		"--reason", "better understanding now",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tribal supersede failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Superseded fact 1") {
		t.Errorf("expected success message, got: %q", out)
	}

	// Verify the original fact status is 'superseded'.
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	var status string
	err = sqlDB.QueryRow("SELECT status FROM tribal_facts WHERE id = 1").Scan(&status)
	if err != nil {
		t.Fatalf("query fact status: %v", err)
	}
	if status != "superseded" {
		t.Errorf("expected status 'superseded', got %q", status)
	}

	// Verify the correction row was inserted.
	var action string
	err = sqlDB.QueryRow("SELECT action FROM tribal_corrections WHERE fact_id = 1").Scan(&action)
	if err != nil {
		t.Fatalf("query correction: %v", err)
	}
	if action != "supersede" {
		t.Errorf("expected action 'supersede', got %q", action)
	}

	// Verify replacement fact exists.
	var newFactCount int
	err = sqlDB.QueryRow("SELECT COUNT(*) FROM tribal_facts WHERE body = 'superseded body text'").
		Scan(&newFactCount)
	if err != nil {
		t.Fatalf("query new fact: %v", err)
	}
	if newFactCount != 1 {
		t.Errorf("expected 1 replacement fact, got %d", newFactCount)
	}
}

func TestTribalCorrectionCLIDelete(t *testing.T) {
	resetExtractFlags()
	dbPath := createTribalTestDB(t)

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{
		"tribal", "delete",
		"--db", dbPath,
		"--fact-id", "1",
		"--reason", "no longer relevant",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tribal delete failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Deleted fact 1") {
		t.Errorf("expected success message, got: %q", out)
	}

	// Verify the fact status is 'deleted'.
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	var status string
	err = sqlDB.QueryRow("SELECT status FROM tribal_facts WHERE id = 1").Scan(&status)
	if err != nil {
		t.Fatalf("query fact status: %v", err)
	}
	if status != "deleted" {
		t.Errorf("expected status 'deleted', got %q", status)
	}

	// Verify the correction row was inserted.
	var action, reason string
	err = sqlDB.QueryRow("SELECT action, reason FROM tribal_corrections WHERE fact_id = 1").
		Scan(&action, &reason)
	if err != nil {
		t.Fatalf("query correction: %v", err)
	}
	if action != "delete" {
		t.Errorf("expected action 'delete', got %q", action)
	}
	if reason != "no longer relevant" {
		t.Errorf("expected reason 'no longer relevant', got %q", reason)
	}

	// Verify NO replacement fact was created (delete does not create a new fact).
	var factCount int
	err = sqlDB.QueryRow("SELECT COUNT(*) FROM tribal_facts").Scan(&factCount)
	if err != nil {
		t.Fatalf("query fact count: %v", err)
	}
	if factCount != 1 {
		t.Errorf("expected exactly 1 fact (the original, now deleted), got %d", factCount)
	}
}

// resetTribalCorrectionFlags resets all flag values on the tribal correction
// subcommands so tests can be run in sequence without leaking state.
func resetTribalCorrectionFlags() {
	for _, flags := range []*pflag.FlagSet{tribalCorrectCmd.Flags(), tribalSupersedeCmd.Flags(), tribalDeleteCmd.Flags()} {
		flags.VisitAll(func(f *pflag.Flag) {
			f.Changed = false
			_ = f.Value.Set(f.DefValue)
		})
	}
}

func TestTribalCorrectionCLIMissingFlags(t *testing.T) {
	resetExtractFlags()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "correct missing body",
			args: []string{"tribal", "correct", "--db", "/tmp/x.db", "--fact-id", "1", "--reason", "r"},
			want: "required flag",
		},
		{
			name: "correct missing reason",
			args: []string{"tribal", "correct", "--db", "/tmp/x.db", "--fact-id", "1", "--body", "b"},
			want: "required flag",
		},
		{
			name: "delete missing reason",
			args: []string{"tribal", "delete", "--db", "/tmp/x.db", "--fact-id", "1"},
			want: "required flag",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetTribalCorrectionFlags()
			buf := new(bytes.Buffer)
			rootCmd.SetOut(buf)
			rootCmd.SetErr(buf)
			rootCmd.SetArgs(tc.args)
			err := rootCmd.Execute()
			if err == nil {
				t.Fatalf("expected error for %v", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) && !strings.Contains(buf.String(), tc.want) {
				t.Errorf("expected error containing %q, got: %v / %s", tc.want, err, buf.String())
			}
		})
	}
}

func TestTribalCorrectionCLIFactNotFound(t *testing.T) {
	resetExtractFlags()
	dbPath := createTribalTestDB(t)

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{
		"tribal", "delete",
		"--db", dbPath,
		"--fact-id", "999",
		"--reason", "does not exist",
	})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-existent fact")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Body length limit tests
// ---------------------------------------------------------------------------

func TestValidateBodyLength(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		errMsg  string
	}{
		{name: "empty body", body: "", wantErr: false},
		{name: "short body", body: "this is fine", wantErr: false},
		{name: "exactly at limit", body: strings.Repeat("a", db.MaxBodyBytes), wantErr: false},
		{name: "one byte over limit", body: strings.Repeat("a", db.MaxBodyBytes+1), wantErr: true, errMsg: "exceeds maximum"},
		{name: "way over limit", body: strings.Repeat("x", db.MaxBodyBytes*2), wantErr: true, errMsg: "4096"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBodyLength(tc.body)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.errMsg) {
					t.Errorf("expected error containing %q, got: %v", tc.errMsg, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// createTribalTestDBWithFeedback creates a temp claims DB with tribal schema,
// a fact, feedback rows, and correction rows for S4 gate testing.
func createTribalTestDBWithFeedback(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "s4gate.claims.db")

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer sqlDB.Close()

	_, err = sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS symbols (
			id INTEGER PRIMARY KEY, repo TEXT, import_path TEXT,
			symbol_name TEXT, language TEXT, kind TEXT, visibility TEXT,
			display_name TEXT, scip_symbol TEXT
		);
		INSERT INTO symbols (id, repo, import_path, symbol_name, language, kind, visibility)
		VALUES (1, 'test-repo', 'pkg/foo', 'Foo', 'go', 'function', 'public');
		INSERT INTO symbols (id, repo, import_path, symbol_name, language, kind, visibility)
		VALUES (2, 'test-repo', 'pkg/bar', 'Bar', 'go', 'function', 'public');

		CREATE TABLE IF NOT EXISTS claims (
			id INTEGER PRIMARY KEY, subject_id INTEGER, predicate TEXT,
			object_text TEXT, object_id INTEGER, source_file TEXT,
			source_line INTEGER, confidence REAL, claim_tier TEXT,
			extractor TEXT, extractor_version TEXT, last_verified TEXT
		);

		CREATE TABLE IF NOT EXISTS tribal_facts (
			id INTEGER PRIMARY KEY, subject_id INTEGER NOT NULL,
			kind TEXT NOT NULL CHECK(kind IN ('ownership','rationale','invariant','quirk','todo','deprecation')),
			body TEXT NOT NULL, source_quote TEXT NOT NULL,
			confidence REAL NOT NULL, corroboration INTEGER NOT NULL DEFAULT 1,
			extractor TEXT NOT NULL, extractor_version TEXT NOT NULL, model TEXT,
			staleness_hash TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stale','quarantined','superseded','deleted')),
			created_at TEXT NOT NULL, last_verified TEXT NOT NULL,
			cluster_key TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS tribal_evidence (
			id INTEGER PRIMARY KEY, fact_id INTEGER NOT NULL,
			source_type TEXT NOT NULL CHECK(source_type IN ('blame','commit_msg','pr_comment','codeowners','inline_marker','runbook','correction')),
			source_ref TEXT NOT NULL, author TEXT, authored_at TEXT,
			content_hash TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS tribal_corrections (
			id INTEGER PRIMARY KEY, fact_id INTEGER NOT NULL,
			action TEXT NOT NULL CHECK(action IN ('correct','delete','supersede')),
			new_body TEXT, reason TEXT NOT NULL, actor TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS tribal_feedback (
			id INTEGER PRIMARY KEY, fact_id INTEGER NOT NULL,
			reason TEXT NOT NULL CHECK(reason IN ('wrong','stale','misleading','offensive')),
			details TEXT, reporter TEXT NOT NULL, created_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS source_files (
			id INTEGER PRIMARY KEY, repo TEXT NOT NULL, relative_path TEXT NOT NULL,
			content_hash TEXT NOT NULL, extractor_version TEXT NOT NULL,
			extracted_at TEXT NOT NULL, last_pr_id_set BLOB, pr_miner_version TEXT DEFAULT ''
		);

		-- Two facts.
		INSERT INTO tribal_facts (id, subject_id, kind, body, source_quote,
			confidence, corroboration, extractor, extractor_version, staleness_hash,
			status, created_at, last_verified, cluster_key)
		VALUES (1, 1, 'invariant', 'fact one', 'quote one',
			1.0, 1, 'test', 'v1', 'hash1',
			'active', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z', '');
		INSERT INTO tribal_facts (id, subject_id, kind, body, source_quote,
			confidence, corroboration, extractor, extractor_version, staleness_hash,
			status, created_at, last_verified, cluster_key)
		VALUES (2, 2, 'rationale', 'fact two', 'quote two',
			0.8, 1, 'test', 'v1', 'hash2',
			'active', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z', '');

		INSERT INTO tribal_evidence (fact_id, source_type, source_ref, content_hash)
		VALUES (1, 'correction', 'test-ref', 'abc1');
		INSERT INTO tribal_evidence (fact_id, source_type, source_ref, content_hash)
		VALUES (2, 'correction', 'test-ref', 'abc2');

		-- Feedback: 2 wrong, 1 stale, 1 misleading on fact 1.
		INSERT INTO tribal_feedback (fact_id, reason, reporter, created_at) VALUES (1, 'wrong', 'user1', '2025-02-01T00:00:00Z');
		INSERT INTO tribal_feedback (fact_id, reason, reporter, created_at) VALUES (1, 'wrong', 'user2', '2025-02-02T00:00:00Z');
		INSERT INTO tribal_feedback (fact_id, reason, reporter, created_at) VALUES (1, 'stale', 'user3', '2025-02-03T00:00:00Z');
		INSERT INTO tribal_feedback (fact_id, reason, reporter, created_at) VALUES (1, 'misleading', 'user4', '2025-02-04T00:00:00Z');

		-- Correction: 1 delete on fact 2.
		INSERT INTO tribal_corrections (fact_id, action, reason, actor, created_at)
		VALUES (2, 'delete', 'no longer relevant', 'admin', '2025-02-05T00:00:00Z');
	`)
	if err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	return dbPath
}

func TestTribalS4GateStatus(t *testing.T) {
	resetExtractFlags()
	dbPath := createTribalTestDBWithFeedback(t)

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tribal", "s4-gate-status", dbPath})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tribal s4-gate-status failed: %v", err)
	}

	out := buf.String()

	// Check header.
	if !strings.Contains(out, "S4 Gate Status") {
		t.Error("output missing 'S4 Gate Status' header")
	}

	// Check feedback counts.
	if !strings.Contains(out, "wrong: 2") {
		t.Errorf("expected 'wrong: 2', got: %q", out)
	}
	if !strings.Contains(out, "stale: 1") {
		t.Errorf("expected 'stale: 1', got: %q", out)
	}
	if !strings.Contains(out, "misleading: 1") {
		t.Errorf("expected 'misleading: 1', got: %q", out)
	}

	// Check corrections.
	if !strings.Contains(out, "delete: 1") {
		t.Errorf("expected 'delete: 1', got: %q", out)
	}

	// Hallucinations = wrong_reports(2) + delete_corrections(1) = 3, distinct facts = 2.
	// Raw ratio 3/2 = 1.5, capped at 1.0 = 100%.
	if !strings.Contains(out, "100.00%") {
		t.Errorf("expected hallucination rate '100.00%%', got: %q", out)
	}

	// Check threshold not reached.
	if !strings.Contains(out, "NOT reached") {
		t.Errorf("expected 'NOT reached' message, got: %q", out)
	}
	if !strings.Contains(out, "2/50") {
		t.Errorf("expected '2/50 labeled facts', got: %q", out)
	}
}

func TestTribalS4GateStatus_EmptyDB(t *testing.T) {
	resetExtractFlags()

	// Create a DB with tribal schema but no feedback or corrections.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "empty-s4.claims.db")
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	_, err = sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS symbols (id INTEGER PRIMARY KEY, repo TEXT, import_path TEXT, symbol_name TEXT, language TEXT, kind TEXT, visibility TEXT, display_name TEXT, scip_symbol TEXT);
		CREATE TABLE IF NOT EXISTS claims (id INTEGER PRIMARY KEY, subject_id INTEGER, predicate TEXT, object_text TEXT, object_id INTEGER, source_file TEXT, source_line INTEGER, confidence REAL, claim_tier TEXT, extractor TEXT, extractor_version TEXT, last_verified TEXT);
		CREATE TABLE IF NOT EXISTS tribal_facts (id INTEGER PRIMARY KEY, subject_id INTEGER NOT NULL, kind TEXT NOT NULL, body TEXT NOT NULL, source_quote TEXT NOT NULL, confidence REAL NOT NULL, corroboration INTEGER NOT NULL DEFAULT 1, extractor TEXT NOT NULL, extractor_version TEXT NOT NULL, model TEXT, staleness_hash TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, last_verified TEXT NOT NULL, cluster_key TEXT NOT NULL DEFAULT '');
		CREATE TABLE IF NOT EXISTS tribal_evidence (id INTEGER PRIMARY KEY, fact_id INTEGER NOT NULL, source_type TEXT NOT NULL, source_ref TEXT NOT NULL, author TEXT, authored_at TEXT, content_hash TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS tribal_corrections (id INTEGER PRIMARY KEY, fact_id INTEGER NOT NULL, action TEXT NOT NULL, new_body TEXT, reason TEXT NOT NULL, actor TEXT NOT NULL, created_at TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS tribal_feedback (id INTEGER PRIMARY KEY, fact_id INTEGER NOT NULL, reason TEXT NOT NULL, details TEXT, reporter TEXT NOT NULL, created_at TEXT NOT NULL);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	sqlDB.Close()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tribal", "s4-gate-status", dbPath})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tribal s4-gate-status failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "0.00%") {
		t.Errorf("expected '0.00%%' for empty DB, got: %q", out)
	}
	if !strings.Contains(out, "0/50") {
		t.Errorf("expected '0/50' for empty DB, got: %q", out)
	}
}

func TestTribalS4GateStatusCommandRegistered(t *testing.T) {
	var hasS4Gate bool
	for _, cmd := range tribalCmd.Commands() {
		if cmd.Name() == "s4-gate-status" {
			hasS4Gate = true
		}
	}
	if !hasS4Gate {
		t.Error("s4-gate-status subcommand not registered on tribal command")
	}
}
