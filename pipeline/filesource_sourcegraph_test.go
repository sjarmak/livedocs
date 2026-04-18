package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjarmak/livedocs/gitdiff"
)

// mockMCPCaller is a test double that returns canned responses keyed by tool name.
// It is safe for concurrent use.
type mockMCPCaller struct {
	responses map[string]string
	errors    map[string]error

	mu    sync.Mutex
	calls []mockCall
}

type mockCall struct {
	ToolName string
	Args     map[string]any
}

func (m *mockMCPCaller) CallTool(_ context.Context, toolName string, args map[string]any) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, mockCall{ToolName: toolName, Args: args})
	m.mu.Unlock()
	if err, ok := m.errors[toolName]; ok {
		return "", err
	}
	return m.responses[toolName], nil
}

// mockToolLister is a test double for ToolLister.
type mockToolLister struct {
	tools []string
	err   error
}

func (m *mockToolLister) ListTools(_ context.Context) ([]string, error) {
	return m.tools, m.err
}

func TestNewSourcegraphFileSource_Success(t *testing.T) {
	caller := &mockMCPCaller{responses: map[string]string{}}
	lister := &mockToolLister{tools: []string{"read_file", "list_files", "compare_revisions"}}

	src, err := NewSourcegraphFileSource(caller, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src == nil {
		t.Fatal("expected non-nil source")
	}
}

func TestNewSourcegraphFileSource_MissingReadFile(t *testing.T) {
	caller := &mockMCPCaller{responses: map[string]string{}}
	lister := &mockToolLister{tools: []string{"list_files"}}

	_, err := NewSourcegraphFileSource(caller, lister)
	if err == nil {
		t.Fatal("expected error for missing read_file")
	}
	if !strings.Contains(err.Error(), "read_file") {
		t.Errorf("error should mention read_file, got: %v", err)
	}
}

func TestNewSourcegraphFileSource_MissingListFiles(t *testing.T) {
	caller := &mockMCPCaller{responses: map[string]string{}}
	lister := &mockToolLister{tools: []string{"read_file"}}

	_, err := NewSourcegraphFileSource(caller, lister)
	if err == nil {
		t.Fatal("expected error for missing list_files")
	}
	if !strings.Contains(err.Error(), "list_files") {
		t.Errorf("error should mention list_files, got: %v", err)
	}
}

func TestNewSourcegraphFileSource_ListToolsError(t *testing.T) {
	caller := &mockMCPCaller{responses: map[string]string{}}
	lister := &mockToolLister{err: fmt.Errorf("connection refused")}

	_, err := NewSourcegraphFileSource(caller, lister)
	if err == nil {
		t.Fatal("expected error when ListTools fails")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error should wrap ListTools error, got: %v", err)
	}
}

func TestReadFile(t *testing.T) {
	caller := &mockMCPCaller{
		responses: map[string]string{
			"read_file": "package main\n\nfunc main() {}\n",
		},
	}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := src.ReadFile(context.Background(), "org/repo", "main", "main.go")
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	expected := "package main\n\nfunc main() {}\n"
	if string(data) != expected {
		t.Errorf("got %q, want %q", string(data), expected)
	}

	// Verify correct tool call args.
	if len(caller.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(caller.calls))
	}
	call := caller.calls[0]
	if call.ToolName != "read_file" {
		t.Errorf("expected tool read_file, got %s", call.ToolName)
	}
	if call.Args["repo"] != "org/repo" {
		t.Errorf("expected repo org/repo, got %v", call.Args["repo"])
	}
	if call.Args["path"] != "main.go" {
		t.Errorf("expected path main.go, got %v", call.Args["path"])
	}
	if call.Args["revision"] != "main" {
		t.Errorf("expected revision main, got %v", call.Args["revision"])
	}
}

func TestReadFile_Error(t *testing.T) {
	caller := &mockMCPCaller{
		responses: map[string]string{},
		errors:    map[string]error{"read_file": fmt.Errorf("file not found")},
	}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = src.ReadFile(context.Background(), "org/repo", "", "missing.go")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("error should wrap caller error, got: %v", err)
	}
}

