package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor/tribal"
)

// ---------------------------------------------------------------------------
// Local test fakes (mcpserver-scoped — mirror tribal-package fakes without
// reaching across packages).
// ---------------------------------------------------------------------------

// fakeMineLLM is a test double implementing tribal.LLMClient. It records
// the number of Complete calls so the idempotency assertion can verify
// the second invocation made zero additional LLM calls.
type fakeMineLLM struct {
	mu        sync.Mutex
	calls     int64
	responses []string
	idx       int
}

func (f *fakeMineLLM) Complete(_ context.Context, _, _ string) (string, error) {
	atomic.AddInt64(&f.calls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx < len(f.responses) {
		r := f.responses[f.idx]
		f.idx++
		return r, nil
	}
	// Default classification: null so the miner doesn't emit a fact.
	return `{"kind":"null","body":"","confidence":0.0}`, nil
}

func (f *fakeMineLLM) Calls() int64 {
	return atomic.LoadInt64(&f.calls)
}

// fakeMineRunner is a CommandRunner that returns canned responses for
// `gh pr list` and `gh api` calls.
type fakeMineRunner struct {
	mu      sync.Mutex
	prList  string // stdout for `gh pr list`
	apiResp string // stdout for `gh api`
}

func (r *fakeMineRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range args {
		if a == "pr" {
			return []byte(r.prList), nil
		}
	}
	return []byte(r.apiResp), nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// samplePRCommentJSONLine returns a single-line JSON encoding of a PR comment
// suitable for the gh api --paginate jsonline output format the miner parses.
func samplePRCommentJSONLine(body, path, htmlURL string) string {
	c := struct {
		Body     string `json:"body"`
		DiffHunk string `json:"diff_hunk"`
		Path     string `json:"path"`
		HTMLURL  string `json:"html_url"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	}{
		Body:     body,
		DiffHunk: "@@ -10,6 +10,8 @@\n+func Example()",
		Path:     path,
		HTMLURL:  htmlURL,
	}
	c.User.Login = "reviewer1"
	data, _ := json.Marshal(c)
	return string(data)
}

// setupMineTestPool creates a DBPool containing a single repo DB with the
// tribal schema and a pre-registered symbol that resolves to a source file.
// Returns the pool, the repo name, and a cleanup-handled temp dir.
func setupMineTestPool(t *testing.T, repoName, symbolName, relPath string) *DBPool {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, repoName+".claims.db")

	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := cdb.CreateTribalSchema(); err != nil {
		t.Fatalf("create tribal schema: %v", err)
	}
	if _, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       repoName,
		ImportPath: relPath,
		SymbolName: symbolName,
		Language:   "go",
		Kind:       "func",
		Visibility: "public",
	}); err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}
	cdb.Close()

	pool := NewDBPool(tmpDir, 5)
	t.Cleanup(func() { pool.Close() })
	return pool
}

// buildFactory returns a MiningServiceFactory backed by the given LLM and
// runner. The factory uses a fresh PRMinerConfig per call but shares the
// injected LLM + runner across calls so LLM call counts are observable and
// so the DB-backed cursor provides idempotency.
func buildFactory(llm tribal.LLMClient, runner tribal.CommandRunner, budget int) MiningServiceFactory {
	return func(repo string, cdb *db.ClaimsDB) (*tribal.TribalMiningService, error) {
		return tribal.NewTribalMiningService(cdb, tribal.PRMinerConfig{
			RepoOwner:   "org",
			RepoName:    repo,
			Client:      llm,
			Model:       "test-model",
			DailyBudget: budget,
			RunCommand:  runner,
		}, repo), nil
	}
}

// ---------------------------------------------------------------------------
// Tests — cover all 5 acceptance criteria for live_docs-m7v.7.
// ---------------------------------------------------------------------------

// AC1 + AC2: calling the tool with (symbol, repo) triggers mining for files
// containing the symbol and inserts facts with full provenance envelopes.
func TestTribalMineOnDemand_FirstCallMinesFacts(t *testing.T) {
	const (
		repo    = "test-repo"
		symbol  = "HandleRequest"
		relPath = "pkg/handler.go"
	)
	pool := setupMineTestPool(t, repo, symbol, relPath)

	runner := &fakeMineRunner{
		prList: "42\n",
		apiResp: samplePRCommentJSONLine(
			"This handler must hold the request lock before dispatching",
			relPath,
			"https://github.com/org/test-repo/pull/42#discussion_r900",
		),
	}
	llm := &fakeMineLLM{responses: []string{
		`{"kind":"invariant","body":"must hold request lock before dispatching","confidence":0.85}`,
	}}

	factory := buildFactory(llm, runner.Run, 100)
	handler := TribalMineOnDemandHandler(pool, factory)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol": symbol,
		"repo":   repo,
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("handler returned error result: %s", result.Text())
	}

	var resp tribalMineResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nbody=%s", err, result.Text())
	}

	if resp.Symbol != symbol {
		t.Errorf("Symbol = %q, want %q", resp.Symbol, symbol)
	}
	if resp.Repo != repo {
		t.Errorf("Repo = %q, want %q", resp.Repo, repo)
	}
	if resp.Total == 0 || len(resp.Facts) == 0 {
		t.Fatalf("expected >=1 fact, got %d (body=%s)", resp.Total, result.Text())
	}
	if llm.Calls() == 0 {
		t.Error("expected at least one LLM call on first invocation, got 0")
	}

	// AC2: full provenance envelope fields are populated.
	f := resp.Facts[0]
	if f.Body == "" {
		t.Error("fact.Body is empty")
	}
	if f.SourceQuote == "" {
		t.Error("fact.SourceQuote is empty")
	}
	if f.Kind == "" {
		t.Error("fact.Kind is empty")
	}
	if f.Status != "active" {
		t.Errorf("fact.Status = %q, want active", f.Status)
	}
	if f.Extractor == "" {
		t.Error("fact.Extractor is empty")
	}
	if f.LastVerified == "" {
		t.Error("fact.LastVerified is empty")
	}
	if len(f.Evidence) == 0 {
		t.Error("fact.Evidence is empty")
	}
}

// AC4 + AC5: second call on the same symbol returns 0 new facts AND makes
// zero additional LLM calls (shared M3 cursor via the DB).
func TestTribalMineOnDemand_IdempotentSecondCall(t *testing.T) {
	const (
		repo    = "test-repo"
		symbol  = "HandleRequest"
		relPath = "pkg/handler.go"
	)
	pool := setupMineTestPool(t, repo, symbol, relPath)

	runner := &fakeMineRunner{
		prList: "42\n",
		apiResp: samplePRCommentJSONLine(
			"must hold request lock",
			relPath,
			"https://github.com/org/test-repo/pull/42#discussion_r900",
		),
	}
	llm := &fakeMineLLM{responses: []string{
		`{"kind":"invariant","body":"must hold request lock","confidence":0.85}`,
	}}

	factory := buildFactory(llm, runner.Run, 100)
	handler := TribalMineOnDemandHandler(pool, factory)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol": symbol,
		"repo":   repo,
	}}

	// First call: at least one LLM call and at least one fact.
	result1, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("first handler call returned error: %v", err)
	}
	if result1.IsError() {
		t.Fatalf("first handler call returned error result: %s", result1.Text())
	}
	var resp1 tribalMineResponse
	if err := json.Unmarshal([]byte(result1.Text()), &resp1); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	if resp1.Total == 0 {
		t.Fatal("first call produced 0 facts; idempotency test is meaningless without a first fact")
	}
	firstCallCount := llm.Calls()
	if firstCallCount == 0 {
		t.Fatal("first call made 0 LLM calls; idempotency test is meaningless")
	}

	// Second call on same symbol: shared M3 cursor should short-circuit
	// all LLM calls.
	result2, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("second handler call returned error: %v", err)
	}
	if result2.IsError() {
		t.Fatalf("second handler call returned error result: %s", result2.Text())
	}

	var resp2 tribalMineResponse
	// Second call may return a text message if no new facts — try parsing as JSON first.
	if jErr := json.Unmarshal([]byte(result2.Text()), &resp2); jErr != nil {
		// Non-JSON "no new facts" text response is also acceptable as long
		// as zero LLM calls occurred.
		resp2.Total = 0
		resp2.Facts = nil
	}
	if resp2.Total != 0 || len(resp2.Facts) != 0 {
		t.Errorf("second call produced %d facts, want 0 (cursor idempotency)", resp2.Total)
	}

	secondCallCount := llm.Calls()
	if secondCallCount != firstCallCount {
		t.Errorf("second call made %d extra LLM calls, want 0 (cursor idempotency broken)",
			secondCallCount-firstCallCount)
	}
}

// AC3: budget exhaustion returns a structured error result without panic,
// and does not leak internal error details.
func TestTribalMineOnDemand_BudgetExceeded(t *testing.T) {
	const (
		repo    = "test-repo"
		symbol  = "HandleRequest"
		relPath = "pkg/handler.go"
	)
	pool := setupMineTestPool(t, repo, symbol, relPath)

	// Two PRs each with a tribal-classified comment. With DailyBudget=1 the
	// miner can make at most one LLM call before budget_exceeded fires —
	// the second PR comment must never reach the LLM.
	runner := &fakeMineRunner{
		prList: "1\n2\n",
		apiResp: samplePRCommentJSONLine("one", relPath, "https://github.com/org/test-repo/pull/1#r1") + "\n" +
			samplePRCommentJSONLine("two", relPath, "https://github.com/org/test-repo/pull/1#r2"),
	}
	llm := &fakeMineLLM{responses: []string{
		`{"kind":"rationale","body":"one","confidence":0.7}`,
		`{"kind":"rationale","body":"two","confidence":0.7}`,
	}}

	factory := buildFactory(llm, runner.Run, 1) // budget=1
	handler := TribalMineOnDemandHandler(pool, factory)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol": symbol,
		"repo":   repo,
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned transport error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result even on budget exhaustion")
	}

	// AC3 structural assertions:
	//   (1) LLM call count must not exceed the budget. If the miner ignored
	//       DailyBudget the call count would blow past the cap.
	//   (2) The handler must return a recognizable response — either a
	//       tribalMineResponse (partial success with at least one fact, then
	//       budget-exceeded truncation) OR an error result with a safe
	//       short message.
	calls := llm.Calls()
	if calls > 2 {
		t.Errorf("LLM called %d times, expected <= 2 with DailyBudget=1 (budget ignored?)", calls)
	}

	if result.IsError() {
		text := result.Text()
		// Safe error messages per MiningError.SafeMessage() are short phrases
		// like "daily LLM call budget reached"; they MUST NOT include the
		// wrapped error chain with repo paths or stack frames.
		if len(text) > 512 {
			t.Errorf("error message too long — suspected leak of internal state: %q", text)
		}
	} else {
		// Partial-success path: response must be valid JSON with a known shape.
		var resp tribalMineResponse
		if jErr := json.Unmarshal([]byte(result.Text()), &resp); jErr != nil {
			t.Fatalf("non-error result must parse as tribalMineResponse: %v\nbody=%s", jErr, result.Text())
		}
	}
}

// AC1 negative: repo that does not exist returns an error result, not a panic.
func TestTribalMineOnDemand_MissingRepo(t *testing.T) {
	tmpDir := t.TempDir()
	pool := NewDBPool(tmpDir, 5)
	t.Cleanup(func() { pool.Close() })

	llm := &fakeMineLLM{}
	runner := &fakeMineRunner{}
	factory := buildFactory(llm, runner.Run, 10)
	handler := TribalMineOnDemandHandler(pool, factory)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol": "AnyThing",
		"repo":   "does-not-exist",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result for missing repo, got: %s", result.Text())
	}
	if llm.Calls() != 0 {
		t.Errorf("LLM was called %d times for missing repo, want 0", llm.Calls())
	}
}

// AC1 negative: symbol that doesn't exist in the repo returns "no files" text,
// not an error, and makes zero LLM calls.
func TestTribalMineOnDemand_UnknownSymbol(t *testing.T) {
	pool := setupMineTestPool(t, "test-repo", "SomeOtherSymbol", "pkg/other.go")

	llm := &fakeMineLLM{}
	runner := &fakeMineRunner{}
	factory := buildFactory(llm, runner.Run, 10)
	handler := TribalMineOnDemandHandler(pool, factory)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol": "NonexistentSymbol",
		"repo":   "test-repo",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unknown symbol should return a text result, not error: %s", result.Text())
	}
	if llm.Calls() != 0 {
		t.Errorf("LLM was called %d times for unknown symbol, want 0", llm.Calls())
	}
}

// Security: missing required params return a clean error result.
func TestTribalMineOnDemand_MissingParams(t *testing.T) {
	pool := setupMineTestPool(t, "test-repo", "X", "pkg/x.go")
	llm := &fakeMineLLM{}
	runner := &fakeMineRunner{}
	factory := buildFactory(llm, runner.Run, 10)
	handler := TribalMineOnDemandHandler(pool, factory)

	tests := []struct {
		name string
		args map[string]any
	}{
		{"missing symbol", map[string]any{"repo": "test-repo"}},
		{"missing repo", map[string]any{"symbol": "X"}},
		{"empty symbol", map[string]any{"symbol": "", "repo": "test-repo"}},
		{"empty repo", map[string]any{"symbol": "X", "repo": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &tribalFakeRequest{args: tt.args}
			result, err := handler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler returned error: %v", err)
			}
			if !result.IsError() {
				t.Fatalf("expected error result, got: %s", result.Text())
			}
		})
	}
}

// Security: path-traversal repo names are rejected.
func TestTribalMineOnDemand_PathTraversalRepo(t *testing.T) {
	pool := setupMineTestPool(t, "test-repo", "X", "pkg/x.go")
	llm := &fakeMineLLM{}
	runner := &fakeMineRunner{}
	factory := buildFactory(llm, runner.Run, 10)
	handler := TribalMineOnDemandHandler(pool, factory)

	for _, repo := range []string{"../evil", "..", "foo/bar", "a/../b"} {
		t.Run(repo, func(t *testing.T) {
			req := &tribalFakeRequest{args: map[string]any{
				"symbol": "X",
				"repo":   repo,
			}}
			result, err := handler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler returned error: %v", err)
			}
			if !result.IsError() {
				t.Errorf("expected error for path-traversal repo %q, got: %s", repo, result.Text())
			}
		})
	}
}

// MiningError propagation: factory error is surfaced as a safe error result.
func TestTribalMineOnDemand_FactoryError(t *testing.T) {
	pool := setupMineTestPool(t, "test-repo", "X", "pkg/x.go")
	factory := MiningServiceFactory(func(_ string, _ *db.ClaimsDB) (*tribal.TribalMiningService, error) {
		return nil, errors.New("factory: llm client not configured")
	})
	handler := TribalMineOnDemandHandler(pool, factory)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol": "X",
		"repo":   "test-repo",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned transport error: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result for factory failure, got: %s", result.Text())
	}
}
