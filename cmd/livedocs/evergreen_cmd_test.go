package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjarmak/livedocs/evergreen"
)

// setupEvergreenTest installs a temporary DB path on the evergreenCmd tree
// and swaps the executor factory with a stub. Returns the DB path plus a
// cleanup function that restores globals.
//
// The DB file is pre-populated from a per-binary migrated template — see
// evergreen_template_test.go — to keep parallel `go test ./...` from
// thrashing the modernc.org/sqlite connection pool with ~17 fresh schema
// builds. The CLI's eventual OpenSQLiteStore still runs Migrate; every DDL
// becomes a no-op.
func setupEvergreenTest(t *testing.T, exec *stubExecutor) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "evergreen_cli.db")
	copyEvergreenTemplate(t, dbPath)

	prevFactory := evergreenExecutorFactory
	evergreenExecutorFactory = func() (evergreen.RefreshExecutor, func(), error) {
		if exec == nil {
			return &stubExecutor{}, func() {}, nil
		}
		return exec, func() { exec.closed = true }, nil
	}
	t.Cleanup(func() {
		evergreenExecutorFactory = prevFactory
	})
	// Cross-test flag leak is handled by the `defer resetCmdFlags(cmd)`
	// installed at the top of each RunE (see cmd/livedocs/flags.go:28 for
	// the convention). --help and error paths that return before RunE
	// do not trigger that defer, so tests in this file always supply the
	// flags they depend on explicitly.
	return dbPath
}

// runEvergreen exercises the command from argv, returning captured
// stdout/stderr and the error.
func runEvergreen(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(args)
	err := rootCmd.ExecuteContext(context.Background())
	// Reset afterwards so other tests in the file are not affected.
	rootCmd.SetArgs(nil)
	return stdout.String(), stderr.String(), err
}

// --- stub executor -------------------------------------------------------

type stubExecutor struct {
	result evergreen.RefreshResult
	err    error
	calls  int
	closed bool
}

func (s *stubExecutor) Refresh(_ context.Context, _ *evergreen.Document) (evergreen.RefreshResult, error) {
	s.calls++
	if s.err != nil {
		return evergreen.RefreshResult{}, s.err
	}
	return s.result, nil
}
func (s *stubExecutor) Name() string { return "stub" }

// --- list ----------------------------------------------------------------

