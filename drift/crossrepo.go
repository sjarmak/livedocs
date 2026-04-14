// Cross-repo semantic drift detection.
//
// Validates documentation sections against code in remote repositories by:
//  1. Parsing a doc-map that links source file patterns to documentation files
//  2. Splitting each doc into sections
//  3. Fetching relevant code from mapped repos via a CodeSearcher
//  4. Sending each section + code context to an LLM that identifies stale claims
//
// This extends the single-repo SemanticChecker to work with documentation
// that describes code living in other repositories (the Gas City use case).
package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/live-docs/live_docs/semantic"
)

// CodeSearcher fetches code context from remote repositories.
// Implementations wrap Sourcegraph MCP, local git, or test mocks.
type CodeSearcher interface {
	// Search returns code snippets from repo relevant to the query.
	Search(ctx context.Context, repo, query string) (string, error)
}

// DocMap is the parsed doc-map.yaml structure mapping repos to documentation.
type DocMap struct {
	Repos []DocMapRepo `yaml:"repos"`
}

// DocMapRepo is a single repository entry in the doc-map.
type DocMapRepo struct {
	Name     string          `yaml:"name"`
	Short    string          `yaml:"short"`
	Mappings []DocMapMapping `yaml:"mappings"`
}

// DocMapMapping links a source file pattern to documentation files.
type DocMapMapping struct {
	Source string   `yaml:"source"`
	Docs   []string `yaml:"docs"`
}

// LoadDocMap reads and parses a doc-map.yaml file.
func LoadDocMap(path string) (*DocMap, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read doc-map %s: %w", path, err)
	}
	var dm DocMap
	if err := yaml.Unmarshal(data, &dm); err != nil {
		return nil, fmt.Errorf("parse doc-map %s: %w", path, err)
	}
	return &dm, nil
}

// ReposForDoc returns the repo entries whose mappings reference the given doc path.
func (dm *DocMap) ReposForDoc(docPath string) []DocMapRepo {
	var result []DocMapRepo
	seen := make(map[string]bool)
	for _, repo := range dm.Repos {
		for _, m := range repo.Mappings {
			for _, doc := range m.Docs {
				if doc == docPath && !seen[repo.Name] {
					result = append(result, repo)
					seen[repo.Name] = true
				}
			}
		}
	}
	return result
}

// StaleClaim is a specific factual claim in a doc section that may be outdated.
type StaleClaim struct {
	Claim    string `json:"claim"`
	Evidence string `json:"evidence"`
	Severity string `json:"severity"` // HIGH, MEDIUM, LOW
}

// CrossRepoFinding extends Finding with structured stale claims.
type CrossRepoFinding struct {
	Finding
	StaleClaims []StaleClaim `json:"stale_claims,omitempty"`
}

// CrossRepoReport holds findings for a single documentation file.
type CrossRepoReport struct {
	DocPath  string             `json:"doc_path"`
	Repos    []string           `json:"repos"`
	Findings []CrossRepoFinding `json:"findings"`
	Sections int                `json:"sections_checked"`
	Stale    int                `json:"stale_sections"`
}

// CrossRepoChecker validates documentation against code in remote repositories.
type CrossRepoChecker struct {
	llm      semantic.LLMClient
	searcher CodeSearcher
	docMap   *DocMap
}

// NewCrossRepoChecker creates a checker with the given LLM client, code searcher,
// and doc-map configuration.
func NewCrossRepoChecker(llm semantic.LLMClient, searcher CodeSearcher, docMap *DocMap) *CrossRepoChecker {
	return &CrossRepoChecker{
		llm:      llm,
		searcher: searcher,
		docMap:   docMap,
	}
}

// CheckDoc validates a single documentation file against all mapped repos.
// mapKey is the path as it appears in the doc-map (e.g. "docs/01-foo.md").
// filePath is the actual filesystem path to read. If mapKey is empty, filePath
// is used for both lookup and reading.
func (c *CrossRepoChecker) CheckDoc(ctx context.Context, mapKey, filePath string) (*CrossRepoReport, error) {
	if mapKey == "" {
		mapKey = filePath
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read doc %s: %w", filePath, err)
	}

	repos := c.docMap.ReposForDoc(mapKey)
	if len(repos) == 0 {
		return &CrossRepoReport{DocPath: mapKey}, nil
	}

	repoNames := make([]string, len(repos))
	for i, r := range repos {
		repoNames[i] = r.Short
	}

	sections := parseSections(string(content))

	report := &CrossRepoReport{
		DocPath:  mapKey,
		Repos:    repoNames,
		Sections: len(sections),
	}

	for _, sec := range sections {
		if sec.Body == "" {
			continue
		}

		finding, err := c.checkSection(ctx, sec, repos, mapKey)
		if err != nil {
			// Non-fatal: log and continue to next section.
			continue
		}
		if finding != nil {
			report.Findings = append(report.Findings, *finding)
			report.Stale++
		}
	}

	return report, nil
}

