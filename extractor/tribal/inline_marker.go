// Package tribal provides extractors for tribal knowledge from source code.
package tribal

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/live-docs/live_docs/db"
)

const (
	inlineMarkerExtractorName    = "inline_marker"
	inlineMarkerExtractorVersion = "0.1.0"
)

// markerPattern matches TODO/FIXME/XXX/HACK/NOTE/WHY markers in comments.
// The marker keyword is captured in group 1, the rest of the text in group 2.
var markerPattern = regexp.MustCompile(`(?i)\b(TODO|FIXME|XXX|HACK|NOTE|WHY)[\s:](.*)`)

// todoMarkers maps marker keywords (uppercased) to their fact kind.
var markerKinds = map[string]string{
	"TODO":  "todo",
	"FIXME": "todo",
	"XXX":   "todo",
	"HACK":  "quirk",
	"NOTE":  "quirk",
	"WHY":   "quirk",
}

// MarkerExtractor extracts TODO/FIXME/XXX/HACK/NOTE/WHY comments from source
// files and produces TribalFact entries. It is deterministic (no model calls).
type MarkerExtractor struct{}

// MarkerMatch represents a single marker found in a source file.
type MarkerMatch struct {
	LineNum     int
	Marker      string // e.g. "TODO", "HACK"
	CommentText string // full comment text including marker
	Kind        string // "todo" or "quirk"
}

// ExtractFromFile scans the given file content for marker comments and returns
// tribal facts. The filePath is used for source_ref metadata only.
func (e *MarkerExtractor) ExtractFromFile(filePath string, content []byte) ([]db.TribalFact, error) {
	matches := findMarkers(content)
	if len(matches) == 0 {
		return nil, nil
	}

	facts := make([]db.TribalFact, 0, len(matches))
	for _, m := range matches {
		hash := sha256.Sum256([]byte(m.CommentText))
		sourceRef := fmt.Sprintf("%s:%d", filePath, m.LineNum)

		fact := db.TribalFact{
			Kind:             m.Kind,
			Body:             m.CommentText,
			SourceQuote:      m.CommentText,
			Confidence:       1.0,
			Corroboration:    1,
			Extractor:        inlineMarkerExtractorName,
			ExtractorVersion: inlineMarkerExtractorVersion,
			Model:            "", // deterministic, no model
			StalenessHash:    fmt.Sprintf("%x", hash),
			Status:           "active",
			Evidence: []db.TribalEvidence{
				{
					SourceType:  "inline_marker",
					SourceRef:   sourceRef,
					ContentHash: fmt.Sprintf("%x", hash),
				},
			},
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// findMarkers scans content line by line, extracts comment text, and matches
// marker patterns. Handles //, #, and /* */ comment syntax.
func findMarkers(content []byte) []MarkerMatch {
	var matches []MarkerMatch
	scanner := bufio.NewScanner(bytes.NewReader(content))
	lineNum := 0
	inBlockComment := false

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if inBlockComment {
			// Look for end of block comment
			endIdx := strings.Index(line, "*/")
			var commentText string
			if endIdx >= 0 {
				commentText = line[:endIdx]
				inBlockComment = false
			} else {
				commentText = line
			}
			commentText = strings.TrimSpace(commentText)
			// Strip leading * common in block comments
			commentText = strings.TrimPrefix(commentText, "*")
			commentText = strings.TrimSpace(commentText)
			if m := matchMarker(commentText, lineNum); m != nil {
				matches = append(matches, *m)
			}
			continue
		}

		// Check for line comments: // or #
		if cm := extractLineComment(line); cm != "" {
			if m := matchMarker(cm, lineNum); m != nil {
				matches = append(matches, *m)
			}
			continue
		}

		// Check for block comment start: /*
		if startIdx := strings.Index(line, "/*"); startIdx >= 0 {
			after := line[startIdx+2:]
			endIdx := strings.Index(after, "*/")
			var commentText string
			if endIdx >= 0 {
				// Single-line block comment: /* ... */
				commentText = after[:endIdx]
			} else {
				commentText = after
				inBlockComment = true
			}
			commentText = strings.TrimSpace(commentText)
			if m := matchMarker(commentText, lineNum); m != nil {
				matches = append(matches, *m)
			}
		}
	}
	return matches
}

// extractLineComment extracts the text from a line comment (// or #).
// Returns empty string if the line is not a line comment.
func extractLineComment(line string) string {
	trimmed := strings.TrimSpace(line)

	// Check for // comment
	if idx := strings.Index(trimmed, "//"); idx >= 0 {
		return strings.TrimSpace(trimmed[idx+2:])
	}

	// Check for # comment (Python/Shell style)
	// Must start with # to avoid matching Go struct tags, URLs, etc.
	if strings.HasPrefix(trimmed, "#") {
		return strings.TrimSpace(trimmed[1:])
	}

	return ""
}

// matchMarker checks if the comment text contains a marker and returns a
// MarkerMatch if found.
func matchMarker(commentText string, lineNum int) *MarkerMatch {
	loc := markerPattern.FindStringSubmatchIndex(commentText)
	if loc == nil {
		return nil
	}

	marker := strings.ToUpper(commentText[loc[2]:loc[3]])
	// The full comment text from the marker onward is the source quote
	fullText := strings.TrimSpace(commentText[loc[0]:])

	kind, ok := markerKinds[marker]
	if !ok {
		return nil
	}

	return &MarkerMatch{
		LineNum:     lineNum,
		Marker:      marker,
		CommentText: fullText,
		Kind:        kind,
	}
}
