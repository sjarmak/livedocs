package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/live-docs/live_docs/db"

	_ "modernc.org/sqlite"
)

// setupTribalTestDB creates a test database with tribal schema, a symbol,
// and seeded tribal facts with evidence. Returns the pool and the temp dir path.
func setupTribalTestDB(t *testing.T) *DBPool {
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
		SymbolName: "NewServer",
		Language:   "go",
		Kind:       "function",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}

	// Insert ownership fact.
	_, err = cdb.InsertTribalFact(db.TribalFact{
		SubjectID:        symID,
		Kind:             "ownership",
		Body:             "Team Platform owns NewServer",
		SourceQuote:      "// OWNER: platform-team",
		Confidence:       0.95,
		Corroboration:    2,
		Extractor:        "codeowners_miner",
		ExtractorVersion: "v0.1",
		Model:            "claude-haiku-4-5-20251001",
		StalenessHash:    "abc123",
		Status:           "active",
		CreatedAt:        "2026-04-01T00:00:00Z",
		LastVerified:     "2026-04-08T00:00:00Z",
	}, []db.TribalEvidence{
		{
			SourceType:  "codeowners",
			SourceRef:   "CODEOWNERS:42",
			Author:      "alice",
			AuthoredAt:  "2026-03-15T00:00:00Z",
			ContentHash: "hash1",
		},
	})
	if err != nil {
		t.Fatalf("insert ownership fact: %v", err)
	}

	// Insert rationale fact.
	_, err = cdb.InsertTribalFact(db.TribalFact{
		SubjectID:        symID,
		Kind:             "rationale",
		Body:             "NewServer uses connection pooling because of thundering herd",
		SourceQuote:      "connection pooling prevents thundering herd on restart",
		Confidence:       0.72,
		Corroboration:    1,
		Extractor:        "pr_comment_miner",
		ExtractorVersion: "v0.1",
		Model:            "claude-haiku-4-5-20251001",
		StalenessHash:    "def456",
		Status:           "active",
		CreatedAt:        "2026-04-02T00:00:00Z",
		LastVerified:     "2026-04-08T00:00:00Z",
	}, []db.TribalEvidence{
		{
			SourceType:  "pr_comment",
			SourceRef:   "https://github.com/test/repo/pull/42#comment-1",
			Author:      "bob",
			AuthoredAt:  "2026-03-20T00:00:00Z",
			ContentHash: "hash2",
		},
	})
	if err != nil {
		t.Fatalf("insert rationale fact: %v", err)
	}

	// Insert invariant fact.
	_, err = cdb.InsertTribalFact(db.TribalFact{
		SubjectID:        symID,
		Kind:             "invariant",
		Body:             "NewServer must always bind to localhost in test mode",
		SourceQuote:      "// INVARIANT: test mode binds localhost only",
		Confidence:       0.85,
		Corroboration:    3,
		Extractor:        "inline_marker_miner",
		ExtractorVersion: "v0.2",
		Model:            "",
		StalenessHash:    "ghi789",
		Status:           "active",
		CreatedAt:        "2026-04-03T00:00:00Z",
		LastVerified:     "2026-04-09T00:00:00Z",
	}, []db.TribalEvidence{
		{
			SourceType:  "inline_marker",
			SourceRef:   "server.go:15",
			Author:      "",
			AuthoredAt:  "",
			ContentHash: "hash3",
		},
	})
	if err != nil {
		t.Fatalf("insert invariant fact: %v", err)
	}

	// Insert a low-confidence quirk fact.
	_, err = cdb.InsertTribalFact(db.TribalFact{
		SubjectID:        symID,
		Kind:             "quirk",
		Body:             "NewServer has an undocumented debug flag",
		SourceQuote:      "// QUIRK: debug flag not in docs",
		Confidence:       0.3,
		Corroboration:    1,
		Extractor:        "commit_msg_miner",
		ExtractorVersion: "v0.1",
		Model:            "claude-haiku-4-5-20251001",
		StalenessHash:    "jkl012",
		Status:           "active",
		CreatedAt:        "2026-04-04T00:00:00Z",
		LastVerified:     "2026-04-09T00:00:00Z",
	}, []db.TribalEvidence{
		{
			SourceType:  "commit_msg",
			SourceRef:   "abc123def",
			Author:      "charlie",
			AuthoredAt:  "2026-03-25T00:00:00Z",
			ContentHash: "hash4",
		},
	})
	if err != nil {
		t.Fatalf("insert quirk fact: %v", err)
	}

	cdb.Close()

	pool := NewDBPool(tmpDir, 5)
	t.Cleanup(func() { pool.Close() })

	return pool
}

