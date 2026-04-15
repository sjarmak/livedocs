// Package tribal provides extractors for tribal knowledge from source code.
package tribal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/live-docs/live_docs/db"
)

const (
	prCommentExtractorName    = "pr_comment_miner"
	prCommentExtractorVersion = "0.1.0"
)

// ErrBudgetExceeded is returned when the daily LLM call budget has been reached.
var ErrBudgetExceeded = errors.New("daily LLM call budget exceeded")

// LLMClient abstracts the LLM API so tests can inject a mock.
// This mirrors semantic.LLMClient but is defined locally to avoid
// a dependency on the semantic package.
type LLMClient interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// CommandRunner executes an external command and returns its combined stdout.
// The default implementation uses os/exec. Tests inject a mock.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// defaultCommandRunner shells out to the named binary.
func defaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// PRComment represents a single PR review comment from the GitHub API.
type PRComment struct {
	Body      string    `json:"body"`
	DiffHunk  string    `json:"diff_hunk"`
	Path      string    `json:"path"`
	HTMLURL   string    `json:"html_url"`
	User      prUser    `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

type prUser struct {
	Login string `json:"login"`
}

// classificationResult is the expected JSON response from the LLM.
type classificationResult struct {
	Kind       string  `json:"kind"`
	Body       string  `json:"body"`
	Confidence float64 `json:"confidence"`
}

// validClassificationKinds is the set of non-null classification kinds.
var validClassificationKinds = map[string]bool{
	"rationale": true,
	"invariant": true,
	"quirk":     true,
}

// userPromptTmpl is the parsed text/template for the user prompt.
var userPromptTmpl = template.Must(template.New("prComment").Parse(prCommentUserPromptTemplate))

// PRCommentMiner fetches GitHub PR review comments via `gh api`, redacts PII,
// classifies each comment via LLM, and produces TribalFact entries with full
// provenance.
type PRCommentMiner struct {
	// RepoOwner is the GitHub repository owner (e.g. "kubernetes").
	RepoOwner string
	// RepoName is the GitHub repository name (e.g. "kubernetes").
	RepoName string
	// Client is the LLM client used for comment classification.
	Client LLMClient
	// Model is the model identifier stored in fact provenance.
	Model string
	// DailyBudget is the maximum number of LLM calls per day. Zero means unlimited.
	DailyBudget int

	// RunCommand is the command runner. If nil, defaultCommandRunner is used.
	RunCommand CommandRunner

	mu        sync.Mutex
	callCount int
}

// ExtractForFile fetches PR review comments for the given file path,
// classifies each one via LLM (with PII redaction), and returns tribal facts.
// The symbolID is used as the subject_id for all produced facts.
func (m *PRCommentMiner) ExtractForFile(ctx context.Context, filePath string, symbolID int64) ([]db.TribalFact, error) {
	comments, err := m.fetchComments(ctx, filePath)
	if err != nil {
		return nil, fmt.Errorf("pr comment miner: fetch comments for %s: %w", filePath, err)
	}

	if len(comments) == 0 {
		return nil, nil
	}

	var facts []db.TribalFact
	for _, comment := range comments {
		// Check budget before making an LLM call.
		if err := m.checkBudget(); err != nil {
			return facts, err
		}

		fact, err := m.classifyComment(ctx, filePath, symbolID, comment)
		if err != nil {
			return facts, fmt.Errorf("pr comment miner: classify comment %s: %w", comment.HTMLURL, err)
		}
		if fact != nil {
			facts = append(facts, *fact)
		}
	}

	return facts, nil
}

// maxPRsPerFile is the maximum number of PRs to scan for comments per file.
// Bounded to avoid excessive API calls on files touched by many PRs.
const maxPRsPerFile = 10

// fetchComments retrieves PR review comments for the given file.
// Strategy: find merged PRs that touched this file (bounded), then fetch
// review comments from each PR filtered by path. This avoids the O(all-PRs)
// problem of paginating repos/{owner}/{repo}/pulls/comments.
func (m *PRCommentMiner) fetchComments(ctx context.Context, filePath string) ([]PRComment, error) {
	runner := m.RunCommand
	if runner == nil {
		runner = defaultCommandRunner
	}

	// Step 1: Find recent merged PRs that touched this file via gh pr list.
	prNumbers, err := m.findPRsForFile(ctx, runner, filePath)
	if err != nil {
		return nil, fmt.Errorf("find PRs for %s: %w", filePath, err)
	}
	if len(prNumbers) == 0 {
		return nil, nil
	}

	// Step 2: For each PR, fetch review comments filtered to this file.
	var comments []PRComment
	for _, prNum := range prNumbers {
		prComments, err := m.fetchPRComments(ctx, runner, prNum, filePath)
		if err != nil {
			// Non-fatal: skip this PR and continue.
			continue
		}
		comments = append(comments, prComments...)
	}

	return comments, nil
}

// findPRsForFile uses `gh pr list` to find merged PRs that touched the given file.
func (m *PRCommentMiner) findPRsForFile(ctx context.Context, runner CommandRunner, filePath string) ([]int, error) {
	// Use GitHub search to find PRs that mention this file path.
	// gh pr list --search "filename:path" returns PRs touching that file.
	output, err := runner(ctx, "gh", "pr", "list",
		"--repo", fmt.Sprintf("%s/%s", m.RepoOwner, m.RepoName),
		"--state", "merged",
		"--limit", fmt.Sprintf("%d", maxPRsPerFile),
		"--json", "number",
		"-q", ".[].number",
		"--search", filePath,
	)
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil, nil
	}

	var numbers []int
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(line, "%d", &n); err == nil {
			numbers = append(numbers, n)
		}
	}
	return numbers, nil
}

// fetchPRComments fetches review comments from a single PR, filtered to the given file.
func (m *PRCommentMiner) fetchPRComments(ctx context.Context, runner CommandRunner, prNumber int, filePath string) ([]PRComment, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/comments", m.RepoOwner, m.RepoName, prNumber)
	jqFilter := fmt.Sprintf(`.[] | select(.path == %q)`, filePath)

	output, err := runner(ctx, "gh", "api", endpoint, "-q", jqFilter)
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil, nil
	}

	// gh with -q and select outputs one JSON object per line (newline-delimited JSON).
	lines := strings.Split(trimmed, "\n")
	var comments []PRComment
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var c PRComment
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("parse PR comment JSON: %w", err)
		}
		comments = append(comments, c)
	}
	return comments, nil
}

// checkBudget returns ErrBudgetExceeded if the daily budget has been reached.
func (m *PRCommentMiner) checkBudget() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.DailyBudget > 0 && m.callCount >= m.DailyBudget {
		return fmt.Errorf("%w: %d calls made, budget is %d",
			ErrBudgetExceeded, m.callCount, m.DailyBudget)
	}
	return nil
}

// incrementCallCount records an LLM call. Must be called after a successful
// budget check and before the actual LLM call.
func (m *PRCommentMiner) incrementCallCount() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
}

// classifyComment redacts PII, sends the comment to the LLM for classification,
// and returns a TribalFact if the classification is non-null.
func (m *PRCommentMiner) classifyComment(ctx context.Context, filePath string, symbolID int64, comment PRComment) (*db.TribalFact, error) {
	// Redact PII from both the comment body and the diff hunk BEFORE
	// sending to the LLM.
	redactedBody := RedactPII(comment.Body)
	redactedHunk := RedactPII(comment.DiffHunk)

	// Render the user prompt.
	var userPrompt bytes.Buffer
	err := userPromptTmpl.Execute(&userPrompt, struct {
		FilePath    string
		CommentBody string
		DiffHunk    string
	}{
		FilePath:    filePath,
		CommentBody: redactedBody,
		DiffHunk:    redactedHunk,
	})
	if err != nil {
		return nil, fmt.Errorf("render user prompt: %w", err)
	}

	// Increment call count before making the LLM call.
	m.incrementCallCount()

	// Call the LLM.
	response, err := m.Client.Complete(ctx, prCommentSystemPrompt, userPrompt.String())
	if err != nil {
		return nil, fmt.Errorf("LLM complete: %w", err)
	}

	// Parse the LLM response.
	var result classificationResult
	// Strip markdown code fences if present.
	cleaned := strings.TrimSpace(response)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		return nil, fmt.Errorf("parse LLM response: %w: raw=%q", err, response)
	}

	// Null classification means this comment is not tribal knowledge.
	if result.Kind == "null" || result.Kind == "" {
		return nil, nil
	}

	// Validate the classification kind.
	if !validClassificationKinds[result.Kind] {
		return nil, fmt.Errorf("invalid classification kind %q from LLM", result.Kind)
	}

	// Clamp confidence to < 1.0 for LLM-classified facts.
	confidence := result.Confidence
	if confidence >= 1.0 {
		confidence = 0.95
	}
	if confidence <= 0.0 {
		confidence = 0.1
	}

	now := time.Now().UTC().Format(time.RFC3339)
	hash := sha256Hash(comment.Body + comment.DiffHunk)

	fact := &db.TribalFact{
		SubjectID:        symbolID,
		Kind:             result.Kind,
		Body:             result.Body,
		SourceQuote:      comment.Body,
		Confidence:       confidence,
		Corroboration:    1,
		Extractor:        prCommentExtractorName,
		ExtractorVersion: prCommentExtractorVersion,
		Model:            m.Model,
		StalenessHash:    hash,
		Status:           "active",
		CreatedAt:        now,
		LastVerified:     now,
		Evidence: []db.TribalEvidence{
			{
				SourceType:  "pr_comment",
				SourceRef:   comment.HTMLURL,
				Author:      comment.User.Login,
				AuthoredAt:  comment.CreatedAt.Format(time.RFC3339),
				ContentHash: hash,
			},
		},
	}

	return fact, nil
}