func TestEvergreenList_EmptyStore(t *testing.T) {
	dbPath := setupEvergreenTest(t, nil)
	out, _, err := runEvergreen(t, "evergreen", "list", "--db", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(no evergreen documents)") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestEvergreenList_JSONEmpty(t *testing.T) {
	dbPath := setupEvergreenTest(t, nil)
	out, _, err := runEvergreen(t, "evergreen", "list", "--db", dbPath, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var docs []*evergreen.Document
	// Valid JSON and specifically an empty array.
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		t.Fatalf("JSON: %v; output: %q", err, out)
	}
	if len(docs) != 0 {
		t.Errorf("expected empty slice, got %d", len(docs))
	}
}

// --- save ----------------------------------------------------------------

func TestEvergreenSave_HappyPath(t *testing.T) {
	exec := &stubExecutor{
		result: evergreen.RefreshResult{
			RenderedAnswer: "fresh answer",
			Manifest: []evergreen.ManifestEntry{
				{Repo: "github.com/x/y", CommitSHA: "abc", FilePath: "f.go", LineStart: 1, LineEnd: 5},
			},
			Backend: "stub",
		},
	}
	dbPath := setupEvergreenTest(t, exec)
	out, _, err := runEvergreen(t, "evergreen", "save",
		"--db", dbPath,
		"--query", "how does X work?",
		"--max-age-days", "30",
	)
	if err != nil {
		t.Fatal(err)
	}
	if exec.calls != 1 {
		t.Errorf("executor called %d times", exec.calls)
	}
	if !strings.Contains(out, "saved doc-") {
		t.Errorf("expected 'saved' line, got %q", out)
	}
	if !exec.closed {
		t.Error("executor close was not called")
	}

	// Confirm persisted.
	listOut, _, err := runEvergreen(t, "evergreen", "list", "--db", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listOut, "how does X work?") {
		t.Errorf("list output did not include saved query: %q", listOut)
	}
}

func TestEvergreenSave_MissingQuery(t *testing.T) {
	dbPath := setupEvergreenTest(t, nil)
	_, _, err := runEvergreen(t, "evergreen", "save", "--db", dbPath)
	if err == nil {
		t.Fatal("expected error for missing --query")
	}
}

func TestEvergreenSave_ExecutorError(t *testing.T) {
	exec := &stubExecutor{err: errors.New("upstream down")}
	dbPath := setupEvergreenTest(t, exec)
	_, _, err := runEvergreen(t, "evergreen", "save",
		"--db", dbPath,
		"--query", "q",
	)
	if err == nil {
		t.Fatal("expected error")
	}
	// Nothing should have been persisted.
	out, _, _ := runEvergreen(t, "evergreen", "list", "--db", dbPath)
	if !strings.Contains(out, "(no evergreen documents)") {
		t.Errorf("store mutated on executor failure: %q", out)
	}
}

// --- check ---------------------------------------------------------------

func TestEvergreenCheck_NoDocs(t *testing.T) {
	dbPath := setupEvergreenTest(t, nil)
	out, _, err := runEvergreen(t, "evergreen", "check", "--db", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(no evergreen documents)") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestEvergreenCheck_AgeColdFindingHumanReadable(t *testing.T) {
	dbPath := setupEvergreenTest(t, nil)
	// Seed a doc directly via the store so we control LastRefreshedAt.
	ctx := context.Background()
	s, err := evergreen.OpenSQLiteStore(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	doc := &evergreen.Document{
		ID:              "doc-old",
		Query:           "aged query",
		RenderedAnswer:  "answer",
		Status:          evergreen.FreshStatus,
		RefreshPolicy:   evergreen.AlertPolicy,
		MaxAgeDays:      1,
		CreatedAt:       time.Now().Add(-365 * 24 * time.Hour),
		LastRefreshedAt: time.Now().Add(-365 * 24 * time.Hour),
	}
	if err := s.Save(ctx, doc); err != nil {
		t.Fatal(err)
	}
	s.Close()

	out, _, err := runEvergreen(t, "evergreen", "check", "--db", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "doc-old") {
		t.Errorf("missing doc id: %q", out)
	}
	if !strings.Contains(out, "cold") {
		t.Errorf("expected cold finding: %q", out)
	}
}

func TestEvergreenCheck_SingleDocJSON(t *testing.T) {
	dbPath := setupEvergreenTest(t, nil)
	ctx := context.Background()
	s, _ := evergreen.OpenSQLiteStore(ctx, dbPath)
	doc := &evergreen.Document{
		ID: "d1", Query: "q", RenderedAnswer: "a",
		Status: evergreen.FreshStatus, RefreshPolicy: evergreen.AlertPolicy,
		CreatedAt: time.Now(), LastRefreshedAt: time.Now(),
	}
	_ = s.Save(ctx, doc)
	s.Close()

	out, _, err := runEvergreen(t, "evergreen", "check", "d1", "--db", dbPath, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var parsed evergreen.StatusOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("bad JSON: %v; got %q", err, out)
	}
	if len(parsed.Documents) != 1 || parsed.Documents[0].Document.ID != "d1" {
		t.Errorf("unexpected payload: %+v", parsed)
	}
}

func TestEvergreenCheck_NotFound(t *testing.T) {
	dbPath := setupEvergreenTest(t, nil)
	_, _, err := runEvergreen(t, "evergreen", "check", "missing", "--db", dbPath)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got %q", err)
	}
}

// --- refresh -------------------------------------------------------------

func TestEvergreenRefresh_HappyPath(t *testing.T) {
	exec := &stubExecutor{
		result: evergreen.RefreshResult{
			RenderedAnswer: "v2 answer",
			Manifest: []evergreen.ManifestEntry{
				{Repo: "r", Fuzzy: true},
			},
			Backend: "stub",
		},
	}
	dbPath := setupEvergreenTest(t, exec)
	ctx := context.Background()
	s, _ := evergreen.OpenSQLiteStore(ctx, dbPath)
	doc := &evergreen.Document{
		ID: "d1", Query: "q", RenderedAnswer: "v1",
		Status: evergreen.FreshStatus, RefreshPolicy: evergreen.AlertPolicy,
		CreatedAt: time.Now(), LastRefreshedAt: time.Now(),
	}
	_ = s.Save(ctx, doc)
	s.Close()

	out, _, err := runEvergreen(t, "evergreen", "refresh", "d1", "--db", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if exec.calls != 1 {
		t.Errorf("executor calls = %d", exec.calls)
	}
	if !strings.Contains(out, "refreshed d1") {
		t.Errorf("expected refreshed line, got %q", out)
	}
}

func TestEvergreenRefresh_DryRunDoesNotPersist(t *testing.T) {
	exec := &stubExecutor{
		result: evergreen.RefreshResult{RenderedAnswer: "v2", Backend: "stub"},
	}
	dbPath := setupEvergreenTest(t, exec)
	ctx := context.Background()
	s, _ := evergreen.OpenSQLiteStore(ctx, dbPath)
	doc := &evergreen.Document{
		ID: "d1", Query: "q", RenderedAnswer: "original",
		Status: evergreen.FreshStatus, RefreshPolicy: evergreen.AlertPolicy,
		CreatedAt: time.Now(), LastRefreshedAt: time.Now(),
	}
	_ = s.Save(ctx, doc)
	s.Close()

	out, _, err := runEvergreen(t, "evergreen", "refresh", "d1",
		"--db", dbPath, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got %q", out)
	}
	// Confirm store is untouched.
	s2, _ := evergreen.OpenSQLiteStore(ctx, dbPath)
	got, _ := s2.Get(ctx, "d1")
	s2.Close()
	if got.RenderedAnswer != "original" {
		t.Errorf("dry-run mutated store: answer = %q", got.RenderedAnswer)
	}
}

func TestEvergreenRefresh_OrphanedBlockedWithoutAck(t *testing.T) {
	exec := &stubExecutor{result: evergreen.RefreshResult{RenderedAnswer: "v2"}}
	dbPath := setupEvergreenTest(t, exec)
	ctx := context.Background()
	s, _ := evergreen.OpenSQLiteStore(ctx, dbPath)
	doc := &evergreen.Document{
		ID: "d1", Query: "q", RenderedAnswer: "v1",
		Status: evergreen.OrphanedStatus, RefreshPolicy: evergreen.AlertPolicy,
		CreatedAt: time.Now(), LastRefreshedAt: time.Now(),
	}
	_ = s.Save(ctx, doc)
	s.Close()

	_, _, err := runEvergreen(t, "evergreen", "refresh", "d1", "--db", dbPath)
	if err == nil {
		t.Fatal("expected orphan block")
	}
	if !strings.Contains(err.Error(), "acknowledge-orphan") {
		t.Errorf("expected remediation hint, got %q", err.Error())
	}
	if exec.calls != 0 {
		t.Errorf("executor called despite orphan block: %d", exec.calls)
	}
}

// Dry-run must respect the orphan guard: a user running --dry-run on an
// orphaned document could otherwise spend executor cost without being
// prompted to review the orphan status first.
func TestEvergreenRefresh_DryRunRespectsOrphanGuard(t *testing.T) {
	exec := &stubExecutor{result: evergreen.RefreshResult{RenderedAnswer: "v2"}}
	dbPath := setupEvergreenTest(t, exec)
	ctx := context.Background()
	s, _ := evergreen.OpenSQLiteStore(ctx, dbPath)
	doc := &evergreen.Document{
		ID: "d1", Query: "q", RenderedAnswer: "v1",
		Status: evergreen.OrphanedStatus, RefreshPolicy: evergreen.AlertPolicy,
		CreatedAt: time.Now(), LastRefreshedAt: time.Now(),
	}
	_ = s.Save(ctx, doc)
	s.Close()

	_, _, err := runEvergreen(t, "evergreen", "refresh", "d1",
		"--db", dbPath, "--dry-run")
	if err == nil {
		t.Fatal("expected dry-run to be blocked on orphaned doc")
	}
	if !strings.Contains(err.Error(), "acknowledge-orphan") {
		t.Errorf("expected orphan-remediation hint, got %q", err.Error())
	}
	if exec.calls != 0 {
		t.Errorf("executor called despite dry-run orphan block: %d", exec.calls)
	}
}

func TestEvergreenRefresh_OrphanedProceedsWithAck(t *testing.T) {
	exec := &stubExecutor{
		result: evergreen.RefreshResult{RenderedAnswer: "v2", Backend: "stub"},
	}
	dbPath := setupEvergreenTest(t, exec)
	ctx := context.Background()
	s, _ := evergreen.OpenSQLiteStore(ctx, dbPath)
	doc := &evergreen.Document{
		ID: "d1", Query: "q", RenderedAnswer: "v1",
		Status: evergreen.OrphanedStatus, RefreshPolicy: evergreen.AlertPolicy,
		CreatedAt: time.Now(), LastRefreshedAt: time.Now(),
	}
	_ = s.Save(ctx, doc)
	s.Close()

	_, _, err := runEvergreen(t, "evergreen", "refresh", "d1",
		"--db", dbPath, "--acknowledge-orphan")
	if err != nil {
		t.Fatal(err)
	}
	if exec.calls != 1 {
		t.Errorf("executor calls = %d, want 1", exec.calls)
	}
}

func TestEvergreenRefresh_NotFound(t *testing.T) {
	exec := &stubExecutor{}
	dbPath := setupEvergreenTest(t, exec)
	_, _, err := runEvergreen(t, "evergreen", "refresh", "missing", "--db", dbPath)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("got %q", err.Error())
	}
}

// --- delete --------------------------------------------------------------

func TestEvergreenDelete_Existing(t *testing.T) {
	dbPath := setupEvergreenTest(t, nil)
	ctx := context.Background()
	s, _ := evergreen.OpenSQLiteStore(ctx, dbPath)
	doc := &evergreen.Document{
		ID: "d1", Query: "q", RenderedAnswer: "a",
		Status: evergreen.FreshStatus, RefreshPolicy: evergreen.AlertPolicy,
		CreatedAt: time.Now(), LastRefreshedAt: time.Now(),
	}
	_ = s.Save(ctx, doc)
	s.Close()

	out, _, err := runEvergreen(t, "evergreen", "delete", "d1", "--db", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "deleted d1") {
		t.Errorf("got %q", out)
	}
}

func TestEvergreenDelete_NotFound(t *testing.T) {
	dbPath := setupEvergreenTest(t, nil)
	_, _, err := runEvergreen(t, "evergreen", "delete", "missing", "--db", dbPath)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("got %v", err)
	}
}

// --- help smoke ----------------------------------------------------------

// Help for each subcommand must render without panic and mention the
// subcommand name in its Use line. Catches flag-registration typos.
func TestEvergreenSubcommandsHaveHelp(t *testing.T) {
	for _, sub := range []string{"list", "save", "check", "refresh", "delete"} {
		t.Run(sub, func(t *testing.T) {
			out, _, err := runEvergreen(t, "evergreen", sub, "--help")
			if err != nil {
				t.Fatalf("help error: %v", err)
			}
			if !strings.Contains(out, sub) {
				t.Errorf("help missing subcommand name: %q", out)
			}
		})
	}
}