// tribalFakeRequest implements ToolRequest for testing tribal tools.
type tribalFakeRequest struct {
	args map[string]any
}

func (r *tribalFakeRequest) GetString(key, defaultValue string) string {
	if v, ok := r.args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return defaultValue
}

func (r *tribalFakeRequest) RequireString(key string) (string, error) {
	if v, ok := r.args[key]; ok {
		if s, ok := v.(string); ok {
			return s, nil
		}
	}
	return "", fmt.Errorf("missing required parameter %q", key)
}

func (r *tribalFakeRequest) GetInt(key string, defaultValue int) int {
	if v, ok := r.args[key]; ok {
		if n, ok := v.(int); ok {
			return n
		}
	}
	return defaultValue
}

func (r *tribalFakeRequest) RequireInt(key string) (int, error) {
	if v, ok := r.args[key]; ok {
		if n, ok := v.(int); ok {
			return n, nil
		}
	}
	return 0, fmt.Errorf("missing required parameter %q", key)
}

func (r *tribalFakeRequest) GetFloat(key string, defaultValue float64) float64 {
	if v, ok := r.args[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		}
	}
	return defaultValue
}

func (r *tribalFakeRequest) RequireFloat(key string) (float64, error) {
	if v, ok := r.args[key]; ok {
		if n, ok := v.(float64); ok {
			return n, nil
		}
	}
	return 0, fmt.Errorf("missing required parameter %q", key)
}

func (r *tribalFakeRequest) GetBool(key string, defaultValue bool) bool {
	if v, ok := r.args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return defaultValue
}

func (r *tribalFakeRequest) RequireBool(key string) (bool, error) {
	if v, ok := r.args[key]; ok {
		if b, ok := v.(bool); ok {
			return b, nil
		}
	}
	return false, fmt.Errorf("missing required parameter %q", key)
}

func (r *tribalFakeRequest) GetArguments() map[string]any {
	return r.args
}

