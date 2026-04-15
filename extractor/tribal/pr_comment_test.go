package tribal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// mockLLMClient records calls and returns canned responses.
type mockLLMClient struct {
	mu        sync.Mutex
	calls     []llmCall
	responses []string
	callIdx   int
}

type llmCall struct {
	System string
	User   string
}

func (m *mockLLMClient) Complete(_ context.Context, system, user string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, llmCall{System: system, User: user})
	if m.callIdx < len(m.responses) {
		resp := m.responses[m.callIdx]
		m.callIdx++
		return resp, nil
	}
	return `{"kind":"null","body":"","confidence":0.0}`, nil
}

func (m *mockLLMClient) getCalls() []llmCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]llmCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

// mockCommandOutput returns a CommandRunner that returns PR number "1" for
// `gh pr list` calls and the given output for `gh api` calls. This matches
// the two-step fetch pattern: findPRsForFile then fetchPRComments.
func mockCommandOutput(output string) CommandRunner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		// Detect `gh pr list` calls (returns PR numbers).
		for _, a := range args {
			if a == "pr" {
				return []byte("1\n"), nil
			}
		}
		// `gh api` calls return the comment output.
		return []byte(output), nil
	}
}

// mockCommandError returns a CommandRunner that returns an error.
// For `gh pr list` calls it returns the error immediately.
func mockCommandError(err error) CommandRunner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		return nil, err
	}
}

// samplePRCommentJSON returns a JSON line for a PR comment with optional PII.
func samplePRCommentJSON(body, diffHunk, path, htmlURL, login string) string {
	c := PRComment{
		Body:     body,
		DiffHunk: diffHunk,
		Path:     path,
		HTMLURL:  htmlURL,
		User:     prUser{Login: login},
	}
	data, _ := json.Marshal(c)
	return string(data)
}

// --- Tests ---

func TestPRCommentMiner_GHOutputParsing(t *testing.T) {
	line1 := samplePRCommentJSON(
		"This needs a mutex for thread safety",
		"@@ -10,6 +10,8 @@\n+var cache map[string]string",
		"pkg/cache.go",
		"https://github.com/org/repo/pull/1#discussion_r100",
		"reviewer1",
	)
	line2 := samplePRCommentJSON(
		"Consider using sync.Map instead",
		"@@ -10,6 +10,8 @@\n+var cache map[string]string",
		"pkg/cache.go",
		"https://github.com/org/repo/pull/1#discussion_r101",
		"reviewer2",
	)

	ghOutput := line1 + "\n" + line2

	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"invariant","body":"Cache map requires mutex protection for concurrent access","confidence":0.85}`,
			`{"kind":"rationale","body":"sync.Map is preferred for concurrent map access patterns","confidence":0.7}`,
		},
	}

	miner := &PRCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "claude-haiku-4-5-20251001",
		RunCommand: mockCommandOutput(ghOutput),
	}

	facts, _, err := miner.ExtractForFile(context.Background(), "pkg/cache.go", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}

	// Verify first fact
	if facts[0].Kind != "invariant" {
		t.Errorf("fact[0].Kind = %q, want %q", facts[0].Kind, "invariant")
	}
	if facts[0].Body != "Cache map requires mutex protection for concurrent access" {
		t.Errorf("fact[0].Body = %q, want mutex-related", facts[0].Body)
	}

	// Verify second fact
	if facts[1].Kind != "rationale" {
		t.Errorf("fact[1].Kind = %q, want %q", facts[1].Kind, "rationale")
	}
}

func TestPRCommentMiner_PIIRedactedBeforeLLM(t *testing.T) {
	commentWithPII := samplePRCommentJSON(
		"@alice reported that alice@example.com sees a bug at 192.168.1.1",
		"@@ -5,3 +5,5 @@\n+// Contact bob@corp.com for help",
		"pkg/auth.go",
		"https://github.com/org/repo/pull/2#discussion_r200",
		"alice",
	)

	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"quirk","body":"Bug report from user about specific IP","confidence":0.6}`,
		},
	}

	miner := &PRCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "claude-haiku-4-5-20251001",
		RunCommand: mockCommandOutput(commentWithPII),
	}

	_, _, err := miner.ExtractForFile(context.Background(), "pkg/auth.go", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := llm.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(calls))
	}

	userPrompt := calls[0].User

	// Verify PII is redacted in the user prompt
	if strings.Contains(userPrompt, "alice@example.com") {
		t.Error("LLM received un-redacted email: alice@example.com")
	}
	if strings.Contains(userPrompt, "bob@corp.com") {
		t.Error("LLM received un-redacted email: bob@corp.com")
	}
	if strings.Contains(userPrompt, "192.168.1.1") {
		t.Error("LLM received un-redacted IP: 192.168.1.1")
	}

	// Verify redaction placeholders are present
	if !strings.Contains(userPrompt, "[REDACTED_EMAIL]") {
		t.Error("expected [REDACTED_EMAIL] in user prompt")
	}
	if !strings.Contains(userPrompt, "[REDACTED_IP]") {
		t.Error("expected [REDACTED_IP] in user prompt")
	}
}

