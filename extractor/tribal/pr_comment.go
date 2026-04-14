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

// fetchComments calls `gh api` to retrieve PR review comments for the given file.
func (m *PRCommentMiner) fetchComments(ctx context.Context, filePath string) ([]PRComment, error) {
	runner := m.RunCommand
	if runner == nil {
		runner = defaultCommandRunner
	}

	endpoint := fmt.Sprintf("repos/%s/%s/pulls/comments", m.RepoOwner, m.RepoName)
	jqFilter := fmt.Sprintf(`.[] | select(.path == %q)`, filePath)

	output, err := runner(ctx, "gh", "api", endpoint, "--paginate", "-q", jqFilter)
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil, nil
	}

	// gh with -q and select outputs one JSON object per line (newline-delimited JSON).
	// Wrap them into a JSON array for unmarshalling.
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
