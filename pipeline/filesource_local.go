package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/live-docs/live_docs/gitdiff"
)

// LocalFileSource implements FileSource by reading from a local git repository.
// It ignores the repo and revision parameters — those exist for remote
// implementations. All operations use the configured repoDir.
type LocalFileSource struct {
	repoDir string
}

// NewLocalFileSource creates a LocalFileSource rooted at the given directory.
func NewLocalFileSource(repoDir string) *LocalFileSource {
	return &LocalFileSource{repoDir: repoDir}
}

// ReadFile reads a file from the local repository by joining repoDir and path.
func (s *LocalFileSource) ReadFile(_ context.Context, _, _, path string) ([]byte, error) {
	absPath := filepath.Join(s.repoDir, path)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("local read %s: %w", path, err)
	}
	return data, nil
}

// ListFiles walks the local repository and returns paths matching the glob pattern.
// The returned paths are relative to repoDir.
func (s *LocalFileSource) ListFiles(_ context.Context, _, _, pattern string) ([]string, error) {
	var matches []string
	err := filepath.WalkDir(s.repoDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip hidden directories (e.g. .git).
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(s.repoDir, path)
		if err != nil {
			return err
		}
		matched, err := filepath.Match(pattern, rel)
		if err != nil {
			return fmt.Errorf("bad glob pattern %q: %w", pattern, err)
		}
		if matched {
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("local list files: %w", err)
	}
	return matches, nil
}

// DiffBetween delegates to gitdiff.DiffBetween using the local repoDir.
func (s *LocalFileSource) DiffBetween(_ context.Context, _, fromRev, toRev string) ([]gitdiff.FileChange, error) {
	changes, err := gitdiff.DiffBetween(s.repoDir, fromRev, toRev)
	if err != nil {
		return nil, fmt.Errorf("local diff: %w", err)
	}
	return changes, nil
}
