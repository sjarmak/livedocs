package mcpserver

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/live-docs/live_docs/db"
)

// setupEnrichmentTestDB creates a test database with a package and structural claims.
// Returns the ClaimsDB and the symbol ID for further manipulation.
func setupEnrichmentTestDB(t *testing.T, lastIndexed string) (*db.ClaimsDB, int64) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })

	// Insert a source file record.
	_, err = cdb.UpsertSourceFile(db.SourceFile{
		Repo:             "test-repo",
		RelativePath:     "pkg/server.go",
		ContentHash:      "hash1",
		ExtractorVersion: "v1",
		LastIndexed:      lastIndexed,
	})
	if err != nil {
		t.Fatalf("upsert source file: %v", err)
	}

	// Insert a symbol in the package.
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

	// Insert a structural claim referencing the source file.
	_, err = cdb.InsertClaim(db.Claim{
		SubjectID:        symID,
		Predicate:        "defines",
		ObjectText:       "creates a new server instance",
		SourceFile:       "pkg/server.go",
		SourceLine:       42,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "treesitter",
		ExtractorVersion: "v1",
		LastVerified:     lastIndexed,
	})
	if err != nil {
		t.Fatalf("insert structural claim: %v", err)
	}

	return cdb, symID
}

func TestEnrichmentStatus_NotYetEnriched(t *testing.T) {
	now := time.Now().UTC()
	lastIndexed := now.Add(-1 * time.Hour).Format(time.RFC3339)
	cdb, _ := setupEnrichmentTestDB(t, lastIndexed)

	got := enrichmentStatus(cdb, "github.com/test/pkg", now)
	if got != "Not yet enriched" {
		t.Errorf("expected 'Not yet enriched', got %q", got)
	}
}

func TestEnrichmentStatus_Enriched(t *testing.T) {
	now := time.Now().UTC()
	lastIndexed := now.Add(-2 * time.Hour).Format(time.RFC3339)
	enrichedAt := now.Add(-1 * time.Hour)
	enrichedAtStr := enrichedAt.Format(time.RFC3339)

	cdb, symID := setupEnrichmentTestDB(t, lastIndexed)

	// Insert a semantic claim (enrichment) that is newer than source_files.last_indexed.
	_, err := cdb.InsertClaim(db.Claim{
		SubjectID:        symID,
		Predicate:        "purpose",
		ObjectText:       "creates HTTP servers",
		SourceFile:       "pkg/server.go",
		SourceLine:       1,
		Confidence:       0.9,
		ClaimTier:        "semantic",
		Extractor:        "enrich-llm",
		ExtractorVersion: "v1",
		LastVerified:     enrichedAtStr,
	})
	if err != nil {
		t.Fatalf("insert semantic claim: %v", err)
	}

	got := enrichmentStatus(cdb, "github.com/test/pkg", now)
	wantDate := enrichedAt.Format("2006-01-02")
	if !hasSubstr(got, "Enriched at "+wantDate) {
		t.Errorf("expected 'Enriched at %s (...)' prefix, got %q", wantDate, got)
	}
	// Should contain an age indicator.
	if !hasSubstr(got, "ago") && !hasSubstr(got, "just now") {
		t.Errorf("expected age indicator in output, got %q", got)
	}
}

func TestEnrichmentStatus_Stale(t *testing.T) {
	now := time.Now().UTC()
	// Enrichment happened first, then source was re-indexed later.
	enrichedAt := now.Add(-3 * time.Hour)
	enrichedAtStr := enrichedAt.Format(time.RFC3339)
	reIndexedAt := now.Add(-1 * time.Hour).Format(time.RFC3339)

	cdb, symID := setupEnrichmentTestDB(t, reIndexedAt)

	// Insert a semantic claim that is older than the source_files.last_indexed.
	_, err := cdb.InsertClaim(db.Claim{
		SubjectID:        symID,
		Predicate:        "purpose",
		ObjectText:       "creates HTTP servers",
		SourceFile:       "pkg/server.go",
		SourceLine:       1,
		Confidence:       0.9,
		ClaimTier:        "semantic",
		Extractor:        "enrich-llm",
		ExtractorVersion: "v1",
		LastVerified:     enrichedAtStr,
	})
	if err != nil {
		t.Fatalf("insert semantic claim: %v", err)
	}

	got := enrichmentStatus(cdb, "github.com/test/pkg", now)
	wantDate := enrichedAt.Format("2006-01-02")
	wantPrefix := "Enrichment stale: source changed since " + wantDate
	if got != wantPrefix {
		t.Errorf("expected %q, got %q", wantPrefix, got)
	}
}

func TestEnrichmentStatus_NoSymbols(t *testing.T) {
	now := time.Now().UTC()
	lastIndexed := now.Add(-1 * time.Hour).Format(time.RFC3339)
	cdb, _ := setupEnrichmentTestDB(t, lastIndexed)

	// Query for a non-existent package.
	got := enrichmentStatus(cdb, "github.com/nonexistent/pkg", now)
	if got != "Not yet enriched" {
		t.Errorf("expected 'Not yet enriched', got %q", got)
	}
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"just now", 30 * time.Minute, "just now"},
		{"1 hour", 90 * time.Minute, "1 hour ago"},
		{"5 hours", 5 * time.Hour, "5 hours ago"},
		{"1 day", 36 * time.Hour, "1 day ago"},
		{"3 days", 3 * 24 * time.Hour, "3 days ago"},
		{"1 month", 35 * 24 * time.Hour, "1 month ago"},
		{"3 months", 100 * 24 * time.Hour, "3 months ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatAge(tt.d)
			if got != tt.want {
				t.Errorf("formatAge(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
