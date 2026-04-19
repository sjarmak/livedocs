package tribal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjarmak/livedocs/db"
)

// waitForSettle spins until the runner's prListArrivals count stops
// changing, indicating the caller set has quiesced (either all N have
// reached the barrier, or singleflight has parked N-1 and only 1 is at
// the barrier). Bounded by a generous deadline so a stuck test still
// terminates; no wall-clock branching inside the assertion path.
func waitForSettle(runner *countingRunner, maxArrivals int) {
	deadline := time.Now().Add(2 * time.Second)
	var last int64 = -1
	stable := 0
	for time.Now().Before(deadline) {
		cur := atomic.LoadInt64(&runner.prListArrivals)
		if cur == last {
			stable++
			if stable >= 50 || cur >= int64(maxArrivals) {
				return
			}
		} else {
			stable = 0
			last = cur
		}
		runtime.Gosched()
	}
}

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
	if me.Code != CodeBudgetExceeded {
		t.Errorf("code = %q, want %s", me.Code, CodeBudgetExceeded)
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
	if me.Code != CodeCursorRegression {
		t.Errorf("code = %q, want %s", me.Code, CodeCursorRegression)
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

// TestMiningErrorCodes_Pinned pins the wire values of every canonical
// MiningError code constant (live_docs-m7v.44). External callers
// (cmd/livedocs/extract_cmd.go, mcpserver tests, scripts/extract-corpus.sh)
// match on the literal strings, so a rename of a constant's value is a
// silent contract break — this table catches it as a test failure.
//
// The table also asserts that constructing a MiningError with the constant
// produces an error whose Code field equals the expected literal, which
// guards against accidental defined-type drift away from the alias contract
// declared in service.go.
func TestMiningErrorCodes_Pinned(t *testing.T) {
	cases := []struct {
		name string
		code MiningErrorCode
		want string
	}{
		{"budget_exceeded", CodeBudgetExceeded, "budget_exceeded"},
		{"cursor_regression", CodeCursorRegression, "cursor_regression"},
		{"symbol_resolution_failed", CodeSymbolResolutionFailed, "symbol_resolution_failed"},
		{"symbol_upsert_failed", CodeSymbolUpsertFailed, "symbol_upsert_failed"},
		{"extraction_failed", CodeExtractionFailed, "extraction_failed"},
		{"mine_throttled", CodeMineThrottled, "mine_throttled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if string(tc.code) != tc.want {
				t.Errorf("constant value = %q, want %q (renaming the wire "+
					"value silently breaks external string comparisons)",
					string(tc.code), tc.want)
			}
			me := &MiningError{Code: tc.code, Message: "x"}
			if me.Code != tc.want {
				t.Errorf("MiningError.Code = %q, want %q", me.Code, tc.want)
			}
			// Alias contract: the constant must be assignable to a raw
			// string and comparable against a string literal without a
			// conversion. If this stops compiling, the alias has been
			// changed to a defined type and external callers that compare
			// `me.Code == "budget_exceeded"` will silently fall through.
			var asString string = tc.code
			if asString != tc.want {
				t.Errorf("alias assignment = %q, want %q", asString, tc.want)
			}
		})
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

// countingRunner records the number of `gh api` calls (the expensive
// extract-work calls) atomically so concurrent tests can assert
// exactly-once semantics without timing flakiness.
//
// If barrier is non-nil, every `gh pr list` call waits on it before
// returning. This is how concurrent tests force all N goroutines to be
// simultaneously inside ExtractForFile (after the cursor load, before any
// SetPRIDSet write) — without the barrier, the serial nature of SQLite
// writes can accidentally order goroutines such that later callers see a
// populated cursor and skip the extract, masking the dedup bug.
type countingRunner struct {
	prList     string
	apiResp    string
	apiCalls   int64 // atomic counter of `gh api` invocations
	totalCalls int64 // atomic counter of all invocations

	// barrier, if non-nil, blocks every `gh pr list` caller until the
	// barrier is closed. Test harnesses close it after all expected
	// goroutines have arrived to guarantee true concurrency.
	barrier chan struct{}

	// ctxAware, if true, makes the barrier wait cancel-aware: the runner
	// returns ctx.Err() when the caller's context is cancelled while
	// parked at the barrier. This models real IO (subprocess, network)
	// which returns a cancellation error rather than blocking forever.
	// Tests that need to exercise cancellation paths through the runner
	// must set this; the default (false) preserves the original
	// "wait-forever" semantics used by existing dedup tests.
	ctxAware bool

	// prListArrived is closed once prListArrivals reaches the expected
	// count, letting the test harness synchronize on "all N goroutines are
	// parked at the barrier". Optional; zero-value disables the signal.
	prListArrivals int64
}

func (r *countingRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	atomic.AddInt64(&r.totalCalls, 1)
	for _, a := range args {
		if a == "pr" {
			atomic.AddInt64(&r.prListArrivals, 1)
			if r.barrier != nil {
				if r.ctxAware {
					select {
					case <-r.barrier:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				} else {
					<-r.barrier
				}
			}
			return []byte(r.prList), nil
		}
	}
	atomic.AddInt64(&r.apiCalls, 1)
	return []byte(r.apiResp), nil
}

// newConcurrentTestService builds a service + miner wired to the counting
// runner for the given path. Responses cover up to N concurrent callers.
func newConcurrentTestService(t *testing.T, path string, responses int) (*TribalMiningService, *countingRunner, *mockLLMClient) {
	t.Helper()
	cdb := newTestClaimsDB(t)

	comment := PRComment{
		Body:     "This function must hold the mutex before calling",
		DiffHunk: "@@",
		Path:     path,
		HTMLURL:  fmt.Sprintf("https://github.com/org/repo/pull/42#discussion_r100_%s", path),
		User:     prUser{Login: "reviewer1"},
	}
	commentJSON, _ := json.Marshal(comment)

	runner := &countingRunner{
		prList:  "42\n",
		apiResp: string(commentJSON),
	}

	llmResponses := make([]string, responses)
	for i := 0; i < responses; i++ {
		llmResponses[i] = `{"kind":"invariant","body":"must hold mutex before calling","confidence":0.85}`
	}
	llm := &mockLLMClient{responses: llmResponses}

	miner := &prCommentMiner{
		RepoOwner:   "org",
		RepoName:    "repo",
		Client:      llm,
		Model:       "test-model",
		DailyBudget: 1_000, // generous: test asserts budget is NOT drained by concurrency
		RunCommand:  runner.run,
	}

	svc := newServiceWithMiner(cdb, miner, "repo")
	return svc, runner, llm
}

// TestTribalMiningService_MineFile_ConcurrentDedup asserts that N concurrent
// MineFile calls for the SAME relPath result in exactly one underlying
// ExtractForFile invocation. Before singleflight was added, each caller
// independently ran the full mine path — N times the cost, N times the
// budget charge. This test is the RED test that locks in the fix.
//
// The runner's barrier channel forces true concurrency: the first caller
// to reach `gh pr list` parks at the barrier. Without singleflight, ALL
// N callers would eventually arrive at the barrier; with singleflight,
// only ONE arrives and the remaining N-1 wait on the shared result. The
// test waits for each goroutine to be in-flight (wgStarted) before
// releasing the barrier, ensuring none has been able to finish early and
// populate the cursor (which would accidentally dedup through the DB).
func TestTribalMiningService_MineFile_ConcurrentDedup(t *testing.T) {
	const N = 10
	svc, runner, llm := newConcurrentTestService(t, "pkg/hot.go", N)
	runner.barrier = make(chan struct{})

	start := make(chan struct{})
	var wgStarted sync.WaitGroup
	var wgDone sync.WaitGroup
	results := make([]*MiningResult, N)
	errs := make([]error, N)

	for i := 0; i < N; i++ {
		wgStarted.Add(1)
		wgDone.Add(1)
		go func(idx int) {
			defer wgDone.Done()
			<-start
			wgStarted.Done()
			r, err := svc.MineFile(context.Background(), "pkg/hot.go", TriggerJITOnDemand)
			results[idx] = r
			errs[idx] = err
		}(i)
	}
	close(start)
	wgStarted.Wait() // all goroutines have entered MineFile (or are about to)

	// Wait for goroutines to settle: either all N arrive at `gh pr list`
	// (no-dedup path, apiCalls will later be N), or just 1 arrives and
	// N-1 park inside singleflight.Do (dedup path, apiCalls will be 1).
	// We spin for a bounded number of Gosched()s — no wall clock, so this
	// is deterministic and race-detector clean.
	waitForSettle(runner, N)

	close(runner.barrier)
	wgDone.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: MineFile: %v", i, err)
		}
	}

	// Core assertion: extraction's gh api (expensive path) ran exactly once.
	// singleflight guarantees only ONE goroutine ran mineFileOnce; all
	// waiters received the shared result. Without singleflight, multiple
	// goroutines would race into gh api before any cursor write landed.
	if got := atomic.LoadInt64(&runner.apiCalls); got != 1 {
		t.Errorf("gh api calls = %d, want 1 (singleflight dedup missing)", got)
	}

	// LLM was called at most once (one comment classified).
	if got := len(llm.getCalls()); got != 1 {
		t.Errorf("LLM calls = %d, want 1", got)
	}

	// prListArrivals is the direct singleflight signal: without dedup
	// every goroutine would arrive at gh pr list; with dedup only one does.
	if got := atomic.LoadInt64(&runner.prListArrivals); got != 1 {
		t.Errorf("gh pr list arrivals = %d, want 1 (singleflight should serialize)", got)
	}

	// Generation counter bumps exactly once per dedup window.
	if g := svc.FactsGeneration(); g != 1 {
		t.Errorf("generation = %d, want 1", g)
	}
}

// TestTribalMiningService_MineFile_BudgetChargedOnce asserts that the
// DailyBudget is decremented exactly once when N concurrent callers race
// for the same relPath. This is the direct integrity invariant — the
// security review called out that without dedup, a single file could be
// charged N times for a single unit of work.
func TestTribalMiningService_MineFile_BudgetChargedOnce(t *testing.T) {
	const N = 10
	svc, runner, _ := newConcurrentTestService(t, "pkg/charged.go", N)
	runner.barrier = make(chan struct{})

	start := make(chan struct{})
	var wgStarted sync.WaitGroup
	var wgDone sync.WaitGroup
	for i := 0; i < N; i++ {
		wgStarted.Add(1)
		wgDone.Add(1)
		go func() {
			defer wgDone.Done()
			<-start
			wgStarted.Done()
			_, _ = svc.MineFile(context.Background(), "pkg/charged.go", TriggerJITOnDemand)
		}()
	}
	close(start)
	wgStarted.Wait()
	waitForSettle(runner, N)
	close(runner.barrier)
	wgDone.Wait()

	// The miner's callCount reflects the LLM budget charge.
	svc.miner.mu.Lock()
	calls := svc.miner.callCount
	svc.miner.mu.Unlock()
	if calls != 1 {
		t.Errorf("miner callCount = %d, want 1 (budget charged once per dedup window)", calls)
	}
}

// TestTribalMiningService_MineFile_DifferentKeysNoDedup asserts that
// concurrent calls with DIFFERENT relPaths each run their own extraction
// — singleflight must key on relPath, not a global lock.
func TestTribalMiningService_MineFile_DifferentKeysNoDedup(t *testing.T) {
	const N = 10
	cdb := newTestClaimsDB(t)

	runner := &countingRunner{
		prList:  "1\n",
		apiResp: `{"body":"x","diff_hunk":"@@","path":"pkg/x.go","html_url":"https://github.com/org/repo/pull/1#r1","user":{"login":"r"}}`,
	}

	llmResponses := make([]string, N)
	for i := range llmResponses {
		llmResponses[i] = `{"kind":"rationale","body":"x","confidence":0.8}`
	}
	llm := &mockLLMClient{responses: llmResponses}

	miner := &prCommentMiner{
		RepoOwner:   "org",
		RepoName:    "repo",
		Client:      llm,
		Model:       "test",
		DailyBudget: 1_000,
		RunCommand:  runner.run,
	}
	svc := newServiceWithMiner(cdb, miner, "repo")

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			path := fmt.Sprintf("pkg/file_%d.go", idx)
			_, _ = svc.MineFile(context.Background(), path, TriggerBatchSchedule)
		}(i)
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&runner.apiCalls); got != int64(N) {
		t.Errorf("ExtractForFile gh api calls = %d, want %d (no dedup across distinct keys)", got, N)
	}
}

