package drift

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockLLM implements semantic.LLMClient for testing.
type mockLLM struct {
	response string
	err      error
	calls    []mockLLMCall
}

type mockLLMCall struct {
	System string
	User   string
}

func (m *mockLLM) Complete(_ context.Context, system, user string) (string, error) {
	m.calls = append(m.calls, mockLLMCall{System: system, User: user})
	return m.response, m.err
}

// mockSearcher implements CodeSearcher for testing.
type mockSearcher struct {
	results map[string]string // repo -> result
	err     error
	calls   []mockSearchCall
}

type mockSearchCall struct {
	Repo  string
	Query string
}

func (m *mockSearcher) Search(_ context.Context, repo, query string) (string, error) {
	m.calls = append(m.calls, mockSearchCall{Repo: repo, Query: query})
	if m.err != nil {
		return "", m.err
	}
	return m.results[repo], nil
}

func TestLoadDocMap(t *testing.T) {
	content := `repos:
  - name: github.com/org/repo-a
    short: repo-a
    mappings:
      - source: "cmd/**/*.go"
        docs:
          - docs/01-intro.md
          - docs/02-guide.md
      - source: "internal/**/*.go"
        docs:
          - docs/01-intro.md
  - name: github.com/org/repo-b
    short: repo-b
    mappings:
      - source: "**/*.go"
        docs:
          - docs/02-guide.md
`
	path := filepath.Join(t.TempDir(), "doc-map.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	dm, err := LoadDocMap(path)
	if err != nil {
		t.Fatalf("LoadDocMap: %v", err)
	}

	if len(dm.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(dm.Repos))
	}
	if dm.Repos[0].Short != "repo-a" {
		t.Errorf("expected short=repo-a, got %s", dm.Repos[0].Short)
	}
	if len(dm.Repos[0].Mappings) != 2 {
		t.Errorf("expected 2 mappings for repo-a, got %d", len(dm.Repos[0].Mappings))
	}
}

func TestDocMap_ReposForDoc(t *testing.T) {
	dm := &DocMap{
		Repos: []DocMapRepo{
			{Name: "github.com/org/alpha", Short: "alpha", Mappings: []DocMapMapping{
				{Source: "**/*.go", Docs: []string{"docs/shared.md", "docs/alpha-only.md"}},
			}},
			{Name: "github.com/org/beta", Short: "beta", Mappings: []DocMapMapping{
				{Source: "**/*.go", Docs: []string{"docs/shared.md"}},
			}},
			{Name: "github.com/org/gamma", Short: "gamma", Mappings: []DocMapMapping{
				{Source: "**/*.go", Docs: []string{"docs/gamma-only.md"}},
			}},
		},
	}

	repos := dm.ReposForDoc("docs/shared.md")
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos for shared.md, got %d", len(repos))
	}
	shorts := []string{repos[0].Short, repos[1].Short}
	if shorts[0] != "alpha" || shorts[1] != "beta" {
		t.Errorf("expected [alpha, beta], got %v", shorts)
	}

	repos = dm.ReposForDoc("docs/alpha-only.md")
	if len(repos) != 1 || repos[0].Short != "alpha" {
		t.Errorf("expected [alpha] for alpha-only.md, got %v", repos)
	}

	repos = dm.ReposForDoc("docs/nonexistent.md")
	if len(repos) != 0 {
		t.Errorf("expected 0 repos for nonexistent.md, got %d", len(repos))
	}
}

