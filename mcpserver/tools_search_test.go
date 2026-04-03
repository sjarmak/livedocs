package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/live-docs/live_docs/db"
)

// testToolRequest implements ToolRequest for testing.
type testToolRequest struct {
	args map[string]any
}

func (r *testToolRequest) GetString(key, defaultValue string) string {
	if v, ok := r.args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return defaultValue
}

func (r *testToolRequest) RequireString(key string) (string, error) {
	if v, ok := r.args[key]; ok {
		if s, ok := v.(string); ok {
			return s, nil
		}
	}
	return "", &missingParamError{key: key}
}

func (r *testToolRequest) GetInt(key string, defaultValue int) int {
	if v, ok := r.args[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case float64:
			return int(n)
		}
	}
	return defaultValue
}

func (r *testToolRequest) RequireInt(key string) (int, error) {
	return r.GetInt(key, 0), nil
}

func (r *testToolRequest) GetFloat(key string, defaultValue float64) float64 {
	if v, ok := r.args[key]; ok {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return defaultValue
}

func (r *testToolRequest) RequireFloat(key string) (float64, error) {
	return r.GetFloat(key, 0), nil
}

func (r *testToolRequest) GetBool(key string, defaultValue bool) bool {
	if v, ok := r.args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return defaultValue
}

func (r *testToolRequest) RequireBool(key string) (bool, error) {
	return r.GetBool(key, false), nil
}

func (r *testToolRequest) GetArguments() map[string]any {
	return r.args
}

// missingParamError implements error for missing required params.
type missingParamError struct {
	key string
}

func (e *missingParamError) Error() string {
	return "missing required parameter: " + e.key
}

// setupSearchTestEnv creates a temp directory with multiple repo DBs populated
// with symbols, and returns a pool and routing index.
func setupSearchTestEnv(t *testing.T) (*DBPool, *RoutingIndex) {
	t.Helper()
	dir := t.TempDir()

	createTestDBWithSymbols(t, dir, "api", []db.Symbol{
		{Repo: "api", ImportPath: "k8s.io/api/core/v1", SymbolName: "NewPod", Language: "go", Kind: "function", Visibility: "public"},
		{Repo: "api", ImportPath: "k8s.io/api/core/v1", SymbolName: "NewService", Language: "go", Kind: "function", Visibility: "public"},
		{Repo: "api", ImportPath: "k8s.io/api/apps/v1", SymbolName: "Deployment", Language: "go", Kind: "type", Visibility: "public"},
	})
	createTestDBWithSymbols(t, dir, "client-go", []db.Symbol{
		{Repo: "client-go", ImportPath: "k8s.io/client-go/kubernetes", SymbolName: "NewForConfig", Language: "go", Kind: "function", Visibility: "public"},
		{Repo: "client-go", ImportPath: "k8s.io/client-go/rest", SymbolName: "NewRequest", Language: "go", Kind: "function", Visibility: "public"},
	})
	createTestDBWithSymbols(t, dir, "apimachinery", []db.Symbol{
		{Repo: "apimachinery", ImportPath: "k8s.io/apimachinery/pkg/runtime", SymbolName: "NewScheme", Language: "go", Kind: "function", Visibility: "public"},
	})

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	t.Cleanup(func() { pool.Close() })

	index := NewRoutingIndex()
	if err := index.Build(pool); err != nil {
		t.Fatalf("Build routing index: %v", err)
	}

	return pool, index
}

func TestSearchSymbols_SingleRepo(t *testing.T) {
	pool, index := setupSearchTestEnv(t)

	handler := SearchSymbolsHandler(pool, index)
	req := &testToolRequest{args: map[string]any{
		"query": "New%",
		"repo":  "api",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("handler returned error result: %s", result.Text())
	}

	var resp searchSymbolsResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.TotalCount != 2 {
		t.Errorf("TotalCount = %d, want 2", resp.TotalCount)
	}
	for _, m := range resp.Results {
		if m.Repo != "api" {
			t.Errorf("result repo = %q, want 'api'", m.Repo)
		}
	}
}

func TestSearchSymbols_MultiRepo(t *testing.T) {
	pool, index := setupSearchTestEnv(t)

	handler := SearchSymbolsHandler(pool, index)
	// "New%" should match across all repos via wildcard fallback.
	req := &testToolRequest{args: map[string]any{
		"query": "New%",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("handler returned error result: %s", result.Text())
	}

	var resp searchSymbolsResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	// Should find NewPod, NewService, NewForConfig, NewRequest, NewScheme = 5.
	if resp.TotalCount != 5 {
		t.Errorf("TotalCount = %d, want 5", resp.TotalCount)
	}
	if len(resp.Results) != 5 {
		t.Errorf("len(Results) = %d, want 5", len(resp.Results))
	}
}

func TestSearchSymbols_ExactMatch(t *testing.T) {
	pool, index := setupSearchTestEnv(t)

	handler := SearchSymbolsHandler(pool, index)
	// Exact match for "NewPod" should route only to "api" via prefix "new".
	req := &testToolRequest{args: map[string]any{
		"query": "NewPod",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	var resp searchSymbolsResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.TotalCount != 1 {
		t.Errorf("TotalCount = %d, want 1", resp.TotalCount)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].SymbolName != "NewPod" {
		t.Errorf("Results[0].SymbolName = %q, want 'NewPod'", resp.Results[0].SymbolName)
	}
	if resp.Results[0].Repo != "api" {
		t.Errorf("Results[0].Repo = %q, want 'api'", resp.Results[0].Repo)
	}
}

func TestSearchSymbols_ResultFields(t *testing.T) {
	pool, index := setupSearchTestEnv(t)

	handler := SearchSymbolsHandler(pool, index)
	req := &testToolRequest{args: map[string]any{
		"query": "Deployment",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	var resp searchSymbolsResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(resp.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(resp.Results))
	}

	m := resp.Results[0]
	if m.Repo != "api" {
		t.Errorf("Repo = %q, want 'api'", m.Repo)
	}
	if m.ImportPath != "k8s.io/api/apps/v1" {
		t.Errorf("ImportPath = %q, want 'k8s.io/api/apps/v1'", m.ImportPath)
	}
	if m.SymbolName != "Deployment" {
		t.Errorf("SymbolName = %q, want 'Deployment'", m.SymbolName)
	}
	if m.Kind != "type" {
		t.Errorf("Kind = %q, want 'type'", m.Kind)
	}
	if m.Visibility != "public" {
		t.Errorf("Visibility = %q, want 'public'", m.Visibility)
	}
}

func TestSearchSymbols_NoResults(t *testing.T) {
	pool, index := setupSearchTestEnv(t)

	handler := SearchSymbolsHandler(pool, index)
	req := &testToolRequest{args: map[string]any{
		"query": "ZZZDoesNotExist",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	var resp searchSymbolsResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.TotalCount != 0 {
		t.Errorf("TotalCount = %d, want 0", resp.TotalCount)
	}
	if len(resp.Results) != 0 {
		t.Errorf("len(Results) = %d, want 0", len(resp.Results))
	}
}

func TestSearchSymbols_MissingQuery(t *testing.T) {
	pool, index := setupSearchTestEnv(t)

	handler := SearchSymbolsHandler(pool, index)
	req := &testToolRequest{args: map[string]any{}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if !result.IsError() {
		t.Errorf("expected error result for missing query, got: %s", result.Text())
	}
}

func TestSearchSymbols_CapAt50(t *testing.T) {
	dir := t.TempDir()

	// Create a repo with 60 symbols all matching "Sym%".
	symbols := make([]db.Symbol, 60)
	for i := range symbols {
		symbols[i] = db.Symbol{
			Repo:       "big-repo",
			ImportPath: "pkg/big",
			SymbolName: "Sym" + string(rune('A'+i/26)) + string(rune('a'+i%26)),
			Language:   "go",
			Kind:       "function",
			Visibility: "public",
		}
	}
	createTestDBWithSymbols(t, dir, "big-repo", symbols)

	pool := NewDBPool(dir, DefaultMaxOpenDBs)
	defer pool.Close()

	index := NewRoutingIndex()
	if err := index.Build(pool); err != nil {
		t.Fatalf("Build routing index: %v", err)
	}

	handler := SearchSymbolsHandler(pool, index)
	req := &testToolRequest{args: map[string]any{
		"query": "Sym%",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	var resp searchSymbolsResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.TotalCount != 60 {
		t.Errorf("TotalCount = %d, want 60", resp.TotalCount)
	}
	if len(resp.Results) != 50 {
		t.Errorf("len(Results) = %d, want 50 (capped)", len(resp.Results))
	}
}
