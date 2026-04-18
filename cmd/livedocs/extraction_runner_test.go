package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/mcpserver"
)

// mockMCPCaller records tool calls and returns canned responses.
type mockMCPCaller struct {
	mu    sync.Mutex
	calls []mockCall

	// commitResponse is returned for commit_search calls.
	commitResponse string
	commitErr      error

	// listFilesResponse is returned for list_files calls.
	listFilesResponse string

	// readFileResponses maps path -> content for read_file calls.
	readFileResponses map[string]string

	// compareResponse is returned for compare_revisions calls.
	compareResponse string

	// defaultResponse is returned for unmatched tool calls.
	defaultResponse string
}

type mockCall struct {
	ToolName string
	Args     map[string]any
}

func (m *mockMCPCaller) CallTool(_ context.Context, toolName string, args map[string]any) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, mockCall{ToolName: toolName, Args: args})
	m.mu.Unlock()

	switch toolName {
	case "commit_search":
		if m.commitErr != nil {
			return "", m.commitErr
		}
		return m.commitResponse, nil
	case "list_files":
		return m.listFilesResponse, nil
	case "read_file":
		if m.readFileResponses != nil {
			path, _ := args["path"].(string)
			if resp, ok := m.readFileResponses[path]; ok {
				return resp, nil
			}
		}
		return m.defaultResponse, nil
	case "compare_revisions":
		return m.compareResponse, nil
	default:
		return m.defaultResponse, nil
	}
}

func (m *mockMCPCaller) getCalls() []mockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mockCall, len(m.calls))
	copy(result, m.calls)
	return result
}

// TestExtractionRunnerImplementsInterface verifies that extractionRunner
// satisfies the mcpserver.ExtractionRunner interface at compile time.
func TestExtractionRunnerImplementsInterface(t *testing.T) {
	var _ mcpserver.ExtractionRunner = (*extractionRunner)(nil)
}

func TestRemoteHeadCommit_ParsesSHA(t *testing.T) {
	mock := &mockMCPCaller{
		commitResponse: `Commit: abc123def456789012345678901234567890abcd
Author: Test User
Date: 2024-01-01
Message: Initial commit`,
	}

	runner := newExtractionRunner(mock, t.TempDir(), 1)
	sha, err := runner.RemoteHeadCommit(context.Background(), "github.com/org/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "abc123def456789012345678901234567890abcd" {
		t.Errorf("got SHA %q, want %q", sha, "abc123def456789012345678901234567890abcd")
	}

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].ToolName != "commit_search" {
		t.Errorf("expected commit_search call, got %s", calls[0].ToolName)
	}
}

func TestRemoteHeadCommit_NoSHAInResponse(t *testing.T) {
	mock := &mockMCPCaller{
		commitResponse: "No commits found",
	}

	runner := newExtractionRunner(mock, t.TempDir(), 1)
	_, err := runner.RemoteHeadCommit(context.Background(), "github.com/org/repo")
	if err == nil {
		t.Fatal("expected error for missing SHA")
	}
	if !strings.Contains(err.Error(), "no commit SHA found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRemoteHeadCommit_CallToolError(t *testing.T) {
	mock := &mockMCPCaller{
		commitErr: fmt.Errorf("network error"),
	}

	runner := newExtractionRunner(mock, t.TempDir(), 1)
	_, err := runner.RemoteHeadCommit(context.Background(), "github.com/org/repo")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "network error") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRepoNameFromPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"github.com/org/repo", "repo"},
		{"repo", "repo"},
		{"github.com/kubernetes/kubernetes", "kubernetes"},
		{"a/b/c", "c"},
	}
	for _, tt := range tests {
		got := repoNameFromPath(tt.input)
		if got != tt.want {
			t.Errorf("repoNameFromPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRunExtraction_FullExtraction_InvokesMCPCaller(t *testing.T) {
	dataDir := t.TempDir()
	headSHA := "abc123def456789012345678901234567890abcd"

	mock := &mockMCPCaller{
		commitResponse:    fmt.Sprintf("Commit: %s\nAuthor: Test", headSHA),
		listFilesResponse: "main.go\nutils.go",
		readFileResponses: map[string]string{
			"main.go":  "package main\n\nfunc main() {}\n",
			"utils.go": "package main\n\nfunc helper() string { return \"\" }\n",
		},
	}

	runner := newExtractionRunner(mock, dataDir, 2)

	err := runner.RunExtraction(context.Background(), "github.com/test/repo", "")
	if err != nil {
		t.Fatalf("RunExtraction failed: %v", err)
	}

	// Verify DB was created.
	dbPath := filepath.Join(dataDir, "repo.claims.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("claims DB was not created at %s", dbPath)
	}

	// Verify extraction metadata was stored.
	claimsDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open claims DB: %v", err)
	}
	defer claimsDB.Close()

	meta, err := claimsDB.GetExtractionMeta()
	if err != nil {
		t.Fatalf("get extraction meta: %v", err)
	}
	if meta.CommitSHA != headSHA {
		t.Errorf("commit SHA = %q, want %q", meta.CommitSHA, headSHA)
	}
	if meta.RepoRoot != "github.com/test/repo" {
		t.Errorf("repo root = %q, want %q", meta.RepoRoot, "github.com/test/repo")
	}

	// Verify MCP caller was invoked with list_files and commit_search.
	calls := mock.getCalls()
	toolNames := make(map[string]int)
	for _, c := range calls {
		toolNames[c.ToolName]++
	}
	if toolNames["list_files"] < 1 {
		t.Error("expected at least one list_files call")
	}
	if toolNames["commit_search"] < 1 {
		t.Error("expected at least one commit_search call")
	}
}

func TestRunExtraction_IncrementalExtraction(t *testing.T) {
	dataDir := t.TempDir()
	oldSHA := "0000000000000000000000000000000000000000"
	newSHA := "1111111111111111111111111111111111111111"

	// Create an existing claims DB with old commit SHA.
	dbPath := filepath.Join(dataDir, "repo.claims.db")
	existingDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("create existing DB: %v", err)
	}
	if err := existingDB.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := existingDB.SetExtractionMeta(db.ExtractionMeta{
		CommitSHA:   oldSHA,
		ExtractedAt: db.Now(),
		RepoRoot:    "github.com/test/repo",
	}); err != nil {
		t.Fatalf("set extraction meta: %v", err)
	}
	existingDB.Close()

	mock := &mockMCPCaller{
		commitResponse:  fmt.Sprintf("Commit: %s\nAuthor: Test", newSHA),
		compareResponse: "A\tnewfile.go",
		readFileResponses: map[string]string{
			"newfile.go": "package main\n\nfunc newFunc() {}\n",
		},
	}

	runner := newExtractionRunner(mock, dataDir, 2)

	err = runner.RunExtraction(context.Background(), "github.com/test/repo", "")
	if err != nil {
		t.Fatalf("RunExtraction failed: %v", err)
	}

	// Verify metadata was updated with new SHA.
	updatedDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open updated DB: %v", err)
	}
	defer updatedDB.Close()

	meta, err := updatedDB.GetExtractionMeta()
	if err != nil {
		t.Fatalf("get extraction meta: %v", err)
	}
	if meta.CommitSHA != newSHA {
		t.Errorf("commit SHA = %q, want %q", meta.CommitSHA, newSHA)
	}

	// Verify compare_revisions was called (incremental path).
	calls := mock.getCalls()
	hasCompare := false
	for _, c := range calls {
		if c.ToolName == "compare_revisions" {
			hasCompare = true
			break
		}
	}
	if !hasCompare {
		t.Error("expected compare_revisions call for incremental extraction")
	}
}