func TestListFiles(t *testing.T) {
	caller := &mockMCPCaller{
		responses: map[string]string{
			"list_files": "src/main.go\nsrc/util.go\nsrc/handler.go\n",
		},
	}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	paths, err := src.ListFiles(context.Background(), "org/repo", "main", "*.go")
	if err != nil {
		t.Fatalf("ListFiles error: %v", err)
	}

	expected := []string{"src/main.go", "src/util.go", "src/handler.go"}
	if len(paths) != len(expected) {
		t.Fatalf("got %d paths, want %d", len(paths), len(expected))
	}
	for i, p := range paths {
		if p != expected[i] {
			t.Errorf("path[%d] = %q, want %q", i, p, expected[i])
		}
	}
}

func TestListFiles_Empty(t *testing.T) {
	caller := &mockMCPCaller{
		responses: map[string]string{
			"list_files": "",
		},
	}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	paths, err := src.ListFiles(context.Background(), "org/repo", "", "*.xyz")
	if err != nil {
		t.Fatalf("ListFiles error: %v", err)
	}
	if paths != nil {
		t.Errorf("expected nil for empty result, got %v", paths)
	}
}

func TestDiffBetween_NameStatusFormat(t *testing.T) {
	caller := &mockMCPCaller{
		responses: map[string]string{
			"compare_revisions": "A\tsrc/new.go\nM\tsrc/main.go\nD\tsrc/old.go\n",
		},
	}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	changes, err := src.DiffBetween(context.Background(), "org/repo", "abc123", "def456")
	if err != nil {
		t.Fatalf("DiffBetween error: %v", err)
	}

	if len(changes) != 3 {
		t.Fatalf("got %d changes, want 3", len(changes))
	}

	tests := []struct {
		status gitdiff.ChangeStatus
		path   string
	}{
		{gitdiff.StatusAdded, "src/new.go"},
		{gitdiff.StatusModified, "src/main.go"},
		{gitdiff.StatusDeleted, "src/old.go"},
	}

	for i, tt := range tests {
		if changes[i].Status != tt.status {
			t.Errorf("changes[%d].Status = %q, want %q", i, changes[i].Status, tt.status)
		}
		if changes[i].Path != tt.path {
			t.Errorf("changes[%d].Path = %q, want %q", i, changes[i].Path, tt.path)
		}
	}

	// Verify args.
	call := caller.calls[0]
	if call.Args["repo"] != "org/repo" {
		t.Errorf("expected repo org/repo, got %v", call.Args["repo"])
	}
	if call.Args["from"] != "abc123" {
		t.Errorf("expected from abc123, got %v", call.Args["from"])
	}
	if call.Args["to"] != "def456" {
		t.Errorf("expected to def456, got %v", call.Args["to"])
	}
}

func TestDiffBetween_UnifiedDiffFormat(t *testing.T) {
	unifiedDiff := `diff --git a/src/new.go b/src/new.go
--- /dev/null
+++ b/src/new.go
@@ -0,0 +1,5 @@
+package src
diff --git a/src/main.go b/src/main.go
--- a/src/main.go
+++ b/src/main.go
@@ -1,3 +1,4 @@
 package src
+// modified
diff --git a/src/old.go b/src/old.go
--- a/src/old.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package src
`

	caller := &mockMCPCaller{
		responses: map[string]string{
			"compare_revisions": unifiedDiff,
		},
	}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	changes, err := src.DiffBetween(context.Background(), "org/repo", "abc123", "def456")
	if err != nil {
		t.Fatalf("DiffBetween error: %v", err)
	}

	if len(changes) != 3 {
		t.Fatalf("got %d changes, want 3", len(changes))
	}

	tests := []struct {
		status gitdiff.ChangeStatus
		path   string
	}{
		{gitdiff.StatusAdded, "src/new.go"},
		{gitdiff.StatusModified, "src/main.go"},
		{gitdiff.StatusDeleted, "src/old.go"},
	}

	for i, tt := range tests {
		if changes[i].Status != tt.status {
			t.Errorf("changes[%d].Status = %q, want %q", i, changes[i].Status, tt.status)
		}
		if changes[i].Path != tt.path {
			t.Errorf("changes[%d].Path = %q, want %q", i, changes[i].Path, tt.path)
		}
	}
}