func TestExtractKeyTerms(t *testing.T) {
	tests := []struct {
		heading  string
		body     string
		wantAny  []string // at least these terms should appear
		wantNone []string // these should not appear
	}{
		{
			heading: "Supervisor & Reconciliation",
			body:    "The `ReconcileLoop` manages per-city state. Uses `city.toml` for config.",
			wantAny: []string{"ReconcileLoop", "city.toml"},
		},
		{
			heading: "Port Coordination",
			body:    "The supervisor_api runs on port 8372. Uses AutoAllocate for dolt_port.",
			wantAny: []string{"supervisor_api", "dolt_port", "AutoAllocate"},
		},
		{
			heading:  "Simple heading",
			body:     "No technical terms here at all.",
			wantNone: []string{"No", "technical", "terms"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.heading, func(t *testing.T) {
			terms := extractKeyTerms(tt.heading, tt.body)
			termSet := make(map[string]bool)
			for _, term := range terms {
				termSet[term] = true
			}
			for _, want := range tt.wantAny {
				if !termSet[want] {
					t.Errorf("expected term %q in %v", want, terms)
				}
			}
			for _, notWant := range tt.wantNone {
				if termSet[notWant] {
					t.Errorf("did not expect term %q in %v", notWant, terms)
				}
			}
		})
	}
}

func TestParseCrossRepoResponse_Current(t *testing.T) {
	response := `{"status": "CURRENT", "stale_claims": []}`
	finding, err := parseCrossRepoResponse(response, "Test Section", "doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if finding != nil {
		t.Errorf("expected nil finding for CURRENT status, got %+v", finding)
	}
}

func TestParseCrossRepoResponse_Stale(t *testing.T) {
	response := `{
		"status": "STALE",
		"stale_claims": [
			{
				"claim": "Default port is 8372",
				"evidence": "Code shows default is 8400 in config.go:42",
				"severity": "HIGH"
			},
			{
				"claim": "Uses two-phase startup",
				"evidence": "Now three phases: init, connect, ready",
				"severity": "MEDIUM"
			}
		]
	}`

	finding, err := parseCrossRepoResponse(response, "Supervisor", "docs/07.md")
	if err != nil {
		t.Fatal(err)
	}
	if finding == nil {
		t.Fatal("expected finding for STALE status")
	}
	if finding.Kind != SemanticDrift {
		t.Errorf("expected SemanticDrift, got %s", finding.Kind)
	}
	if len(finding.StaleClaims) != 2 {
		t.Errorf("expected 2 stale claims, got %d", len(finding.StaleClaims))
	}
	if finding.StaleClaims[0].Severity != "HIGH" {
		t.Errorf("expected HIGH severity, got %s", finding.StaleClaims[0].Severity)
	}
}

func TestParseCrossRepoResponse_MarkdownFenced(t *testing.T) {
	response := "```json\n{\"status\": \"CURRENT\", \"stale_claims\": []}\n```"
	finding, err := parseCrossRepoResponse(response, "Test", "doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if finding != nil {
		t.Errorf("expected nil for CURRENT, got %+v", finding)
	}
}

func TestParseCrossRepoResponse_Uncertain(t *testing.T) {
	response := `{"status": "UNCERTAIN", "stale_claims": []}`
	finding, err := parseCrossRepoResponse(response, "Test", "doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if finding == nil {
		return // correct
	}
	t.Errorf("expected nil finding for UNCERTAIN, got %+v", finding)
}

func TestCrossRepoChecker_CheckDoc(t *testing.T) {
	// Write a test doc.
	dir := t.TempDir()
	docPath := filepath.Join(dir, "test-doc.md")
	docContent := `# Test Doc

## Supervisor Architecture

The supervisor runs on port 8372 by default. It uses a ` + "`ReconcileLoop`" + ` to manage state.

## Storage Modes

There are three storage modes: embedded, server, and remote.
`
	if err := os.WriteFile(docPath, []byte(docContent), 0644); err != nil {
		t.Fatal(err)
	}

	dm := &DocMap{
		Repos: []DocMapRepo{
			{
				Name:  "github.com/org/myrepo",
				Short: "myrepo",
				Mappings: []DocMapMapping{
					{Source: "**/*.go", Docs: []string{docPath}},
				},
			},
		},
	}

	llm := &mockLLM{
		response: `{"status": "STALE", "stale_claims": [{"claim": "port 8372", "evidence": "now 8400", "severity": "HIGH"}]}`,
	}
	searcher := &mockSearcher{
		results: map[string]string{
			"github.com/org/myrepo": "func DefaultPort() int { return 8400 }",
		},
	}

	checker := NewCrossRepoChecker(llm, searcher, dm)
	report, err := checker.CheckDoc(context.Background(), docPath, docPath)
	if err != nil {
		t.Fatal(err)
	}

	if report.Sections < 2 {
		t.Errorf("expected at least 2 sections, got %d", report.Sections)
	}

	// Verify the searcher was called for each section with relevant terms.
	if len(searcher.calls) == 0 {
		t.Fatal("expected searcher to be called")
	}
	for _, call := range searcher.calls {
		if call.Repo != "github.com/org/myrepo" {
			t.Errorf("expected repo github.com/org/myrepo, got %s", call.Repo)
		}
	}

	// Verify the LLM was called with section content and code context.
	if len(llm.calls) == 0 {
		t.Fatal("expected LLM to be called")
	}
	for _, call := range llm.calls {
		if !strings.Contains(call.User, "Documentation Content") {
			t.Error("expected user prompt to contain 'Documentation Content'")
		}
		if !strings.Contains(call.User, "Code Context") {
			t.Error("expected user prompt to contain 'Code Context'")
		}
	}

	// All sections get the same STALE response from the mock.
	if report.Stale == 0 {
		t.Error("expected at least 1 stale section")
	}
	if len(report.Findings) == 0 {
		t.Fatal("expected findings")
	}
	if len(report.Findings[0].StaleClaims) != 1 {
		t.Errorf("expected 1 stale claim, got %d", len(report.Findings[0].StaleClaims))
	}
}

func TestCrossRepoChecker_SkipsEmptySearch(t *testing.T) {
	dir := t.TempDir()
	docPath := filepath.Join(dir, "test-doc.md")
	if err := os.WriteFile(docPath, []byte("# Doc\n\n## Section\n\nSome content.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	dm := &DocMap{
		Repos: []DocMapRepo{
			{Name: "github.com/org/repo", Short: "repo", Mappings: []DocMapMapping{
				{Source: "**/*.go", Docs: []string{docPath}},
			}},
		},
	}

	llm := &mockLLM{response: `{"status": "CURRENT", "stale_claims": []}`}
	// Searcher returns empty results — LLM should not be called.
	searcher := &mockSearcher{results: map[string]string{}}

	checker := NewCrossRepoChecker(llm, searcher, dm)
	report, err := checker.CheckDoc(context.Background(), docPath, docPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(llm.calls) > 0 {
		t.Errorf("LLM should not be called when searcher returns empty results, got %d calls", len(llm.calls))
	}
	if report.Stale != 0 {
		t.Errorf("expected 0 stale sections, got %d", report.Stale)
	}
}

func TestCrossRepoChecker_SearcherError(t *testing.T) {
	dir := t.TempDir()
	docPath := filepath.Join(dir, "test-doc.md")
	if err := os.WriteFile(docPath, []byte("# Doc\n\n## Section\n\nContent with `SomeFunc`.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	dm := &DocMap{
		Repos: []DocMapRepo{
			{Name: "github.com/org/repo", Short: "repo", Mappings: []DocMapMapping{
				{Source: "**/*.go", Docs: []string{docPath}},
			}},
		},
	}

	llm := &mockLLM{response: `{"status": "CURRENT", "stale_claims": []}`}
	searcher := &mockSearcher{err: fmt.Errorf("network error")}

	checker := NewCrossRepoChecker(llm, searcher, dm)
	report, err := checker.CheckDoc(context.Background(), docPath, docPath)
	if err != nil {
		t.Fatal(err)
	}

	// Should gracefully handle search errors — no findings, no LLM calls.
	if len(llm.calls) > 0 {
		t.Error("LLM should not be called when search fails")
	}
	if report.Stale != 0 {
		t.Errorf("expected 0 stale, got %d", report.Stale)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("expected 'short', got %q", got)
	}
	if got := truncate("this is a long string", 10); got != "this is..." {
		t.Errorf("expected 'this is...', got %q", got)
	}
}

func TestStripMarkdownJSON(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`{"status": "CURRENT"}`, `{"status": "CURRENT"}`},
		{"```json\n{\"status\": \"CURRENT\"}\n```", `{"status": "CURRENT"}`},
		{"```\n{\"status\": \"CURRENT\"}\n```", `{"status": "CURRENT"}`},
	}
	for _, tt := range tests {
		if got := stripMarkdownJSON(tt.input); got != tt.want {
			t.Errorf("stripMarkdownJSON(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatCrossRepoReport(t *testing.T) {
	report := &CrossRepoReport{
		DocPath:  "docs/05-primitives.md",
		Repos:    []string{"gascity", "gastown"},
		Sections: 5,
		Stale:    1,
		Findings: []CrossRepoFinding{
			{
				Finding: Finding{
					Kind:   SemanticDrift,
					Symbol: "Port Coordination",
				},
				StaleClaims: []StaleClaim{
					{Claim: "Default port is 8372", Evidence: "Now 8400", Severity: "HIGH"},
				},
			},
		},
	}

	output := FormatCrossRepoReport(report)
	if !strings.Contains(output, "docs/05-primitives.md") {
		t.Error("expected doc path in output")
	}
	if !strings.Contains(output, "gascity") {
		t.Error("expected repo name in output")
	}
	if !strings.Contains(output, "Default port is 8372") {
		t.Error("expected stale claim in output")
	}
	if !strings.Contains(output, "HIGH") {
		t.Error("expected severity in output")
	}
}