func TestRunExtraction_AlreadyUpToDate(t *testing.T) {
	dataDir := t.TempDir()
	sha := "abc123def456789012345678901234567890abcd"

	// Create existing DB at the same commit.
	dbPath := filepath.Join(dataDir, "repo.claims.db")
	existingDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("create existing DB: %v", err)
	}
	if err := existingDB.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := existingDB.SetExtractionMeta(db.ExtractionMeta{
		CommitSHA:   sha,
		ExtractedAt: db.Now(),
		RepoRoot:    "github.com/test/repo",
	}); err != nil {
		t.Fatalf("set extraction meta: %v", err)
	}
	existingDB.Close()

	mock := &mockMCPCaller{
		commitResponse: fmt.Sprintf("Commit: %s\nAuthor: Test", sha),
	}

	runner := newExtractionRunner(mock, dataDir, 1)

	err = runner.RunExtraction(context.Background(), "github.com/test/repo", "")
	if err != nil {
		t.Fatalf("RunExtraction failed: %v", err)
	}

	// Should not have called list_files or compare_revisions (short-circuit).
	calls := mock.getCalls()
	for _, c := range calls {
		if c.ToolName == "list_files" || c.ToolName == "compare_revisions" {
			t.Errorf("unexpected %s call when already up-to-date", c.ToolName)
		}
	}
}

// TestRequestExtractionIntegration verifies the end-to-end flow: the MCP
// request_extraction handler returns "queued" and the runner is invoked.
func TestRequestExtractionIntegration(t *testing.T) {
	dataDir := t.TempDir()
	headSHA := "abc123def456789012345678901234567890abcd"

	mock := &mockMCPCaller{
		commitResponse:    fmt.Sprintf("Commit: %s\nAuthor: Test", headSHA),
		listFilesResponse: "main.go",
		readFileResponses: map[string]string{
			"main.go": "package main\n\nfunc main() {}\n",
		},
	}

	runner := newExtractionRunner(mock, dataDir, 2)
	tracker := mcpserver.NewExtractionTracker(runner)

	// Create a DBPool pointing at our data dir.
	pool := mcpserver.NewDBPool(dataDir, 5)

	handler := mcpserver.RequestExtractionHandler(pool, tracker)

	// Call with a repo that has no claims DB -> should queue.
	// Note: the request_extraction handler validates repo names and rejects
	// path separators, so use a short name like the existing tests do.
	req := mcpserver.WrapRequest(mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"repo": "myrepo",
			},
		},
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	// Parse the response.
	var resp struct {
		Status  string `json:"status"`
		Repo    string `json:"repo"`
		Message string `json:"message"`
	}

	text := result.Text()
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", text, err)
	}

	if resp.Status != "queued" {
		t.Errorf("status = %q, want %q", resp.Status, "queued")
	}
	if resp.Repo != "myrepo" {
		t.Errorf("repo = %q, want %q", resp.Repo, "myrepo")
	}

	// Wait for background extraction to complete.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !tracker.IsInProgress("myrepo") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if tracker.IsInProgress("myrepo") {
		t.Fatal("extraction still in progress after timeout")
	}

	// Verify the runner was invoked (claims DB should exist).
	dbPath := filepath.Join(dataDir, "myrepo.claims.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("claims DB was not created at %s", dbPath)
	}
}

func TestNewExtractionRunner_DefaultConcurrency(t *testing.T) {
	runner := newExtractionRunner(nil, "/tmp", 0)
	if runner.concurrency != 10 {
		t.Errorf("concurrency = %d, want 10", runner.concurrency)
	}
}
