package watch

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

// --- LocalGitOps tests ---

// initGitRepo creates a git repo in dir with an initial commit and returns
// the HEAD SHA.
func initGitRepo(t *testing.T, dir string) string {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "initial"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %s\n%s", args, err, out)
		}
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out[:len(out)-1]) // trim newline
}

// addCommit creates a new empty commit and returns its SHA.
func addCommit(t *testing.T, dir, msg string) string {
	t.Helper()
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", msg)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit failed: %s\n%s", err, out)
	}
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out[:len(out)-1])
}

func TestLocalGitOps_RevParseHEAD(t *testing.T) {
	dir := t.TempDir()
	expectedSHA := initGitRepo(t, dir)

	ops := LocalGitOps{}
	sha, err := ops.RevParseHEAD(context.Background(), dir)
	if err != nil {
		t.Fatalf("RevParseHEAD: %v", err)
	}
	if sha != expectedSHA {
		t.Fatalf("RevParseHEAD = %q, want %q", sha, expectedSHA)
	}
}

func TestLocalGitOps_RevParseHEAD_InvalidDir(t *testing.T) {
	ops := LocalGitOps{}
	_, err := ops.RevParseHEAD(context.Background(), filepath.Join(t.TempDir(), "nonexistent"))
	if err == nil {
		t.Fatal("expected error for invalid directory")
	}
}

func TestLocalGitOps_IsAncestor_True(t *testing.T) {
	dir := t.TempDir()
	ancestorSHA := initGitRepo(t, dir)
	descendantSHA := addCommit(t, dir, "second")

	ops := LocalGitOps{}
	isAnc, err := ops.IsAncestor(context.Background(), dir, ancestorSHA, descendantSHA)
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if !isAnc {
		t.Fatal("expected ancestor=true")
	}
}

func TestLocalGitOps_IsAncestor_False(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	sha1 := addCommit(t, dir, "second")

	// Create orphan branch — sha1 is not ancestor of orphan HEAD.
	cmds := [][]string{
		{"git", "checkout", "--orphan", "orphan"},
		{"git", "commit", "--allow-empty", "-m", "orphan-commit"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %s\n%s", args, err, out)
		}
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	orphanSHA := string(out[:len(out)-1])

	ops := LocalGitOps{}
	isAnc, err := ops.IsAncestor(context.Background(), dir, sha1, orphanSHA)
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if isAnc {
		t.Fatal("expected ancestor=false for unrelated orphan commit")
	}
}

// --- RemoteGitOps tests ---

// mockMCPCaller implements MCPCaller for testing.
type mockMCPCaller struct {
	calls    []mcpCall
	response string
	err      error
}

type mcpCall struct {
	toolName string
	args     map[string]any
}

func (m *mockMCPCaller) CallTool(_ context.Context, toolName string, args map[string]any) (string, error) {
	m.calls = append(m.calls, mcpCall{toolName: toolName, args: args})
	return m.response, m.err
}

func TestRemoteGitOps_RevParseHEAD_Success(t *testing.T) {
	caller := &mockMCPCaller{
		response: "commit abc123def456789012345678901234567890abcd\nAuthor: Test <test@test.com>\nDate: 2026-01-01\n\nInitial commit\n",
	}
	ops := &RemoteGitOps{Caller: caller}

	sha, err := ops.RevParseHEAD(context.Background(), "kubernetes/kubernetes")
	if err != nil {
		t.Fatalf("RevParseHEAD: %v", err)
	}
	if sha != "abc123def456789012345678901234567890abcd" {
		t.Fatalf("RevParseHEAD = %q, want abc123def456789012345678901234567890abcd", sha)
	}

	// Verify the correct tool was called.
	if len(caller.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(caller.calls))
	}
	if caller.calls[0].toolName != "commit_search" {
		t.Fatalf("expected commit_search tool, got %q", caller.calls[0].toolName)
	}
}

func TestRemoteGitOps_RevParseHEAD_NoSHA(t *testing.T) {
	caller := &mockMCPCaller{
		response: "No commits found for this repository.",
	}
	ops := &RemoteGitOps{Caller: caller}

	_, err := ops.RevParseHEAD(context.Background(), "nonexistent/repo")
	if err == nil {
		t.Fatal("expected error when no SHA in response")
	}
}

func TestRemoteGitOps_RevParseHEAD_CallerError(t *testing.T) {
	caller := &mockMCPCaller{
		err: fmt.Errorf("connection refused"),
	}
	ops := &RemoteGitOps{Caller: caller}

	_, err := ops.RevParseHEAD(context.Background(), "some/repo")
	if err == nil {
		t.Fatal("expected error when caller fails")
	}
}

func TestRemoteGitOps_RevParseHEAD_NilCaller(t *testing.T) {
	ops := &RemoteGitOps{Caller: nil}

	_, err := ops.RevParseHEAD(context.Background(), "some/repo")
	if err == nil {
		t.Fatal("expected error when caller is nil")
	}
}

func TestRemoteGitOps_IsAncestor_AlwaysTrue(t *testing.T) {
	// RemoteGitOps.IsAncestor always returns true as a safe default
	// because Sourcegraph MCP doesn't support ancestry checks.
	ops := &RemoteGitOps{}

	isAnc, err := ops.IsAncestor(context.Background(), "some/repo", "sha1", "sha2")
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if !isAnc {
		t.Fatal("expected IsAncestor to always return true for RemoteGitOps")
	}
}
