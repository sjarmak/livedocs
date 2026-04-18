package tribal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sjarmak/livedocs/db"
)

// newTestClaimsDB creates an in-memory claims DB with both core and tribal schemas.
func newTestClaimsDB(t *testing.T) *db.ClaimsDB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claims.db")
	cdb, err := db.OpenClaimsDB(path)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := cdb.CreateTribalSchema(); err != nil {
		t.Fatalf("create tribal schema: %v", err)
	}
	return cdb
}

// mockRunnerRecording records all gh command calls and returns canned responses.
type mockRunnerRecording struct {
	calls   [][]string
	prList  string // response for `gh pr list`
	apiResp string // response for `gh api`
	prErr   error
	apiErr  error
}

func (m *mockRunnerRecording) run(_ context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, append([]string{name}, args...))
	for _, a := range args {
		if a == "pr" {
			if m.prErr != nil {
				return nil, m.prErr
			}
			return []byte(m.prList), nil
		}
	}
	if m.apiErr != nil {
		return nil, m.apiErr
	}
	return []byte(m.apiResp), nil
}

func TestTribalMiningService_MineFile_Basic(t *testing.T) {
	cdb := newTestClaimsDB(t)
	comment := PRComment{
		Body:     "This function must hold the mutex before calling",
		DiffHunk: "@@ -10,6 +10,8 @@\n+func doWork()",
		Path:     "pkg/worker.go",
		HTMLURL:  "https://github.com/org/repo/pull/42#discussion_r100",
		User:     prUser{Login: "reviewer1"},
	}
	commentJSON, _ := json.Marshal(comment)

	runner := &mockRunnerRecording{
		prList:  "42\n",
		apiResp: string(commentJSON),
	}

	llm := &mockLLMClient{
		responses: []string{`{"kind":"invariant","body":"must hold mutex before calling","confidence":0.85}`},
	}

	miner := &prCommentMiner{
		RepoOwner:   "org",
		RepoName:    "repo",
		Client:      llm,
		Model:       "test-model",
		DailyBudget: 100,
		RunCommand:  runner.run,
	}

	svc := newServiceWithMiner(cdb, miner, "repo")

	result, err := svc.MineFile(context.Background(), "pkg/worker.go", TriggerBatchSchedule)
	if err != nil {
		t.Fatalf("MineFile: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Facts) != 1 {
		t.Fatalf("got %d facts, want 1", len(result.Facts))
	}
	if result.Facts[0].Kind != "invariant" {
		t.Errorf("kind = %q, want invariant", result.Facts[0].Kind)
	}
	if result.Trigger != TriggerBatchSchedule {
		t.Errorf("trigger = %q, want %q", result.Trigger, TriggerBatchSchedule)
	}
	if result.Path != "pkg/worker.go" {
		t.Errorf("path = %q, want pkg/worker.go", result.Path)
	}

	// Generation counter should have bumped.
	if g := svc.FactsGeneration(); g != 1 {
		t.Errorf("generation = %d, want 1", g)
	}
}

func TestTribalMiningService_MineFile_BudgetExceeded(t *testing.T) {
	cdb := newTestClaimsDB(t)
	comment := PRComment{
		Body:     "needs review",
		DiffHunk: "@@",
		Path:     "pkg/x.go",
		HTMLURL:  "https://github.com/org/repo/pull/1#r1",
		User:     prUser{Login: "r"},
	}
	commentJSON, _ := json.Marshal(comment)

	runner := &mockRunnerRecording{
		prList:  "1\n",
		apiResp: string(commentJSON),
	}
	llm := &mockLLMClient{
		responses: []string{`{"kind":"rationale","body":"test","confidence":0.8}`},
	}
	miner := &prCommentMiner{
		RepoOwner:   "org",
		RepoName:    "repo",
		Client:      llm,
		Model:       "test",
		DailyBudget: 0, // unlimited for comment classification
		RunCommand:  runner.run,
	}

	// Set budget to 0 to trigger exceeded.
	miner.DailyBudget = 1
	miner.mu.Lock()
	miner.callCount = 1 // already at budget
	miner.mu.Unlock()

	svc := newServiceWithMiner(cdb, miner, "repo")
	_, err := svc.MineFile(context.Background(), "pkg/x.go", TriggerJITOnDemand)
	if err == nil {
		t.Fatal("expected error for budget exceeded")
	}
	var me *MiningError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MiningError, got %T", err)
	}
	if me.Code != "budget_exceeded" {
		t.Errorf("code = %q, want budget_exceeded", me.Code)
	}
}

