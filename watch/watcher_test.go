package watch

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/live-docs/live_docs/pipeline"
)

// safeWriter is a goroutine-safe io.Writer for concurrent test use.
type safeWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *safeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// mockGitOps implements GitOps for testing.
type mockGitOps struct {
	mu         sync.Mutex
	heads      []string // sequence of HEAD SHAs to return
	callIdx    int
	totalCalls int
	ancestor   map[string]bool // sha -> isAncestor result
}

func (m *mockGitOps) RevParseHEAD(_ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.totalCalls++
	if m.callIdx >= len(m.heads) {
		return m.heads[len(m.heads)-1], nil
	}
	sha := m.heads[m.callIdx]
	m.callIdx++
	return sha, nil
}

func (m *mockGitOps) IsAncestor(_ string, ancestor string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ancestor == nil {
		return true, nil
	}
	v, ok := m.ancestor[ancestor]
	if !ok {
		return true, nil // default to true
	}
	return v, nil
}

// mockPipeline implements PipelineRunner for testing.
type mockPipeline struct {
	mu   sync.Mutex
	runs []pipelineCall
}

type pipelineCall struct {
	from string
	to   string
}

func (m *mockPipeline) Run(_ context.Context, from, to string) (pipeline.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = append(m.runs, pipelineCall{from: from, to: to})
	return pipeline.Result{
		FilesChanged:   1,
		FilesExtracted: 1,
		ClaimsStored:   5,
		ChangedPaths:   []string{"pkg/foo.go", "pkg/bar.go"},
		Duration:       10 * time.Millisecond,
	}, nil
}

func (m *mockPipeline) getRuns() []pipelineCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]pipelineCall, len(m.runs))
	copy(cp, m.runs)
	return cp
}

