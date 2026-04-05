package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/live-docs/live_docs/gitdiff"
)

// defaultConcurrency is the default number of concurrent MCP calls.
const defaultConcurrency = 10

// SourcegraphOption configures a SourcegraphFileSource.
type SourcegraphOption func(*SourcegraphFileSource)

// WithConcurrency sets the maximum number of concurrent MCP calls.
// Values less than 1 are treated as 1.
func WithConcurrency(n int) SourcegraphOption {
	return func(s *SourcegraphFileSource) {
		if n < 1 {
			n = 1
		}
		s.concurrency = n
	}
}

// ToolLister abstracts MCP tool discovery so the constructor can verify that
// required tools are available without depending on the full MCP client.
type ToolLister interface {
	ListTools(ctx context.Context) ([]string, error)
}

// SourcegraphFileSource implements FileSource by delegating to Sourcegraph MCP
// tools: read_file, list_files, and compare_revisions. Concurrent ReadFile calls
// are bounded by an internal semaphore (default 10, configurable via WithConcurrency).
type SourcegraphFileSource struct {
	caller      MCPCaller
	concurrency int
	sem         chan struct{}
}

// MCPCaller abstracts the MCP client's CallTool method. It matches the
// signature of sourcegraph.MCPCaller but is redeclared here to avoid a
// package dependency from pipeline -> sourcegraph.
type MCPCaller interface {
	CallTool(ctx context.Context, toolName string, args map[string]any) (string, error)
}

// requiredTools lists the Sourcegraph MCP tools that must be available.
var requiredTools = []string{"read_file", "list_files"}

// NewSourcegraphFileSource creates a SourcegraphFileSource after verifying that
// required MCP tools (read_file, list_files) are available via the ToolLister.
// Use WithConcurrency to control the maximum number of concurrent MCP calls
// (default 10).
func NewSourcegraphFileSource(caller MCPCaller, lister ToolLister, opts ...SourcegraphOption) (*SourcegraphFileSource, error) {
	ctx := context.Background()
	tools, err := lister.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("sourcegraph filesource: failed to list tools: %w", err)
	}

	available := make(map[string]bool, len(tools))
	for _, t := range tools {
		available[t] = true
	}

	for _, req := range requiredTools {
		if !available[req] {
			return nil, fmt.Errorf("sourcegraph filesource: required tool %q not available", req)
		}
	}

	s := &SourcegraphFileSource{
		caller:      caller,
		concurrency: defaultConcurrency,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.sem = make(chan struct{}, s.concurrency)

	return s, nil
}

// ReadFile calls the Sourcegraph MCP read_file tool and returns the content.
// It acquires a semaphore slot before making the call, ensuring that no more
// than the configured concurrency limit of calls are in flight at once.
func (s *SourcegraphFileSource) ReadFile(ctx context.Context, repo, revision, path string) ([]byte, error) {
	// Acquire semaphore.
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.sem }()

	args := map[string]any{
		"repo": repo,
		"path": path,
	}
	if revision != "" {
		args["revision"] = revision
	}

	result, err := s.caller.CallTool(ctx, "read_file", args)
	if err != nil {
		return nil, fmt.Errorf("sourcegraph read_file %s: %w", path, err)
	}
	return []byte(result), nil
}

// Concurrency returns the configured concurrency limit.
func (s *SourcegraphFileSource) Concurrency() int {
	return s.concurrency
}

// BatchReadFile holds the result of a single file read in a batch operation.
type BatchReadFile struct {
	Path    string
	Content []byte
	Err     error
}

// BatchReadFiles reads multiple files concurrently, bounded by the semaphore.
// It returns results for all paths; individual errors are captured per-file.
func (s *SourcegraphFileSource) BatchReadFiles(ctx context.Context, repo, revision string, paths []string) []BatchReadFile {
	results := make([]BatchReadFile, len(paths))
	var wg sync.WaitGroup

	for i, path := range paths {
		wg.Add(1)
		go func(idx int, p string) {
			defer wg.Done()
			content, err := s.ReadFile(ctx, repo, revision, p)
			results[idx] = BatchReadFile{Path: p, Content: content, Err: err}
		}(i, path)
	}

	wg.Wait()
	return results
}

// ListFiles calls the Sourcegraph MCP list_files tool and returns matching paths.
func (s *SourcegraphFileSource) ListFiles(ctx context.Context, repo, revision, pattern string) ([]string, error) {
	args := map[string]any{
		"repo":    repo,
		"pattern": pattern,
	}
	if revision != "" {
		args["revision"] = revision
	}

	result, err := s.caller.CallTool(ctx, "list_files", args)
	if err != nil {
		return nil, fmt.Errorf("sourcegraph list_files %s: %w", pattern, err)
	}

	if strings.TrimSpace(result) == "" {
		return nil, nil
	}

	var paths []string
	for _, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	return paths, nil
}

// DiffBetween calls the Sourcegraph MCP compare_revisions tool and parses the
// response into []gitdiff.FileChange.
//
// The compare_revisions tool is expected to return output in one of two formats:
//   - git diff --name-status format: lines of "<status>\t<path>"
//   - unified diff format: parsed by extracting file paths from diff headers
//
// If compare_revisions is not available, it returns an error.
func (s *SourcegraphFileSource) DiffBetween(ctx context.Context, repo, fromRev, toRev string) ([]gitdiff.FileChange, error) {
	result, err := s.caller.CallTool(ctx, "compare_revisions", map[string]any{
		"repo": repo,
		"from": fromRev,
		"to":   toRev,
	})
	if err != nil {
		return nil, fmt.Errorf("sourcegraph compare_revisions %s..%s: %w", fromRev, toRev, err)
	}

	if strings.TrimSpace(result) == "" {
		return nil, nil
	}

	// Try git diff --name-status format first.
	changes, err := gitdiff.ParseNameStatus(result)
	if err == nil && len(changes) > 0 {
		return changes, nil
	}

	// Fall back to parsing unified diff headers.
	return parseUnifiedDiffPaths(result)
}

// parseUnifiedDiffPaths extracts file changes from unified diff output by
// looking for "diff --git a/<path> b/<path>" headers and "+++ b/<path>" lines.
// Files that appear only in "--- a/<path>" (deleted) without a corresponding
// "+++ b/<path>" are marked as deleted.
func parseUnifiedDiffPaths(output string) ([]gitdiff.FileChange, error) {
	var changes []gitdiff.FileChange
	seen := make(map[string]bool)

	lines := strings.Split(output, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}

		// Extract path from "diff --git a/foo b/foo".
		parts := strings.SplitN(line, " b/", 2)
		if len(parts) < 2 {
			continue
		}
		path := strings.TrimSpace(parts[1])
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true

		// Determine status by looking at the following lines for ---/+++ markers.
		status := gitdiff.StatusModified
		for j := i + 1; j < len(lines) && j <= i+4; j++ {
			if strings.HasPrefix(lines[j], "--- /dev/null") {
				status = gitdiff.StatusAdded
				break
			}
			if strings.HasPrefix(lines[j], "+++ /dev/null") {
				status = gitdiff.StatusDeleted
				break
			}
			if strings.HasPrefix(lines[j], "diff --git ") {
				break
			}
		}

		changes = append(changes, gitdiff.FileChange{
			Status: status,
			Path:   path,
		})
	}

	if len(changes) == 0 {
		return nil, fmt.Errorf("sourcegraph compare_revisions: unable to parse diff output")
	}
	return changes, nil
}
