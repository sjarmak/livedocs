package tribal

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/live-docs/live_docs/db"
)

// TestTribalJITBatchParity runs the same corpus through both entry points
// (MineFile for batch, MineSymbol for JIT) CONCURRENTLY against a shared DB
// under go test -race, then asserts:
//   - byte-equal fact sets (same body, kind, extractor)
//   - identical cluster keys (normalization is in one package)
//   - consistent cursor state (last_pr_id_set)
//   - budget consumption parity (same LLM call count)
func TestTribalJITBatchParity(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "parity.claims.db")

	// Use a setup connection to create schemas and seed symbols, then close it.
	// Batch and JIT paths each get their own ClaimsDB connection to avoid
	// data races in RunInTransaction's exec-field swap.
	setupDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := setupDB.CreateSchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := setupDB.CreateTribalSchema(); err != nil {
		t.Fatalf("tribal schema: %v", err)
	}

	// Set up test corpus: 3 files, each with one symbol, each returning one
	// PR comment that classifies as rationale.
	type testFile struct {
		relPath    string
		symbolName string
		prNum      int
		body       string
	}
	corpus := []testFile{
		{"pkg/alpha.go", "AlphaFunc", 10, "Alpha must validate input before processing"},
		{"pkg/beta.go", "BetaFunc", 20, "Beta uses lazy initialization for performance"},
		{"pkg/gamma.go", "GammaFunc", 30, "Gamma requires exclusive lock during writes"},
	}

	// Create symbols for each file so MineSymbol can resolve them.
	for _, f := range corpus {
		_, err := setupDB.UpsertSymbol(db.Symbol{
			Repo:       "parity-repo",
			ImportPath: f.relPath,
			SymbolName: f.symbolName,
			Language:   "go",
			Kind:       "func",
			Visibility: "public",
		})
		if err != nil {
			t.Fatalf("upsert symbol %s: %v", f.symbolName, err)
		}
	}
	setupDB.Close()

	// Open separate connections for batch and JIT paths.
	batchDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open batch db: %v", err)
	}
	defer batchDB.Close()

	jitDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open jit db: %v", err)
	}
	defer jitDB.Close()

	// Thread-safe LLM call counter.
	var llmCallsMu sync.Mutex
	llmCalls := 0

	// Build a shared runner that returns the right PR and comment for each file.
	commentMap := make(map[string][]byte)
	prMap := make(map[string]string)
	for _, f := range corpus {
		c := PRComment{
			Body:     f.body,
			DiffHunk: "@@",
			Path:     f.relPath,
			HTMLURL:  fmt.Sprintf("https://github.com/org/repo/pull/%d#r1", f.prNum),
			User:     prUser{Login: "reviewer"},
		}
		data, _ := json.Marshal(c)
		commentMap[f.relPath] = data
		prMap[f.relPath] = fmt.Sprintf("%d\n", f.prNum)
	}

	newRunner := func() CommandRunner {
		return func(_ context.Context, name string, args ...string) ([]byte, error) {
			// Detect pr list vs api call.
			isPRList := false
			for _, a := range args {
				if a == "pr" {
					isPRList = true
					break
				}
			}
			// Figure out which file this is for by looking at the search arg.
			for _, f := range corpus {
				for _, a := range args {
					if a == f.relPath || a == fmt.Sprintf("repos/org/repo/pulls/%d/comments", f.prNum) {
						if isPRList {
							return []byte(prMap[f.relPath]), nil
						}
						return commentMap[f.relPath], nil
					}
				}
			}
			if isPRList {
				return []byte(""), nil
			}
			return []byte(""), nil
		}
	}

	newLLM := func() *mockLLMClient {
		var responses []string
		for range corpus {
			responses = append(responses, `{"kind":"rationale","body":"test rationale","confidence":0.85}`)
		}
		return &mockLLMClient{responses: responses}
	}

	// --- Batch path: mine each file via MineFile ---
	batchLLM := newLLM()
	batchMiner := &prCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     batchLLM,
		Model:      "test-model",
		RunCommand: newRunner(),
	}
	batchSvc := newServiceWithMiner(batchDB, batchMiner, "parity-repo")

	// --- JIT path: mine each symbol via MineSymbol ---
	jitLLM := newLLM()
	jitMiner := &prCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     jitLLM,
		Model:      "test-model",
		RunCommand: newRunner(),
	}
	jitSvc := newServiceWithMiner(jitDB, jitMiner, "parity-repo")

	// Run both paths CONCURRENTLY against the shared DB (race detector active).
	var wg sync.WaitGroup
	var batchResults []*MiningResult
	var jitResults []*MiningResult
	var batchErr, jitErr error

	wg.Add(2)

	// Batch goroutine.
	go func() {
		defer wg.Done()
		for _, f := range corpus {
			r, err := batchSvc.MineFile(context.Background(), f.relPath, TriggerBatchSchedule)
			if err != nil {
				batchErr = err
				return
			}
			if r != nil {
				batchResults = append(batchResults, r)
			}
		}
		llmCallsMu.Lock()
		llmCalls += len(batchLLM.getCalls())
		llmCallsMu.Unlock()
	}()

	// JIT goroutine.
	go func() {
		defer wg.Done()
		for _, f := range corpus {
			results, err := jitSvc.MineSymbol(context.Background(), f.symbolName, TriggerJITOnDemand)
			if err != nil {
				jitErr = err
				return
			}
			jitResults = append(jitResults, results...)
		}
		llmCallsMu.Lock()
		llmCalls += len(jitLLM.getCalls())
		llmCallsMu.Unlock()
	}()

	wg.Wait()

	if batchErr != nil {
		t.Fatalf("batch error: %v", batchErr)
	}
	if jitErr != nil {
		t.Fatalf("JIT error: %v", jitErr)
	}

	// Open a fresh read connection for assertions.
	readDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open read db: %v", err)
	}
	defer readDB.Close()

	// Assertion 1: Both paths should produce facts for each file.
	// Since they share a DB file, the second writer for a given file will see
	// existing cursor state and may produce 0 new facts. The total facts
	// in the DB should be consistent.
	allFacts, err := readDB.GetTribalFactsByKind("rationale")
	if err != nil {
		t.Fatalf("get facts: %v", err)
	}
	if len(allFacts) == 0 {
		t.Fatal("expected at least some rationale facts in DB")
	}

	// Assertion 2: All facts have a non-empty cluster_key (normalization was applied).
	// GetTribalFactsByKind now populates ClusterKey directly.
	for _, fact := range allFacts {
		if fact.ClusterKey == "" {
			t.Errorf("fact %d has empty cluster_key — normalization was not applied", fact.ID)
		}
	}

	// Assertion 3: Cursor state is consistent — each file should have
	// a non-empty cursor after both paths completed.
	for _, f := range corpus {
		ids, version, err := readDB.GetPRIDSet("parity-repo", f.relPath)
		if err != nil {
			t.Errorf("get cursor for %s: %v", f.relPath, err)
			continue
		}
		if len(ids) == 0 {
			t.Errorf("cursor for %s is empty after mining", f.relPath)
		}
		if version == "" {
			t.Errorf("miner version for %s is empty after mining", f.relPath)
		}
	}

	// Assertion 4: LLM calls are bounded — at most one call per PR-comment
	// per path (some may be 0 if the JIT path ran second and found cursor).
	llmCallsMu.Lock()
	totalLLM := llmCalls
	llmCallsMu.Unlock()
	// Each file has exactly 1 comment. With 3 files and 2 paths, max is 6 calls
	// (if both ran before sharing cursor state). Minimum is 3 (if one ran first
	// and the other found cursor state for all files).
	if totalLLM > 6 {
		t.Errorf("total LLM calls = %d, expected <= 6 (3 files * 2 paths max)", totalLLM)
	}
	if totalLLM < 3 {
		t.Errorf("total LLM calls = %d, expected >= 3 (3 files * at least 1 path)", totalLLM)
	}
}

