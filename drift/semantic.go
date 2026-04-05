package drift

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/live-docs/live_docs/semantic"
)

// SemanticChecker validates README section descriptions against actual code
// behavior using an LLM client (e.g. Sourcegraph deepsearch).
type SemanticChecker struct {
	client semantic.LLMClient
}

// NewSemanticChecker creates a SemanticChecker with the given LLM client.
func NewSemanticChecker(client semantic.LLMClient) *SemanticChecker {
	return &SemanticChecker{client: client}
}

// readmeSection represents a parsed section from a README file.
type readmeSection struct {
	Heading string
	Body    string
}

// Check reads the README at readmePath, parses it into sections, and asks the
// LLM whether each section accurately describes the code in packageDir.
// Findings of type SemanticDrift are returned for sections that don't match.
//
// If the LLM client returns an error (e.g. Sourcegraph unavailable), Check
// logs a warning and returns nil findings rather than failing.
func (sc *SemanticChecker) Check(ctx context.Context, readmePath, packageDir, repo string) ([]Finding, error) {
	content, err := os.ReadFile(readmePath)
	if err != nil {
		return nil, fmt.Errorf("read README %s: %w", readmePath, err)
	}

	sections := parseSections(string(content))
	if len(sections) == 0 {
		return nil, nil
	}

	var findings []Finding
	for _, sec := range sections {
		if sec.Body == "" {
			continue
		}

		finding, err := sc.checkSection(ctx, sec, readmePath, packageDir, repo)
		if err != nil {
			log.Printf("semantic: skipping section %q in %s: %v", sec.Heading, readmePath, err)
			continue
		}
		if finding != nil {
			findings = append(findings, *finding)
		}
	}

	return findings, nil
}

// checkSection sends a single README section to the LLM for validation.
// Returns a Finding if the section is semantically inaccurate, nil if it matches.
func (sc *SemanticChecker) checkSection(
	ctx context.Context,
	sec readmeSection,
	readmePath, packageDir, repo string,
) (*Finding, error) {
	systemPrompt := "You are a code documentation reviewer. " +
		"You will be given a README section and information about a code package. " +
		"Determine whether the README section accurately describes the code's actual behavior and purpose. " +
		"Respond with exactly one line: ACCURATE if the section matches the code, or " +
		"INACCURATE: <reason> if it does not."

	userPrompt := fmt.Sprintf(
		"Repository: %s\nPackage directory: %s\nREADME file: %s\n\n"+
			"## README Section: %s\n\n%s\n\n"+
			"Does this README section accurately describe the code in the package directory?",
		repo, packageDir, readmePath, sec.Heading, sec.Body,
	)

	response, err := sc.client.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	response = strings.TrimSpace(response)

	if isInaccurateResponse(response) {
		reason := extractInaccurateReason(response)
		return &Finding{
			Kind:       SemanticDrift,
			Symbol:     sec.Heading,
			SourceFile: readmePath,
			Detail:     fmt.Sprintf("README section %q may not match code: %s", sec.Heading, reason),
		}, nil
	}

	return nil, nil
}

// parseSections splits README content into sections by ## headers.
// The content before the first ## header is treated as the top-level section
// with heading "Overview".
func parseSections(content string) []readmeSection {
	var sections []readmeSection
	var current *readmeSection

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "## ") {
			if current != nil {
				current.Body = strings.TrimSpace(current.Body)
				sections = append(sections, *current)
			}
			heading := strings.TrimPrefix(line, "## ")
			heading = strings.TrimSpace(heading)
			current = &readmeSection{Heading: heading}
			continue
		}

		if current == nil {
			// Content before first ## header.
			current = &readmeSection{Heading: "Overview"}
		}
		current.Body += line + "\n"
	}

	if current != nil {
		current.Body = strings.TrimSpace(current.Body)
		if current.Body != "" {
			sections = append(sections, *current)
		}
	}

	return sections
}

// isInaccurateResponse checks whether the LLM response indicates the section
// is inaccurate.
func isInaccurateResponse(response string) bool {
	lower := strings.ToLower(response)
	// Check first line for the verdict.
	firstLine := lower
	if idx := strings.Index(lower, "\n"); idx >= 0 {
		firstLine = lower[:idx]
	}
	return strings.Contains(firstLine, "inaccurate")
}

// extractInaccurateReason extracts the reason from an "INACCURATE: reason" response.
func extractInaccurateReason(response string) string {
	firstLine := response
	if idx := strings.Index(response, "\n"); idx >= 0 {
		firstLine = response[:idx]
	}
	firstLine = strings.TrimSpace(firstLine)

	if idx := strings.Index(strings.ToUpper(firstLine), "INACCURATE:"); idx >= 0 {
		reason := strings.TrimSpace(firstLine[idx+len("INACCURATE:"):])
		if reason != "" {
			return reason
		}
	}
	return "LLM flagged section as inaccurate"
}