func TestPRCommentMiner_NullClassificationSkipped(t *testing.T) {
	comment := samplePRCommentJSON(
		"LGTM!",
		"@@ -1,3 +1,5 @@\n+func main() {}",
		"main.go",
		"https://github.com/org/repo/pull/3#discussion_r300",
		"reviewer",
	)

	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"null","body":"","confidence":0.0}`,
		},
	}

	miner := &PRCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "claude-haiku-4-5-20251001",
		RunCommand: mockCommandOutput(comment),
	}

	facts, _, err := miner.ExtractForFile(context.Background(), "main.go", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(facts) != 0 {
		t.Errorf("expected 0 facts for null classification, got %d", len(facts))
	}

	// Verify the LLM was still called
	calls := llm.getCalls()
	if len(calls) != 1 {
		t.Errorf("expected 1 LLM call, got %d", len(calls))
	}
}

func TestPRCommentMiner_FactProvenanceFields(t *testing.T) {
	comment := samplePRCommentJSON(
		"This retry logic is needed because the upstream API has a known race condition on deploys",
		"@@ -20,6 +20,10 @@\n+for i := 0; i < 3; i++ {",
		"pkg/client.go",
		"https://github.com/org/repo/pull/4#discussion_r400",
		"senior_dev",
	)

	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"rationale","body":"Retry logic compensates for upstream API race condition during deploys","confidence":0.92}`,
		},
	}

	miner := &PRCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "claude-haiku-4-5-20251001",
		RunCommand: mockCommandOutput(comment),
	}

	facts, _, err := miner.ExtractForFile(context.Background(), "pkg/client.go", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}

	fact := facts[0]

	// AC7: model set (non-empty string)
	if fact.Model == "" {
		t.Error("Model must be non-empty for LLM-classified facts")
	}
	if fact.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %q, want %q", fact.Model, "claude-haiku-4-5-20251001")
	}

	// AC7: confidence < 1.0
	if fact.Confidence >= 1.0 {
		t.Errorf("Confidence = %f, must be < 1.0 for LLM-classified facts", fact.Confidence)
	}
	if fact.Confidence != 0.92 {
		t.Errorf("Confidence = %f, want 0.92", fact.Confidence)
	}

	// AC7: corroboration = 1
	if fact.Corroboration != 1 {
		t.Errorf("Corroboration = %d, want 1", fact.Corroboration)
	}

	// AC7: extractor = "pr_comment_miner"
	if fact.Extractor != "pr_comment_miner" {
		t.Errorf("Extractor = %q, want %q", fact.Extractor, "pr_comment_miner")
	}

	// AC7: extractor_version = "0.1.0"
	if fact.ExtractorVersion != "0.1.0" {
		t.Errorf("ExtractorVersion = %q, want %q", fact.ExtractorVersion, "0.1.0")
	}

	// SubjectID is set by the caller, not the miner. The miner leaves it at 0.
	if fact.SubjectID != 0 {
		t.Errorf("SubjectID = %d, want 0 (caller patches)", fact.SubjectID)
	}

	// AC8: evidence source_type = "pr_comment"
	if len(fact.Evidence) != 1 {
		t.Fatalf("expected 1 evidence row, got %d", len(fact.Evidence))
	}
	ev := fact.Evidence[0]
	if ev.SourceType != "pr_comment" {
		t.Errorf("Evidence.SourceType = %q, want %q", ev.SourceType, "pr_comment")
	}

	// AC8: evidence source_ref = PR comment URL
	if ev.SourceRef != "https://github.com/org/repo/pull/4#discussion_r400" {
		t.Errorf("Evidence.SourceRef = %q, want PR comment URL", ev.SourceRef)
	}

	// Evidence author
	if ev.Author != "senior_dev" {
		t.Errorf("Evidence.Author = %q, want %q", ev.Author, "senior_dev")
	}

	// Content hash is non-empty SHA-256 (64 hex chars)
	if len(ev.ContentHash) != 64 {
		t.Errorf("Evidence.ContentHash length = %d, want 64", len(ev.ContentHash))
	}

	// Staleness hash matches
	if fact.StalenessHash != ev.ContentHash {
		t.Errorf("StalenessHash != Evidence.ContentHash")
	}

	// Status is active
	if fact.Status != "active" {
		t.Errorf("Status = %q, want %q", fact.Status, "active")
	}

	// Kind matches LLM classification
	if fact.Kind != "rationale" {
		t.Errorf("Kind = %q, want %q", fact.Kind, "rationale")
	}
}

