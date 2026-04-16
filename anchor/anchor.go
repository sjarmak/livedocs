// Package anchor ties documentation claims to specific source code locations.
// When code changes, the anchor index detects which claims are invalidated
// and marks them as needing re-verification. This is the contract anchor
// system: claims are anchored to file+line ranges, and changes to those
// regions break the contract.
package anchor

import (
	"sort"
	"time"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/gitdiff"
)

// Status describes the verification state of an anchored claim.
type Status string

const (
	// StatusVerified means the anchored code has not changed since last verification.
	StatusVerified Status = "verified"
	// StatusStale means the anchored code region was modified and the claim
	// needs re-verification.
	StatusStale Status = "stale"
	// StatusInvalid means the anchored file was deleted and the claim is
	// definitely broken.
	StatusInvalid Status = "invalid"
)

// Anchor links a claim to a specific source code location. StartLine and
// EndLine define the line range. A whole-file anchor uses (0, 0) meaning
// any change to the file invalidates the claim.
type Anchor struct {
	ClaimID      int64
	File         string
	StartLine    int
	EndLine      int
	Status       Status
	LastVerified time.Time
}

// NewAnchor creates a verified anchor for the given claim and location.
func NewAnchor(claimID int64, file string, startLine, endLine int) Anchor {
	return Anchor{
		ClaimID:      claimID,
		File:         file,
		StartLine:    startLine,
		EndLine:      endLine,
		Status:       StatusVerified,
		LastVerified: time.Now(),
	}
}

// IsWholeFile reports whether this anchor covers the entire file.
func (a Anchor) IsWholeFile() bool {
	return a.StartLine == 0 && a.EndLine == 0
}

// Overlaps reports whether the anchor's line range overlaps with [start, end].
// A whole-file anchor overlaps with any range.
func (a Anchor) Overlaps(start, end int) bool {
	if a.IsWholeFile() {
		return true
	}
	return a.StartLine <= end && a.EndLine >= start
}

// Summary holds aggregate counts of anchor statuses.
type Summary struct {
	Total    int
	Verified int
	Stale    int
	Invalid  int
}

// Index is an in-memory index mapping files to their anchored claims.
// It supports invalidation from git diff changes and queries for stale claims.
type Index struct {
	byFile map[string][]Anchor
}

// NewIndex creates an empty anchor index.
func NewIndex() *Index {
	return &Index{byFile: make(map[string][]Anchor)}
}

// Add inserts an anchor into the index.
func (idx *Index) Add(a Anchor) {
	idx.byFile[a.File] = append(idx.byFile[a.File], a)
}

// ForFile returns all anchors for the given file path. Returns nil if none.
func (idx *Index) ForFile(file string) []Anchor {
	return idx.byFile[file]
}

// BuildFromClaims constructs an Index from a slice of DB claims. Each claim
// becomes an anchor at its source file and line. The radius parameter defines
// how many lines around the source line to include in the anchor range.
// Claims with SourceLine == 0 become whole-file anchors.
func BuildFromClaims(claims []db.Claim, radius int) *Index {
	idx := NewIndex()
	for _, cl := range claims {
		var startLine, endLine int
		if cl.SourceLine > 0 {
			startLine = cl.SourceLine - radius
			if startLine < 1 {
				startLine = 1
			}
			endLine = cl.SourceLine + radius
		}
		// SourceLine == 0 leaves (0, 0) -> whole-file anchor
		idx.Add(NewAnchor(cl.ID, cl.SourceFile, startLine, endLine))
	}
	return idx
}

