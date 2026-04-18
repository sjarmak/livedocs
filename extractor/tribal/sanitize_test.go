package tribal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestSanitizeUpsertError_CanonicalCategories verifies that sanitizeUpsertError
// maps heterogeneous raw errors — including those with leaky payloads — to a
// fixed, finite set of canonical category strings. The contract is: whatever
// the caller sees MUST be one of the canonical categories, never the raw
// error text. This is what lets us guarantee no SQLite schema details,
// offending values, LLM echo, file paths, or symbol names reach callers.
func TestSanitizeUpsertError_CanonicalCategories(t *testing.T) {
	// canonicalCategories is the exact set sanitizeUpsertError may return.
	// Adding a new category is a conscious API change; the test pins the
	// set to catch accidental drift.
	canonicalCategories := map[string]struct{}{
		"nil_error":                    {},
		"unique_constraint_violation":  {},
		"check_constraint_violation":   {},
		"foreign_key_constraint":       {},
		"not_null_constraint":          {},
		"database_busy":                {},
		"database_locked":              {},
		"database_error":               {},
		"provenance_validation_failed": {},
		"context_canceled":             {},
		"upsert_failed":                {},
	}

	tests := []struct {
		name string
		in   error
		// want is either exact category expectation (non-empty) or the
		// empty string meaning "any canonical category is acceptable".
		want string
	}{
		{
			name: "nil error maps to nil_error",
			in:   nil,
			want: "nil_error",
		},
		{
			// The leakiest real-world case: SQLite UNIQUE constraint error
			// carries table + column names AND the offending value.
			name: "sqlite UNIQUE constraint with table/column and user value",
			in:   errors.New("UNIQUE constraint failed: tribal_facts.subject_id, tribal_facts.kind, tribal_facts.source_quote_hash: body='must acquire /etc/passwd lock'"),
			want: "unique_constraint_violation",
		},
		{
			name: "sqlite CHECK constraint with column",
			in:   errors.New("CHECK constraint failed: tribal_facts_confidence_range"),
			want: "check_constraint_violation",
		},
		{
			name: "sqlite FOREIGN KEY constraint",
			in:   errors.New("FOREIGN KEY constraint failed: tribal_evidence(subject_id) references symbols(id)"),
			want: "foreign_key_constraint",
		},
		{
			name: "sqlite NOT NULL constraint",
			in:   errors.New("NOT NULL constraint failed: tribal_facts.source_quote"),
			want: "not_null_constraint",
		},
		{
			name: "sqlite busy error",
			in:   errors.New("database is busy"),
			want: "database_busy",
		},
		{
			name: "sqlite locked error",
			in:   errors.New("database table is locked: tribal_facts"),
			want: "database_locked",
		},
		{
			// LLM echo: the error wraps a huge blob of model-generated JSON
			// containing user repo content. Sanitizer must not pass through.
			name: "wrapped error with raw LLM JSON echo",
			in:   fmt.Errorf("upsert: %w", errors.New(`{"kind":"invariant","body":"secret=SKbw9_LONG_API_KEY_LEAKED","confidence":0.9}`)),
			want: "upsert_failed",
		},
		{
			name: "context canceled",
			in:   context.Canceled,
			want: "context_canceled",
		},
		{
			name: "context deadline exceeded",
			in:   context.DeadlineExceeded,
			want: "context_canceled",
		},
		{
			name: "unknown wrapped error",
			in:   fmt.Errorf("wrap: %w", errors.New("something arbitrary")),
			want: "upsert_failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeUpsertError(tt.in)
			if got == "" {
				t.Fatalf("sanitizeUpsertError returned empty string")
			}
			if _, ok := canonicalCategories[got]; !ok {
				t.Fatalf("sanitizeUpsertError returned non-canonical category %q (want one of %v)",
					got, keysOf(canonicalCategories))
			}
			if tt.want != "" && got != tt.want {
				t.Errorf("sanitizeUpsertError = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSanitizeUpsertError_NeverLeaksRawContent is the core security assertion:
// for a wide range of inputs that carry sensitive payloads (table names,
// offending values, absolute paths, symbol names, LLM content, secrets),
// the sanitized output must NOT contain any of those substrings.
func TestSanitizeUpsertError_NeverLeaksRawContent(t *testing.T) {
	// Each input embeds a unique leak marker. If any marker appears in the
	// sanitized output, we know a literal substring of the raw error leaked.
	leakInputs := []struct {
		name    string
		err     error
		markers []string
	}{
		{
			name: "sqlite constraint leaks column+value",
			err:  errors.New("UNIQUE constraint failed: tribal_facts.source_quote_hash with value 'CANARY_TABLE_LEAK'"),
			markers: []string{
				"tribal_facts",
				"source_quote_hash",
				"CANARY_TABLE_LEAK",
			},
		},
		{
			name: "absolute file path in error",
			err:  errors.New("upsert failed for /home/user/secret/CANARY_PATH_LEAK.go"),
			markers: []string{
				"/home/user/secret",
				"CANARY_PATH_LEAK",
			},
		},
		{
			name: "symbol name in error",
			err:  errors.New("symbol CANARY_SYMBOL_LEAK_HandleAuthToken not found"),
			markers: []string{
				"CANARY_SYMBOL_LEAK_HandleAuthToken",
			},
		},
		{
			name: "LLM JSON echo with API key",
			err:  errors.New(`{"body":"sk-CANARY_APIKEY_LEAK_abc123","kind":"quirk"}`),
			markers: []string{
				"CANARY_APIKEY_LEAK",
				"sk-",
			},
		},
		{
			name: "raw SQL in error",
			err:  errors.New("INSERT INTO tribal_facts VALUES ('CANARY_SQL_LEAK', 'invariant')"),
			markers: []string{
				"CANARY_SQL_LEAK",
				"INSERT INTO",
				"tribal_facts",
			},
		},
	}

	for _, tt := range leakInputs {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeUpsertError(tt.err)
			for _, marker := range tt.markers {
				if strings.Contains(got, marker) {
					t.Errorf("sanitized output leaked marker %q: got %q", marker, got)
				}
			}
		})
	}
}

// TestMineFile_FailedErrorsAreSanitizedStrings verifies the service-layer
// integration: when upsert fails, MiningResult.FailedErrors (now []string)
// contains canonical category strings, never raw error text. This is the
// boundary test — even if a new error type slips past the helper, this
// catches it because no raw marker should appear in the result.
func TestMineFile_FailedErrorsAreSanitizedStrings(t *testing.T) {
	cdb := newTestClaimsDB(t)
	comment := PRComment{
		Body:     "must acquire lock",
		DiffHunk: "@@",
		Path:     "pkg/x.go",
		HTMLURL:  "https://github.com/org/repo/pull/1#r1",
		User:     prUser{Login: "r"},
	}
	commentJSON, err := json.Marshal(comment)
	if err != nil {
		t.Fatalf("marshal comment: %v", err)
	}

	runner := &mockRunnerRecording{
		prList:  "1\n",
		apiResp: string(commentJSON),
	}
	llm := &mockLLMClient{
		responses: []string{
			`{"kind":"invariant","body":"must acquire lock","confidence":0.9}`,
		},
	}
	miner := &prCommentMiner{
		RepoOwner:  "org",
		RepoName:   "repo",
		Client:     llm,
		Model:      "test",
		RunCommand: runner.run,
	}

	// Simulate a realistic SQLite error with a leaky payload.
	rawErr := errors.New(
		"UNIQUE constraint failed: tribal_facts.subject_id, tribal_facts.kind: " +
			"offending row body='SECRET_CANARY_LEAK pkg/x.go::HandleAuth'",
	)
	svc := newServiceWithMiner(cdb, miner, "repo",
		withFactUpserter(&failingUpserter{err: rawErr}),
	)

	result, err := svc.MineFile(context.Background(), "pkg/x.go", TriggerBatchSchedule)
	if err != nil {
		t.Fatalf("MineFile: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.FailedCount != 1 {
		t.Errorf("FailedCount = %d, want 1", result.FailedCount)
	}
	if len(result.FailedErrors) != 1 {
		t.Fatalf("FailedErrors len = %d, want 1", len(result.FailedErrors))
	}

	sanitized := result.FailedErrors[0]
	if sanitized == "" {
		t.Fatal("sanitized error is empty string")
	}
	// Must not leak any part of the raw SQL error.
	for _, leak := range []string{
		"tribal_facts",
		"subject_id",
		"SECRET_CANARY_LEAK",
		"pkg/x.go",
		"HandleAuth",
		"offending row",
	} {
		if strings.Contains(sanitized, leak) {
			t.Errorf("sanitized error leaked %q: got %q", leak, sanitized)
		}
	}
	// It should still classify correctly.
	if sanitized != "unique_constraint_violation" {
		t.Errorf("sanitized = %q, want unique_constraint_violation", sanitized)
	}
}

// TestMineFile_FailedCountPreservedAcrossSanitization verifies the
// count-vs-slice invariant survives the []error → []string change:
// FailedCount is the true total (uncapped); len(FailedErrors) is bounded
// by maxFailedErrorsCaptured; for N <= cap they are equal.
func TestMineFile_FailedCountPreservedAcrossSanitization(t *testing.T) {
	cdb := newTestClaimsDB(t)
	// N failures under the cap.
	const n = 5
	comments := make([]PRComment, n)
	responses := make([]string, n)
	for i := 0; i < n; i++ {
		comments[i] = PRComment{
			Body:     fmt.Sprintf("c%d", i),
			DiffHunk: "@@",
			Path:     "pkg/a.go",
			HTMLURL:  fmt.Sprintf("https://github.com/org/repo/pull/1#r%d", i),
			User:     prUser{Login: "r"},
		}
		responses[i] = fmt.Sprintf(`{"kind":"rationale","body":"b%d","confidence":0.9}`, i)
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
		withFactUpserter(&failingUpserter{err: errors.New("NOT NULL constraint failed: tribal_facts.body")}),
	)

	result, err := svc.MineFile(context.Background(), "pkg/a.go", TriggerBatchSchedule)
	if err != nil {
		t.Fatalf("MineFile: %v", err)
	}

	if result.FailedCount != n {
		t.Errorf("FailedCount = %d, want %d", result.FailedCount, n)
	}
	if len(result.FailedErrors) != n {
		t.Errorf("FailedErrors len = %d, want %d (all under cap)", len(result.FailedErrors), n)
	}
	for i, s := range result.FailedErrors {
		if s == "" {
			t.Errorf("FailedErrors[%d] is empty string", i)
		}
		// None should leak the table or column name.
		if strings.Contains(s, "tribal_facts") || strings.Contains(s, "body") {
			t.Errorf("FailedErrors[%d] leaked raw DB text: %q", i, s)
		}
	}
}

// TestMineFile_FailedErrorsCapStillApplies ensures the retention cap
// (maxFailedErrorsCaptured) still bounds the sanitized-string slice, while
// FailedCount continues to reflect the true total.
func TestMineFile_FailedErrorsCapStillApplies(t *testing.T) {
	cdb := newTestClaimsDB(t)
	const n = maxFailedErrorsCaptured + 5
	comments := make([]PRComment, n)
	responses := make([]string, n)
	for i := 0; i < n; i++ {
		comments[i] = PRComment{
			Body:     fmt.Sprintf("c%d", i),
			DiffHunk: "@@",
			Path:     "pkg/big.go",
			HTMLURL:  fmt.Sprintf("https://github.com/org/repo/pull/1#r%d", i),
			User:     prUser{Login: "r"},
		}
		responses[i] = fmt.Sprintf(`{"kind":"rationale","body":"b%d","confidence":0.9}`, i)
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
		t.Errorf("FailedCount = %d, want %d (uncapped)", result.FailedCount, n)
	}
	if len(result.FailedErrors) != maxFailedErrorsCaptured {
		t.Errorf("FailedErrors len = %d, want cap %d",
			len(result.FailedErrors), maxFailedErrorsCaptured)
	}
}

// TestMiningResult_FailedErrorsFieldType is a compile-time-ish check that the
// field is []string (not []error). We do this at runtime because Go reflection
// in tests is the natural way to assert the shape of an exported struct field
// without introducing a compile dependency into the test file that would mask
// the type change itself.
func TestMiningResult_FailedErrorsFieldType(t *testing.T) {
	mr := MiningResult{
		FailedErrors: []string{"database_error"}, // must compile as []string
	}
	// Also verify zero-value remains nil slice (not pre-allocated).
	empty := MiningResult{}
	if empty.FailedErrors != nil {
		t.Errorf("zero-value FailedErrors should be nil, got %v", empty.FailedErrors)
	}
	if len(mr.FailedErrors) != 1 {
		t.Fatalf("test construction failed")
	}
	// Using string method must compile.
	if mr.FailedErrors[0] != "database_error" {
		t.Errorf("string access broken")
	}
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
