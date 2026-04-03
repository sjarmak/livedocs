package extractor

import (
	"regexp"
	"strings"
)

// sensitivePattern matches common secret/credential patterns in claim text.
// Case-insensitive matching is handled by compiling with (?i).
var sensitivePattern = regexp.MustCompile(`(?i)(password|secret|token|credential|api_key)`)

// IsSensitiveContent reports whether text contains patterns that indicate
// sensitive content (passwords, secrets, tokens, credentials, API keys).
func IsSensitiveContent(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	return sensitivePattern.MatchString(text)
}

// FilterSensitiveClaims returns a new slice with claims whose ObjectText
// matches sensitive patterns removed. The input slice is not modified.
func FilterSensitiveClaims(claims []Claim) []Claim {
	filtered := make([]Claim, 0, len(claims))
	for _, c := range claims {
		if !IsSensitiveContent(c.ObjectText) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}