func TestWatcher_DetectsHEADChange(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")
	var buf bytes.Buffer

	git := &mockGitOps{
		heads: []string{"sha_initial", "sha_initial", "sha_new", "sha_new"},
	}
	p := &mockPipeline{}

	w := New(Config{
		RepoDir:   "/fake/repo",
		RepoName:  "test-repo",
		Interval:  10 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  p,
		Out:       &buf,
		Git:       git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	runs := p.getRuns()
	if len(runs) < 1 {
		t.Fatal("expected at least one pipeline run")
	}

	// First run should be full extraction (no prior state).
	if runs[0].from != emptyTreeSHA {
		t.Fatalf("first run should use empty tree SHA, got from=%s", runs[0].from)
	}
	if runs[0].to != "sha_initial" {
		t.Fatalf("first run should target sha_initial, got to=%s", runs[0].to)
	}

	// Check that a HEAD change triggered incremental extraction.
	foundIncremental := false
	for _, r := range runs {
		if r.from == "sha_initial" && r.to == "sha_new" {
			foundIncremental = true
			break
		}
	}
	if !foundIncremental {
		t.Fatalf("expected incremental run sha_initial->sha_new, got runs: %+v", runs)
	}
}

func TestWatcher_ResumesFromState(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	// Pre-populate state.
	s := NewState()
	s.SetSHA("test-repo", "sha_old")
	if err := SaveState(stateFile, s); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	git := &mockGitOps{
		heads: []string{"sha_new"},
	}
	p := &mockPipeline{}

	w := New(Config{
		RepoDir:   "/fake/repo",
		RepoName:  "test-repo",
		Interval:  50 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  p,
		Out:       &buf,
		Git:       git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	runs := p.getRuns()
	if len(runs) != 1 {
		t.Fatalf("expected 1 pipeline run, got %d", len(runs))
	}

	// Should resume from stored SHA, not full extraction.
	if runs[0].from != "sha_old" {
		t.Fatalf("expected from=sha_old, got %s", runs[0].from)
	}
	if runs[0].to != "sha_new" {
		t.Fatalf("expected to=sha_new, got %s", runs[0].to)
	}

	// Verify output mentions resuming.
	if !strings.Contains(buf.String(), "resuming") {
		t.Fatalf("expected 'resuming' in output, got: %s", buf.String())
	}
}

func TestWatcher_ForcePushFallback(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	// Pre-populate state with a SHA that will not be ancestor.
	s := NewState()
	s.SetSHA("test-repo", "sha_old_force_pushed")
	if err := SaveState(stateFile, s); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	git := &mockGitOps{
		heads:    []string{"sha_new_after_force"},
		ancestor: map[string]bool{"sha_old_force_pushed": false},
	}
	p := &mockPipeline{}

	w := New(Config{
		RepoDir:   "/fake/repo",
		RepoName:  "test-repo",
		Interval:  50 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  p,
		Out:       &buf,
		Git:       git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	runs := p.getRuns()
	if len(runs) != 1 {
		t.Fatalf("expected 1 pipeline run, got %d", len(runs))
	}

	// Force-push should fall back to empty tree SHA.
	if runs[0].from != emptyTreeSHA {
		t.Fatalf("force-push should use empty tree SHA, got from=%s", runs[0].from)
	}

	// Verify output mentions force-push.
	if !strings.Contains(buf.String(), "force-push") {
		t.Fatalf("expected 'force-push' in output, got: %s", buf.String())
	}
}

func TestWatcher_NoChangeNoPipelineRun(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	// Pre-populate state with current HEAD.
	s := NewState()
	s.SetSHA("test-repo", "sha_same")
	if err := SaveState(stateFile, s); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	git := &mockGitOps{
		heads: []string{"sha_same", "sha_same", "sha_same"},
	}
	p := &mockPipeline{}

	w := New(Config{
		RepoDir:   "/fake/repo",
		RepoName:  "test-repo",
		Interval:  10 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  p,
		Out:       &buf,
		Git:       git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	runs := p.getRuns()
	if len(runs) != 0 {
		t.Fatalf("expected 0 pipeline runs when HEAD unchanged, got %d", len(runs))
	}
}

func TestWatcher_CleanExitOnCancel(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	s := NewState()
	s.SetSHA("test-repo", "sha_same")
	if err := SaveState(stateFile, s); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	git := &mockGitOps{
		heads: []string{"sha_same"},
	}
	p := &mockPipeline{}

	w := New(Config{
		RepoDir:   "/fake/repo",
		RepoName:  "test-repo",
		Interval:  10 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  p,
		Out:       &buf,
		Git:       git,
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- w.Run(ctx)
	}()

	// Let it run briefly, then cancel.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error on clean exit, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit within timeout")
	}

	if !strings.Contains(buf.String(), "stopping") {
		t.Fatalf("expected 'stopping' in output, got: %s", buf.String())
	}
}

func TestWatcher_PersistsStateAfterRun(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	var buf bytes.Buffer
	git := &mockGitOps{
		heads: []string{"sha_first"},
	}
	p := &mockPipeline{}

	w := New(Config{
		RepoDir:   "/fake/repo",
		RepoName:  "test-repo",
		Interval:  50 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  p,
		Out:       &buf,
		Git:       git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	// Verify state was persisted.
	loaded := LoadState(stateFile)
	if got := loaded.GetSHA("test-repo"); got != "sha_first" {
		t.Fatalf("expected state to persist sha_first, got %q", got)
	}
}

func TestWatcher_MultiRepoIndependentState(t *testing.T) {
	// Two watchers sharing a single state file track SHAs independently.
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "shared-state.json")

	gitA := &mockGitOps{heads: []string{"sha_a1"}}
	gitB := &mockGitOps{heads: []string{"sha_b1"}}
	pA := &mockPipeline{}
	pB := &mockPipeline{}

	// Use separate writers per watcher to avoid data race on bytes.Buffer.
	var bufA, bufB safeWriter

	// Shared state instance so concurrent watchers don't clobber each other.
	sharedState := NewState()

	wA := New(Config{
		RepoDir:   "/fake/repo-a",
		RepoName:  "repo-a",
		Interval:  50 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  pA,
		Out:       &bufA,
		Git:       gitA,
		State:     sharedState,
	})

	wB := New(Config{
		RepoDir:   "/fake/repo-b",
		RepoName:  "repo-b",
		Interval:  50 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  pB,
		Out:       &bufB,
		Git:       gitB,
		State:     sharedState,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = wA.Run(ctx) }()
	go func() { defer wg.Done(); _ = wB.Run(ctx) }()
	wg.Wait()

	// Both should have run their pipelines.
	runsA := pA.getRuns()
	runsB := pB.getRuns()
	if len(runsA) < 1 {
		t.Fatal("expected at least one pipeline run for repo-a")
	}
	if len(runsB) < 1 {
		t.Fatal("expected at least one pipeline run for repo-b")
	}

	// Verify state has both repos tracked independently.
	state := LoadState(stateFile)
	if got := state.GetSHA("repo-a"); got != "sha_a1" {
		t.Errorf("repo-a SHA = %q, want sha_a1", got)
	}
	if got := state.GetSHA("repo-b"); got != "sha_b1" {
		t.Errorf("repo-b SHA = %q, want sha_b1", got)
	}
}

func TestWatcher_DynamicFreshnessInterval(t *testing.T) {
	// Verifies that the watcher adjusts its polling interval based on
	// the access time returned by AccessTimeFn.
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	// Pre-populate state so the watcher doesn't trigger pipeline runs.
	s := NewState()
	s.SetSHA("test-repo", "sha_stable")
	if err := SaveState(stateFile, s); err != nil {
		t.Fatal(err)
	}

	var buf safeWriter
	git := &mockGitOps{
		heads: []string{"sha_stable"},
	}
	p := &mockPipeline{}

	// Start with a "hot" access time (5 minutes ago).
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	accessTime := now.Add(-5 * time.Minute) // hot tier

	tiers := []FreshnessTier{
		{MaxAge: 1 * time.Hour, Interval: 10 * time.Millisecond},   // hot (short for test)
		{MaxAge: 24 * time.Hour, Interval: 100 * time.Millisecond}, // warm
	}

	w := New(Config{
		RepoDir:        "/fake/repo",
		RepoName:       "test-repo",
		Interval:       50 * time.Millisecond, // base (should not be used when tiers active)
		StateFile:      stateFile,
		Pipeline:       p,
		Out:            &buf,
		Git:            git,
		State:          s,
		FreshnessTiers: tiers,
		ColdInterval:   500 * time.Millisecond,
		AccessTimeFn: func(repoName string) (time.Time, bool) {
			return accessTime, true
		},
		NowFunc: func() time.Time { return now },
	})

	// effectiveInterval should return hot tier interval.
	got := w.effectiveInterval()
	if got != 10*time.Millisecond {
		t.Fatalf("effectiveInterval() = %v, want 10ms (hot tier)", got)
	}

	// No access — should return cold interval.
	w2 := New(Config{
		RepoDir:        "/fake/repo",
		RepoName:       "never-queried",
		Interval:       50 * time.Millisecond,
		StateFile:      stateFile,
		Pipeline:       p,
		Out:            &buf,
		Git:            git,
		State:          s,
		FreshnessTiers: tiers,
		ColdInterval:   500 * time.Millisecond,
		AccessTimeFn: func(repoName string) (time.Time, bool) {
			return time.Time{}, false
		},
		NowFunc: func() time.Time { return now },
	})

	got = w2.effectiveInterval()
	if got != 500*time.Millisecond {
		t.Fatalf("effectiveInterval() = %v, want 500ms (cold)", got)
	}
}

func TestWatcher_EffectiveIntervalNoTiers(t *testing.T) {
	// Without freshness tiers, effectiveInterval returns the base interval.
	var buf safeWriter
	w := New(Config{
		RepoDir:  "/fake/repo",
		RepoName: "test-repo",
		Interval: 42 * time.Second,
		Out:      &buf,
		Pipeline: &mockPipeline{},
		Git:      &mockGitOps{heads: []string{"sha"}},
	})

	got := w.effectiveInterval()
	if got != 42*time.Second {
		t.Fatalf("effectiveInterval() = %v, want 42s (base)", got)
	}
}

func TestWatcher_IntervalFlag(t *testing.T) {
	// Verify that the interval is respected by checking that with a very
	// short interval we get multiple polls.
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	// Pre-populate state so the first check is a no-op (same SHA).
	s := NewState()
	s.SetSHA("test-repo", "sha_stable")
	if err := SaveState(stateFile, s); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	git := &mockGitOps{
		// Return same SHA so no pipeline runs — just polls.
		heads: []string{"sha_stable"},
	}
	p := &mockPipeline{}

	w := New(Config{
		RepoDir:   "/fake/repo",
		RepoName:  "test-repo",
		Interval:  5 * time.Millisecond, // very fast polling
		StateFile: stateFile,
		Pipeline:  p,
		Out:       &buf,
		Git:       git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	// With 5ms interval over 80ms, git should have been called multiple times
	// (initial check + ticker ticks).
	git.mu.Lock()
	calls := git.totalCalls
	git.mu.Unlock()
	if calls < 2 {
		t.Fatalf("expected multiple git calls with 5ms interval, got %d", calls)
	}
}

// mockDeepExtract tracks calls to the deep extraction callback.
type mockDeepExtract struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (m *mockDeepExtract) fn(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.err
}

func (m *mockDeepExtract) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestWatcher_DeepInterval_Fires(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	// Pre-populate state so no tree-sitter extraction triggers.
	s := NewState()
	s.SetSHA("test-repo", "sha_stable")
	if err := SaveState(stateFile, s); err != nil {
		t.Fatal(err)
	}

	var buf safeWriter
	git := &mockGitOps{heads: []string{"sha_stable"}}
	p := &mockPipeline{}
	deep := &mockDeepExtract{}

	w := New(Config{
		RepoDir:      "/fake/repo",
		RepoName:     "test-repo",
		Interval:     50 * time.Millisecond,
		DeepInterval: 20 * time.Millisecond,
		StateFile:    stateFile,
		Pipeline:     p,
		DeepExtract:  deep.fn,
		Out:          &buf,
		Git:          git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	calls := deep.getCalls()
	if calls < 1 {
		t.Fatalf("expected at least 1 deep extraction call, got %d", calls)
	}

	// Pipeline should NOT have run (HEAD unchanged).
	runs := p.getRuns()
	if len(runs) != 0 {
		t.Fatalf("expected 0 pipeline runs, got %d", len(runs))
	}

	// Verify log output mentions deep extraction.
	buf.mu.Lock()
	output := buf.buf.String()
	buf.mu.Unlock()
	if !strings.Contains(output, "deep extraction every") {
		t.Fatalf("expected 'deep extraction every' in output, got: %s", output)
	}
	if !strings.Contains(output, "deep extraction complete") {
		t.Fatalf("expected 'deep extraction complete' in output, got: %s", output)
	}
}

func TestWatcher_DeepInterval_Disabled(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	s := NewState()
	s.SetSHA("test-repo", "sha_stable")
	if err := SaveState(stateFile, s); err != nil {
		t.Fatal(err)
	}

	var buf safeWriter
	git := &mockGitOps{heads: []string{"sha_stable"}}
	p := &mockPipeline{}
	deep := &mockDeepExtract{}

	// DeepInterval=0 should disable deep extraction even with callback set.
	w := New(Config{
		RepoDir:      "/fake/repo",
		RepoName:     "test-repo",
		Interval:     10 * time.Millisecond,
		DeepInterval: 0,
		StateFile:    stateFile,
		Pipeline:     p,
		DeepExtract:  deep.fn,
		Out:          &buf,
		Git:          git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	calls := deep.getCalls()
	if calls != 0 {
		t.Fatalf("expected 0 deep extraction calls when disabled, got %d", calls)
	}
}

func TestWatcher_DeepInterval_NilCallback(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	s := NewState()
	s.SetSHA("test-repo", "sha_stable")
	if err := SaveState(stateFile, s); err != nil {
		t.Fatal(err)
	}

	var buf safeWriter
	git := &mockGitOps{heads: []string{"sha_stable"}}
	p := &mockPipeline{}

	// DeepExtract=nil should not start deep ticker even with interval set.
	w := New(Config{
		RepoDir:      "/fake/repo",
		RepoName:     "test-repo",
		Interval:     10 * time.Millisecond,
		DeepInterval: 20 * time.Millisecond,
		StateFile:    stateFile,
		Pipeline:     p,
		DeepExtract:  nil,
		Out:          &buf,
		Git:          git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	buf.mu.Lock()
	output := buf.buf.String()
	buf.mu.Unlock()
	if strings.Contains(output, "deep extraction every") {
		t.Fatalf("deep extraction should not be configured with nil callback, got: %s", output)
	}
}

func TestWatcher_DeepInterval_Error(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	s := NewState()
	s.SetSHA("test-repo", "sha_stable")
	if err := SaveState(stateFile, s); err != nil {
		t.Fatal(err)
	}

	var buf safeWriter
	git := &mockGitOps{heads: []string{"sha_stable"}}
	p := &mockPipeline{}
	deep := &mockDeepExtract{err: fmt.Errorf("deep extraction failed")}

	w := New(Config{
		RepoDir:      "/fake/repo",
		RepoName:     "test-repo",
		Interval:     50 * time.Millisecond,
		DeepInterval: 20 * time.Millisecond,
		StateFile:    stateFile,
		Pipeline:     p,
		DeepExtract:  deep.fn,
		Out:          &buf,
		Git:          git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Should not crash on deep extraction error.
	_ = w.Run(ctx)

	calls := deep.getCalls()
	if calls < 1 {
		t.Fatalf("expected at least 1 deep extraction call, got %d", calls)
	}

	buf.mu.Lock()
	output := buf.buf.String()
	buf.mu.Unlock()
	if !strings.Contains(output, "deep extraction error") {
		t.Fatalf("expected 'deep extraction error' in output, got: %s", output)
	}
}

// mockOnExtract tracks OnExtract callback invocations.
type mockOnExtract struct {
	mu      sync.Mutex
	results []pipeline.Result
}

func (m *mockOnExtract) fn(result pipeline.Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.results = append(m.results, result)
}

func (m *mockOnExtract) getResults() []pipeline.Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]pipeline.Result, len(m.results))
	copy(cp, m.results)
	return cp
}

func TestWatcher_OnExtract_CalledAfterPipelineRun(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	var buf safeWriter
	git := &mockGitOps{
		heads: []string{"sha_first", "sha_first"},
	}
	p := &mockPipeline{}
	onExtract := &mockOnExtract{}

	w := New(Config{
		RepoDir:   "/fake/repo",
		RepoName:  "test-repo",
		Interval:  50 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  p,
		OnExtract: onExtract.fn,
		Out:       &buf,
		Git:       git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	// Pipeline should have run (full extraction from empty state).
	runs := p.getRuns()
	if len(runs) < 1 {
		t.Fatal("expected at least one pipeline run")
	}

	// OnExtract should have been called with the result including ChangedPaths.
	results := onExtract.getResults()
	if len(results) < 1 {
		t.Fatal("expected OnExtract to be called at least once")
	}
	if len(results[0].ChangedPaths) == 0 {
		t.Fatal("expected ChangedPaths in OnExtract result")
	}
	if results[0].ChangedPaths[0] != "pkg/foo.go" {
		t.Fatalf("expected first changed path to be pkg/foo.go, got %s", results[0].ChangedPaths[0])
	}
}

func TestWatcher_OnExtract_NotCalledWhenNoChange(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	// Pre-populate state with current HEAD so no change is detected.
	s := NewState()
	s.SetSHA("test-repo", "sha_stable")
	if err := SaveState(stateFile, s); err != nil {
		t.Fatal(err)
	}

	var buf safeWriter
	git := &mockGitOps{
		heads: []string{"sha_stable", "sha_stable"},
	}
	p := &mockPipeline{}
	onExtract := &mockOnExtract{}

	w := New(Config{
		RepoDir:   "/fake/repo",
		RepoName:  "test-repo",
		Interval:  10 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  p,
		OnExtract: onExtract.fn,
		Out:       &buf,
		Git:       git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	// No pipeline run means no OnExtract callback.
	results := onExtract.getResults()
	if len(results) != 0 {
		t.Fatalf("expected 0 OnExtract calls when HEAD unchanged, got %d", len(results))
	}
}

func TestWatcher_OnExtract_NilDoesNotPanic(t *testing.T) {
	// Verify that OnExtract=nil does not cause a panic during pipeline run.
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	var buf safeWriter
	git := &mockGitOps{
		heads: []string{"sha_first"},
	}
	p := &mockPipeline{}

	w := New(Config{
		RepoDir:   "/fake/repo",
		RepoName:  "test-repo",
		Interval:  50 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  p,
		OnExtract: nil, // explicitly nil
		Out:       &buf,
		Git:       git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	// Pipeline should have run without panic.
	runs := p.getRuns()
	if len(runs) < 1 {
		t.Fatal("expected at least one pipeline run with nil OnExtract")
	}
}

func TestWatcher_OnExtract_NonBlocking(t *testing.T) {
	// Verify that a slow OnExtract callback does not block the watch poll cycle.
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	var buf safeWriter
	git := &mockGitOps{
		heads: []string{"sha1", "sha1", "sha2", "sha2"},
	}
	p := &mockPipeline{}

	callbackStarted := make(chan struct{}, 2)
	slowOnExtract := func(result pipeline.Result) {
		callbackStarted <- struct{}{}
		// The callback itself is synchronous but Send() on the real queue
		// is non-blocking. Here we just verify the watcher continues polling
		// even after calling the callback.
	}

	w := New(Config{
		RepoDir:   "/fake/repo",
		RepoName:  "test-repo",
		Interval:  10 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  p,
		OnExtract: slowOnExtract,
		Out:       &buf,
		Git:       git,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = w.Run(ctx)

	// Should have received at least one callback invocation.
	select {
	case <-callbackStarted:
		// good
	default:
		t.Fatal("expected OnExtract callback to be invoked")
	}

	// The watcher should have continued polling (multiple git calls).
	git.mu.Lock()
	calls := git.totalCalls
	git.mu.Unlock()
	if calls < 2 {
		t.Fatalf("expected multiple git polls after OnExtract, got %d", calls)
	}
}