// CheckAllDocs validates all documentation files referenced in the doc-map.
func (c *CrossRepoChecker) CheckAllDocs(ctx context.Context, docsDir string) ([]*CrossRepoReport, error) {
	// Collect unique doc paths from all mappings.
	docSet := make(map[string]bool)
	for _, repo := range c.docMap.Repos {
		for _, m := range repo.Mappings {
			for _, doc := range m.Docs {
				docSet[doc] = true
			}
		}
	}

	docPaths := make([]string, 0, len(docSet))
	for doc := range docSet {
		docPaths = append(docPaths, doc)
	}
	sort.Strings(docPaths)

	var reports []*CrossRepoReport
	for _, docPath := range docPaths {
		if err := ctx.Err(); err != nil {
			return reports, err
		}

		fullPath := docPath
		if docsDir != "" {
			// doc-map paths are relative (e.g. "docs/01-foo.md"),
			// but docsDir might point to the parent directory.
			// If the file doesn't exist at docPath, try docsDir + basename.
			if _, err := os.Stat(fullPath); err != nil {
				// Try resolving relative to docsDir's parent.
				fullPath = docsDir + "/" + strings.TrimPrefix(docPath, "docs/")
				if _, err := os.Stat(fullPath); err != nil {
					continue
				}
			}
		}

		report, err := c.CheckDoc(ctx, docPath, fullPath)
		if err != nil {
			continue
		}
		reports = append(reports, report)
	}

	return reports, nil
}

// checkSection gathers code context from mapped repos and sends the section
// to the LLM for verification.
func (c *CrossRepoChecker) checkSection(
	ctx context.Context,
	sec readmeSection,
	repos []DocMapRepo,
	docPath string,
) (*CrossRepoFinding, error) {
	// Build search query from section heading and key terms.
	query := buildSearchQuery(sec)
	if query == "" {
		return nil, nil
	}

	// Gather code context from each mapped repo.
	var codeCtx strings.Builder
	for _, repo := range repos {
		result, err := c.searcher.Search(ctx, repo.Name, query)
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(result)
		if trimmed == "" {
			continue
		}
		fmt.Fprintf(&codeCtx, "\n### Code from %s\n\n%s\n", repo.Short, truncate(trimmed, 6000))
	}

	if codeCtx.Len() == 0 {
		// No code context found — can't verify, skip.
		return nil, nil
	}

	// Ask the LLM to verify the section against the code.
	response, err := c.llm.Complete(ctx, crossRepoSystemPrompt, buildCrossRepoUserPrompt(sec, codeCtx.String(), docPath))
	if err != nil {
		return nil, fmt.Errorf("LLM verification: %w", err)
	}

	return parseCrossRepoResponse(response, sec.Heading, docPath)
}

// crossRepoSystemPrompt instructs the LLM to verify documentation against code.
const crossRepoSystemPrompt = `You are a documentation accuracy reviewer. You will be given a documentation section and code snippets from the repositories it describes.

Your job is to identify specific factual claims in the documentation that may be STALE or INACCURATE based on the code evidence.

Focus on concrete, verifiable claims:
- Port numbers, default values, configuration keys
- Number of modes, phases, or steps in a process
- Function/type/struct names referenced in the text
- Behavioral descriptions (what happens when X)
- Architecture descriptions (component relationships)
- CLI commands and flags

Ignore:
- Stylistic or subjective statements
- Historical context that can't be verified from current code
- Claims about external systems not shown in the code

Respond with a JSON object:
{
  "status": "CURRENT" | "STALE" | "UNCERTAIN",
  "stale_claims": [
    {
      "claim": "the specific claim from the doc",
      "evidence": "what the code actually shows",
      "severity": "HIGH" | "MEDIUM" | "LOW"
    }
  ]
}

If the documentation appears current, return {"status": "CURRENT", "stale_claims": []}.
If you cannot determine accuracy from the code provided, return {"status": "UNCERTAIN", "stale_claims": []}.
Only flag claims as STALE when you have concrete code evidence contradicting them.`

// buildCrossRepoUserPrompt constructs the user prompt for cross-repo verification.
func buildCrossRepoUserPrompt(sec readmeSection, codeContext, docPath string) string {
	return fmt.Sprintf(
		"Documentation file: %s\nSection: %s\n\n## Documentation Content\n\n%s\n\n## Code Context\n%s",
		docPath, sec.Heading, sec.Body, codeContext,
	)
}

