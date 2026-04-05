package drift

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockLLMClient is a test double for semantic.LLMClient.
type mockLLMClient struct {
	response string
	err      error
}

func (m *mockLLMClient) Complete(_ context.Context, _, _ string) (string, error) {
	return m.response, m.err
}

func TestSemanticChecker_DriftDetected(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "README.md")
	content := "## Purpose\n\nThis package handles authentication and user login.\n"
	if err := os.WriteFile(readme, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	client := &mockLLMClient{
		response: "INACCURATE: The package actually handles caching, not authentication",
	}
	checker := NewSemanticChecker(client)

	findings, err := checker.Check(context.Background(), readme, dir, "test/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(findings) == 0 {
		t.Fatal("expected at least one semantic drift finding, got none")
	}

	f := findings[0]
	if f.Kind != SemanticDrift {
		t.Errorf("expected kind %q, got %q", SemanticDrift, f.Kind)
	}
	if f.Symbol != "Purpose" {
		t.Errorf("expected symbol %q, got %q", "Purpose", f.Symbol)
	}
	if !strings.Contains(f.Detail, "caching") {
		t.Errorf("expected detail to contain LLM reason, got %q", f.Detail)
	}
}

func TestSemanticChecker_NoDrift(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "README.md")
	content := "# MyPackage\n\n## Purpose\n\nThis package handles caching with TTL-based expiry.\n"
	if err := os.WriteFile(readme, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	client := &mockLLMClient{
		response: "ACCURATE",
	}
	checker := NewSemanticChecker(client)

	findings, err := checker.Check(context.Background(), readme, dir, "test/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %d: %+v", len(findings), findings)
	}
}

func TestSemanticChecker_GracefulSkipOnError(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "README.md")
	content := "# MyPackage\n\n## Purpose\n\nSome description.\n\n## Usage\n\nAnother section.\n"
	if err := os.WriteFile(readme, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	client := &mockLLMClient{
		err: fmt.Errorf("sourcegraph: SRC_ACCESS_TOKEN is not set; deepsearch is unavailable"),
	}
	checker := NewSemanticChecker(client)

	findings, err := checker.Check(context.Background(), readme, dir, "test/repo")
	if err != nil {
		t.Fatalf("expected graceful skip, got error: %v", err)
	}

	if len(findings) != 0 {
		t.Fatalf("expected no findings on error, got %d", len(findings))
	}
}

func TestParseSections(t *testing.T) {
	content := `# Title

Some overview text.

## Architecture

The system uses microservices.

## Testing

Run go test.
`
	sections := parseSections(content)

	if len(sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(sections))
	}

	// Overview section (content before first ##)
	if sections[0].Heading != "Overview" {
		t.Errorf("expected heading %q, got %q", "Overview", sections[0].Heading)
	}
	if !strings.Contains(sections[0].Body, "Title") {
		t.Errorf("expected overview to contain title, got %q", sections[0].Body)
	}

	// Architecture section
	if sections[1].Heading != "Architecture" {
		t.Errorf("expected heading %q, got %q", "Architecture", sections[1].Heading)
	}
	if !strings.Contains(sections[1].Body, "microservices") {
		t.Errorf("expected body to contain 'microservices', got %q", sections[1].Body)
	}

	// Testing section
	if sections[2].Heading != "Testing" {
		t.Errorf("expected heading %q, got %q", "Testing", sections[2].Heading)
	}
}

func TestParseSections_EmptyContent(t *testing.T) {
	sections := parseSections("")
	if len(sections) != 0 {
		t.Fatalf("expected 0 sections for empty content, got %d", len(sections))
	}
}

func TestParseSections_NoHeaders(t *testing.T) {
	content := "Just some text\nwith no headers.\n"
	sections := parseSections(content)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if sections[0].Heading != "Overview" {
		t.Errorf("expected heading %q, got %q", "Overview", sections[0].Heading)
	}
}

func TestIsInaccurateResponse(t *testing.T) {
	tests := []struct {
		response string
		want     bool
	}{
		{"ACCURATE", false},
		{"INACCURATE: wrong description", true},
		{"inaccurate: the code does X not Y", true},
		{"The section is accurate.", false},
		{"INACCURATE\nsome details", true},
	}
	for _, tt := range tests {
		got := isInaccurateResponse(tt.response)
		if got != tt.want {
			t.Errorf("isInaccurateResponse(%q) = %v, want %v", tt.response, got, tt.want)
		}
	}
}

func TestExtractInaccurateReason(t *testing.T) {
	tests := []struct {
		response string
		want     string
	}{
		{"INACCURATE: wrong description", "wrong description"},
		{"INACCURATE:", "LLM flagged section as inaccurate"},
		{"just some text", "LLM flagged section as inaccurate"},
	}
	for _, tt := range tests {
		got := extractInaccurateReason(tt.response)
		if got != tt.want {
			t.Errorf("extractInaccurateReason(%q) = %q, want %q", tt.response, got, tt.want)
		}
	}
}

func TestSemanticChecker_MultipleSections(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "README.md")
	content := "## Good\n\nAccurate description.\n\n## Bad\n\nWrong description.\n"
	if err := os.WriteFile(readme, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	callCount := 0
	client := &callCountLLMClient{
		fn: func(_, user string) (string, error) {
			callCount++
			if strings.Contains(user, "Bad") {
				return "INACCURATE: description is wrong", nil
			}
			return "ACCURATE", nil
		},
	}
	checker := NewSemanticChecker(client)

	findings, err := checker.Check(context.Background(), readme, dir, "test/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The overview section "# Pkg" has no body content, so only Good and Bad are checked.
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls, got %d", callCount)
	}

	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Symbol != "Bad" {
		t.Errorf("expected finding for 'Bad' section, got %q", findings[0].Symbol)
	}
}

// callCountLLMClient is a test double that calls a function for each Complete call.
type callCountLLMClient struct {
	fn func(system, user string) (string, error)
}

func (c *callCountLLMClient) Complete(_ context.Context, system, user string) (string, error) {
	return c.fn(system, user)
}
