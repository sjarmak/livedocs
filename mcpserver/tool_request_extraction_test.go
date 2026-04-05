package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/live-docs/live_docs/db"
)

// mockExtractionRunner implements ExtractionRunner for testing.
type mockExtractionRunner struct {
	mu             sync.Mutex
	headCommit     string
	headErr        error
	runErr         error
	runCalled      chan struct{} // closed when RunExtraction starts
	runBlock       chan struct{} // blocks RunExtraction until closed
	lastRepo       string
	lastImportPath string
}

func (m *mockExtractionRunner) RemoteHeadCommit(_ context.Context, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.headCommit, m.headErr
}

func (m *mockExtractionRunner) RunExtraction(_ context.Context, repo, importPath string) error {
	m.mu.Lock()
	m.lastRepo = repo
	m.lastImportPath = importPath
	m.mu.Unlock()

	if m.runCalled != nil {
		close(m.runCalled)
	}
	if m.runBlock != nil {
		<-m.runBlock
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runErr
}

// newTestPool creates a DBPool backed by a temp directory.
func newTestPool(t *testing.T) *DBPool {
	t.Helper()
	dir := t.TempDir()
	return NewDBPool(dir, 5)
}

// createTestClaimsDB creates a claims DB file for the given repo in the pool's data dir,
// optionally setting extraction meta with a commit SHA.
func createTestClaimsDB(t *testing.T, pool *DBPool, repo, commitSHA string) {
	t.Helper()
	dbPath := filepath.Join(pool.DataDir(), repo+".claims.db")
	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open test claims db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if commitSHA != "" {
		if err := cdb.SetExtractionMeta(db.ExtractionMeta{
			CommitSHA:   commitSHA,
			ExtractedAt: time.Now().UTC().Format(time.RFC3339),
			RepoRoot:    "/tmp/" + repo,
		}); err != nil {
			t.Fatalf("set extraction meta: %v", err)
		}
	}
	cdb.Close()
}

func TestRequestExtraction_MissingRepo(t *testing.T) {
	pool := newTestPool(t)
	tracker := NewExtractionTracker(&mockExtractionRunner{})
	handler := RequestExtractionHandler(pool, tracker)

	req := &testToolRequest{args: map[string]any{}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Errorf("expected error result for missing repo, got: %s", result.Text())
	}
}

func TestRequestExtraction_NilRunner(t *testing.T) {
	pool := newTestPool(t)
	tracker := NewExtractionTracker(nil)
	handler := RequestExtractionHandler(pool, tracker)

	req := &testToolRequest{args: map[string]any{"repo": "test-repo"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp requestExtractionResponse
	if jsonErr := json.Unmarshal([]byte(result.Text()), &resp); jsonErr != nil {
		t.Fatalf("unmarshal response: %v", jsonErr)
	}
	if resp.Status != "error" {
		t.Errorf("expected status 'error', got %q", resp.Status)
	}
}

func TestRequestExtraction_NilTracker(t *testing.T) {
	pool := newTestPool(t)
	handler := RequestExtractionHandler(pool, nil)

	req := &testToolRequest{args: map[string]any{"repo": "test-repo"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp requestExtractionResponse
	if jsonErr := json.Unmarshal([]byte(result.Text()), &resp); jsonErr != nil {
		t.Fatalf("unmarshal response: %v", jsonErr)
	}
	if resp.Status != "error" {
		t.Errorf("expected status 'error', got %q", resp.Status)
	}
}

func TestRequestExtraction_NoDB_Queued(t *testing.T) {
	pool := newTestPool(t)
	runCalled := make(chan struct{})
	runBlock := make(chan struct{})
	runner := &mockExtractionRunner{
		headCommit: "abc123",
		runCalled:  runCalled,
		runBlock:   runBlock,
	}
	tracker := NewExtractionTracker(runner)
	handler := RequestExtractionHandler(pool, tracker)

	// No DB file exists for "new-repo"
	req := &testToolRequest{args: map[string]any{"repo": "new-repo", "import_path": "pkg/foo"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp requestExtractionResponse
	if jsonErr := json.Unmarshal([]byte(result.Text()), &resp); jsonErr != nil {
		t.Fatalf("unmarshal response: %v", jsonErr)
	}
	if resp.Status != statusQueued {
		t.Errorf("expected status %q, got %q", statusQueued, resp.Status)
	}
	if resp.Repo != "new-repo" {
		t.Errorf("expected repo 'new-repo', got %q", resp.Repo)
	}

	// Wait for the goroutine to start.
	<-runCalled

	// Verify the runner received the correct parameters.
	runner.mu.Lock()
	if runner.lastRepo != "new-repo" {
		t.Errorf("expected runner repo 'new-repo', got %q", runner.lastRepo)
	}
	if runner.lastImportPath != "pkg/foo" {
		t.Errorf("expected runner import_path 'pkg/foo', got %q", runner.lastImportPath)
	}
	runner.mu.Unlock()

	// Should be in-progress now.
	if !tracker.IsInProgress("new-repo") {
		t.Error("expected in-progress after queuing")
	}

	// Unblock the extraction.
	close(runBlock)

	// Wait for goroutine to finish.
	time.Sleep(50 * time.Millisecond)
	if tracker.IsInProgress("new-repo") {
		t.Error("expected not in-progress after extraction completes")
	}
}

func TestRequestExtraction_AlreadyFresh(t *testing.T) {
	pool := newTestPool(t)
	commitSHA := "def456"
	createTestClaimsDB(t, pool, "fresh-repo", commitSHA)

	runner := &mockExtractionRunner{headCommit: commitSHA}
	tracker := NewExtractionTracker(runner)
	handler := RequestExtractionHandler(pool, tracker)

	req := &testToolRequest{args: map[string]any{"repo": "fresh-repo"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp requestExtractionResponse
	if jsonErr := json.Unmarshal([]byte(result.Text()), &resp); jsonErr != nil {
		t.Fatalf("unmarshal response: %v", jsonErr)
	}
	if resp.Status != statusAlreadyFresh {
		t.Errorf("expected status %q, got %q", statusAlreadyFresh, resp.Status)
	}
	if resp.Repo != "fresh-repo" {
		t.Errorf("expected repo 'fresh-repo', got %q", resp.Repo)
	}
}

func TestRequestExtraction_StaleDB_Queued(t *testing.T) {
	pool := newTestPool(t)
	createTestClaimsDB(t, pool, "stale-repo", "old-commit")

	runBlock := make(chan struct{})
	runner := &mockExtractionRunner{
		headCommit: "new-commit",
		runBlock:   runBlock,
	}
	tracker := NewExtractionTracker(runner)
	handler := RequestExtractionHandler(pool, tracker)

	req := &testToolRequest{args: map[string]any{"repo": "stale-repo"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp requestExtractionResponse
	if jsonErr := json.Unmarshal([]byte(result.Text()), &resp); jsonErr != nil {
		t.Fatalf("unmarshal response: %v", jsonErr)
	}
	if resp.Status != statusQueued {
		t.Errorf("expected status %q, got %q", statusQueued, resp.Status)
	}

	// Unblock extraction goroutine.
	close(runBlock)
	time.Sleep(50 * time.Millisecond)
}

func TestRequestExtraction_InProgress(t *testing.T) {
	pool := newTestPool(t)

	runBlock := make(chan struct{})
	runCalled := make(chan struct{})
	runner := &mockExtractionRunner{
		headCommit: "abc",
		runCalled:  runCalled,
		runBlock:   runBlock,
	}
	tracker := NewExtractionTracker(runner)
	handler := RequestExtractionHandler(pool, tracker)

	// First call: triggers extraction (no DB exists).
	req := &testToolRequest{args: map[string]any{"repo": "busy-repo"}}
	result1, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error on first call: %v", err)
	}

	var resp1 requestExtractionResponse
	if jsonErr := json.Unmarshal([]byte(result1.Text()), &resp1); jsonErr != nil {
		t.Fatalf("unmarshal first response: %v", jsonErr)
	}
	if resp1.Status != statusQueued {
		t.Errorf("first call: expected status %q, got %q", statusQueued, resp1.Status)
	}

	// Wait for extraction goroutine to start.
	<-runCalled

	// Second call: should see in_progress.
	result2, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}

	var resp2 requestExtractionResponse
	if jsonErr := json.Unmarshal([]byte(result2.Text()), &resp2); jsonErr != nil {
		t.Fatalf("unmarshal second response: %v", jsonErr)
	}
	if resp2.Status != statusInProgress {
		t.Errorf("second call: expected status %q, got %q", statusInProgress, resp2.Status)
	}

	// Unblock extraction.
	close(runBlock)
	time.Sleep(50 * time.Millisecond)
}

func TestRequestExtraction_DBExistsNoCommitSHA_Queued(t *testing.T) {
	pool := newTestPool(t)
	// Create a DB with empty commit SHA — simulates a DB extracted without meta.
	createTestClaimsDB(t, pool, "no-sha-repo", "")

	runBlock := make(chan struct{})
	runner := &mockExtractionRunner{
		headCommit: "remote-head",
		runBlock:   runBlock,
	}
	tracker := NewExtractionTracker(runner)
	handler := RequestExtractionHandler(pool, tracker)

	req := &testToolRequest{args: map[string]any{"repo": "no-sha-repo"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp requestExtractionResponse
	if jsonErr := json.Unmarshal([]byte(result.Text()), &resp); jsonErr != nil {
		t.Fatalf("unmarshal response: %v", jsonErr)
	}
	if resp.Status != statusQueued {
		t.Errorf("expected status %q, got %q", statusQueued, resp.Status)
	}

	close(runBlock)
	time.Sleep(50 * time.Millisecond)
}

func TestRequestExtraction_ToolDef(t *testing.T) {
	pool := newTestPool(t)
	tracker := NewExtractionTracker(&mockExtractionRunner{})
	def := RequestExtractionToolDef(pool, tracker)

	if def.Name != "request_extraction" {
		t.Errorf("expected tool name 'request_extraction', got %q", def.Name)
	}
	if len(def.Params) != 2 {
		t.Errorf("expected 2 params, got %d", len(def.Params))
	}

	// Verify repo param is required.
	foundRepo := false
	for _, p := range def.Params {
		if p.Name == "repo" {
			foundRepo = true
			if !p.Required {
				t.Error("repo param should be required")
			}
		}
	}
	if !foundRepo {
		t.Error("expected 'repo' param in tool definition")
	}
}

func TestDataDirAccessor(t *testing.T) {
	dir := t.TempDir()
	pool := NewDBPool(dir, 5)
	if pool.DataDir() != dir {
		t.Errorf("expected DataDir() = %q, got %q", dir, pool.DataDir())
	}
}

// createTestClaimsDBWithMeta is a helper that creates a DB file at the expected path
// and verifies it can be opened via the pool.
func TestRequestExtraction_DBFileExistsButPoolOpenFails(t *testing.T) {
	pool := newTestPool(t)

	// Create a corrupt DB file.
	dbPath := filepath.Join(pool.DataDir(), "corrupt-repo.claims.db")
	if err := os.WriteFile(dbPath, []byte("not a sqlite db"), 0o644); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}

	runner := &mockExtractionRunner{headCommit: "abc"}
	tracker := NewExtractionTracker(runner)
	handler := RequestExtractionHandler(pool, tracker)

	req := &testToolRequest{args: map[string]any{"repo": "corrupt-repo"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return an error result since pool.Open fails.
	if !result.IsError() {
		t.Errorf("expected error result for corrupt DB, got: %s", result.Text())
	}
}