// parseCrossRepoResponse parses the LLM's JSON response into a finding.
func parseCrossRepoResponse(response, heading, docPath string) (*CrossRepoFinding, error) {
	response = strings.TrimSpace(response)
	response = stripMarkdownJSON(response)

	var result struct {
		Status      string       `json:"status"`
		StaleClaims []StaleClaim `json:"stale_claims"`
	}

	if err := json.Unmarshal([]byte(response), &result); err != nil {
		// If JSON parsing fails, fall back to text analysis.
		lower := strings.ToLower(response)
		if strings.Contains(lower, "stale") || strings.Contains(lower, "inaccurate") {
			return &CrossRepoFinding{
				Finding: Finding{
					Kind:       SemanticDrift,
					Symbol:     heading,
					SourceFile: docPath,
					Detail:     fmt.Sprintf("Section %q may be stale (LLM response unparseable: %s)", heading, truncate(response, 200)),
				},
			}, nil
		}
		return nil, nil
	}

	if result.Status == "CURRENT" || result.Status == "UNCERTAIN" {
		return nil, nil
	}

	if len(result.StaleClaims) == 0 {
		return nil, nil
	}

	// Build a summary detail string from the stale claims.
	var details []string
	for _, sc := range result.StaleClaims {
		details = append(details, fmt.Sprintf("[%s] %s — %s", sc.Severity, sc.Claim, sc.Evidence))
	}

	return &CrossRepoFinding{
		Finding: Finding{
			Kind:       SemanticDrift,
			Symbol:     heading,
			SourceFile: docPath,
			Detail:     fmt.Sprintf("Section %q has %d stale claim(s):\n%s", heading, len(result.StaleClaims), strings.Join(details, "\n")),
		},
		StaleClaims: result.StaleClaims,
	}, nil
}

// buildSearchQuery extracts a search query from a section heading and body.
// Combines the heading with key technical terms from the body.
func buildSearchQuery(sec readmeSection) string {
	terms := extractKeyTerms(sec.Heading, sec.Body)
	if len(terms) == 0 {
		return sec.Heading
	}
	// Heading provides the topic; key terms add specificity.
	return sec.Heading + " " + strings.Join(terms, " ")
}

// technicalTermRe matches backtick-quoted terms, PascalCase, camelCase, snake_case,
// and dotted identifiers in text.
var technicalTermRe = regexp.MustCompile(
	"`([^`]+)`" + "|" + // backtick-quoted
		`\b([A-Z][a-z]+[A-Z][a-zA-Z0-9]*)\b` + "|" + // PascalCase
		`\b([a-z][a-zA-Z0-9]*[A-Z][a-zA-Z0-9]*)\b` + "|" + // camelCase
		`\b([a-zA-Z][a-zA-Z0-9]*_[a-zA-Z0-9_]+)\b`, // snake_case
)

// extractKeyTerms pulls technical identifiers from heading and first ~500 chars of body.
func extractKeyTerms(heading, body string) []string {
	// Limit body scan to avoid overwhelming the search query.
	scanBody := body
	if len(scanBody) > 500 {
		scanBody = scanBody[:500]
	}
	text := heading + " " + scanBody

	termSet := make(map[string]bool)
	for _, match := range technicalTermRe.FindAllStringSubmatch(text, -1) {
		for _, group := range match[1:] {
			if group != "" && len(group) >= 2 {
				termSet[group] = true
			}
		}
	}

	terms := make([]string, 0, len(termSet))
	for t := range termSet {
		terms = append(terms, t)
	}
	sort.Strings(terms)

	// Cap at 8 terms to keep the search query focused.
	if len(terms) > 8 {
		terms = terms[:8]
	}
	return terms
}

// stripMarkdownJSON removes ```json fences from an LLM response.
func stripMarkdownJSON(s string) string {
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// FormatCrossRepoReport formats a CrossRepoReport as readable text.
func FormatCrossRepoReport(r *CrossRepoReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n", r.DocPath)
	fmt.Fprintf(&b, "- **Repos**: %s\n", strings.Join(r.Repos, ", "))
	fmt.Fprintf(&b, "- **Sections checked**: %d\n", r.Sections)
	fmt.Fprintf(&b, "- **Stale sections**: %d\n\n", r.Stale)

	if len(r.Findings) == 0 {
		fmt.Fprintf(&b, "No drift detected.\n\n")
		return b.String()
	}

	for _, f := range r.Findings {
		fmt.Fprintf(&b, "#### %s — %s\n\n", f.Symbol, f.Kind)
		if len(f.StaleClaims) > 0 {
			for _, sc := range f.StaleClaims {
				fmt.Fprintf(&b, "- **[%s]** %s\n", sc.Severity, sc.Claim)
				fmt.Fprintf(&b, "  Evidence: %s\n", sc.Evidence)
			}
		} else {
			fmt.Fprintf(&b, "- %s\n", f.Detail)
		}
		b.WriteString("\n")
	}

	return b.String()
}