// TestTribalMiningService_MineFile_LimiterDenial asserts that a limiter
// denial returns a mine_throttled MiningError WITHOUT entering singleflight
// or touching the miner. With Burst=1 and a fast second call, the second
// call must be throttled.
func TestTribalMiningService_MineFile_LimiterDenial(t *testing.T) {
	svc, runner, _ := newConcurrentTestService(t, "pkg/throttled.go", 2)
	// Very low burst so two quick calls trip the limiter.
	lim := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:  0.001, // effectively no refill on test time scales
		Burst: 1,
	})
	WithMineLimiter(lim)(svc)

	// First call: allowed, runs full mine.
	r1, err := svc.MineFile(context.Background(), "pkg/throttled.go", TriggerJITOnDemand)
	if err != nil {
		t.Fatalf("first MineFile: %v", err)
	}
	if r1 == nil {
		t.Fatal("first MineFile: expected non-nil result")
	}

	// Second call: same key, burst exhausted → denied.
	r2, err := svc.MineFile(context.Background(), "pkg/throttled.go", TriggerJITOnDemand)
	if err == nil {
		t.Fatal("second MineFile: expected error, got nil")
	}
	if r2 != nil {
		t.Errorf("second MineFile: expected nil result on denial, got %+v", r2)
	}

	var me *MiningError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MiningError, got %T: %v", err, err)
	}
	if me.Code != CodeMineThrottled {
		t.Errorf("Code = %q, want %s", me.Code, CodeMineThrottled)
	}
	if !errors.Is(err, ErrMineThrottled) {
		t.Error("error should unwrap to ErrMineThrottled")
	}
	// Must be distinguishable from budget_exceeded.
	if errors.Is(err, ErrBudgetExceeded) {
		t.Error("throttle error must NOT unwrap to ErrBudgetExceeded")
	}
	// SafeMessage gives caller-facing text.
	if msg := me.SafeMessage(); msg == "" || msg == me.Code {
		t.Errorf("SafeMessage() = %q, want specific throttle message", msg)
	}

	// Critical: the miner was NOT invoked for the denied call.
	if got := atomic.LoadInt64(&runner.apiCalls); got != 1 {
		t.Errorf("gh api calls = %d, want 1 (denied call must not enter extract)", got)
	}
}

