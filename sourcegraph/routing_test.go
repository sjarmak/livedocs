package sourcegraph

import (
	"context"
	"fmt"
	"testing"

	"github.com/sjarmak/livedocs/extractor"
)

// mockCaller implements MCPCaller for testing.
type mockCaller struct {
	// callLog records each call for assertion.
	callLog []mockCall
	// results maps toolName to a canned response.
	results map[string]string
	// errs maps toolName to a canned error.
	errs map[string]error
}

type mockCall struct {
	ToolName string
	Args     map[string]any
}

func (m *mockCaller) CallTool(_ context.Context, toolName string, args map[string]any) (string, error) {
	m.callLog = append(m.callLog, mockCall{ToolName: toolName, Args: args})
	if err, ok := m.errs[toolName]; ok {
		return "", err
	}
	return m.results[toolName], nil
}

func newMockCaller(results map[string]string, errs map[string]error) *mockCaller {
	if results == nil {
		results = make(map[string]string)
	}
	if errs == nil {
		errs = make(map[string]error)
	}
	return &mockCaller{results: results, errs: errs}
}

var testSym = SymbolContext{
	Name:       "Pod",
	Repo:       "kubernetes/kubernetes",
	ImportPath: "k8s.io/api/core/v1",
}

func TestDefaultRouter_Route(t *testing.T) {
	tests := []struct {
		name        string
		predicate   extractor.Predicate
		results     map[string]string
		errs        map[string]error
		wantTool    string
		wantContain string
		wantErr     bool
	}{
		{
			name:        "purpose routes to deepsearch",
			predicate:   extractor.PredicatePurpose,
			results:     map[string]string{"deepsearch": "Pod is the fundamental deployable unit"},
			wantTool:    "deepsearch",
			wantContain: "Pod is the fundamental deployable unit",
		},
		{
			name:        "usage_pattern routes to find_references",
			predicate:   extractor.PredicateUsagePattern,
			results:     map[string]string{"find_references": "scheduler.go:42\nkubelet.go:88"},
			wantTool:    "find_references",
			wantContain: "scheduler.go:42",
		},
		{
			name:        "complexity routes to deepsearch",
			predicate:   extractor.PredicateComplexity,
			results:     map[string]string{"deepsearch": "High cyclomatic complexity due to validation"},
			wantTool:    "deepsearch",
			wantContain: "High cyclomatic complexity",
		},
		{
			name:        "stability routes to commit_search",
			predicate:   extractor.PredicateStability,
			results:     map[string]string{"commit_search": "commit1\ncommit2"},
			wantTool:    "commit_search",
			wantContain: "High stability: 2 commits in 6 months",
		},
		{
			name:      "purpose error propagates",
			predicate: extractor.PredicatePurpose,
			errs:      map[string]error{"deepsearch": fmt.Errorf("network timeout")},
			wantTool:  "deepsearch",
			wantErr:   true,
		},
		{
			name:      "usage_pattern error propagates",
			predicate: extractor.PredicateUsagePattern,
			errs:      map[string]error{"find_references": fmt.Errorf("not found")},
			wantTool:  "find_references",
			wantErr:   true,
		},
		{
			name:      "stability error propagates",
			predicate: extractor.PredicateStability,
			errs:      map[string]error{"commit_search": fmt.Errorf("rate limited")},
			wantTool:  "commit_search",
			wantErr:   true,
		},
		{
			name:      "complexity error propagates",
			predicate: extractor.PredicateComplexity,
			errs:      map[string]error{"deepsearch": fmt.Errorf("server error")},
			wantTool:  "deepsearch",
			wantErr:   true,
		},
		{
			name:      "unsupported predicate returns error",
			predicate: extractor.Predicate("unknown_pred"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockCaller(tt.results, tt.errs)
			router := NewDefaultRouter(mock)

			got, err := router.Route(context.Background(), tt.predicate, testSym)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify correct tool was called.
			if tt.wantTool != "" {
				if len(mock.callLog) == 0 {
					t.Fatal("expected a tool call, got none")
				}
				if mock.callLog[0].ToolName != tt.wantTool {
					t.Errorf("called tool %q, want %q", mock.callLog[0].ToolName, tt.wantTool)
				}
			}

			// Verify output contains expected text.
			if tt.wantContain != "" {
				if got == "" || !contains(got, tt.wantContain) {
					t.Errorf("result %q does not contain %q", got, tt.wantContain)
				}
			}
		})
	}
}