func TestPRCommentMiner_CostBudgetEnforcement(t *testing.T) {
	// Create 3 comments but budget of 2
	comments := strings.Join([]string{
		samplePRCommentJSON("comment 1 about something", "@@ hunk1", "file.go", "https://github.com/org/repo/pull/5#r1", "user1"),
		samplePRCommentJSON("comment 2 about something", "@@ hunk2", "file.go", "https://github.com/org/repo/pull/5#r2", "user2"),
		samplePRCommentJSON("comment 3 about something", "@@ hunk3", "file.go", "https://github.com/org/repo/pull/5#r3", "user3"),
	}, "\n")

	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"rationale","body":"First fact","confidence":0.8}`,
			`{"kind":"invariant","body":"Second fact","confidence":0.7}`,
			// Third call should never happen due to budget
			`{"kind":"quirk","body":"Third fact","confidence":0.6}`,
		},
	}

	miner := &PRCommentMiner{
		RepoOwner:   "org",
		RepoName:    "repo",
		Client:      llm,
		Model:       "claude-haiku-4-5-20251001",
		DailyBudget: 2,
		RunCommand:  mockCommandOutput(comments),
	}

	facts, _, err := miner.ExtractForFile(context.Background(), "file.go", nil)

	// Should return ErrBudgetExceeded
	if err == nil {
		t.Fatal("expected ErrBudgetExceeded, got nil")
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got: %v", err)
	}

	// Should have produced 2 facts before hitting budget
	if len(facts) != 2 {
		t.Errorf("expected 2 facts before budget exceeded, got %d", len(facts))
	}

	// Verify only 2 LLM calls were made
	calls := llm.getCalls()
	if len(calls) != 2 {
		t.Errorf("expected 2 LLM calls, got %d", len(calls))
	}
}

func TestPRCommentMiner_EmptyGHOutput(t *testing.T) {
	llm := &mockLLMClient{}

	miner := &PRCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "claude-haiku-4-5-20251001",
		RunCommand: mockCommandOutput(""),
	}

	facts, _, err := miner.ExtractForFile(context.Background(), "nonexistent.go", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("expected 0 facts for empty gh output, got %d", len(facts))
	}

	// No LLM calls should be made
	calls := llm.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 LLM calls, got %d", len(calls))
	}
}

func TestPRCommentMiner_GHAPIError(t *testing.T) {
	llm := &mockLLMClient{}

	miner := &PRCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "claude-haiku-4-5-20251001",
		RunCommand: mockCommandError(errors.New("gh: not authenticated")),
	}

	_, _, err := miner.ExtractForFile(context.Background(), "file.go", nil)
	if err == nil {
		t.Fatal("expected error from gh api failure, got nil")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("error should contain 'not authenticated', got: %v", err)
	}
}

func TestPRCommentMiner_ConfidenceClampedBelowOne(t *testing.T) {
	comment := samplePRCommentJSON(
		"This absolutely must never change",
		"@@ -1 +1 @@\n+const MAX = 100",
		"config.go",
		"https://github.com/org/repo/pull/6#r600",
		"lead",
	)

	// LLM returns confidence of exactly 1.0
	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"invariant","body":"MAX constant must not change","confidence":1.0}`,
		},
	}

	miner := &PRCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "claude-haiku-4-5-20251001",
		RunCommand: mockCommandOutput(comment),
	}

	facts, _, err := miner.ExtractForFile(context.Background(), "config.go", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}

	// Confidence should be clamped to < 1.0
	if facts[0].Confidence >= 1.0 {
		t.Errorf("Confidence = %f, must be < 1.0 (should be clamped)", facts[0].Confidence)
	}
}

