package db

import (
	"testing"
)

// tribalSearchDB creates a fresh DB with core + tribal schemas (including FTS5 index).
func tribalSearchDB(t *testing.T) *ClaimsDB {
	t.Helper()
	return tribalDB(t) // tribalDB calls CreateTribalSchema which now calls CreateTribalSearchIndex
}

// insertTribalSearchFact is a helper that inserts a tribal fact with a single evidence row.
func insertTribalSearchFact(t *testing.T, cdb *ClaimsDB, subjectID int64, kind, body, sourceQuote string) int64 {
	t.Helper()
	id, err := cdb.InsertTribalFact(TribalFact{
		SubjectID:        subjectID,
		Kind:             kind,
		Body:             body,
		SourceQuote:      sourceQuote,
		Confidence:       0.9,
		Corroboration:    1,
		Extractor:        "test_extractor",
		ExtractorVersion: "v1",
		StalenessHash:    "hash",
		Status:           "active",
		CreatedAt:        "2026-04-01T00:00:00Z",
		LastVerified:     "2026-04-08T00:00:00Z",
	}, []TribalEvidence{
		{
			SourceType:  "commit_msg",
			SourceRef:   "ref123",
			Author:      "alice",
			ContentHash: "ehash",
		},
	})
	if err != nil {
		t.Fatalf("insert tribal search fact: %v", err)
	}
	return id
}

func TestTribalSearchIndex_Creation(t *testing.T) {
	cdb := tribalSearchDB(t)

	// Verify FTS5 virtual table exists.
	var count int
	err := cdb.DB().QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'tribal_facts_fts'
	`).Scan(&count)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if count != 1 {
		t.Errorf("expected tribal_facts_fts table to exist, got count=%d", count)
	}

	// Verify triggers exist.
	triggers := []string{
		"tribal_facts_ai",
		"tribal_facts_ad",
		"tribal_facts_au_del",
		"tribal_facts_au_ins",
	}
	for _, name := range triggers {
		var tcount int
		err := cdb.DB().QueryRow(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'trigger' AND name = ?
		`, name).Scan(&tcount)
		if err != nil {
			t.Fatalf("query trigger %s: %v", name, err)
		}
		if tcount != 1 {
			t.Errorf("expected trigger %s to exist, got count=%d", name, tcount)
		}
	}
}

func TestTribalSearchIndex_Idempotent(t *testing.T) {
	cdb := tribalSearchDB(t)

	// Calling CreateTribalSearchIndex again should not fail.
	if err := cdb.CreateTribalSearchIndex(); err != nil {
		t.Fatalf("second CreateTribalSearchIndex call failed: %v", err)
	}
}

func TestTribalSearchBM25_BasicQuery(t *testing.T) {
	cdb := tribalSearchDB(t)

	symID := insertTestSymbol(t, cdb, "ServerHandler")
	insertTribalSearchFact(t, cdb, symID, "rationale", "connection pooling prevents thundering herd", "pooling comment in PR")
	insertTribalSearchFact(t, cdb, symID, "ownership", "Team Platform owns ServerHandler", "CODEOWNERS entry")

	facts, err := cdb.SearchTribalFactsBM25("test/repo", "connection pooling", "", 10)
	if err != nil {
		t.Fatalf("SearchTribalFactsBM25: %v", err)
	}
	if len(facts) == 0 {
		t.Fatal("expected at least one result for 'connection pooling'")
	}

	// The connection pooling fact should be the top result.
	if facts[0].Body != "connection pooling prevents thundering herd" {
		t.Errorf("expected top result to be connection pooling fact, got %q", facts[0].Body)
	}

	// Evidence should be populated.
	for i, f := range facts {
		if len(f.Evidence) == 0 {
			t.Errorf("fact[%d] has no evidence populated", i)
		}
	}
}

