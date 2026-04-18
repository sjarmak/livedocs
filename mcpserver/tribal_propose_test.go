package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjarmak/livedocs/db"
)

// setupProposeTestDB creates a test pool with a repo DB containing tribal
// schema and an existing symbol + fact for correct/supersede tests.
func setupProposeTestDB(t *testing.T) (*DBPool, int64) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-repo.claims.db")

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

	// Insert a symbol.
	symID, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       "test-repo",
		ImportPath: "github.com/test/pkg",
		SymbolName: "MyHandler",
		Language:   "go",
		Kind:       "function",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}

	// Insert an existing fact for correct/supersede tests.
	factID, err := cdb.InsertTribalFact(db.TribalFact{
		SubjectID:        symID,
		Kind:             "rationale",
		Body:             "original design rationale",
		SourceQuote:      "// original quote",
		Confidence:       0.9,
		Corroboration:    1,
		Extractor:        "test",
		ExtractorVersion: "1.0",
		StalenessHash:    "orig_hash",
		Status:           "active",
		CreatedAt:        "2026-01-01T00:00:00Z",
		LastVerified:     "2026-01-01T00:00:00Z",
	}, []db.TribalEvidence{{
		SourceType:  "blame",
		SourceRef:   "handler.go:10",
		ContentHash: "ev_orig",
	}})
	if err != nil {
		t.Fatalf("insert existing fact: %v", err)
	}

	cdb.Close()

	pool := NewDBPool(tmpDir, 5)
	t.Cleanup(func() { pool.Close() })

	return pool, factID
}

// validEvidence returns a JSON string for a valid evidence array.
func validEvidence() string {
	return `[{"source_type":"correction","source_ref":"PR#100","content_hash":"abc123"}]`
}

func TestTribalProposeFact_SignedCreate(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol":          "MyHandler",
		"repo":            "test-repo",
		"kind":            "rationale",
		"body":            "new rationale from signed user",
		"source_quote":    "// evidence from PR",
		"evidence":        validEvidence(),
		"writer_identity": "alice@example.com",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp proposeFactResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.FactID <= 0 {
		t.Errorf("expected positive fact_id, got %d", resp.FactID)
	}
	if resp.Status != "active" {
		t.Errorf("expected status 'active' for signed proposal, got %q", resp.Status)
	}
	if resp.Action != "create" {
		t.Errorf("expected action 'create', got %q", resp.Action)
	}
}

func TestTribalProposeFact_UnsignedCreate(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol":       "MyHandler",
		"repo":         "test-repo",
		"kind":         "ownership",
		"body":         "owned by team-x",
		"source_quote": "CODEOWNERS line 5",
		"evidence":     validEvidence(),
		// No writer_identity — unsigned.
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp proposeFactResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Status != "quarantined" {
		t.Errorf("expected status 'quarantined' for unsigned proposal, got %q", resp.Status)
	}
}

func TestTribalProposeFact_MissingEvidence(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	// Empty array.
	req := &tribalFakeRequest{args: map[string]any{
		"symbol":       "MyHandler",
		"repo":         "test-repo",
		"kind":         "rationale",
		"body":         "some body",
		"source_quote": "some quote",
		"evidence":     "[]",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Fatal("expected error result for empty evidence")
	}
}

func TestTribalProposeFact_InvalidKind(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol":       "MyHandler",
		"repo":         "test-repo",
		"kind":         "nonexistent_kind",
		"body":         "some body",
		"source_quote": "some quote",
		"evidence":     validEvidence(),
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Fatal("expected error result for invalid kind")
	}
}

func TestTribalProposeFact_Supersede(t *testing.T) {
	pool, existingFactID := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol":          "MyHandler",
		"repo":            "test-repo",
		"kind":            "rationale",
		"body":            "updated rationale superseding old one",
		"source_quote":    "// new evidence from code review",
		"evidence":        validEvidence(),
		"writer_identity": "bob@example.com",
		"action":          "supersede",
		"fact_id":         float64(existingFactID),
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp proposeFactResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Action != "supersede" {
		t.Errorf("expected action 'supersede', got %q", resp.Action)
	}
	if resp.FactID <= 0 {
		t.Errorf("expected positive new fact_id, got %d", resp.FactID)
	}
	if resp.FactID == existingFactID {
		t.Error("new fact_id should differ from old fact_id")
	}

	// Verify the old fact is now superseded.
	cdb, err := pool.Open("test-repo")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	var oldStatus string
	err = cdb.DB().QueryRow(
		"SELECT status FROM tribal_facts WHERE id = ?", existingFactID,
	).Scan(&oldStatus)
	if err != nil {
		t.Fatalf("query old fact: %v", err)
	}
	if oldStatus != "superseded" {
		t.Errorf("old fact status: got %q, want 'superseded'", oldStatus)
	}

	// Verify a correction row was recorded.
	var corrAction string
	err = cdb.DB().QueryRow(
		"SELECT action FROM tribal_corrections WHERE fact_id = ?", existingFactID,
	).Scan(&corrAction)
	if err != nil {
		t.Fatalf("query correction: %v", err)
	}
	if corrAction != "supersede" {
		t.Errorf("correction action: got %q, want 'supersede'", corrAction)
	}
}

