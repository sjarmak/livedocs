// Package gitdiff detects file changes between two git commits using
// git diff --name-status. It produces a structured list of added, modified,
// deleted, renamed, and copied files suitable for incremental pipeline processing.
package gitdiff

import (
	"fmt"
	"os/exec"
	"strings"
)

// ChangeStatus describes what happened to a file between two commits.
type ChangeStatus string

const (
	StatusAdded    ChangeStatus = "A"
	StatusModified ChangeStatus = "M"
	StatusDeleted  ChangeStatus = "D"
	StatusRenamed  ChangeStatus = "R"
	StatusCopied   ChangeStatus = "C"
)

// FileChange represents a single file change between two commits.
type FileChange struct {
	Status  ChangeStatus
	Path    string // current path (destination for renames/copies)
	OldPath string // previous path (only for renames/copies)
}

// DiffBetween runs git diff --name-status between two commits in the given
// repo directory and returns the list of file changes.
func DiffBetween(repoDir, fromCommit, toCommit string) ([]FileChange, error) {
	cmd := exec.Command("git", "diff", "--name-status", fromCommit, toCommit)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gitdiff: git diff %s..%s: %w", fromCommit, toCommit, err)
	}
	return ParseNameStatus(string(out))
}

// ParseNameStatus parses the output of git diff --name-status into FileChange
// values. Each line has the format: <status>\t<path> or <status>\t<old>\t<new>
// for renames and copies.
func ParseNameStatus(output string) ([]FileChange, error) {
	if output == "" {
		return nil, nil
	}

	var changes []FileChange
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			return nil, fmt.Errorf("gitdiff: malformed line: %q", line)
		}

		statusCode := parts[0]
		var fc FileChange

		switch {
		case statusCode == "A":
			fc = FileChange{Status: StatusAdded, Path: parts[1]}
		case statusCode == "M":
			fc = FileChange{Status: StatusModified, Path: parts[1]}
		case statusCode == "D":
			fc = FileChange{Status: StatusDeleted, Path: parts[1]}
		case strings.HasPrefix(statusCode, "R"):
			if len(parts) < 3 {
				return nil, fmt.Errorf("gitdiff: rename line missing paths: %q", line)
			}
			fc = FileChange{Status: StatusRenamed, Path: parts[2], OldPath: parts[1]}
		case strings.HasPrefix(statusCode, "C"):
			if len(parts) < 3 {
				return nil, fmt.Errorf("gitdiff: copy line missing paths: %q", line)
			}
			fc = FileChange{Status: StatusCopied, Path: parts[2], OldPath: parts[1]}
		default:
			return nil, fmt.Errorf("gitdiff: unknown status %q in line: %q", statusCode, line)
		}

		changes = append(changes, fc)
	}

	return changes, nil
}

// Added returns only the changes with Added status.
func Added(changes []FileChange) []FileChange {
	return filterByStatus(changes, StatusAdded)
}

// Modified returns only the changes with Modified status.
func Modified(changes []FileChange) []FileChange {
	return filterByStatus(changes, StatusModified)
}

// Deleted returns only the changes with Deleted status.
func Deleted(changes []FileChange) []FileChange {
	return filterByStatus(changes, StatusDeleted)
}

// ChangedPaths returns the paths of all non-deleted changes (files that need
// re-extraction: added, modified, renamed, copied).
func ChangedPaths(changes []FileChange) []string {
	var paths []string
	for _, c := range changes {
		if c.Status != StatusDeleted {
			paths = append(paths, c.Path)
		}
	}
	return paths
}

// DeletedPaths returns the paths of all deleted files (including the old paths
// of renamed files, since the old path no longer exists).
func DeletedPaths(changes []FileChange) []string {
	var paths []string
	for _, c := range changes {
		if c.Status == StatusDeleted {
			paths = append(paths, c.Path)
		}
		if c.Status == StatusRenamed && c.OldPath != "" {
			paths = append(paths, c.OldPath)
		}
	}
	return paths
}

func filterByStatus(changes []FileChange, status ChangeStatus) []FileChange {
	var out []FileChange
	for _, c := range changes {
		if c.Status == status {
			out = append(out, c)
		}
	}
	return out
}
