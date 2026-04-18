// Package prbot analyzes PR diffs to find documentation claims invalidated
// by code changes. It bridges the gitdiff and anchor packages to produce
// structured impact reports suitable for posting as GitHub PR comments.
package prbot

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sjarmak/livedocs/anchor"
	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/gitdiff"
)

// InvalidatedClaim pairs an invalidated anchor with the claim it references
// and the change that caused the invalidation.
type InvalidatedClaim struct {
	Anchor anchor.Anchor
	Claim  db.Claim
	Change gitdiff.FileChange
}

// Report is the result of analyzing a PR diff against the claims database.
type Report struct {
	Invalidated []InvalidatedClaim
	Summary     anchor.Summary
}

// CommentPoster abstracts the ability to post a comment on a PR.
// Implementations handle GitHub API details; tests use a mock.
type CommentPoster interface {
	PostComment(owner, repo string, prNumber int, body string) error
}

// Analyze takes a set of file changes (from a PR diff) and a set of claims,
// builds an anchor index, runs invalidation, and returns a report of all
// claims affected by the changes.
//
// The radius parameter controls how many lines around each claim's source line
// are included in the anchor range. A radius of 0 means only the exact line.
func Analyze(changes []gitdiff.FileChange, claims []db.Claim, radius int) Report {
	idx := anchor.BuildFromClaims(claims, radius)
	affected := idx.Invalidate(changes)

	// Build a lookup from claim ID to claim for enrichment.
	claimByID := make(map[int64]db.Claim, len(claims))
	for _, cl := range claims {
		claimByID[cl.ID] = cl
	}

	// Build a lookup from file path to change for enrichment.
	changeByPath := buildChangeIndex(changes)

	invalidated := make([]InvalidatedClaim, 0, len(affected))
	for _, a := range affected {
		cl, ok := claimByID[a.ClaimID]
		if !ok {
			continue
		}
		ch := resolveChange(a, changeByPath)
		invalidated = append(invalidated, InvalidatedClaim{
			Anchor: a,
			Claim:  cl,
			Change: ch,
		})
	}

	// Sort by file then line for stable output.
	sort.Slice(invalidated, func(i, j int) bool {
		if invalidated[i].Claim.SourceFile != invalidated[j].Claim.SourceFile {
			return invalidated[i].Claim.SourceFile < invalidated[j].Claim.SourceFile
		}
		return invalidated[i].Claim.SourceLine < invalidated[j].Claim.SourceLine
	})

	return Report{
		Invalidated: invalidated,
		Summary:     idx.Summary(),
	}
}

// buildChangeIndex maps file paths to changes. For renames, both old and new
// paths point to the same change.
func buildChangeIndex(changes []gitdiff.FileChange) map[string]gitdiff.FileChange {
	m := make(map[string]gitdiff.FileChange, len(changes))
	for _, ch := range changes {
		m[ch.Path] = ch
		if ch.OldPath != "" {
			m[ch.OldPath] = ch
		}
	}
	return m
}

// resolveChange finds the FileChange that caused an anchor to be invalidated.
func resolveChange(a anchor.Anchor, changeByPath map[string]gitdiff.FileChange) gitdiff.FileChange {
	if ch, ok := changeByPath[a.File]; ok {
		return ch
	}
	return gitdiff.FileChange{}
}

// FormatComment renders an invalidation report as a markdown comment body
// suitable for posting on a GitHub PR.
func FormatComment(report Report) string {
	if len(report.Invalidated) == 0 {
		return formatCleanComment(report.Summary)
	}
	return formatImpactComment(report)
}

func formatCleanComment(summary anchor.Summary) string {
	var b strings.Builder
	b.WriteString("## Live Docs: No Impact Detected\n\n")
	fmt.Fprintf(&b, "Checked %d anchored claims -- none are affected by this PR.\n", summary.Total)
	return b.String()
}

func formatImpactComment(report Report) string {
	var b strings.Builder
	b.WriteString("## Live Docs: Documentation Impact\n\n")
	fmt.Fprintf(&b, "This PR invalidates **%d** documentation claim(s).\n\n", len(report.Invalidated))

	// Group by file for readability.
	grouped := groupByFile(report.Invalidated)
	files := sortedKeys(grouped)

	for _, file := range files {
		items := grouped[file]
		fmt.Fprintf(&b, "### `%s`\n\n", file)
		b.WriteString("| Line | Predicate | Claim | Status |\n")
		b.WriteString("|------|-----------|-------|--------|\n")
		for _, ic := range items {
			status := statusEmoji(ic.Anchor.Status)
			objText := truncate(ic.Claim.ObjectText, 60)
			fmt.Fprintf(&b, "| %d | %s | %s | %s |\n",
				ic.Claim.SourceLine, ic.Claim.Predicate, objText, status)
		}
		b.WriteString("\n")
	}

	b.WriteString("---\n")
	fmt.Fprintf(&b, "**Summary**: %d verified, %d stale, %d invalid (of %d total)\n\n",
		report.Summary.Verified, report.Summary.Stale, report.Summary.Invalid, report.Summary.Total)
	b.WriteString("_Advisory: these claims may need updating. This check is non-blocking._\n")

	return b.String()
}

func groupByFile(items []InvalidatedClaim) map[string][]InvalidatedClaim {
	m := make(map[string][]InvalidatedClaim)
	for _, ic := range items {
		m[ic.Claim.SourceFile] = append(m[ic.Claim.SourceFile], ic)
	}
	return m
}

func sortedKeys(m map[string][]InvalidatedClaim) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func statusEmoji(s anchor.Status) string {
	switch s {
	case anchor.StatusStale:
		return "stale"
	case anchor.StatusInvalid:
		return "invalid"
	default:
		return string(s)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