func TestPRCommentMiner_MentionRedactedInDiffHunk(t *testing.T) {
	// Verify PII in diff hunk is also redacted
	comment := samplePRCommentJSON(
		"Clean comment with no PII",
		"@@ -1 +1 @@\n+// Author: @secret_dev with key sk-abc123def456ghi789jkl012mno",
		"internal.go",
		"https://github.com/org/repo/pull/7#r700",
		"reviewer",
	)

	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"quirk","body":"Secret token in comment","confidence":0.5}`,
		},
	}

	miner := &PRCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "claude-haiku-4-5-20251001",
		RunCommand: mockCommandOutput(comment),
	}

	_, _, err := miner.ExtractForFile(context.Background(), "internal.go", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := llm.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(calls))
	}

	userPrompt := calls[0].User

	// Verify PII from diff hunk is redacted
	if strings.Contains(userPrompt, "sk-abc123def456ghi789jkl012mno") {
		t.Error("LLM received un-redacted API token from diff hunk")
	}
	if !strings.Contains(userPrompt, "[REDACTED_TOKEN]") {
		t.Error("expected [REDACTED_TOKEN] in user prompt from diff hunk")
	}
}

// --- Incremental cursor tests ---

// scriptedRunner returns a CommandRunner that recognises `gh pr list` and
// `gh api` calls separately. prListOutputs is consumed in order for each
// subsequent `gh pr list` invocation (the last entry sticks if more calls
// arrive than scripted outputs). The same applies to apiOutputs.
type scriptedRunner struct {
	mu             sync.Mutex
	prListCalls    int
	apiCalls       int
	prListOutputs  []string
	apiOutputs     []string
	defaultPRList  string
	defaultAPIBody string
}

func (s *scriptedRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	isPRList := false
	for i, a := range args {
		if a == "pr" && i+1 < len(args) && args[i+1] == "list" {
			isPRList = true
			break
		}
	}
	if isPRList {
		out := s.defaultPRList
		if s.prListCalls < len(s.prListOutputs) {
			out = s.prListOutputs[s.prListCalls]
		}
		s.prListCalls++
		return []byte(out), nil
	}
	// gh api
	out := s.defaultAPIBody
	if s.apiCalls < len(s.apiOutputs) {
		out = s.apiOutputs[s.apiCalls]
	}
	s.apiCalls++
	return []byte(out), nil
}

func (s *scriptedRunner) Runner() CommandRunner { return s.run }

func TestPRCommentIncremental(t *testing.T) {
	ResetCursorRegressionCount()

	// First run: gh pr list returns PRs 1 and 2. gh api returns one comment each.
	comment1 := samplePRCommentJSON("comment for pr 1 about a race", "@@ hunk1", "pkg/x.go", "https://github.com/org/repo/pull/1#r1", "user1")
	comment2 := samplePRCommentJSON("comment for pr 2 about an invariant", "@@ hunk2", "pkg/x.go", "https://github.com/org/repo/pull/2#r2", "user2")

	script := &scriptedRunner{
		prListOutputs: []string{"1\n2\n"},
		apiOutputs:    []string{comment1, comment2},
	}

	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"rationale","body":"fact 1","confidence":0.8}`,
			`{"kind":"invariant","body":"fact 2","confidence":0.8}`,
		},
	}

	miner := &PRCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "claude-haiku-4-5-20251001",
		RunCommand: script.Runner(),
	}

	facts, seen, err := miner.ExtractForFile(context.Background(), "pkg/x.go", nil)
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("first run: expected 2 facts, got %d", len(facts))
	}
	if len(seen) != 2 || seen[0] != 1 || seen[1] != 2 {
		t.Errorf("first run seen = %v, want [1 2]", seen)
	}

	firstRunLLMCalls := len(llm.getCalls())
	if firstRunLLMCalls != 2 {
		t.Errorf("first run LLM calls = %d, want 2", firstRunLLMCalls)
	}

	// Second run: same PRs returned by gh pr list. No new PRs, so zero LLM calls.
	script2 := &scriptedRunner{
		prListOutputs: []string{"1\n2\n"},
		// apiOutputs deliberately empty — if the miner tries to fetch, test fails.
	}
	miner.RunCommand = script2.Runner()

	facts2, seen2, err := miner.ExtractForFile(context.Background(), "pkg/x.go", seen)
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}
	if len(facts2) != 0 {
		t.Errorf("second run: expected 0 facts, got %d", len(facts2))
	}
	if len(seen2) != 2 {
		t.Errorf("second run seen = %v, want [1 2]", seen2)
	}

	// Total LLM calls must still be 2 — second run must NOT invoke LLM.
	if total := len(llm.getCalls()); total != 2 {
		t.Errorf("second run must produce 0 LLM calls; total across both runs = %d, want 2", total)
	}
}

