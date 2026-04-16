// Package gitdiff detects file changes between two git commits using
// git diff --name-status. It produces a structured list of added, modified,
// deleted, renamed, and copied files suitable for incremental pipeline processing.
package gitdiff

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
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

// Hunk represents a single diff hunk with its line range information.
// These are parsed from unified diff @@ headers.
type Hunk struct {
	OldStart int // starting line in the old file
	OldCount int // number of lines in the old file
	NewStart int // starting line in the new file
	NewCount int // number of lines in the new file
}

// FileChange represents a single file change between two commits.
type FileChange struct {
	Status  ChangeStatus
	Path    string // current path (destination for renames/copies)
	OldPath string // previous path (only for renames/copies)
	Hunks   []Hunk // line-level hunk info (populated by ParseUnifiedDiff)
}

// emptyTreeSHA is the well-known SHA of the empty git tree object. It is used
// as a sentinel by the watcher to signal "diff against nothing" (full extraction).
// However, this object may not exist in all git repositories, so DiffBetween
// handles it specially by listing all files at toCommit instead.
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf899d69f82cf7118"

// DiffBetween runs git diff --name-status between two commits in the given
// repo directory and returns the list of file changes.
//
// When fromCommit is the empty tree SHA, it falls back to listing all files
// at toCommit via git ls-tree, returning each as StatusAdded. This avoids
// the "Invalid revision range" error that occurs when the empty tree object
// does not exist in the repository's object store.
func DiffBetween(repoDir, fromCommit, toCommit string) ([]FileChange, error) {
	if fromCommit == emptyTreeSHA {
		return listAllFiles(repoDir, toCommit)
	}
	cmd := exec.Command("git", "diff", "--name-status", fromCommit, toCommit)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gitdiff: git diff %s..%s: %w", fromCommit, toCommit, err)
	}
	return ParseNameStatus(string(out))
}

// listAllFiles returns all files tracked at the given commit as StatusAdded
// changes. It uses git ls-tree -r --name-only which works on any valid commit
// without requiring the empty tree object to exist.
func listAllFiles(repoDir, commit string) ([]FileChange, error) {
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", commit)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gitdiff: git ls-tree %s: %w", commit, err)
	}
	var changes []FileChange
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		changes = append(changes, FileChange{Status: StatusAdded, Path: line})
	}
	return changes, nil
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

// hunkRe matches unified diff hunk headers: @@ -old_start[,old_count] +new_start[,new_count] @@
var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ParseUnifiedDiff parses the output of git diff (unified format) into
// FileChange values with hunk-level line information. Each file's hunks
// contain the old and new line ranges from the @@ headers.
func ParseUnifiedDiff(output string) ([]FileChange, error) {
	if output == "" {
		return nil, nil
	}

	var changes []FileChange
	var current *FileChange
	lines := strings.Split(output, "\n")

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// New file diff starts with "diff --git a/... b/..."
		if strings.HasPrefix(line, "diff --git ") {
			// Finalize previous file change.
			if current != nil {
				changes = append(changes, *current)
			}
			fc, err := parseDiffHeader(line, lines, &i)
			if err != nil {
				return nil, err
			}
			current = &fc
			continue
		}

		// Parse hunk headers.
		if current != nil && strings.HasPrefix(line, "@@") {
			h, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			current.Hunks = append(current.Hunks, h)
			continue
		}
	}

	// Finalize last file.
	if current != nil {
		changes = append(changes, *current)
	}

	return changes, nil
}

// parseDiffHeader parses the "diff --git" line and subsequent metadata lines
// (new file, deleted file, rename from/to, --- , +++) to determine the file
// change status and paths. The index i is advanced past consumed metadata lines.
func parseDiffHeader(diffLine string, lines []string, i *int) (FileChange, error) {
	// Extract paths from "diff --git a/path b/path"
	parts := strings.SplitN(diffLine, " ", 4)
	if len(parts) < 4 {
		return FileChange{}, fmt.Errorf("gitdiff: malformed diff header: %q", diffLine)
	}
	// b-path is after the last " b/"
	bIdx := strings.LastIndex(diffLine, " b/")
	if bIdx == -1 {
		return FileChange{}, fmt.Errorf("gitdiff: cannot find b/ path in: %q", diffLine)
	}
	bPath := diffLine[bIdx+3:]

	fc := FileChange{
		Status: StatusModified, // default, may be overridden
		Path:   bPath,
	}

	// Scan ahead for metadata lines before the first hunk.
	for *i+1 < len(lines) {
		next := lines[*i+1]
		switch {
		case strings.HasPrefix(next, "new file mode"):
			fc.Status = StatusAdded
			*i++
		case strings.HasPrefix(next, "deleted file mode"):
			fc.Status = StatusDeleted
			*i++
		case strings.HasPrefix(next, "rename from "):
			fc.OldPath = strings.TrimPrefix(next, "rename from ")
			fc.Status = StatusRenamed
			*i++
		case strings.HasPrefix(next, "rename to "):
			fc.Path = strings.TrimPrefix(next, "rename to ")
			*i++
		case strings.HasPrefix(next, "similarity index"),
			strings.HasPrefix(next, "dissimilarity index"),
			strings.HasPrefix(next, "index "),
			strings.HasPrefix(next, "--- "),
			strings.HasPrefix(next, "+++ "):
			*i++
		default:
			return fc, nil
		}
	}

	return fc, nil
}

// parseHunkHeader extracts line range information from a unified diff @@ header.
func parseHunkHeader(line string) (Hunk, error) {
	m := hunkRe.FindStringSubmatch(line)
	if m == nil {
		return Hunk{}, fmt.Errorf("gitdiff: malformed hunk header: %q", line)
	}

	oldStart, _ := strconv.Atoi(m[1])
	oldCount := 1
	if m[2] != "" {
		oldCount, _ = strconv.Atoi(m[2])
	}
	newStart, _ := strconv.Atoi(m[3])
	newCount := 1
	if m[4] != "" {
		newCount, _ = strconv.Atoi(m[4])
	}

	return Hunk{
		OldStart: oldStart,
		OldCount: oldCount,
		NewStart: newStart,
		NewCount: newCount,
	}, nil
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