// Invalidate processes a set of git file changes and marks affected anchors
// as stale or invalid. Returns the list of anchors whose status changed.
//
// Rules:
//   - Deleted files: all anchors become StatusInvalid
//   - Modified/Added/Renamed/Copied files: all anchors become StatusStale
//   - Renamed files: anchors on the old path become StatusStale
//   - Files with no anchors: no effect
func (idx *Index) Invalidate(changes []gitdiff.FileChange) []Anchor {
	var affected []Anchor

	for _, ch := range changes {
		switch ch.Status {
		case gitdiff.StatusDeleted:
			affected = append(affected, idx.markFile(ch.Path, StatusInvalid)...)

		case gitdiff.StatusModified, gitdiff.StatusAdded, gitdiff.StatusCopied:
			if len(ch.Hunks) > 0 {
				// Line-level: only mark anchors overlapping changed hunks.
				affected = append(affected, idx.markHunks(ch.Path, ch.Hunks, StatusStale)...)
			} else {
				// File-level fallback (name-status format, no hunk data).
				affected = append(affected, idx.markFile(ch.Path, StatusStale)...)
			}

		case gitdiff.StatusRenamed:
			// Old path is effectively deleted from the anchor perspective,
			// but the code still exists at the new path, so mark stale not invalid.
			affected = append(affected, idx.markFile(ch.OldPath, StatusStale)...)
			if len(ch.Hunks) > 0 {
				affected = append(affected, idx.markHunks(ch.Path, ch.Hunks, StatusStale)...)
			} else {
				affected = append(affected, idx.markFile(ch.Path, StatusStale)...)
			}
		}
	}

	return affected
}

// markFile sets all anchors for a file to the given status and returns
// the affected anchors. Only anchors that actually change status are returned.
func (idx *Index) markFile(file string, status Status) []Anchor {
	anchors := idx.byFile[file]
	var changed []Anchor
	for i := range anchors {
		if anchors[i].Status != status {
			anchors[i].Status = status
			changed = append(changed, anchors[i])
		}
	}
	return changed
}

// markHunks sets anchors that overlap any of the given hunks to the specified
// status. Uses each hunk's old-file line range [OldStart, OldStart+OldCount-1]
// to check overlap with anchor line ranges. Returns only anchors whose status
// actually changed.
func (idx *Index) markHunks(file string, hunks []gitdiff.Hunk, status Status) []Anchor {
	anchors := idx.byFile[file]
	var changed []Anchor
	for i := range anchors {
		if anchors[i].Status == status {
			continue
		}
		for _, h := range hunks {
			hunkEnd := h.OldStart + h.OldCount - 1
			if h.OldCount == 0 {
				// Pure addition (e.g. new file or insert): use new-file range.
				hunkEnd = h.NewStart + h.NewCount - 1
				if anchors[i].Overlaps(h.NewStart, hunkEnd) {
					anchors[i].Status = status
					changed = append(changed, anchors[i])
					break
				}
				continue
			}
			if anchors[i].Overlaps(h.OldStart, hunkEnd) {
				anchors[i].Status = status
				changed = append(changed, anchors[i])
				break
			}
		}
	}
	return changed
}

// QueryStale returns all anchors with StatusStale or StatusInvalid.
func (idx *Index) QueryStale() []Anchor {
	var stale []Anchor
	for _, anchors := range idx.byFile {
		for _, a := range anchors {
			if a.Status == StatusStale || a.Status == StatusInvalid {
				stale = append(stale, a)
			}
		}
	}
	return stale
}

// QueryByStatus returns all anchors with the given status.
func (idx *Index) QueryByStatus(status Status) []Anchor {
	var result []Anchor
	for _, anchors := range idx.byFile {
		for _, a := range anchors {
			if a.Status == status {
				result = append(result, a)
			}
		}
	}
	return result
}

// StaleClaimIDs returns the claim IDs of all stale or invalid anchors, sorted.
func (idx *Index) StaleClaimIDs() []int64 {
	stale := idx.QueryStale()
	ids := make([]int64, len(stale))
	for i, a := range stale {
		ids[i] = a.ClaimID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// MarkVerified sets a specific claim's anchor back to StatusVerified with
// the given verification time.
func (idx *Index) MarkVerified(claimID int64, at time.Time) {
	for file := range idx.byFile {
		for i := range idx.byFile[file] {
			if idx.byFile[file][i].ClaimID == claimID {
				idx.byFile[file][i].Status = StatusVerified
				idx.byFile[file][i].LastVerified = at
				return
			}
		}
	}
}

// Summary returns aggregate counts of anchor statuses.
func (idx *Index) Summary() Summary {
	var s Summary
	for _, anchors := range idx.byFile {
		for _, a := range anchors {
			s.Total++
			switch a.Status {
			case StatusVerified:
				s.Verified++
			case StatusStale:
				s.Stale++
			case StatusInvalid:
				s.Invalid++
			}
		}
	}
	return s
}
