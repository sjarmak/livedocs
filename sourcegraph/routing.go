// Package sourcegraph provides routing logic that maps semantic predicates to
// the optimal Sourcegraph MCP tool for gathering context. Each route returns
// context text that feeds into the semantic.Generator LLM pipeline.
package sourcegraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/live-docs/live_docs/extractor"
)

// MCPCaller abstracts the MCP client's CallTool method so tests can use mocks.
type MCPCaller interface {
	CallTool(ctx context.Context, toolName string, args map[string]any) (string, error)
}

// SymbolContext holds the identifying information for a symbol being routed.
type SymbolContext struct {
	Name       string // e.g. "Pod"
	Repo       string // e.g. "kubernetes/kubernetes"
	ImportPath string // e.g. "k8s.io/api/core/v1"
}

// PredicateRouter routes a semantic predicate to the appropriate Sourcegraph
// tool and returns context text for LLM consumption.
type PredicateRouter interface {
	// Route dispatches the given predicate to the optimal Sourcegraph tool
	// and returns gathered context text.
	Route(ctx context.Context, predicate extractor.Predicate, sym SymbolContext) (string, error)
}

// DefaultRouter implements PredicateRouter using the standard routing table:
//
//	purpose      -> deepsearch
//	usage_pattern -> find_references
//	complexity   -> deepsearch
//	stability    -> commit_search (no LLM needed)
type DefaultRouter struct {
	Caller MCPCaller
}

// NewDefaultRouter creates a DefaultRouter with the given MCPCaller.
func NewDefaultRouter(caller MCPCaller) *DefaultRouter {
	return &DefaultRouter{Caller: caller}
}

// Route dispatches the predicate to the appropriate Sourcegraph tool.
func (r *DefaultRouter) Route(ctx context.Context, predicate extractor.Predicate, sym SymbolContext) (string, error) {
	if r.Caller == nil {
		return "", fmt.Errorf("sourcegraph: MCPCaller is nil")
	}

	switch predicate {
	case extractor.PredicatePurpose:
		return r.routePurpose(ctx, sym)
	case extractor.PredicateUsagePattern:
		return r.routeUsagePattern(ctx, sym)
	case extractor.PredicateComplexity:
		return r.routeComplexity(ctx, sym)
	case extractor.PredicateStability:
		return r.routeStability(ctx, sym)
	default:
		return "", fmt.Errorf("sourcegraph: unsupported predicate %q", predicate)
	}
}

// routePurpose calls deepsearch with a purpose-focused query about the symbol.
func (r *DefaultRouter) routePurpose(ctx context.Context, sym SymbolContext) (string, error) {
	query := fmt.Sprintf("What is the purpose and responsibility of %s in %s (%s)?",
		sym.Name, sym.ImportPath, sym.Repo)

	result, err := r.Caller.CallTool(ctx, "deepsearch", map[string]any{
		"query": query,
	})
	if err != nil {
		return "", fmt.Errorf("sourcegraph: deepsearch for purpose of %s: %w", sym.Name, err)
	}

	return result, nil
}

// routeUsagePattern calls find_references to collect usage sites as context
// text for LLM synthesis.
func (r *DefaultRouter) routeUsagePattern(ctx context.Context, sym SymbolContext) (string, error) {
	result, err := r.Caller.CallTool(ctx, "find_references", map[string]any{
		"symbolDescriptor": sym.Name,
		"repo":             sym.Repo,
	})
	if err != nil {
		return "", fmt.Errorf("sourcegraph: find_references for usage_pattern of %s: %w", sym.Name, err)
	}

	return result, nil
}

// routeComplexity calls deepsearch with a complexity-focused query.
func (r *DefaultRouter) routeComplexity(ctx context.Context, sym SymbolContext) (string, error) {
	query := fmt.Sprintf("Analyze the complexity of %s in %s (%s): cyclomatic complexity, dependencies, abstraction layers",
		sym.Name, sym.ImportPath, sym.Repo)

	result, err := r.Caller.CallTool(ctx, "deepsearch", map[string]any{
		"query": query,
	})
	if err != nil {
		return "", fmt.Errorf("sourcegraph: deepsearch for complexity of %s: %w", sym.Name, err)
	}

	return result, nil
}

// routeStability calls commit_search and computes a stability assessment
// directly from commit count. No LLM needed.
func (r *DefaultRouter) routeStability(ctx context.Context, sym SymbolContext) (string, error) {
	query := fmt.Sprintf("%s type:commit after:\"6 months ago\"", sym.Name)

	result, err := r.Caller.CallTool(ctx, "commit_search", map[string]any{
		"query": query,
		"repo":  sym.Repo,
	})
	if err != nil {
		return "", fmt.Errorf("sourcegraph: commit_search for stability of %s: %w", sym.Name, err)
	}

	commitCount := countCommits(result)
	return formatStabilityAssessment(sym.Name, commitCount), nil
}

// countCommits parses commit_search output to count the number of commits.
// It counts non-empty lines as individual commit entries.
func countCommits(searchResult string) int {
	if strings.TrimSpace(searchResult) == "" {
		return 0
	}

	count := 0
	for _, line := range strings.Split(searchResult, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// formatStabilityAssessment produces a human-readable stability assessment
// based on commit frequency in the last 6 months.
func formatStabilityAssessment(symbolName string, commitCount int) string {
	var level string
	switch {
	case commitCount <= 5:
		level = "High stability"
	case commitCount <= 20:
		level = "Moderate stability"
	default:
		level = "Low stability"
	}

	return fmt.Sprintf("%s: %d commits in 6 months for %s", level, commitCount, symbolName)
}