func TestPRCommentForceRemine(t *testing.T) {
	ResetCursorRegressionCount()

	comment1 := samplePRCommentJSON("force remine comment 1", "@@ h1", "f.go", "https://github.com/org/repo/pull/1#r1", "u")
	comment2 := samplePRCommentJSON("force remine comment 2", "@@ h2", "f.go", "https://github.com/org/repo/pull/2#r2", "u")

	newRunner := func() CommandRunner {
		s := &scriptedRunner{
			prListOutputs: []string{"1\n2\n"},
			apiOutputs:    []string{comment1, comment2},
		}
		return s.Runner()
	}

	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"rationale","body":"r1","confidence":0.8}`,
			`{"kind":"rationale","body":"r2","confidence":0.8}`,
			// Force-remine simulated: cursor cleared, runner reset.
			`{"kind":"rationale","body":"r3","confidence":0.8}`,
			`{"kind":"rationale","body":"r4","confidence":0.8}`,
		},
	}

	miner := &PRCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "claude-haiku-4-5-20251001",
		RunCommand: newRunner(),
	}

	facts1, seen, err := miner.ExtractForFile(context.Background(), "f.go", nil)
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}
	if len(facts1) != 2 {
		t.Fatalf("first run: expected 2 facts, got %d", len(facts1))
	}

	// Simulate --force-remine by passing sinceCursor=nil again with a fresh runner.
	miner.RunCommand = newRunner()
	facts2, seen2, err := miner.ExtractForFile(context.Background(), "f.go", nil)
	if err != nil {
		t.Fatalf("force-remine run error: %v", err)
	}
	if len(facts2) != len(facts1) {
		t.Errorf("force-remine facts = %d, want %d (same as first run)", len(facts2), len(facts1))
	}
	if len(seen2) != len(seen) {
		t.Errorf("force-remine seen = %v, want %v", seen2, seen)
	}

	// Total LLM calls must be 4 (2 per run).
	if total := len(llm.getCalls()); total != 4 {
		t.Errorf("force-remine total LLM calls = %d, want 4", total)
	}
}

func TestTribalIncrementalCursorMonotonicity(t *testing.T) {
	ResetCursorRegressionCount()

	// Prior cursor has PRs up to #20. gh pr list now only returns #5 (max<20).
	script := &scriptedRunner{
		prListOutputs: []string{"5\n"},
	}

	llm := &mockLLMClient{} // must never be called

	miner := &PRCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "claude-haiku-4-5-20251001",
		RunCommand: script.Runner(),
	}

	facts, seen, err := miner.ExtractForFile(context.Background(), "pkg/y.go", []int{10, 20})
	if err == nil {
		t.Fatal("expected ErrCursorRegression, got nil")
	}
	if !errors.Is(err, ErrCursorRegression) {
		t.Errorf("expected ErrCursorRegression, got %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("expected 0 facts on regression, got %d", len(facts))
	}
	if seen != nil {
		t.Errorf("expected nil seenPRs on regression, got %v", seen)
	}
	if CursorRegressionCount() != 1 {
		t.Errorf("CursorRegressionCount = %d, want 1", CursorRegressionCount())
	}
	if len(llm.getCalls()) != 0 {
		t.Errorf("LLM calls on regression = %d, want 0", len(llm.getCalls()))
	}
}