func TestTribalMiningService_MineFile_CursorRegression(t *testing.T) {
	cdb := newTestClaimsDB(t)
	ResetCursorRegressionCount()

	// Set up a cursor with max PR=100, then gh returns only PR=50 (regression).
	runner := &mockRunnerRecording{
		prList: "50\n",
	}
	llm := &mockLLMClient{}
	miner := &prCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		RunCommand: runner.run,
	}

	svc := newServiceWithMiner(cdb, miner, "repo")

	// Seed a cursor with PR 100 already seen.
	_ = cdb.SetPRIDSet("repo", "pkg/x.go", []int{100}, "v1")

	_, err := svc.MineFile(context.Background(), "pkg/x.go", TriggerBatchSchedule)
	if err == nil {
		t.Fatal("expected error for cursor regression")
	}
	var me *MiningError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MiningError, got %T", err)
	}
	if me.Code != "cursor_regression" {
		t.Errorf("code = %q, want cursor_regression", me.Code)
	}
}

func TestTribalMiningService_MineFile_IdempotentSecondRun(t *testing.T) {
	cdb := newTestClaimsDB(t)
	comment := PRComment{
		Body:     "important rationale",
		DiffHunk: "@@",
		Path:     "pkg/a.go",
		HTMLURL:  "https://github.com/org/repo/pull/5#r1",
		User:     prUser{Login: "r"},
	}
	commentJSON, _ := json.Marshal(comment)

	callCount := 0
	runner := CommandRunner(func(_ context.Context, name string, args ...string) ([]byte, error) {
		for _, a := range args {
			if a == "pr" {
				return []byte("5\n"), nil
			}
		}
		callCount++
		return commentJSON, nil
	})

	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"rationale","body":"important","confidence":0.9}`,
			`{"kind":"rationale","body":"important","confidence":0.9}`,
		},
	}
	miner := &prCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "test",
		RunCommand: runner,
	}

	svc := newServiceWithMiner(cdb, miner, "repo")

	// First run: should produce facts.
	r1, err := svc.MineFile(context.Background(), "pkg/a.go", TriggerBatchSchedule)
	if err != nil {
		t.Fatalf("first MineFile: %v", err)
	}
	if r1 == nil || len(r1.Facts) == 0 {
		t.Fatal("first run should produce facts")
	}

	// Second run with same service: PR 5 is in the cursor now, so miner
	// should make zero LLM calls and return zero new facts.
	llmCallsBefore := len(llm.getCalls())
	r2, err := svc.MineFile(context.Background(), "pkg/a.go", TriggerJITOnDemand)
	if err != nil {
		t.Fatalf("second MineFile: %v", err)
	}
	llmCallsAfter := len(llm.getCalls())

	if r2 != nil && len(r2.Facts) > 0 {
		t.Errorf("second run should produce 0 new facts, got %d", len(r2.Facts))
	}
	if llmCallsAfter != llmCallsBefore {
		t.Errorf("second run made %d LLM calls, want 0", llmCallsAfter-llmCallsBefore)
	}
}

func TestTribalMiningService_GenerationCounter(t *testing.T) {
	cdb := newTestClaimsDB(t)
	llm := &mockLLMClient{}
	runner := &mockRunnerRecording{prList: "\n"} // no PRs
	miner := &prCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		RunCommand: runner.run,
	}

	svc := newServiceWithMiner(cdb, miner, "repo")

	if g := svc.FactsGeneration(); g != 0 {
		t.Fatalf("initial generation = %d, want 0", g)
	}

	// Mine a file that produces no facts — generation should NOT bump.
	_, _ = svc.MineFile(context.Background(), "pkg/empty.go", TriggerBatchSchedule)
	if g := svc.FactsGeneration(); g != 0 {
		t.Fatalf("generation after empty mine = %d, want 0", g)
	}
}

func TestTribalMiningService_MineSymbol(t *testing.T) {
	cdb := newTestClaimsDB(t)

	// Create a symbol so resolveSymbolFiles finds a file.
	_, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       "repo",
		ImportPath: "pkg/handler.go",
		SymbolName: "HandleRequest",
		Language:   "go",
		Kind:       "func",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}

	comment := PRComment{
		Body:     "This handler needs rate limiting",
		DiffHunk: "@@",
		Path:     "pkg/handler.go",
		HTMLURL:  "https://github.com/org/repo/pull/10#r1",
		User:     prUser{Login: "reviewer"},
	}
	commentJSON, _ := json.Marshal(comment)

	runner := &mockRunnerRecording{
		prList:  "10\n",
		apiResp: string(commentJSON),
	}
	llm := &mockLLMClient{
		responses: []string{`{"kind":"quirk","body":"needs rate limiting","confidence":0.75}`},
	}
	miner := &prCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "test",
		RunCommand: runner.run,
	}

	svc := newServiceWithMiner(cdb, miner, "repo")

	results, err := svc.MineSymbol(context.Background(), "HandleRequest", TriggerJITOnDemand)
	if err != nil {
		t.Fatalf("MineSymbol: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if len(results[0].Facts) != 1 {
		t.Fatalf("got %d facts, want 1", len(results[0].Facts))
	}
	if results[0].Facts[0].Kind != "quirk" {
		t.Errorf("kind = %q, want quirk", results[0].Facts[0].Kind)
	}
}

func TestIsSourceFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Accepted: real source file paths
		{"cmd/main.go", true},
		{"pkg/handler.go", true},
		{"lib/utils.ts", true},
		{"components/App.tsx", true},
		{"scripts/build.py", true},
		{"scripts/deploy.sh", true},
		{"main.go", true},

		// Rejected: Go import paths with dots (the bug this fixes)
		{"k8s.io/client-go/tools/cache", false},
		{"github.com/org/repo", false},
		{"golang.org/x/tools/go/ast", false},
		{"sigs.k8s.io/controller-runtime", false},

		// Rejected: no extension
		{"pkg/controller/replicaset", false},
		{"cmd/kubelet", false},
		{"", false},

		// Rejected: unsupported extensions
		{"data/config.xml", false},
		{"assets/image.png", false},

		// Edge cases
		{"k8s.io/api/core/v1/types.go", true}, // import path with dots BUT ends in .go
		{".hidden/file.go", true},             // dotfile directory
		{"path/to/file.JS", true},             // case insensitive
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isSourceFile(tt.path)
			if got != tt.want {
				t.Errorf("isSourceFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestResolveSymbolFiles_FiltersNonFilePaths(t *testing.T) {
	cdb := newTestClaimsDB(t)

	// Insert symbols: one with a real file path, one with a Go import path (dot but not a file).
	for _, sym := range []db.Symbol{
		{Repo: "repo", ImportPath: "pkg/handler.go", SymbolName: "HandleRequest", Language: "go", Kind: "func", Visibility: "public"},
		{Repo: "repo", ImportPath: "k8s.io/client-go/tools/cache", SymbolName: "Store", Language: "go", Kind: "interface", Visibility: "public"},
		{Repo: "repo", ImportPath: "pkg/controller", SymbolName: "Run", Language: "go", Kind: "func", Visibility: "public"},
	} {
		if _, err := cdb.UpsertSymbol(sym); err != nil {
			t.Fatalf("upsert symbol: %v", err)
		}
	}

	svc := newServiceWithMiner(cdb, nil, "repo")

	// HandleRequest should resolve to the .go file only.
	paths, err := svc.resolveSymbolFiles("HandleRequest")
	if err != nil {
		t.Fatalf("resolveSymbolFiles: %v", err)
	}
	if len(paths) != 1 || paths[0] != "pkg/handler.go" {
		t.Errorf("got paths %v, want [pkg/handler.go]", paths)
	}

	// Store lives at k8s.io/client-go/tools/cache — should resolve to zero files.
	paths, err = svc.resolveSymbolFiles("Store")
	if err != nil {
		t.Fatalf("resolveSymbolFiles: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("got paths %v for import path, want none", paths)
	}

	// Run lives at pkg/controller (no extension) — should resolve to zero files.
	paths, err = svc.resolveSymbolFiles("Run")
	if err != nil {
		t.Fatalf("resolveSymbolFiles: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("got paths %v for extensionless path, want none", paths)
	}
}

// TestResolveSymbolFiles_WildcardNoFanOut guards against an MCP caller draining
// the daily budget by passing LIKE wildcards (%, _) as a symbol name. Exact
// match must treat them as literal characters, returning zero results rather
// than every symbol in the repo.
func TestResolveSymbolFiles_WildcardNoFanOut(t *testing.T) {
	cdb := newTestClaimsDB(t)

	for _, sym := range []db.Symbol{
		{Repo: "repo", ImportPath: "pkg/a.go", SymbolName: "Alpha", Language: "go", Kind: "func", Visibility: "public"},
		{Repo: "repo", ImportPath: "pkg/b.go", SymbolName: "Beta", Language: "go", Kind: "func", Visibility: "public"},
		{Repo: "repo", ImportPath: "pkg/c.go", SymbolName: "Gamma", Language: "go", Kind: "func", Visibility: "public"},
	} {
		if _, err := cdb.UpsertSymbol(sym); err != nil {
			t.Fatalf("upsert symbol: %v", err)
		}
	}

	svc := newServiceWithMiner(cdb, nil, "repo")

	for _, input := range []string{"%", "_", "%lph%", "Alpha_"} {
		paths, err := svc.resolveSymbolFiles(input)
		if err != nil {
			t.Fatalf("resolveSymbolFiles(%q): %v", input, err)
		}
		if len(paths) != 0 {
			t.Errorf("resolveSymbolFiles(%q) = %v, want 0 paths (wildcards must be literal)", input, paths)
		}
	}
}

// failingUpserter implements factUpserter; UpsertTribalFact always returns err.
// Used to exercise MineFile's partial-failure bookkeeping.
type failingUpserter struct {
	err error
}

func (f *failingUpserter) UpsertTribalFact(
	_ db.TribalFact, _ []db.TribalEvidence,
) (int64, bool, error) {
	return 0, false, f.err
}

// mixedUpserter fails every other call, simulating a flaky writer.
type mixedUpserter struct {
	real  factUpserter
	err   error
	calls int
}

func (m *mixedUpserter) UpsertTribalFact(
	fact db.TribalFact, evidence []db.TribalEvidence,
) (int64, bool, error) {
	m.calls++
	if m.calls%2 == 0 {
		return 0, false, m.err
	}
	return m.real.UpsertTribalFact(fact, evidence)
}

func TestTribalMiningService_MineFile_FailedUpsertsAreSurfaced(t *testing.T) {
	cdb := newTestClaimsDB(t)
	comment := PRComment{
		Body:     "must acquire lock",
		DiffHunk: "@@",
		Path:     "pkg/x.go",
		HTMLURL:  "https://github.com/org/repo/pull/1#r1",
		User:     prUser{Login: "r"},
	}
	commentJSON, _ := json.Marshal(comment)

	runner := &mockRunnerRecording{
		prList:  "1\n",
		apiResp: string(commentJSON),
	}
	llm := &mockLLMClient{
		responses: []string{`{"kind":"invariant","body":"must acquire lock","confidence":0.9}`},
	}
	miner := &prCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "test",
		RunCommand: runner.run,
	}

	svc := newServiceWithMiner(cdb, miner, "repo",
		withFactUpserter(&failingUpserter{err: errors.New("simulated upsert failure")}),
	)

	result, err := svc.MineFile(context.Background(), "pkg/x.go", TriggerBatchSchedule)
	if err != nil {
		t.Fatalf("MineFile should return nil error on partial upsert failure (additive API), got %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result even on all-fail")
	}
	if len(result.Facts) != 0 {
		t.Errorf("Facts = %d, want 0 (upsert failed)", len(result.Facts))
	}
	if result.FailedCount != 1 {
		t.Errorf("FailedCount = %d, want 1", result.FailedCount)
	}
	if len(result.FailedErrors) != 1 {
		t.Fatalf("FailedErrors len = %d, want 1", len(result.FailedErrors))
	}
	if result.FailedErrors[0] == "" {
		t.Error("FailedErrors[0] should not be empty")
	}
	// Generation should NOT bump when no facts were written.
	if g := svc.FactsGeneration(); g != 0 {
		t.Errorf("generation = %d, want 0 (no successful facts)", g)
	}
}

// encodeNDJSON serializes comments as newline-delimited JSON, matching the
// output of `gh api ... -q '.[] | select(...)'` which emits one object per line.
func encodeNDJSON(t *testing.T, comments []PRComment) string {
	t.Helper()
	var out []byte
	for _, c := range comments {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal comment: %v", err)
		}
		out = append(out, b...)
		out = append(out, '\n')
	}
	return string(out)
}

func TestTribalMiningService_MineFile_MixedSuccessAndFailure(t *testing.T) {
	cdb := newTestClaimsDB(t)
	comments := []PRComment{
		{Body: "first rationale", DiffHunk: "@@", Path: "pkg/m.go",
			HTMLURL: "https://github.com/org/repo/pull/1#r1", User: prUser{Login: "r"}},
		{Body: "second rationale", DiffHunk: "@@", Path: "pkg/m.go",
			HTMLURL: "https://github.com/org/repo/pull/1#r2", User: prUser{Login: "r"}},
		{Body: "third rationale", DiffHunk: "@@", Path: "pkg/m.go",
			HTMLURL: "https://github.com/org/repo/pull/1#r3", User: prUser{Login: "r"}},
		{Body: "fourth rationale", DiffHunk: "@@", Path: "pkg/m.go",
			HTMLURL: "https://github.com/org/repo/pull/1#r4", User: prUser{Login: "r"}},
	}

	runner := &mockRunnerRecording{
		prList:  "1\n",
		apiResp: encodeNDJSON(t, comments),
	}
	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"rationale","body":"first rationale body","confidence":0.9}`,
			`{"kind":"rationale","body":"second rationale body","confidence":0.9}`,
			`{"kind":"rationale","body":"third rationale body","confidence":0.9}`,
			`{"kind":"rationale","body":"fourth rationale body","confidence":0.9}`,
		},
	}
	miner := &prCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "test",
		RunCommand: runner.run,
	}

	realUpserter := &claimsDBUpserter{cdb: cdb}
	mixed := &mixedUpserter{
		real: realUpserter,
		err:  errors.New("simulated flaky upsert"),
	}

	svc := newServiceWithMiner(cdb, miner, "repo", withFactUpserter(mixed))

	result, err := svc.MineFile(context.Background(), "pkg/m.go", TriggerBatchSchedule)
	if err != nil {
		t.Fatalf("MineFile: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Four facts: calls 1,3 succeed; calls 2,4 fail.
	if len(result.Facts) != 2 {
		t.Errorf("Facts = %d, want 2 successful", len(result.Facts))
	}
	if result.FailedCount != 2 {
		t.Errorf("FailedCount = %d, want 2 failed", result.FailedCount)
	}
	if len(result.FailedErrors) != 2 {
		t.Errorf("FailedErrors len = %d, want 2", len(result.FailedErrors))
	}
	// Generation bumps exactly once (not per successful fact).
	if g := svc.FactsGeneration(); g != 1 {
		t.Errorf("generation = %d, want 1 (bumps once when any fact written)", g)
	}
}

func TestTribalMiningService_MineFile_FailedErrorsCapped(t *testing.T) {
	cdb := newTestClaimsDB(t)
	// Build enough comments to exceed the retention cap (maxFailedErrorsCaptured = 32).
	const n = maxFailedErrorsCaptured + 5
	comments := make([]PRComment, n)
	responses := make([]string, n)
	for i := 0; i < n; i++ {
		comments[i] = PRComment{
			Body:     fmt.Sprintf("comment %d", i),
			DiffHunk: "@@",
			Path:     "pkg/big.go",
			HTMLURL:  fmt.Sprintf("https://github.com/org/repo/pull/1#r%d", i),
			User:     prUser{Login: "r"},
		}
		responses[i] = fmt.Sprintf(`{"kind":"rationale","body":"body %d","confidence":0.9}`, i)
	}

	runner := &mockRunnerRecording{
		prList:  "1\n",
		apiResp: encodeNDJSON(t, comments),
	}
	llm := &mockLLMClient{responses: responses}
	miner := &prCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "test",
		RunCommand: runner.run,
	}

	svc := newServiceWithMiner(cdb, miner, "repo",
		withFactUpserter(&failingUpserter{err: errors.New("boom")}),
	)

	result, err := svc.MineFile(context.Background(), "pkg/big.go", TriggerBatchSchedule)
	if err != nil {
		t.Fatalf("MineFile: %v", err)
	}
	if result.FailedCount != n {
		t.Errorf("FailedCount = %d, want %d", result.FailedCount, n)
	}
	if len(result.FailedErrors) != maxFailedErrorsCaptured {
		t.Errorf("FailedErrors len = %d, want cap %d",
			len(result.FailedErrors), maxFailedErrorsCaptured)
	}
}

func TestMiningError_Structured(t *testing.T) {
	me := &MiningError{
		Code:    "rate_limited",
		Message: "daily budget reached",
		Err:     ErrBudgetExceeded,
	}

	if !errors.Is(me, ErrBudgetExceeded) {
		t.Error("MiningError should unwrap to ErrBudgetExceeded")
	}
	if me.Code != "rate_limited" {
		t.Errorf("Code = %q, want rate_limited", me.Code)
	}
	s := me.Error()
	if s == "" {
		t.Error("Error() should return non-empty string")
	}
}
