package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/live-docs/live_docs/gitdiff"
)

// mockMCPCaller is a test double that returns canned responses keyed by tool name.
type mockMCPCaller struct {
	responses map[string]string
	errors    map[string]error
	calls     []mockCall
}

type mockCall struct {
	ToolName string
	Args     map[string]any
}

func (m *mockMCPCaller) CallTool(_ context.Context, toolName string, args map[string]any) (string, error) {
	m.calls = append(m.calls, mockCall{ToolName: toolName, Args: args})
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
