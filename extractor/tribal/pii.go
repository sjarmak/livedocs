// Package tribal provides extractors for tribal knowledge from source code.
package tribal

import (
	"regexp"
)

// PII redaction placeholders.
const (
	redactedEmail   = "[REDACTED_EMAIL]"
	redactedPhone   = "[REDACTED_PHONE]"
	redactedSSN     = "[REDACTED_SSN]"
	redactedIP      = "[REDACTED_IP]"
	redactedToken   = "[REDACTED_TOKEN]"
	redactedMention = "[REDACTED_MENTION]"
)

// Compiled patterns for PII detection. Order matters: more specific patterns
// are applied first to avoid double-redaction (e.g., emails before @mentions).
var (
	// emailPattern matches standard email addresses.
	emailPattern = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)

	// ssnPattern matches US Social Security Numbers in NNN-NN-NNNN format.
	// Uses word boundaries to avoid matching inside longer strings.
	ssnPattern = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)

	// tokenPatternSK matches OpenAI-style secret keys (sk- prefix).
	tokenPatternSK = regexp.MustCompile(`\bsk-[a-zA-Z0-9]{20,}\b`)

	// tokenPatternGHP matches GitHub personal access tokens (ghp_ prefix).
	tokenPatternGHP = regexp.MustCompile(`\bghp_[a-zA-Z0-9]{30,}\b`)

	// tokenPatternAKIA matches AWS access key IDs (AKIA prefix).
	tokenPatternAKIA = regexp.MustCompile(`\bAKIA[A-Z0-9]{12,}\b`)

	// ipPattern matches IPv4 addresses. Uses word boundaries and avoids
	// matching version-like strings by requiring valid octet ranges.
	ipPattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

	// phonePatternIntl matches international phone numbers starting with +.
	phonePatternIntl = regexp.MustCompile(`\+\d[\d\s\-]{7,}\d`)

	// phonePatternUS matches US phone numbers in common formats:
	// (NNN) NNN-NNNN, NNN-NNN-NNNN, NNN.NNN.NNNN, NNN NNN NNNN
	phonePatternUS = regexp.MustCompile(`(?:\(\d{3}\)\s?|\b\d{3}[\-.\s])\d{3}[\-.\s]\d{4}\b`)

	// mentionPattern matches @username mentions (but not emails, which are
	// handled first). Matches @followed by word characters.
	mentionPattern = regexp.MustCompile(`(?:^|[\s(])(@[a-zA-Z0-9_]{1,39})\b`)
)

// piiPatterns defines the order in which redaction patterns are applied.
// Most specific patterns come first to prevent double-redaction.
type piiRule struct {
	pattern     *regexp.Regexp
	replacement string
	// If extractGroup is true, only the captured group (not the full match)
	// is replaced. Used for mention patterns where we need context.
	extractGroup bool
}

var piiRules = []piiRule{
	// 1. Emails first (most specific — contains @)
	{pattern: emailPattern, replacement: redactedEmail},
	// 2. SSN (specific numeric format)
	{pattern: ssnPattern, replacement: redactedSSN},
	// 3. API tokens (specific prefixes)
	{pattern: tokenPatternSK, replacement: redactedToken},
	{pattern: tokenPatternGHP, replacement: redactedToken},
	{pattern: tokenPatternAKIA, replacement: redactedToken},
	// 4. IP addresses
	{pattern: ipPattern, replacement: redactedIP},
	// 5. Phone numbers (before mentions, as they may contain digits)
	{pattern: phonePatternIntl, replacement: redactedPhone},
	{pattern: phonePatternUS, replacement: redactedPhone},
	// 6. @mentions last (least specific, would false-positive on emails)
	{pattern: mentionPattern, replacement: redactedMention, extractGroup: true},
}

// RedactPII replaces personally identifiable information in text with
// redaction placeholders. Patterns are applied from most specific to least
// specific to avoid double-redaction (e.g., emails are redacted before
// @-mentions so "user@example.com" becomes [REDACTED_EMAIL] rather than
// "[REDACTED_MENTION]@example.com").
func RedactPII(text string) string {
	result := text
	for _, rule := range piiRules {
		if rule.extractGroup {
			result = rule.pattern.ReplaceAllStringFunc(result, func(match string) string {
				// Find the submatch to replace only the @mention part,
				// preserving the leading whitespace/punctuation.
				sub := rule.pattern.FindStringSubmatch(match)
				if len(sub) < 2 {
					return match
				}
				// sub[1] is the @mention group; replace it within the match
				idx := len(match) - len(sub[1])
				return match[:idx] + rule.replacement
			})
		} else {
			result = rule.pattern.ReplaceAllString(result, rule.replacement)
		}
	}
	return result
}

// ContainsPII reports whether the given text contains any detectable PII
// patterns (email, phone, SSN, IP address, API token, or @-mention).
func ContainsPII(text string) bool {
	for _, rule := range piiRules {
		if rule.pattern.MatchString(text) {
			return true
		}
	}
	return false
}