func TestDiffBetween_Empty(t *testing.T) {
	caller := &mockMCPCaller{
		responses: map[string]string{
			"compare_revisions": "",
		},
	}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	changes, err := src.DiffBetween(context.Background(), "org/repo", "abc123", "abc123")
	if err != nil {
		t.Fatalf("DiffBetween error: %v", err)
	}
	if changes != nil {
		t.Errorf("expected nil for empty diff, got %v", changes)
	}
}

func TestDiffBetween_Error(t *testing.T) {
	caller := &mockMCPCaller{
		responses: map[string]string{},
		errors:    map[string]error{"compare_revisions": fmt.Errorf("revision not found")},
	}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = src.DiffBetween(context.Background(), "org/repo", "bad", "ref")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "revision not found") {
		t.Errorf("error should wrap caller error, got: %v", err)
	}
}

func TestReadFile_NoRevision(t *testing.T) {
	caller := &mockMCPCaller{
		responses: map[string]string{
			"read_file": "content",
		},
	}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = src.ReadFile(context.Background(), "org/repo", "", "file.go")
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	// Verify revision is not included when empty.
	call := caller.calls[0]
	if _, ok := call.Args["revision"]; ok {
		t.Error("revision should not be set when empty")
	}
}

// slowMCPCaller adds artificial latency to each CallTool invocation and
// tracks the maximum number of concurrent in-flight calls.
type slowMCPCaller struct {
	latency     time.Duration
	response    string
	maxInFlight atomic.Int32
	curInFlight atomic.Int32
}

func (m *slowMCPCaller) CallTool(_ context.Context, _ string, _ map[string]any) (string, error) {
	cur := m.curInFlight.Add(1)
	defer m.curInFlight.Add(-1)

	// Track peak concurrency.
	for {
		old := m.maxInFlight.Load()
		if cur <= old || m.maxInFlight.CompareAndSwap(old, cur) {
			break
		}
	}

	time.Sleep(m.latency)
	return m.response, nil
}

func TestWithConcurrency_DefaultIs10(t *testing.T) {
	caller := &mockMCPCaller{responses: map[string]string{}}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src.Concurrency() != 10 {
		t.Errorf("default concurrency = %d, want 10", src.Concurrency())
	}
}

