package pipeline

import (
	"context"

	"github.com/live-docs/live_docs/gitdiff"
)

// FileSource abstracts file access and diff operations, decoupling the pipeline
// from local filesystem access. The repo and revision parameters are provided
// for remote implementations; local implementations may ignore them.
type FileSource interface {
	// ReadFile returns the contents of a file at the given path.
	ReadFile(ctx context.Context, repo, revision, path string) ([]byte, error)

	// ListFiles returns file paths matching the glob pattern.
	ListFiles(ctx context.Context, repo, revision, pattern string) ([]string, error)

	// DiffBetween returns the set of file changes between two revisions.
	DiffBetween(ctx context.Context, repo, fromRev, toRev string) ([]gitdiff.FileChange, error)
}