func TestDefaultRouter_NilCaller(t *testing.T) {
	router := &DefaultRouter{Caller: nil}
	_, err := router.Route(context.Background(), extractor.PredicatePurpose, testSym)
	if err == nil {
		t.Fatal("expected error for nil caller")
	}
}

func TestCountCommits(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"empty string", "", 0},
		{"whitespace only", "   \n  \n  ", 0},
		{"single commit", "abc123 fix pod spec", 1},
		{"multiple commits", "abc123 fix pod spec\ndef456 update validation\nghi789 refactor", 3},
		{"trailing newline", "abc123\ndef456\n", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countCommits(tt.input)
			if got != tt.want {
				t.Errorf("countCommits() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFormatStabilityAssessment(t *testing.T) {
	tests := []struct {
		name        string
		commitCount int
		wantLevel   string
	}{
		{"zero commits", 0, "High stability"},
		{"few commits", 5, "High stability"},
		{"moderate commits", 15, "Moderate stability"},
		{"many commits", 45, "Low stability"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStabilityAssessment("Pod", tt.commitCount)
			if !contains(got, tt.wantLevel) {
				t.Errorf("formatStabilityAssessment() = %q, want level %q", got, tt.wantLevel)
			}
			if !contains(got, fmt.Sprintf("%d commits", tt.commitCount)) {
				t.Errorf("formatStabilityAssessment() = %q, missing commit count", got)
			}
		})
	}
}

func TestRouteToolArgs(t *testing.T) {
	t.Run("purpose passes question to deepsearch", func(t *testing.T) {
		mock := newMockCaller(map[string]string{"deepsearch": "result"}, nil)
		router := NewDefaultRouter(mock)

		_, err := router.Route(context.Background(), extractor.PredicatePurpose, testSym)
		if err != nil {
			t.Fatal(err)
		}

		if len(mock.callLog) != 1 {
			t.Fatalf("expected 1 call, got %d", len(mock.callLog))
		}
		args := mock.callLog[0].Args
		question, ok := args["question"].(string)
		if !ok || question == "" {
			t.Error("expected non-empty question arg for deepsearch")
		}
		if !contains(question, "Pod") {
			t.Errorf("question %q should mention symbol name", question)
		}
	})

	t.Run("usage_pattern passes symbol and repo", func(t *testing.T) {
		mock := newMockCaller(map[string]string{"find_references": "result"}, nil)
		router := NewDefaultRouter(mock)

		_, err := router.Route(context.Background(), extractor.PredicateUsagePattern, testSym)
		if err != nil {
			t.Fatal(err)
		}

		args := mock.callLog[0].Args
		if args["symbol"] != "Pod" {
			t.Errorf("symbol = %v, want Pod", args["symbol"])
		}
		if args["repo"] != "kubernetes/kubernetes" {
			t.Errorf("repo = %v, want kubernetes/kubernetes", args["repo"])
		}
	})

	t.Run("stability passes repos and contentTerms to commit_search", func(t *testing.T) {
		mock := newMockCaller(map[string]string{"commit_search": ""}, nil)
		router := NewDefaultRouter(mock)

		_, err := router.Route(context.Background(), extractor.PredicateStability, testSym)
		if err != nil {
			t.Fatal(err)
		}

		args := mock.callLog[0].Args
		repos, ok := args["repos"].([]string)
		if !ok || len(repos) == 0 || repos[0] != "kubernetes/kubernetes" {
			t.Errorf("repos = %v, want [kubernetes/kubernetes]", args["repos"])
		}
		terms, ok := args["contentTerms"].([]string)
		if !ok || len(terms) == 0 || terms[0] != "Pod" {
			t.Errorf("contentTerms = %v, want [Pod]", args["contentTerms"])
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
