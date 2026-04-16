// Package normalize provides a conservative structural pre-hash normalization
// pipeline used by tribal fact clustering. The pipeline deliberately avoids
// semantic transformations: it removes only noise that changes with no change
// in meaning (@-mentions, file:line references, trailing punctuation, case,
// and whitespace). No stopword stripping, no synonym merging, no word
// reordering — under-clustering here is degraded-but-correct behaviour,
// while over-clustering would be silent data loss.
//
// The package is intentionally self-contained (stdlib only) so it can be
// reused from tests, calibration tooling, and the tribal writer without
// pulling transitive dependencies.
package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// atMentionRe matches @-mention tokens that often appear in PR comments or
// chat-style bodies. The regex matches an `@` followed by one or more
// identifier characters.
var atMentionRe = regexp.MustCompile(`@[A-Za-z0-9_-]+`)

// fileLineRe matches file:line references like `event.go:142`. The filename
// component is any word characters followed by `.go`, followed by `:NNN`.
// Matching only Go extensions is intentional: broader matching would start
// eating real content (e.g. email addresses `foo:123`). Other language
// extensions can be added here without changing the public API.
var fileLineRe = regexp.MustCompile(`\w+\.go:\d+`)

// lineRefRe matches bare line references like `L142`. The `\b` anchors ensure
// we do not eat tokens like `HEL123` or `CALL999`.
var lineRefRe = regexp.MustCompile(`\bL\d+\b`)

// trailingPunctRe matches any trailing run of sentence-ending punctuation at
// the very end of the string. Applied after the other scrubs so we strip
// punctuation that may have been revealed by a preceding removal.
var trailingPunctRe = regexp.MustCompile(`[.!?:;]+$`)

// whitespaceRe matches any run of whitespace characters. Used to collapse
// multiple spaces/tabs/newlines into a single space.
var whitespaceRe = regexp.MustCompile(`\s+`)

// stripAtMentions removes all @-mention tokens from s.
func stripAtMentions(s string) string {
	return atMentionRe.ReplaceAllString(s, "")
}

// stripFileLineRefs removes all `file.go:line` references from s.
func stripFileLineRefs(s string) string {
	return fileLineRe.ReplaceAllString(s, "")
}

// stripLineRefs removes bare `L\d+` line references from s.
func stripLineRefs(s string) string {
	return lineRefRe.ReplaceAllString(s, "")
}

// stripTrailingPunct removes a trailing run of [.!?:;]+ from the end of s.
// Only the final run is removed; internal punctuation is preserved.
func stripTrailingPunct(s string) string {
	return trailingPunctRe.ReplaceAllString(s, "")
}

// lower lowercases s. Kept as a private helper for test symmetry with the
// other pipeline steps.
func lower(s string) string {
	return strings.ToLower(s)
}

// collapseWhitespace replaces every run of whitespace with a single space.
func collapseWhitespace(s string) string {
	return whitespaceRe.ReplaceAllString(s, " ")
}

// Scrub applies the normalization pipeline to body and returns the scrubbed
// string (without hashing). Exposed so calibration tooling can inspect what
// ScrubAndHash is actually hashing.
func Scrub(body string) string {
	s := body
	s = stripAtMentions(s)
	s = stripFileLineRefs(s)
	s = stripLineRefs(s)
	s = stripTrailingPunct(s)
	s = lower(s)
	s = collapseWhitespace(s)
	s = strings.TrimSpace(s)
	return s
}

// ScrubAndHash returns the sha256 hex digest of the scrubbed+normalized body.
// Two bodies that differ only in @-mentions, file:line references, bare `L\d+`
// line references, trailing sentence punctuation, case, or whitespace hash to
// the same digest.
func ScrubAndHash(body string) string {
	sum := sha256.Sum256([]byte(Scrub(body)))
	return hex.EncodeToString(sum[:])
}

// TokenJaccard computes the Jaccard similarity between the whitespace-tokenized
// lowercased token sets of a and b. Returns 0 when both sets are empty. The
// helper is used by the cluster-debug instrumentation to record how close
// two bodies are when the structural hash decides they belong in different
// clusters. It is intentionally simple — mechanical comparison, not a
// semantic similarity metric.
func TokenJaccard(a, b string) float64 {
	setA := tokenSet(a)
	setB := tokenSet(b)
	if len(setA) == 0 && len(setB) == 0 {
		return 0
	}
	intersect := 0
	for tok := range setA {
		if _, ok := setB[tok]; ok {
			intersect++
		}
	}
	union := len(setA) + len(setB) - intersect
	if union == 0 {
		return 0
	}
	return float64(intersect) / float64(union)
}

// tokenSet returns the set of lowercased whitespace-delimited tokens in s.
func tokenSet(s string) map[string]struct{} {
	fields := strings.Fields(strings.ToLower(s))
	set := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		set[f] = struct{}{}
	}
	return set
}
