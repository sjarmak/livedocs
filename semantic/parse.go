package semantic

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
)

// llmSymbolResponse is the expected JSON structure from the LLM for one symbol.
type llmSymbolResponse struct {
	SubjectName  string `json:"subject_name"`
	Purpose      string `json:"purpose,omitempty"`
	UsagePattern string `json:"usage_pattern,omitempty"`
	Complexity   string `json:"complexity,omitempty"`
	Stability    string `json:"stability,omitempty"`
}

// validComplexity is the set of accepted complexity values.
var validComplexity = map[string]bool{
	"trivial": true, "simple": true, "moderate": true,
	"complex": true, "very_complex": true,
}

// validStability is the set of accepted stability values.
var validStability = map[string]bool{
	"stable": true, "evolving": true, "unstable": true, "deprecated": true,
}

// parseLLMResponse parses the LLM's JSON response into extractor.Claim values.
// It matches each response entry to a symbol from the provided map. Claims that
// reference unknown symbols or have invalid enum values are silently skipped.
func parseLLMResponse(
	raw string,
	symbolMap map[string]db.Symbol,
	importPath string,
	repo string,
) ([]extractor.Claim, error) {
	// Strip markdown fences if the LLM wrapped the response.
	raw = stripMarkdownFences(raw)
	raw = strings.TrimSpace(raw)

	var responses []llmSymbolResponse
	if err := json.Unmarshal([]byte(raw), &responses); err != nil {
		return nil, fmt.Errorf("parse LLM response: %w", err)
	}

	now := time.Now().UTC()
	var claims []extractor.Claim

	for _, r := range responses {
		sym, ok := symbolMap[r.SubjectName]
		if !ok {
			continue // symbol not in this package; skip
		}

		base := extractor.Claim{
			SubjectRepo:       repo,
			SubjectImportPath: importPath,
			SubjectName:       r.SubjectName,
			Language:          sym.Language,
			Kind:              extractor.SymbolKind(sym.Kind),
			Visibility:        extractor.Visibility(sym.Visibility),
			SourceFile:        "llm-semantic",
			ClaimTier:         extractor.TierSemantic,
			Extractor:         ExtractorName,
			ExtractorVersion:  Version,
			LastVerified:      now,
		}

		if r.Purpose != "" {
			c := base
			c.Predicate = extractor.PredicatePurpose
			c.ObjectText = r.Purpose
			c.Confidence = 0.7
			claims = append(claims, c)
		}
		if r.UsagePattern != "" {
			c := base
			c.Predicate = extractor.PredicateUsagePattern
			c.ObjectText = r.UsagePattern
			c.Confidence = 0.6
			claims = append(claims, c)
		}
		if r.Complexity != "" && validComplexity[r.Complexity] {
			c := base
			c.Predicate = extractor.PredicateComplexity
			c.ObjectText = r.Complexity
			c.Confidence = 0.6
			claims = append(claims, c)
		}
		if r.Stability != "" && validStability[r.Stability] {
			c := base
			c.Predicate = extractor.PredicateStability
			c.ObjectText = r.Stability
			c.Confidence = 0.5
			claims = append(claims, c)
		}
	}

	return claims, nil
}

// stripMarkdownFences removes ```json ... ``` wrappers from LLM output.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Remove opening fence (with optional language tag).
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		// Remove closing fence.
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}