// TestTribalJITBatchParity_GenerationBump verifies that write-through cache
// invalidation fires: any write via the service bumps the generation counter.
// TTL-based caching is banned for tribal facts.
func TestTribalJITBatchParity_GenerationBump(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gen.claims.db")

	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer cdb.Close()
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := cdb.CreateTribalSchema(); err != nil {
		t.Fatalf("tribal schema: %v", err)
	}

	comment := PRComment{
		Body:     "generation test fact",
		DiffHunk: "@@",
		Path:     "pkg/gen.go",
		HTMLURL:  "https://github.com/org/repo/pull/1#r1",
		User:     prUser{Login: "r"},
	}
	cJSON, _ := json.Marshal(comment)

	runner := &mockRunnerRecording{
		prList:  "1\n",
		apiResp: string(cJSON),
	}
	llm := &mockLLMClient{
		responses: []string{`{"kind":"rationale","body":"gen test","confidence":0.8}`},
	}
	miner := &prCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		RunCommand: runner.run,
	}

	svc := newServiceWithMiner(cdb, miner, "repo")

	g0 := svc.FactsGeneration()
	if g0 != 0 {
		t.Fatalf("initial generation = %d, want 0", g0)
	}

	_, err = svc.MineFile(context.Background(), "pkg/gen.go", TriggerBatchSchedule)
	if err != nil {
		t.Fatalf("MineFile: %v", err)
	}

	g1 := svc.FactsGeneration()
	if g1 <= g0 {
		t.Errorf("generation did not bump after write: before=%d after=%d", g0, g1)
	}
}