func TestTribalSearchBM25_RepoScoping(t *testing.T) {
	cdb := tribalSearchDB(t)

	// Insert symbol in repo "test/repo" (the default from insertTestSymbol).
	symID := insertTestSymbol(t, cdb, "MyFunction")
	insertTribalSearchFact(t, cdb, symID, "rationale", "database migration strategy uses flyway", "flyway migration docs")

	// Insert symbol in a different repo.
	otherSymID, err := cdb.UpsertSymbol(Symbol{
		Repo:       "other/repo",
		ImportPath: "other/pkg",
		SymbolName: "OtherFunction",
		Language:   "go",
		Kind:       "function",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert other symbol: %v", err)
	}
	insertTribalSearchFact(t, cdb, otherSymID, "rationale", "database migration uses liquibase", "liquibase docs")

	// Search scoped to "test/repo" should only return the flyway fact.
	facts, err := cdb.SearchTribalFactsBM25("test/repo", "database migration", "", 10)
	if err != nil {
		t.Fatalf("SearchTribalFactsBM25: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 result for test/repo, got %d", len(facts))
	}
	if facts[0].Body != "database migration strategy uses flyway" {
		t.Errorf("unexpected fact body: %q", facts[0].Body)
	}

	// Search scoped to "other/repo" should only return the liquibase fact.
	facts2, err := cdb.SearchTribalFactsBM25("other/repo", "database migration", "", 10)
	if err != nil {
		t.Fatalf("SearchTribalFactsBM25 other repo: %v", err)
	}
	if len(facts2) != 1 {
		t.Fatalf("expected 1 result for other/repo, got %d", len(facts2))
	}
	if facts2[0].Body != "database migration uses liquibase" {
		t.Errorf("unexpected fact body: %q", facts2[0].Body)
	}
}

func TestTribalSearchBM25_KindFilter(t *testing.T) {
	cdb := tribalSearchDB(t)

	symID := insertTestSymbol(t, cdb, "CacheManager")
	insertTribalSearchFact(t, cdb, symID, "rationale", "caching layer uses Redis for performance", "PR comment about Redis")
	insertTribalSearchFact(t, cdb, symID, "ownership", "caching team owns the cache layer", "CODEOWNERS")
	insertTribalSearchFact(t, cdb, symID, "quirk", "cache invalidation has a known race condition", "TODO comment")

	// Search with kind filter should only return matching kind.
	facts, err := cdb.SearchTribalFactsBM25("test/repo", "cache", "ownership", 10)
	if err != nil {
		t.Fatalf("SearchTribalFactsBM25: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 ownership result, got %d", len(facts))
	}
	if facts[0].Kind != "ownership" {
		t.Errorf("expected ownership kind, got %q", facts[0].Kind)
	}
}

func TestTribalSearchBM25_LimitEnforcement(t *testing.T) {
	cdb := tribalSearchDB(t)

	symID := insertTestSymbol(t, cdb, "BigModule")
	for i := 0; i < 20; i++ {
		insertTribalSearchFact(t, cdb, symID, "rationale",
			"performance optimization technique number something",
			"optimization source quote")
	}

	// Search with limit=5 should return at most 5 results.
	facts, err := cdb.SearchTribalFactsBM25("test/repo", "performance optimization", "", 5)
	if err != nil {
		t.Fatalf("SearchTribalFactsBM25: %v", err)
	}
	if len(facts) > 5 {
		t.Errorf("expected at most 5 results with limit=5, got %d", len(facts))
	}
}

func TestTribalSearchBM25_StatusFilter(t *testing.T) {
	cdb := tribalSearchDB(t)

	symID := insertTestSymbol(t, cdb, "StatusTest")

	// Insert an active fact.
	insertTribalSearchFact(t, cdb, symID, "rationale", "active deployment strategy uses blue green", "deploy docs")

	// Insert a fact and then mark it stale.
	staleID := insertTribalSearchFact(t, cdb, symID, "rationale", "stale deployment strategy uses canary", "old deploy docs")
	if err := cdb.UpdateFactStatus(staleID, "stale"); err != nil {
		t.Fatalf("update status: %v", err)
	}

	// Search should only return the active fact.
	facts, err := cdb.SearchTribalFactsBM25("test/repo", "deployment strategy", "", 10)
	if err != nil {
		t.Fatalf("SearchTribalFactsBM25: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 active result, got %d", len(facts))
	}
	if facts[0].Body != "active deployment strategy uses blue green" {
		t.Errorf("unexpected fact body: %q", facts[0].Body)
	}
}

func TestTribalSearchBM25_EmptyQuery(t *testing.T) {
	cdb := tribalSearchDB(t)

	_, err := cdb.SearchTribalFactsBM25("test/repo", "", "", 10)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestTribalSearchBM25_NoResults(t *testing.T) {
	cdb := tribalSearchDB(t)

	facts, err := cdb.SearchTribalFactsBM25("test/repo", "xyznonexistent", "", 10)
	if err != nil {
		t.Fatalf("SearchTribalFactsBM25: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("expected 0 results for nonexistent query, got %d", len(facts))
	}
}

func TestTribalSearchBM25_ContentSyncOnUpdate(t *testing.T) {
	cdb := tribalSearchDB(t)

	symID := insertTestSymbol(t, cdb, "UpdateTest")
	insertTribalSearchFact(t, cdb, symID, "rationale", "original body about microservices", "original source quote")

	// Update the fact body via raw SQL to test the UPDATE triggers.
	_, err := cdb.DB().Exec(`UPDATE tribal_facts SET body = 'updated body about monolith architecture' WHERE subject_id = ?`, symID)
	if err != nil {
		t.Fatalf("update fact body: %v", err)
	}

	// Search for new content should find it.
	facts, err := cdb.SearchTribalFactsBM25("test/repo", "monolith architecture", "", 10)
	if err != nil {
		t.Fatalf("SearchTribalFactsBM25 after update: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 result after update, got %d", len(facts))
	}

	// Search for old content should not find it.
	oldFacts, err := cdb.SearchTribalFactsBM25("test/repo", "microservices", "", 10)
	if err != nil {
		t.Fatalf("SearchTribalFactsBM25 old content: %v", err)
	}
	if len(oldFacts) != 0 {
		t.Errorf("expected 0 results for old content after update, got %d", len(oldFacts))
	}
}
