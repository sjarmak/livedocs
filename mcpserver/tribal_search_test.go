package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjarmak/livedocs/db"

	_ "modernc.org/sqlite"
)

// setupTribalSearchTestDB creates a test database with tribal schema (including FTS5),
// symbols, and seeded tribal facts. Returns the pool.
func setupTribalSearchTestDB(t *testing.T) *DBPool {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "search-repo.claims.db")

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
		Repo:       "search-repo",
		ImportPath: "github.com/test/search",
		SymbolName: "SearchHandler",
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
		Body:             "Team Search owns the search infrastructure",
		SourceQuote:      "// OWNER: search-team",
		Confidence:       0.95,
		Corroboration:    2,
		Extractor:        "codeowners_miner",
		ExtractorVersion: "v0.1",
		StalenessHash:    "abc123",
		Status:           "active",
		CreatedAt:        "2026-04-01T00:00:00Z",
		LastVerified:     "2026-04-08T00:00:00Z",
	}, []db.TribalEvidence{
		{
			SourceType:  "codeowners",
			SourceRef:   "CODEOWNERS:10",
			Author:      "alice",
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
		Body:             "connection pooling prevents thundering herd on restart",
		SourceQuote:      "PR #42: pooling prevents restart storm",
		Confidence:       0.85,
		Corroboration:    1,
		Extractor:        "pr_comment_miner",
		ExtractorVersion: "v0.1",
		StalenessHash:    "def456",
		Status:           "active",
		CreatedAt:        "2026-04-02T00:00:00Z",
		LastVerified:     "2026-04-08T00:00:00Z",
	}, []db.TribalEvidence{
		{
			SourceType:  "pr_comment",
			SourceRef:   "https://github.com/test/repo/pull/42",
			Author:      "bob",
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
		Body:             "search index must always be refreshed before query execution",
		SourceQuote:      "// INVARIANT: refresh index before query",
		Confidence:       0.9,
		Corroboration:    3,
		Extractor:        "inline_marker_miner",
		ExtractorVersion: "v0.2",
		StalenessHash:    "ghi789",
		Status:           "active",
		CreatedAt:        "2026-04-03T00:00:00Z",
		LastVerified:     "2026-04-09T00:00:00Z",
	}, []db.TribalEvidence{
		{
			SourceType:  "inline_marker",
			SourceRef:   "search.go:25",
			ContentHash: "hash3",
		},
	})
	if err != nil {
		t.Fatalf("insert invariant fact: %v", err)
	}

	cdb.Close()

	pool := NewDBPool(tmpDir, 5)
	t.Cleanup(func() { pool.Close() })
	return pool
}

func TestTribalSearch_BasicQuery(t *testing.T) {
	pool := setupTribalSearchTestDB(t)
	handler := TribalSearchHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{"query": "connection pooling"}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp tribalSearchResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Total == 0 {
		t.Fatal("expected at least one result for 'connection pooling'")
	}
	if resp.Query != "connection pooling" {
		t.Errorf("expected query 'connection pooling', got %q", resp.Query)
	}

	// Verify provenance fields on results.
	for i, fact := range resp.Facts {
		if fact.Body == "" {
			t.Errorf("fact[%d]: body is empty", i)
		}
		if fact.SourceQuote == "" {
			t.Errorf("fact[%d]: source_quote is empty", i)
		}
		if len(fact.Evidence) == 0 {
			t.Errorf("fact[%d]: evidence is empty", i)
		}
	}
}

func TestTribalSearch_KindFilter(t *testing.T) {
	pool := setupTribalSearchTestDB(t)
	handler := TribalSearchHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"query": "search",
		"kind":  "ownership",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp tribalSearchResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	for _, fact := range resp.Facts {
		if fact.Kind != "ownership" {
			t.Errorf("expected ownership kind, got %q", fact.Kind)
		}
	}
	if resp.Kind != "ownership" {
		t.Errorf("expected kind field 'ownership' in response, got %q", resp.Kind)
	}
}

func TestTribalSearch_EmptyResult(t *testing.T) {
	pool := setupTribalSearchTestDB(t)
	handler := TribalSearchHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{"query": "xyznonexistentterm"}}
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

func TestTribalSearch_MissingQuery(t *testing.T) {
	pool := setupTribalSearchTestDB(t)
	handler := TribalSearchHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError() {
		t.Fatalf("expected error result for missing query, got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "query") {
		t.Errorf("error should mention 'query', got: %s", result.Text())
	}
}
