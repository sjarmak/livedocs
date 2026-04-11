// Package tribal provides extractors that derive tribal knowledge facts
// from non-code sources such as git history, PR comments, and runbooks.
package tribal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/live-docs/live_docs/db"
)

const (
	commitRationaleExtractorName    = "commit-rationale"
	commitRationaleExtractorVersion = "0.1.0"
	minMessageLen                   = 21 // must be > 20 chars
	endMarker                       = "---END---"
)

// allowedTypes is the set of conventional-commit types that carry rationale.
// Types like chore, ci, docs, style are excluded as trivial.
var allowedTypes = map[string]bool{
	"feat":     true,
	"fix":      true,
	"refactor": true,
	"perf":     true,
}

// trivialTypes is the set of conventional-commit types to filter out.
var trivialTypes = map[string]bool{
	"chore": true,
	"ci":    true,
	"docs":  true,
	"style": true,
}

// CommitEntry represents a parsed git log entry.
type CommitEntry struct {
	SHA        string
	Subject    string // first line of commit message
	Body       string // remaining lines (may be empty)
	FullMsg    string // subject + body combined
	CommitType string // parsed conventional-commit type, empty if none
	Author     string
	AuthoredAt string
}

// CommitRationaleExtractor extracts the most recent non-trivial commit
// message for a file's history and emits a rationale tribal fact.
type CommitRationaleExtractor struct {
	// RepoDir is the root directory of the git repository.
	RepoDir string
}

// ExtractForFile runs git log --follow on the given file path (relative to
// RepoDir) and returns at most one TribalFact for the most recent non-trivial
// commit. The subjectID should be the symbol ID to associate the fact with.
func (e *CommitRationaleExtractor) ExtractForFile(ctx context.Context, relPath string, subjectID int64) (*db.TribalFact, []db.TribalEvidence, error) {
	entries, err := e.gitLog(ctx, relPath)
	if err != nil {
		return nil, nil, fmt.Errorf("commit rationale: git log for %s: %w", relPath, err)
	}

	entry := findFirstNonTrivial(entries)
	if entry == nil {
		return nil, nil, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	hash := sha256Hash(entry.FullMsg)

	fact := &db.TribalFact{
		SubjectID:        subjectID,
		Kind:             "rationale",
		Body:             entry.FullMsg,
		SourceQuote:      entry.Subject,
		Confidence:       1.0,
		Corroboration:    1,
		Extractor:        commitRationaleExtractorName,
		ExtractorVersion: commitRationaleExtractorVersion,
		Model:            "", // NULL — no AI model used
		StalenessHash:    hash,
		Status:           "active",
		CreatedAt:        now,
		LastVerified:     now,
	}

	evidence := []db.TribalEvidence{
		{
			SourceType:  "commit_msg",
			SourceRef:   entry.SHA,
			Author:      entry.Author,
			AuthoredAt:  entry.AuthoredAt,
			ContentHash: hash,
		},
	}

	return fact, evidence, nil
}

// gitLog executes git log --follow for the given file and parses the output.
func (e *CommitRationaleExtractor) gitLog(ctx context.Context, relPath string) ([]CommitEntry, error) {
	cmd := exec.CommandContext(ctx, "git", "log",
		"--follow",
		"--format=%H%n%an%n%aI%n%s%n%b"+endMarker,
		"--", relPath,
	)
	cmd.Dir = e.RepoDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git log: %w: %s", err, stderr.String())
	}

	return parseGitLog(stdout.String()), nil
}

// parseGitLog splits git log output into CommitEntry values.
// Expected format per commit: SHA\nAuthor\nDate\nSubject\nBody...---END---
func parseGitLog(raw string) []CommitEntry {
	var entries []CommitEntry

	blocks := strings.Split(raw, endMarker)
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}

		lines := strings.SplitN(block, "\n", 5)
		if len(lines) < 4 {
			continue
		}

		sha := strings.TrimSpace(lines[0])
		author := strings.TrimSpace(lines[1])
		date := strings.TrimSpace(lines[2])
		subject := strings.TrimSpace(lines[3])

		var body string
		if len(lines) == 5 {
			body = strings.TrimSpace(lines[4])
		}

		fullMsg := subject
		if body != "" {
			fullMsg = subject + "\n\n" + body
		}

		entry := CommitEntry{
			SHA:        sha,
			Subject:    subject,
			Body:       body,
			FullMsg:    fullMsg,
			CommitType: parseConventionalType(subject),
			Author:     author,
			AuthoredAt: date,
		}
		entries = append(entries, entry)
	}

	return entries
}

// parseConventionalType extracts the type prefix from a conventional commit
// subject line. For "feat: add X" it returns "feat". Returns "" if the
// subject does not follow conventional commit format.
func parseConventionalType(subject string) string {
	// Handle optional scope: "feat(auth): add X" or "feat!: breaking"
	idx := strings.IndexAny(subject, ":(!") // find first delimiter
	if idx <= 0 {
		return ""
	}

	candidate := strings.ToLower(strings.TrimSpace(subject[:idx]))

	// Validate: conventional commit types are single lowercase alpha words
	for _, c := range candidate {
		if c < 'a' || c > 'z' {
			return ""
		}
	}

	return candidate
}

// findFirstNonTrivial returns the first (most recent) commit entry that
// passes the non-trivial filter, or nil if none qualify.
func findFirstNonTrivial(entries []CommitEntry) *CommitEntry {
	for i := range entries {
		if isNonTrivial(&entries[i]) {
			return &entries[i]
		}
	}
	return nil
}

// isNonTrivial reports whether a commit entry qualifies as non-trivial.
// A commit is trivial if:
//   - Its full message is <= 20 characters, OR
//   - Its conventional-commit type is in the trivial set (chore, ci, docs, style)
func isNonTrivial(e *CommitEntry) bool {
	if len(e.FullMsg) <= 20 {
		return false
	}

	if e.CommitType != "" && trivialTypes[e.CommitType] {
		return false
	}

	return true
}

// sha256Hash returns the hex-encoded SHA-256 digest of s.
func sha256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:])
}