// TestTribalMiningService_MineFile_NilLimiterUnlimited asserts that a nil
// limiter yields the backward-compatible, unthrottled path — N rapid calls
// for distinct keys must all proceed.
func TestTribalMiningService_MineFile_NilLimiterUnlimited(t *testing.T) {
	const N = 20
	cdb := newTestClaimsDB(t)

	runner := &countingRunner{
		prList:  "1\n",
		apiResp: `{"body":"x","diff_hunk":"@@","path":"pkg/x.go","html_url":"https://github.com/org/repo/pull/1#r1","user":{"login":"r"}}`,
	}
	llmResponses := make([]string, N)
	for i := range llmResponses {
		llmResponses[i] = `{"kind":"rationale","body":"x","confidence":0.8}`
	}
	llm := &mockLLMClient{responses: llmResponses}

	miner := &prCommentMiner{
		RepoOwner:   "org",
		RepoName:    "repo",
		Client:      llm,
		Model:       "test",
		DailyBudget: 1_000,
		RunCommand:  runner.run,
	}

	// No WithMineLimiter → mineLimiter is nil → backward-compatible.
	svc := newServiceWithMiner(cdb, miner, "repo")
	if svc.mineLimiter != nil {
		t.Fatalf("expected nil mineLimiter by default, got %v", svc.mineLimiter)
	}

	for i := 0; i < N; i++ {
		path := fmt.Sprintf("pkg/f_%d.go", i)
		if _, err := svc.MineFile(context.Background(), path, TriggerBatchSchedule); err != nil {
			var me *MiningError
			if errors.As(err, &me) && me.Code == CodeMineThrottled {
				t.Fatalf("nil limiter must never throttle; got %v", err)
			}
			t.Fatalf("MineFile(%d): %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&runner.apiCalls); got != int64(N) {
		t.Errorf("gh api calls = %d, want %d (nil limiter = unlimited)", got, N)
	}
}

// TestTribalMiningService_MineFile_LimiterBoundedKeyspace asserts that
// minting more distinct keys than MaxKeys does NOT grow the limiter's
// tracked bucket set beyond MaxKeys. This is the security invariant:
// an adversary enumerating synthetic relPaths cannot drive unbounded
// limiter-map growth.
func TestTribalMiningService_MineFile_LimiterBoundedKeyspace(t *testing.T) {
	const maxKeys = 8
	const overMint = maxKeys + 12

	cdb := newTestClaimsDB(t)
	runner := &countingRunner{
		prList:  "1\n",
		apiResp: `{"body":"x","diff_hunk":"@@","path":"pkg/x.go","html_url":"https://github.com/org/repo/pull/1#r1","user":{"login":"r"}}`,
	}
	llmResponses := make([]string, overMint)
	for i := range llmResponses {
		llmResponses[i] = `{"kind":"rationale","body":"x","confidence":0.8}`
	}
	llm := &mockLLMClient{responses: llmResponses}
	miner := &prCommentMiner{
		RepoOwner:   "org",
		RepoName:    "repo",
		Client:      llm,
		Model:       "test",
		DailyBudget: 10_000,
		RunCommand:  runner.run,
	}

	lim := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:    100, // generous: we want to observe map growth, not throttle
		Burst:   100,
		MaxKeys: maxKeys,
	})
	svc := newServiceWithMiner(cdb, miner, "repo", WithMineLimiter(lim))

	for i := 0; i < overMint; i++ {
		path := fmt.Sprintf("pkg/synthetic_%d.go", i)
		_, _ = svc.MineFile(context.Background(), path, TriggerBatchSchedule)
	}

	if sz := lim.Size(); sz > maxKeys {
		t.Errorf("limiter Size() = %d, want <= %d (LRU bound)", sz, maxKeys)
	}
}

