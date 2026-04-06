package watch

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// GitOps defines the git operations the watcher needs. This interface
// enables testing without real git repos and supports both local git
// commands and remote backends (e.g., Sourcegraph MCP).
type GitOps interface {
	// RevParseHEAD returns the current HEAD SHA for the repo.
	RevParseHEAD(ctx context.Context, repo string) (string, error)
	// IsAncestor returns true if ancestor is an ancestor of descendant in the repo.
	IsAncestor(ctx context.Context, repo, ancestor, descendant string) (bool, error)
}

// LocalGitOps implements GitOps using local git commands.
type LocalGitOps struct{}

// RevParseHEAD returns the current HEAD SHA by running git rev-parse HEAD.
func (LocalGitOps) RevParseHEAD(ctx context.Context, repoDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("watch: git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// IsAncestor returns true if ancestor is an ancestor of descendant in the repo.
func (LocalGitOps) IsAncestor(ctx context.Context, repoDir, ancestor, descendant string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = repoDir
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	// Exit code 1 means "not an ancestor" — not a real error.
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("watch: git merge-base: %w", err)
}

// MCPCaller abstracts the Sourcegraph MCP client's CallTool method for
// remote git operations. This mirrors sourcegraph.MCPCaller but is defined
// here to avoid a circular dependency between watch and sourcegraph packages.
type MCPCaller interface {
	CallTool(ctx context.Context, toolName string, args map[string]any) (string, error)
}

// RemoteGitOps implements GitOps using Sourcegraph MCP tools for remote
// repositories that are not cloned locally.
type RemoteGitOps struct {
	Caller MCPCaller
}

// shaPattern matches a 40-character hex SHA.
var shaPattern = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

// RevParseHEAD returns the latest commit SHA for the repo by calling
// Sourcegraph's commit_search tool and extracting the first SHA from the
// response text.
func (r *RemoteGitOps) RevParseHEAD(ctx context.Context, repo string) (string, error) {
	if r.Caller == nil {
		return "", fmt.Errorf("watch: RemoteGitOps: MCPCaller is nil")
	}

	result, err := r.Caller.CallTool(ctx, "commit_search", map[string]any{
		"repos":        []string{repo},
		"contentTerms": []string{},
	})
	if err != nil {
		return "", fmt.Errorf("watch: remote rev-parse HEAD for %s: %w", repo, err)
	}

	// Extract the first 40-char hex SHA from the response.
	sha := shaPattern.FindString(result)
	if sha == "" {
		return "", fmt.Errorf("watch: remote rev-parse HEAD for %s: no SHA found in commit_search response", repo)
	}

	return sha, nil
}

// IsAncestor checks whether ancestor is an ancestor of descendant.
//
// Limitation: Sourcegraph MCP commit_search does not support direct ancestry
// checks. We return (true, nil) as a safe default — this means the watcher
// will never trigger a force-push full-extraction for remote repos, which is
// acceptable because remote repos are typically not force-pushed and a missed
// force-push detection only means an incremental (rather than full) extraction.
func (r *RemoteGitOps) IsAncestor(_ context.Context, _, _, _ string) (bool, error) {
	return true, nil
}