func TestTribalProposeFact_MissingRequiredParams(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	requiredParams := []string{"symbol", "repo", "kind", "body", "source_quote", "evidence"}
	baseArgs := map[string]any{
		"symbol":       "MyHandler",
		"repo":         "test-repo",
		"kind":         "rationale",
		"body":         "body",
		"source_quote": "quote",
		"evidence":     validEvidence(),
	}

	for _, param := range requiredParams {
		t.Run("missing_"+param, func(t *testing.T) {
			args := make(map[string]any)
			for k, v := range baseArgs {
				if k != param {
					args[k] = v
				}
			}
			req := &tribalFakeRequest{args: args}
			result, err := handler(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.IsError() {
				t.Errorf("expected error for missing %q", param)
			}
		})
	}
}

func TestTribalProposeFact_Correct(t *testing.T) {
	pool, existingFactID := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol":          "MyHandler",
		"repo":            "test-repo",
		"kind":            "rationale",
		"body":            "corrected rationale",
		"source_quote":    "// corrected quote",
		"evidence":        validEvidence(),
		"writer_identity": "carol@example.com",
		"action":          "correct",
		"fact_id":         float64(existingFactID),
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp proposeFactResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Action != "correct" {
		t.Errorf("expected action 'correct', got %q", resp.Action)
	}
	if resp.FactID <= 0 {
		t.Errorf("expected positive new fact_id, got %d", resp.FactID)
	}

	// Verify a correction row was recorded.
	cdb, err := pool.Open("test-repo")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	var corrAction string
	err = cdb.DB().QueryRow(
		"SELECT action FROM tribal_corrections WHERE fact_id = ?", existingFactID,
	).Scan(&corrAction)
	if err != nil {
		t.Fatalf("query correction: %v", err)
	}
	if corrAction != "correct" {
		t.Errorf("correction action: got %q, want 'correct'", corrAction)
	}
}

func TestTribalProposeFact_NewSymbolCreated(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	// Propose a fact for a symbol that doesn't exist yet.
	req := &tribalFakeRequest{args: map[string]any{
		"symbol":          "BrandNewSymbol",
		"repo":            "test-repo",
		"kind":            "quirk",
		"body":            "quirky behavior",
		"source_quote":    "// QUIRK: weird thing",
		"evidence":        validEvidence(),
		"writer_identity": "dave@example.com",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp proposeFactResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.FactID <= 0 {
		t.Errorf("expected positive fact_id, got %d", resp.FactID)
	}
	if resp.Status != "active" {
		t.Errorf("expected status 'active', got %q", resp.Status)
	}
}

func TestTribalProposeFact_EvidenceMissingFields(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	tests := []struct {
		name     string
		evidence string
	}{
		{"missing_source_type", `[{"source_ref":"ref","content_hash":"h"}]`},
		{"missing_source_ref", `[{"source_type":"blame","content_hash":"h"}]`},
		{"missing_content_hash", `[{"source_type":"blame","source_ref":"ref"}]`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &tribalFakeRequest{args: map[string]any{
				"symbol":       "MyHandler",
				"repo":         "test-repo",
				"kind":         "rationale",
				"body":         "body",
				"source_quote": "quote",
				"evidence":     tc.evidence,
			}}
			result, err := handler(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.IsError() {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func TestTribalProposeFact_UnknownRepoRejected(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol":          "MyHandler",
		"repo":            "nonexistent-repo",
		"kind":            "rationale",
		"body":            "some body",
		"source_quote":    "some quote",
		"evidence":        validEvidence(),
		"writer_identity": "alice@example.com",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Fatal("expected error result for unknown repo")
	}
	if got := result.Text(); !strings.Contains(got, "not found") {
		t.Errorf("error message should mention 'not found', got: %s", got)
	}
}

func TestTribalProposeFact_UnknownRepoNoFileCreated(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol":          "MyHandler",
		"repo":            "attacker-repo",
		"kind":            "rationale",
		"body":            "some body",
		"source_quote":    "some quote",
		"evidence":        validEvidence(),
		"writer_identity": "alice@example.com",
	}}

	handler(context.Background(), req)

	dbPath := filepath.Join(pool.DataDir(), "attacker-repo.claims.db")
	if _, err := os.Stat(dbPath); err == nil {
		t.Fatal("claims.db file should NOT have been created for unknown repo")
	}
}

func TestTribalProposeFact_KnownRepoAccepted(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol":          "MyHandler",
		"repo":            "test-repo",
		"kind":            "rationale",
		"body":            "known repo fact",
		"source_quote":    "// quote",
		"evidence":        validEvidence(),
		"writer_identity": "alice@example.com",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("known repo should be accepted, got error: %s", result.Text())
	}

	var resp proposeFactResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.FactID <= 0 {
		t.Errorf("expected positive fact_id, got %d", resp.FactID)
	}
}

func TestTribalProposeFact_BodyTooLong(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	longBody := strings.Repeat("x", db.MaxBodyBytes+1)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol":          "MyHandler",
		"repo":            "test-repo",
		"kind":            "rationale",
		"body":            longBody,
		"source_quote":    "some quote",
		"evidence":        validEvidence(),
		"writer_identity": "alice@example.com",
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Fatal("expected error result for oversized body")
	}
	if got := result.Text(); !strings.Contains(got, "exceeds maximum") {
		t.Errorf("error message should mention 'exceeds maximum', got: %s", got)
	}
}

func TestTribalProposeFact_SupersedeWithoutFactID(t *testing.T) {
	pool, _ := setupProposeTestDB(t)
	handler := tribalProposeFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol":       "MyHandler",
		"repo":         "test-repo",
		"kind":         "rationale",
		"body":         "some body",
		"source_quote": "some quote",
		"evidence":     validEvidence(),
		"action":       "supersede",
		// No fact_id.
	}}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Fatal("expected error for supersede without fact_id")
	}
}