// TestTribalMiningService_MineFile_FirstCallerCancelPropagates locks in the
// documented singleflight trade-off: when the first caller's context is
// cancelled mid-extract, the shared operation aborts and ALL concurrent
// waiters (which used their own independent contexts) observe the
// cancellation error. This is an intentional property — the alternative
// would duplicate budget spend — and is called out in MineFile's doc
// comment. A regression that silently swapped singleflight.Do for per-call
// execution would pass every other test in this file because none of them
// exercise the "first caller cancels while waiters are parked" path.
//
// Regression signals this test detects:
//  1. prListArrivals > 1 — if singleflight is removed, each goroutine
//     reaches `gh pr list` independently.
//  2. Any waiter returns a non-cancellation result — if singleflight is
//     swapped for per-goroutine execution, waiters 1 and 2 (whose own
//     ctxs were never cancelled) would complete the mine successfully.
func TestTribalMiningService_MineFile_FirstCallerCancelPropagates(t *testing.T) {
	const N = 3
	svc, runner, _ := newConcurrentTestService(t, "pkg/cancelled.go", N)
	runner.barrier = make(chan struct{})
	runner.ctxAware = true // the first caller's cancel must unblock the barrier wait

	// Defensive cleanup: if the test aborts mid-flight (e.g. t.Fatalf
	// before cancel0 fires), close the barrier so any parked mock-runner
	// call returns and its goroutine cannot leak past the test boundary.
	// Ordinarily the cancellation path frees the runner via ctx.Done, but
	// an assertion failure in pre-cancel setup would otherwise strand the
	// runner's goroutine on the barrier.
	var barrierClosed atomic.Bool
	closeBarrierOnce := func() {
		if barrierClosed.CompareAndSwap(false, true) {
			close(runner.barrier)
		}
	}
	t.Cleanup(closeBarrierOnce)

	// Only goroutine 0's context is cancellable. Per the singleflight
	// contract, the first caller's ctx is the authoritative one — it's
	// what mineFileOnce runs under. Waiters use Background so the test
	// isolates "first-caller cancel propagates to waiters" from
	// "everyone cancelled their own ctx".
	//
	// singleflight.Group chooses "the first caller" by whoever wins the
	// race into Do(), so we must serialize g0 before the others:
	// (1) launch g0 alone, (2) wait until g0 is parked at the runner's
	// barrier (meaning g0's call is the one running mineFileOnce), then
	// (3) launch the N-1 waiters — they'll find g0's in-flight key and
	// park as waiters. Without this ordering the "first caller" is
	// non-deterministic and the test flakes.
	ctx0, cancel0 := context.WithCancel(context.Background())
	defer cancel0()

	var wgDone sync.WaitGroup
	errs := make([]error, N)
	results := make([]*MiningResult, N)

	// Launch goroutine 0 (the designated first caller).
	wgDone.Add(1)
	go func() {
		defer wgDone.Done()
		r, err := svc.MineFile(ctx0, "pkg/cancelled.go", TriggerJITOnDemand)
		results[0] = r
		errs[0] = err
	}()

	// Wait until g0 has arrived at the runner (i.e. it is the singleflight
	// leader, currently running mineFileOnce and parked at the barrier).
	// Only once this holds is it safe to launch the waiters; otherwise
	// one of them could win the race into Do() and become leader with a
	// non-cancellable ctx. The deadline is generous (2s) and bounded so
	// a stuck test still terminates rather than hanging.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&runner.prListArrivals) < 1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := atomic.LoadInt64(&runner.prListArrivals); got != 1 {
		t.Fatalf("pre-waiter prListArrivals = %d, want 1 (g0 should be alone at barrier)", got)
	}

	// Launch the N-1 waiter goroutines. They enter MineFile with
	// Background ctx, hit singleflight.Do, find g0's in-flight key, and
	// park waiting for the shared result.
	//
	// We rely on waitForSettle (below) as the sole synchronization gate:
	// a prior attempt used a WaitGroup signalled before MineFile was
	// called, which fires too early (goroutine had received from the
	// start channel but not yet entered singleflight.Do) and left a
	// scheduler-race window. waitForSettle checks the real invariant:
	// prListArrivals stays at 1 because waiters dedup into the leader.
	waiterStart := make(chan struct{})
	for i := 1; i < N; i++ {
		wgDone.Add(1)
		go func(idx int) {
			defer wgDone.Done()
			<-waiterStart
			r, err := svc.MineFile(context.Background(), "pkg/cancelled.go", TriggerJITOnDemand)
			results[idx] = r
			errs[idx] = err
		}(i)
	}
	close(waiterStart)

	// waitForSettle is the anti-regression check: if singleflight
	// were removed and each goroutine ran its own mine, arrivals would
	// climb from 1 toward N. We confirm it stays at 1 (leader only),
	// which also proves all waiters have reached singleflight.Do.
	waitForSettle(runner, N)

	if got := atomic.LoadInt64(&runner.prListArrivals); got != 1 {
		t.Fatalf("pre-cancel prListArrivals = %d, want 1 (singleflight dedup broken before we even cancel)", got)
	}

	// Cancel the first caller's ctx. The ctx-aware runner wakes from its
	// barrier wait and returns ctx.Err(). mineFileOnce wraps that in a
	// MiningError{Code:"extraction_failed"} and singleflight.Do returns
	// the same (nil, err) pair to all N waiters.
	cancel0()

	wgDone.Wait()

	// All N callers see a cancellation-derived error.
	for i, err := range errs {
		if err == nil {
			t.Errorf("goroutine %d: err = nil, want context.Canceled (shared via singleflight)", i)
			continue
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("goroutine %d: err = %v, want errors.Is(..., context.Canceled)", i, err)
		}
		// Waiters also get a nil result alongside the shared error —
		// the read-only contract in MineFile's doc comment says they
		// receive the same (result, err) pair; on error that result
		// pointer must be nil so a nil-check on the result is safe.
		if results[i] != nil {
			t.Errorf("goroutine %d: result = %+v, want nil on cancelled extraction", i, results[i])
		}
	}

	// Singleflight dedup invariant: even after cancellation, exactly one
	// goroutine reached `gh pr list`. The other N-1 never entered the
	// shared work; they were parked waiting for it.
	if got := atomic.LoadInt64(&runner.prListArrivals); got != 1 {
		t.Errorf("prListArrivals = %d, want 1 (singleflight must dedup even on cancel)", got)
	}
	// The miner never got to the LLM step because findPRsForFile returned
	// ctx.Err() before classification. callCount is the budget counter
	// (incremented per LLM call, not per MineFile invocation), so when
	// cancellation fires at the pr list step we expect 0, not 1. The
	// direct singleflight-dedup signal is the prListArrivals==1 check
	// above; callCount here is the consequential "budget not
	// double-charged" invariant.
	svc.miner.mu.Lock()
	calls := svc.miner.callCount
	svc.miner.mu.Unlock()
	if calls != 0 {
		t.Errorf("miner callCount = %d, want 0 (budget must not be charged when extract is cancelled)", calls)
	}
	// Generation counter never bumps: no facts were written.
	if g := svc.FactsGeneration(); g != 0 {
		t.Errorf("generation = %d, want 0 (nothing was written)", g)
	}
}