func TestTribalContextForSymbol_AllFacts(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalContextForSymbolHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{"symbol": "NewServer"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp tribalResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Total != 4 {
		t.Errorf("expected 4 facts, got %d", resp.Total)
	}
	if resp.Symbol != "NewServer" {
		t.Errorf("expected symbol 'NewServer', got %q", resp.Symbol)
	}

	// Verify provenance fields are present on every fact.
	for i, fact := range resp.Facts {
		if fact.Body == "" {
			t.Errorf("fact[%d]: body is empty", i)
		}
		if fact.SourceQuote == "" {
			t.Errorf("fact[%d]: source_quote is empty", i)
		}
		if fact.Kind == "" {
			t.Errorf("fact[%d]: kind is empty", i)
		}
		if fact.Status == "" {
			t.Errorf("fact[%d]: status is empty", i)
		}
		if len(fact.Evidence) == 0 {
			t.Errorf("fact[%d]: evidence is empty", i)
		}
		if fact.Extractor == "" {
			t.Errorf("fact[%d]: extractor is empty", i)
		}
		if fact.LastVerified == "" {
			t.Errorf("fact[%d]: last_verified is empty", i)
		}
	}
}

func TestTribalContextForSymbol_KindsFilter(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalContextForSymbolHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol": "NewServer",
		"kinds":  "ownership,rationale",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp tribalResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Total != 2 {
		t.Errorf("expected 2 facts (ownership+rationale), got %d", resp.Total)
	}

	for _, fact := range resp.Facts {
		if fact.Kind != "ownership" && fact.Kind != "rationale" {
			t.Errorf("unexpected kind %q, expected ownership or rationale", fact.Kind)
		}
	}
}

func TestTribalContextForSymbol_MinConfidence(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalContextForSymbolHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol":         "NewServer",
		"min_confidence": 0.7,
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp tribalResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	// Should exclude the quirk fact (0.3 confidence) and include the other 3.
	if resp.Total != 3 {
		t.Errorf("expected 3 facts with min_confidence=0.7, got %d", resp.Total)
	}

	for _, fact := range resp.Facts {
		if fact.Confidence < 0.7 {
			t.Errorf("fact with confidence %f should have been filtered (min=0.7)", fact.Confidence)
		}
	}
}

func TestTribalOwners_OnlyOwnership(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalOwnersHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{"symbol": "NewServer"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp tribalResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Total != 1 {
		t.Errorf("expected 1 ownership fact, got %d", resp.Total)
	}
	if len(resp.Facts) > 0 && resp.Facts[0].Kind != "ownership" {
		t.Errorf("expected ownership kind, got %q", resp.Facts[0].Kind)
	}
}

func TestTribalWhyThisWay_RationaleAndInvariant(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalWhyThisWayHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{"symbol": "NewServer"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp tribalResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Total != 2 {
		t.Errorf("expected 2 facts (rationale+invariant), got %d", resp.Total)
	}

	for _, fact := range resp.Facts {
		if fact.Kind != "rationale" && fact.Kind != "invariant" {
			t.Errorf("unexpected kind %q, expected rationale or invariant", fact.Kind)
		}
	}
}

func TestTribalContextForSymbol_MissingSourceQuote(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "bad-repo.claims.db")

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

	symID, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       "bad-repo",
		ImportPath: "github.com/bad/pkg",
		SymbolName: "BadFunc",
		Language:   "go",
		Kind:       "function",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}

	// Insert a valid fact, then corrupt it via raw SQL.
	factID, err := cdb.InsertTribalFact(db.TribalFact{
		SubjectID:        symID,
		Kind:             "quirk",
		Body:             "bad fact",
		SourceQuote:      "placeholder",
		Confidence:       0.5,
		Corroboration:    1,
		Extractor:        "test",
		ExtractorVersion: "v1",
		StalenessHash:    "hash",
		Status:           "active",
		CreatedAt:        "2026-04-01T00:00:00Z",
		LastVerified:     "2026-04-08T00:00:00Z",
	}, []db.TribalEvidence{
		{
			SourceType:  "commit_msg",
			SourceRef:   "ref",
			ContentHash: "hash",
		},
	})
	if err != nil {
		t.Fatalf("insert fact: %v", err)
	}
	cdb.Close()

	// Update source_quote to empty via raw SQL.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	_, err = rawDB.Exec("UPDATE tribal_facts SET source_quote = '' WHERE id = ?", factID)
	if err != nil {
		t.Fatalf("update source_quote: %v", err)
	}
	rawDB.Close()

	pool := NewDBPool(tmpDir, 5)
	t.Cleanup(func() { pool.Close() })

	handler := TribalContextForSymbolHandler(pool)
	req := &tribalFakeRequest{args: map[string]any{"symbol": "BadFunc"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return an error result because the fact fails validation.
	if !result.IsError() {
		t.Fatalf("expected error result for missing source_quote, got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "source_quote") {
		t.Errorf("error should mention source_quote, got: %s", result.Text())
	}
}

func TestTribalContextForSymbol_MissingEvidence(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "bad-repo2.claims.db")

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

	symID, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       "bad-repo2",
		ImportPath: "github.com/bad2/pkg",
		SymbolName: "BadFunc2",
		Language:   "go",
		Kind:       "function",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}

	// Insert a valid fact, then delete evidence via raw SQL.
	factID, err := cdb.InsertTribalFact(db.TribalFact{
		SubjectID:        symID,
		Kind:             "quirk",
		Body:             "fact without evidence",
		SourceQuote:      "some quote",
		Confidence:       0.5,
		Corroboration:    1,
		Extractor:        "test",
		ExtractorVersion: "v1",
		StalenessHash:    "hash",
		Status:           "active",
		CreatedAt:        "2026-04-01T00:00:00Z",
		LastVerified:     "2026-04-08T00:00:00Z",
	}, []db.TribalEvidence{
		{
			SourceType:  "commit_msg",
			SourceRef:   "ref",
			ContentHash: "hash",
		},
	})
	if err != nil {
		t.Fatalf("insert fact: %v", err)
	}
	cdb.Close()

	// Delete evidence via raw SQL.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	_, err = rawDB.Exec("DELETE FROM tribal_evidence WHERE fact_id = ?", factID)
	if err != nil {
		t.Fatalf("delete evidence: %v", err)
	}
	rawDB.Close()

	pool := NewDBPool(tmpDir, 5)
	t.Cleanup(func() { pool.Close() })

	handler := TribalContextForSymbolHandler(pool)
	req := &tribalFakeRequest{args: map[string]any{"symbol": "BadFunc2"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError() {
		t.Fatalf("expected error result for missing evidence, got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "evidence") {
		t.Errorf("error should mention evidence, got: %s", result.Text())
	}
}

func TestTribalContextForSymbol_NoSymbolFound(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalContextForSymbolHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{"symbol": "NonExistentSymbol"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "No tribal knowledge facts found") {
		t.Errorf("expected no-facts message, got: %s", result.Text())
	}
}

func TestTribalContextForSymbol_ExtractorVersionFormat(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalContextForSymbolHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol": "NewServer",
		"kinds":  "ownership",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp tribalResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Total != 1 {
		t.Fatalf("expected 1 fact, got %d", resp.Total)
	}

	// Extractor should be formatted as "name@version".
	if resp.Facts[0].Extractor != "codeowners_miner@v0.1" {
		t.Errorf("expected extractor 'codeowners_miner@v0.1', got %q", resp.Facts[0].Extractor)
	}
}

func TestValidateProvenanceEnvelope(t *testing.T) {
	validFact := db.TribalFact{
		ID:               1,
		SourceQuote:      "quote",
		Status:           "active",
		Extractor:        "test",
		ExtractorVersion: "v1",
		LastVerified:     "2026-04-08T00:00:00Z",
		Evidence: []db.TribalEvidence{
			{SourceType: "commit_msg", SourceRef: "ref", ContentHash: "h"},
		},
	}

	if err := validateProvenanceEnvelope(validFact); err != nil {
		t.Errorf("expected no error for valid fact, got: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(f *db.TribalFact)
		wantErr string
	}{
		{"missing source_quote", func(f *db.TribalFact) { f.SourceQuote = "" }, "source_quote"},
		{"missing evidence", func(f *db.TribalFact) { f.Evidence = nil }, "evidence"},
		{"empty evidence", func(f *db.TribalFact) { f.Evidence = []db.TribalEvidence{} }, "evidence"},
		{"missing status", func(f *db.TribalFact) { f.Status = "" }, "status"},
		{"missing extractor", func(f *db.TribalFact) { f.Extractor = "" }, "extractor"},
		{"missing last_verified", func(f *db.TribalFact) { f.LastVerified = "" }, "last_verified"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bad := validFact
			bad.Evidence = append([]db.TribalEvidence{}, validFact.Evidence...)
			tt.mutate(&bad)
			err := validateProvenanceEnvelope(bad)
			if err == nil {
				t.Errorf("expected error for %s", tt.name)
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error to contain %q, got: %v", tt.wantErr, err)
			}
		})
	}
}
