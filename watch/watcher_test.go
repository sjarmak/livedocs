package watch

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/live-docs/live_docs/pipeline"
)

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

	var buf bytes.Buffer

	// Shared state instance so concurrent watchers don't clobber each other.
	sharedState := NewState()

	wA := New(Config{
		RepoDir:   "/fake/repo-a",
		RepoName:  "repo-a",
		Interval:  50 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  pA,
		Out:       &buf,
		Git:       gitA,
		State:     sharedState,
	})

	wB := New(Config{
		RepoDir:   "/fake/repo-b",
		RepoName:  "repo-b",
		Interval:  50 * time.Millisecond,
		StateFile: stateFile,
		Pipeline:  pB,
		Out:       &buf,
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