// TestTribalMiningService_MineSymbol_PropagatesThrottle locks in the m7v.40
// fix: when MineSymbol's per-file loop encounters a mine_throttled denial
// from MineFile (limiter rejection), the loop must surface the wrapped
// ErrMineThrottled to the caller instead of swallowing it via `continue`.
//
// Wave 1 review (m7v.30) found that the renderMineError throttle branch in
// TribalMineOnDemandHandler was dead code via the symbol-mining path because
// MineSymbol only propagated budget_exceeded; mine_throttled (and every
// other per-file MiningError code) was discarded silently. Without this
// propagation, MCP clients cannot distinguish a transient rate-limit denial
// (retry shortly) from "no facts found" — the exact confusion the m7v.30
// SafeMessage wording was designed to prevent.
//
// Setup: two files mapped to the same symbol; KeyedLimiter with Burst=1
// admits the first MineFile call and denies the second. Assert:
//   - MineSymbol returns a non-nil error
//   - errors.Is(err, ErrMineThrottled) is true (the wrapped sentinel chain
//     survived the loop's classification)
//   - The MiningError.Code is "mine_throttled" (not "budget_exceeded")
//   - Partial results from the first file are preserved alongside the error
//     (mirrors the budget_exceeded eager-exit semantics)
func TestTribalMiningService_MineSymbol_PropagatesThrottle(t *testing.T) {
	cdb := newTestClaimsDB(t)

	// Two files share the same symbol name. resolveSymbolFiles will return
	// both paths; the loop processes them in insertion order.
	for _, sym := range []db.Symbol{
		{Repo: "repo", ImportPath: "pkg/first.go", SymbolName: "DoWork", Language: "go", Kind: "func", Visibility: "public"},
		{Repo: "repo", ImportPath: "pkg/second.go", SymbolName: "DoWork", Language: "go", Kind: "func", Visibility: "public"},
	} {
		if _, err := cdb.UpsertSymbol(sym); err != nil {
			t.Fatalf("upsert symbol: %v", err)
		}
	}

	comment := PRComment{
		Body:     "first file rationale",
		DiffHunk: "@@",
		Path:     "pkg/first.go",
		HTMLURL:  "https://github.com/org/repo/pull/1#r1",
		User:     prUser{Login: "r"},
	}
	commentJSON, _ := json.Marshal(comment)

	runner := &mockRunnerRecording{
		prList:  "1\n",
		apiResp: string(commentJSON),
	}
	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"rationale","body":"first file body","confidence":0.9}`,
		},
	}
	miner := &prCommentMiner{
		RepoOwner:   "org",
		RepoName:    "repo",
		Client:      llm,
		Model:       "test",
		DailyBudget: 1_000,
		RunCommand:  runner.run,
	}

	// Burst=1 admits each per-key bucket exactly once; Rate is intentionally
	// near-zero so buckets cannot refill on test time scales (deterministic
	// denial). KeyedLimiter is keyed by relPath, so to deny the second file
	// in MineSymbol we pre-drain its bucket below.
	lim := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:  0.001,
		Burst: 1,
	})
	svc := newServiceWithMiner(cdb, miner, "repo", WithMineLimiter(lim))

	// Pre-drain the limiter bucket for pkg/second.go so when MineSymbol's
	// per-file loop reaches it, the limiter denies the call. The first
	// file (pkg/first.go) keeps its own bucket and proceeds normally.
	if !lim.Allow("pkg/second.go") {
		t.Fatalf("pre-drain Allow(pkg/second.go) should succeed once with Burst=1")
	}

	results, err := svc.MineSymbol(context.Background(), "DoWork", TriggerJITOnDemand)
	if err == nil {
		t.Fatal("MineSymbol: expected error from throttled second file, got nil")
	}
	if !errors.Is(err, ErrMineThrottled) {
		t.Errorf("errors.Is(err, ErrMineThrottled) = false; err=%v", err)
	}
	var me *MiningError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MiningError, got %T: %v", err, err)
	}
	if me.Code != CodeMineThrottled {
		t.Errorf("MiningError.Code = %q, want %s", me.Code, CodeMineThrottled)
	}
	// Distinguishability invariant from m7v.30: throttle MUST NOT alias
	// to budget_exceeded — otherwise the renderMineError fan-out can't
	// give the caller a retry hint.
	if errors.Is(err, ErrBudgetExceeded) {
		t.Error("throttle error must NOT unwrap to ErrBudgetExceeded")
	}

	// Partial results from the first (admitted) file must be preserved,
	// matching the budget_exceeded eager-exit contract: callers see the
	// retry signal alongside whatever work landed before the denial.
	if len(results) != 1 {
		t.Fatalf("got %d partial results, want 1 (first file should have completed)", len(results))
	}
	if results[0].Path != "pkg/first.go" {
		t.Errorf("results[0].Path = %q, want pkg/first.go", results[0].Path)
	}
	if len(results[0].Facts) != 1 {
		t.Errorf("results[0].Facts = %d, want 1", len(results[0].Facts))
	}
}

// TestTribalMiningService_MineSymbol_BudgetExceededStillStops is a
// regression guard: the m7v.40 propagation fix must not change the
// existing budget_exceeded eager-exit behavior. A multi-file symbol
// whose first file trips the budget cap must surface budget_exceeded
// (not mine_throttled, not a generic continue) and halt iteration.
func TestTribalMiningService_MineSymbol_BudgetExceededStillStops(t *testing.T) {
	cdb := newTestClaimsDB(t)

	for _, sym := range []db.Symbol{
		{Repo: "repo", ImportPath: "pkg/first.go", SymbolName: "BudgetSym", Language: "go", Kind: "func", Visibility: "public"},
		{Repo: "repo", ImportPath: "pkg/second.go", SymbolName: "BudgetSym", Language: "go", Kind: "func", Visibility: "public"},
	} {
		if _, err := cdb.UpsertSymbol(sym); err != nil {
			t.Fatalf("upsert symbol: %v", err)
		}
	}

	// A real comment payload so ExtractForFile reaches the per-comment
	// classify loop where checkBudget is consulted.
	comment := PRComment{
		Body:     "first file",
		DiffHunk: "@@",
		Path:     "pkg/first.go",
		HTMLURL:  "https://github.com/org/repo/pull/1#r1",
		User:     prUser{Login: "r"},
	}
	commentJSON, _ := json.Marshal(comment)

	runner := &mockRunnerRecording{
		prList:  "1\n",
		apiResp: string(commentJSON),
	}
	llm := &mockLLMClient{}
	miner := &prCommentMiner{
		RepoOwner:   "org",
		RepoName:    "repo",
		Client:      llm,
		Model:       "test",
		DailyBudget: 1,
		RunCommand:  runner.run,
	}
	miner.mu.Lock()
	miner.callCount = 1 // already at budget
	miner.mu.Unlock()

	svc := newServiceWithMiner(cdb, miner, "repo")

	_, err := svc.MineSymbol(context.Background(), "BudgetSym", TriggerJITOnDemand)
	if err == nil {
		t.Fatal("MineSymbol: expected budget_exceeded error, got nil")
	}
	var me *MiningError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MiningError, got %T: %v", err, err)
	}
	if me.Code != CodeBudgetExceeded {
		t.Errorf("MiningError.Code = %q, want %s", me.Code, CodeBudgetExceeded)
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Error("errors.Is(err, ErrBudgetExceeded) = false")
	}
	// And the throttle propagation must NOT alias the budget path.
	if errors.Is(err, ErrMineThrottled) {
		t.Error("budget error must NOT unwrap to ErrMineThrottled")
	}
}