func TestWithConcurrency_Custom(t *testing.T) {
	caller := &mockMCPCaller{responses: map[string]string{}}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister, WithConcurrency(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src.Concurrency() != 5 {
		t.Errorf("concurrency = %d, want 5", src.Concurrency())
	}
}

func TestWithConcurrency_MinimumIs1(t *testing.T) {
	caller := &mockMCPCaller{responses: map[string]string{}}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister, WithConcurrency(0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src.Concurrency() != 1 {
		t.Errorf("concurrency = %d, want 1 (minimum)", src.Concurrency())
	}
}

func TestBatchReadFiles_ConcurrentSpeedup(t *testing.T) {
	const (
		fileCount   = 20
		concurrency = 10
		latency     = 50 * time.Millisecond
	)

	slow := &slowMCPCaller{latency: latency, response: "content"}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(slow, lister, WithConcurrency(concurrency))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	paths := make([]string, fileCount)
	for i := range paths {
		paths[i] = fmt.Sprintf("file%d.go", i)
	}

	start := time.Now()
	results := src.BatchReadFiles(context.Background(), "org/repo", "", paths)
	elapsed := time.Since(start)

	// Verify all results returned.
	if len(results) != fileCount {
		t.Fatalf("got %d results, want %d", len(results), fileCount)
	}
	for i, r := range results {
		if r.Err != nil {
			t.Errorf("file %d: unexpected error: %v", i, r.Err)
		}
		if string(r.Content) != "content" {
			t.Errorf("file %d: content = %q, want %q", i, string(r.Content), "content")
		}
	}

	// Serial time would be fileCount * latency = 1000ms.
	// Concurrent time should be ~fileCount/concurrency * latency = ~100ms.
	// Allow generous margin: should complete in less than half of serial time.
	serialTime := time.Duration(fileCount) * latency
	maxExpected := serialTime / 2
	if elapsed > maxExpected {
		t.Errorf("batch took %v, expected < %v (serial would be %v)", elapsed, maxExpected, serialTime)
	}
}

func TestBatchReadFiles_ConcurrencyLimitRespected(t *testing.T) {
	const (
		fileCount   = 30
		concurrency = 5
		latency     = 30 * time.Millisecond
	)

	slow := &slowMCPCaller{latency: latency, response: "data"}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(slow, lister, WithConcurrency(concurrency))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	paths := make([]string, fileCount)
	for i := range paths {
		paths[i] = fmt.Sprintf("file%d.go", i)
	}

	src.BatchReadFiles(context.Background(), "org/repo", "", paths)

	// Verify the maximum number of concurrent calls never exceeded the limit.
	maxSeen := slow.maxInFlight.Load()
	if maxSeen > int32(concurrency) {
		t.Errorf("max concurrent calls = %d, want <= %d", maxSeen, concurrency)
	}
	if maxSeen < 2 {
		t.Errorf("max concurrent calls = %d, expected at least 2 (concurrency not working)", maxSeen)
	}
}

func TestReadFile_SemaphoreLimitsConcurrency(t *testing.T) {
	// Verify that even direct ReadFile calls respect the semaphore.
	const (
		concurrency = 3
		callers     = 10
		latency     = 30 * time.Millisecond
	)

	slow := &slowMCPCaller{latency: latency, response: "data"}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(slow, lister, WithConcurrency(concurrency))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _ = src.ReadFile(context.Background(), "org/repo", "", fmt.Sprintf("file%d.go", idx))
		}(i)
	}
	wg.Wait()

	maxSeen := slow.maxInFlight.Load()
	if maxSeen > int32(concurrency) {
		t.Errorf("max concurrent ReadFile calls = %d, want <= %d", maxSeen, concurrency)
	}
}

func TestBatchReadFiles_ErrorHandling(t *testing.T) {
	// Mock that returns errors for specific paths.
	caller := &mockMCPCaller{
		responses: map[string]string{"read_file": "ok"},
		errors:    map[string]error{"read_file": fmt.Errorf("not found")},
	}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	src, err := NewSourcegraphFileSource(caller, lister, WithConcurrency(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	paths := []string{"a.go", "b.go", "c.go"}
	results := src.BatchReadFiles(context.Background(), "org/repo", "", paths)

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	// All should have errors since the mock always returns errors for read_file.
	for _, r := range results {
		if r.Err == nil {
			t.Errorf("expected error for %s", r.Path)
		}
	}
}

func TestReadFile_ContextCancellation(t *testing.T) {
	slow := &slowMCPCaller{latency: time.Second, response: "data"}
	lister := &mockToolLister{tools: []string{"read_file", "list_files"}}

	// Concurrency of 1, so second call blocks on semaphore.
	src, err := NewSourcegraphFileSource(slow, lister, WithConcurrency(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Start a call that holds the semaphore.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = src.ReadFile(context.Background(), "org/repo", "", "holder.go")
	}()

	// Give the first goroutine time to acquire the semaphore.
	time.Sleep(10 * time.Millisecond)

	// Try a second call with a cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err = src.ReadFile(ctx, "org/repo", "", "blocked.go")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}

	wg.Wait()
}
