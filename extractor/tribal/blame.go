// Package tribal provides extractors that derive tribal knowledge facts
// from non-code sources such as git blame, commit messages, and PR comments.
package tribal

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/live-docs/live_docs/db"
)

const (
	extractorName    = "blame-ownership"
	extractorVersion = "0.1.0"
)

// SymbolRange pairs a symbol's database ID with the source line range it spans.
// StartLine and EndLine are 1-based and inclusive.
type SymbolRange struct {
	SymbolID  int64
	StartLine int
	EndLine   int
}

// blameLine holds parsed data from a single git blame porcelain line entry.
type blameLine struct {
	CommitSHA  string
	AuthorName string
	AuthorMail string
	AuthorTime string
	LineNumber int
}

// authorStats accumulates per-author blame statistics.
type authorStats struct {
	Name      string
	Email     string
	Lines     int
	LatestSHA string
	LatestAt  string
}

// BlameExtractor runs git blame on source files and produces ownership
// tribal facts for symbols based on per-line authorship attribution.
// It is deterministic (no LLM involved) and sets Model to empty string.
type BlameExtractor struct{}

// ExtractForFile runs git blame on filePath within repoDir and produces
// ownership TribalFacts for each symbol range provided. Returns nil, nil
// if the file is not tracked by git or is a binary file.
func (b *BlameExtractor) ExtractForFile(
	ctx context.Context,
	repoDir string,
	filePath string,
	symbols []SymbolRange,
) ([]db.TribalFact, error) {
	if len(symbols) == 0 {
		return nil, nil
	}

	blameOutput, err := runGitBlame(ctx, repoDir, filePath)
	if err != nil {
		// Not in git, binary, or untracked — return gracefully.
		return nil, nil
	}

	lines, err := parsePorcelainBlame(blameOutput)
	if err != nil {
		return nil, fmt.Errorf("blame extractor: parse porcelain: %w", err)
	}

	if len(lines) == 0 {
		return nil, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var facts []db.TribalFact

	for _, sym := range symbols {
		rangeLines := filterLinesByRange(lines, sym.StartLine, sym.EndLine)
		if len(rangeLines) == 0 {
			continue
		}

		stats := aggregateAuthors(rangeLines)
		if len(stats) == 0 {
			continue
		}

		body := formatOwnershipBody(stats, len(rangeLines))
		sourceQuote := formatSourceQuote(stats)
		stalenessHash := computeStalenessHash(rangeLines)

		fact := db.TribalFact{
			SubjectID:        sym.SymbolID,
			Kind:             "ownership",
			Body:             body,
			SourceQuote:      sourceQuote,
			Confidence:       1.0,
			Corroboration:    1,
			Extractor:        extractorName,
			ExtractorVersion: extractorVersion,
			Model:            "", // deterministic, no LLM
			StalenessHash:    stalenessHash,
			Status:           "active",
			CreatedAt:        now,
			LastVerified:     now,
		}

		for _, s := range stats {
			fact.Evidence = append(fact.Evidence, db.TribalEvidence{
				SourceType:  "blame",
				SourceRef:   s.LatestSHA,
				Author:      fmt.Sprintf("%s <%s>", s.Name, s.Email),
				AuthoredAt:  s.LatestAt,
				ContentHash: stalenessHash,
			})
		}

		facts = append(facts, fact)
	}

	return facts, nil
}

// runGitBlame executes git blame --porcelain on the given file path.
// Returns the raw output bytes or an error if the command fails.
func runGitBlame(ctx context.Context, repoDir, filePath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "blame", "--porcelain", filePath)
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git blame failed: %w: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// parsePorcelainBlame parses the git blame --porcelain output into blameLine entries.
// Porcelain format:
//
//	<sha> <orig_line> <final_line> [<num_lines>]
//	author <name>
//	author-mail <email>
//	author-time <timestamp>
//	... (other headers)
//	\t<content line>
func parsePorcelainBlame(data []byte) ([]blameLine, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var lines []blameLine
	var current blameLine
	inHeader := false

	// Track seen commits — porcelain only outputs full headers for the first
	// occurrence of a commit; subsequent lines reference the SHA alone.
	commitAuthors := make(map[string]blameLine)

	for scanner.Scan() {
		line := scanner.Text()

		if len(line) > 0 && line[0] == '\t' {
			// Content line — finalize current entry.
			if current.CommitSHA != "" {
				lines = append(lines, current)
			}
			inHeader = false
			continue
		}

		if !inHeader {
			// Start of a new blame entry: "<sha> <orig> <final> [<count>]"
			parts := strings.Fields(line)
			if len(parts) < 3 {
				continue
			}
			sha := parts[0]
			finalLine, err := strconv.Atoi(parts[2])
			if err != nil {
				continue
			}

			current = blameLine{
				CommitSHA:  sha,
				LineNumber: finalLine,
			}

			// If we've seen this commit before, copy cached author info.
			if cached, ok := commitAuthors[sha]; ok {
				current.AuthorName = cached.AuthorName
				current.AuthorMail = cached.AuthorMail
				current.AuthorTime = cached.AuthorTime
			}
			inHeader = true
			continue
		}

		// Parse header lines.
		switch {
		case strings.HasPrefix(line, "author "):
			current.AuthorName = strings.TrimPrefix(line, "author ")
		case strings.HasPrefix(line, "author-mail "):
			mail := strings.TrimPrefix(line, "author-mail ")
			// Strip angle brackets if present.
			mail = strings.TrimPrefix(mail, "<")
			mail = strings.TrimSuffix(mail, ">")
			current.AuthorMail = mail
		case strings.HasPrefix(line, "author-time "):
			ts := strings.TrimPrefix(line, "author-time ")
			if epoch, err := strconv.ParseInt(ts, 10, 64); err == nil {
				current.AuthorTime = time.Unix(epoch, 0).UTC().Format(time.RFC3339)
			}
		}

		// Cache author info for this commit.
		if current.AuthorName != "" && current.AuthorMail != "" {
			commitAuthors[current.CommitSHA] = current
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan blame output: %w", err)
	}

	return lines, nil
}

// filterLinesByRange returns blame lines whose LineNumber falls within [start, end].
func filterLinesByRange(lines []blameLine, start, end int) []blameLine {
	var result []blameLine
	for _, l := range lines {
		if l.LineNumber >= start && l.LineNumber <= end {
			result = append(result, l)
		}
	}
	return result
}

// aggregateAuthors groups blame lines by author email and returns statistics
// sorted by line count descending.
func aggregateAuthors(lines []blameLine) []authorStats {
	byEmail := make(map[string]*authorStats)

	for _, l := range lines {
		email := l.AuthorMail
		if email == "" {
			email = "unknown"
		}
		s, ok := byEmail[email]
		if !ok {
			s = &authorStats{
				Name:  l.AuthorName,
				Email: email,
			}
			byEmail[email] = s
		}
		s.Lines++
		// Track the latest commit for this author.
		if l.AuthorTime > s.LatestAt {
			s.LatestAt = l.AuthorTime
			s.LatestSHA = l.CommitSHA
		}
	}

	stats := make([]authorStats, 0, len(byEmail))
	for _, s := range byEmail {
		stats = append(stats, *s)
	}

	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Lines != stats[j].Lines {
			return stats[i].Lines > stats[j].Lines
		}
		return stats[i].Email < stats[j].Email
	})

	return stats
}

// formatOwnershipBody produces a human-readable ownership summary.
func formatOwnershipBody(stats []authorStats, totalLines int) string {
	if len(stats) == 0 {
		return ""
	}

	var parts []string
	for _, s := range stats {
		pct := float64(s.Lines) / float64(totalLines) * 100
		parts = append(parts, fmt.Sprintf("%s <%s> (%d%%, %d/%d lines)",
			s.Name, s.Email, int(pct), s.Lines, totalLines))
	}

	return "Ownership: " + strings.Join(parts, "; ")
}

// formatSourceQuote produces a compact quote of the top contributor.
func formatSourceQuote(stats []authorStats) string {
	if len(stats) == 0 {
		return ""
	}
	top := stats[0]
	return fmt.Sprintf("git blame: top contributor %s <%s>", top.Name, top.Email)
}

// computeStalenessHash produces a SHA256 hex digest from the blame lines' commit SHAs
// and line numbers, providing a stable hash that changes when blame changes.
func computeStalenessHash(lines []blameLine) string {
	h := sha256.New()
	for _, l := range lines {
		fmt.Fprintf(h, "%s:%d\n", l.CommitSHA, l.LineNumber)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
