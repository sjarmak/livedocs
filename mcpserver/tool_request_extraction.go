// Package mcpserver tool_request_extraction.go implements the request_extraction
// tool that allows agents to trigger extraction for a repository. Uses ONLY
// adapter types — no mcp-go imports.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ExtractionRunner abstracts the extraction logic so the MCP handler can
// trigger extraction without embedding heavy pipeline dependencies. Defined
// at the consumer site per Go convention.
type ExtractionRunner interface {
	// RemoteHeadCommit returns the current HEAD commit SHA for the given repo.
	// This is used to check if the local claims DB is fresh.
	RemoteHeadCommit(ctx context.Context, repo string) (string, error)

	// RunExtraction runs a full or incremental extraction for the repo.
	// The implementation decides whether to shallow-clone or do incremental
	// based on internal state.
	RunExtraction(ctx context.Context, repo, importPath string) error
}

// ExtractionTracker manages in-progress extraction state and delegates to
// an ExtractionRunner. It is safe for concurrent use.
type ExtractionTracker struct {
	runner ExtractionRunner

	mu         sync.Mutex
	inProgress map[string]bool
}

// NewExtractionTracker creates a tracker with the given runner.
// If runner is nil, the tool will return an error when called.
func NewExtractionTracker(runner ExtractionRunner) *ExtractionTracker {
	return &ExtractionTracker{
		runner:     runner,
		inProgress: make(map[string]bool),
	}
}

// IsInProgress reports whether an extraction is currently running for the repo.
func (t *ExtractionTracker) IsInProgress(repo string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inProgress[repo]
}

// setInProgress marks the repo extraction as started or finished.
func (t *ExtractionTracker) setInProgress(repo string, active bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if active {
		t.inProgress[repo] = true
	} else {
		delete(t.inProgress, repo)
	}
}

// extractionStatus enumerates the possible response statuses.
const (
	statusQueued       = "queued"
	statusAlreadyFresh = "already_fresh"
	statusInProgress   = "in_progress"
)

// requestExtractionResponse is the JSON output for request_extraction.
type requestExtractionResponse struct {
	Status  string `json:"status"`
	Repo    string `json:"repo"`
	Message string `json:"message"`
}

// RequestExtractionToolDef returns the ToolDef for request_extraction.
// The pool is used to check for existing claims DBs, and the tracker
// manages in-progress state and delegates extraction to its runner.
func RequestExtractionToolDef(pool *DBPool, tracker *ExtractionTracker) ToolDef {
	return ToolDef{
		Name: "request_extraction",
		Description: `Request extraction (indexing) for a repository.

If the repo has no claims DB, triggers a full shallow-clone extraction in the background.
If the repo has a claims DB but it is stale (behind remote HEAD), triggers incremental extraction.
If the repo is already up-to-date, returns already_fresh.
Returns immediately with status: queued, already_fresh, or in_progress.`,
		Params: []ParamDef{
			{Name: "repo", Type: ParamString, Required: true, Description: "Repository name or identifier (e.g. 'kubernetes' or 'github.com/org/repo')."},
			{Name: "import_path", Type: ParamString, Required: false, Description: "Optional import path to scope incremental extraction."},
		},
		Handler: RequestExtractionHandler(pool, tracker),
	}
}

// RequestExtractionHandler returns a ToolHandler that checks freshness and
// triggers extraction asynchronously.
func RequestExtractionHandler(pool *DBPool, tracker *ExtractionTracker) ToolHandler {
	return func(ctx context.Context, req ToolRequest) (ToolResult, error) {
		repo, err := req.RequireString("repo")
		if err != nil {
			return NewErrorResult("missing required parameter 'repo'"), nil
		}
		importPath := req.GetString("import_path", "")

		// Check if runner is configured.
		if tracker == nil || tracker.runner == nil {
			return marshalResponse(requestExtractionResponse{
				Status:  "error",
				Repo:    repo,
				Message: "extraction runner not configured on this server",
			})
		}

		// Check if extraction is already in progress.
		if tracker.IsInProgress(repo) {
			return marshalResponse(requestExtractionResponse{
				Status:  statusInProgress,
				Repo:    repo,
				Message: fmt.Sprintf("extraction for %s is already in progress", repo),
			})
		}

		// Check if a claims DB exists for this repo.
		dbPath := filepath.Join(pool.DataDir(), repo+claimsDBSuffix)
		_, statErr := os.Stat(dbPath)
		dbExists := statErr == nil

		if !dbExists {
			// No DB — queue full extraction.
			tracker.setInProgress(repo, true)
			go func() {
				defer tracker.setInProgress(repo, false)
				// Use a background context since the request context will be cancelled.
				bgCtx := context.Background()
				_ = tracker.runner.RunExtraction(bgCtx, repo, importPath)
			}()
			return marshalResponse(requestExtractionResponse{
				Status:  statusQueued,
				Repo:    repo,
				Message: fmt.Sprintf("no existing claims DB for %s; full extraction queued", repo),
			})
		}

		// DB exists — check freshness by comparing commits.
		cdb, err := pool.Open(repo)
		if err != nil {
			return NewErrorResultf("open claims DB for %s: %v", repo, err), nil
		}

		meta, err := cdb.GetExtractionMeta()
		if err != nil {
			return NewErrorResultf("get extraction meta for %s: %v", repo, err), nil
		}

		// Get remote HEAD to compare.
		remoteHead, err := tracker.runner.RemoteHeadCommit(ctx, repo)
		if err != nil {
			return NewErrorResultf("get remote HEAD for %s: %v", repo, err), nil
		}

		// If commits match, the DB is fresh.
		if meta.CommitSHA != "" && meta.CommitSHA == remoteHead {
			return marshalResponse(requestExtractionResponse{
				Status:  statusAlreadyFresh,
				Repo:    repo,
				Message: fmt.Sprintf("claims DB for %s is up-to-date at commit %s", repo, meta.CommitSHA),
			})
		}

		// DB is stale — queue incremental extraction.
		tracker.setInProgress(repo, true)
		go func() {
			defer tracker.setInProgress(repo, false)
			bgCtx := context.Background()
			_ = tracker.runner.RunExtraction(bgCtx, repo, importPath)
		}()

		msg := fmt.Sprintf("claims DB for %s is stale (local: %s, remote: %s); extraction queued", repo, meta.CommitSHA, remoteHead)
		if meta.CommitSHA == "" {
			msg = fmt.Sprintf("claims DB for %s has no commit SHA recorded; extraction queued", repo)
		}

		return marshalResponse(requestExtractionResponse{
			Status:  statusQueued,
			Repo:    repo,
			Message: msg,
		})
	}
}

// marshalResponse marshals a response struct into a JSON ToolResult.
func marshalResponse(resp requestExtractionResponse) (ToolResult, error) {
	data, err := json.Marshal(resp)
	if err != nil {
		return NewErrorResultf("marshal response: %v", err), nil
	}
	return NewTextResult(string(data)), nil
}
