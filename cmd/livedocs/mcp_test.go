package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sjarmak/livedocs/db"
)

func TestDiscoverRepoRoots(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string) // populate dir before calling discoverRepoRoots
		wantLen int
		wantKey string // expected key in result map (empty means don't check)
		wantErr bool
	}{
		{
			name:    "empty directory returns empty map",
			setup:   func(t *testing.T, dir string) { /* nothing */ },
			wantLen: 0,
		},
		{
			name: "invalid SQLite file is skipped",
			setup: func(t *testing.T, dir string) {
				// Write garbage bytes so OpenClaimsDB fails on PRAGMA.
				if err := os.WriteFile(filepath.Join(dir, "bad.claims.db"), []byte("not-a-database"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantLen: 0,
		},
		{
			name: "DB without extraction_meta table is skipped",
			setup: func(t *testing.T, dir string) {
				// Open a real SQLite DB but never create extraction_meta.
				path := filepath.Join(dir, "nometa.claims.db")
				cdb, err := db.OpenClaimsDB(path)
				if err != nil {
					t.Fatal(err)
				}
				// CreateSchema makes symbols/claims tables but NOT extraction_meta.
				if err := cdb.CreateSchema(); err != nil {
					t.Fatal(err)
				}
				cdb.Close()
			},
			// GetExtractionMeta returns zero-value (empty RepoRoot) when table missing,
			// so the entry is skipped because meta.RepoRoot == "".
			wantLen: 0,
		},
		{
			name: "GetExtractionMeta failure is skipped",
			setup: func(t *testing.T, dir string) {
				// Create a valid SQLite DB with a malformed extraction_meta table.
				// The table exists (so the table-existence check passes) but has
				// wrong columns, causing the SELECT to fail with "no such column".
				path := filepath.Join(dir, "badmeta.claims.db")
				cdb, err := db.OpenClaimsDB(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := cdb.CreateSchema(); err != nil {
					t.Fatal(err)
				}
				// Create extraction_meta with incompatible schema.
				_, err = cdb.DB().Exec("CREATE TABLE extraction_meta (id INTEGER PRIMARY KEY, bogus TEXT)")
				if err != nil {
					t.Fatal(err)
				}
				// Insert a row so the query doesn't hit sql.ErrNoRows.
				_, err = cdb.DB().Exec("INSERT INTO extraction_meta (id, bogus) VALUES (1, 'bad')")
				if err != nil {
					t.Fatal(err)
				}
				cdb.Close()
			},
			// GetExtractionMeta returns an error because the table has wrong columns.
			wantLen: 0,
		},
		{
			name: "empty RepoRoot is skipped",
			setup: func(t *testing.T, dir string) {
				path := filepath.Join(dir, "emptyroot.claims.db")
				cdb, err := db.OpenClaimsDB(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := cdb.CreateSchema(); err != nil {
					t.Fatal(err)
				}
				if err := cdb.SetExtractionMeta(db.ExtractionMeta{
					CommitSHA:   "abc123",
					ExtractedAt: "2025-01-01T00:00:00Z",
					RepoRoot:    "",
				}); err != nil {
					t.Fatal(err)
				}
				cdb.Close()
			},
			wantLen: 0,
		},
		{
			name: "RepoRoot directory does not exist is skipped",
			setup: func(t *testing.T, dir string) {
				path := filepath.Join(dir, "gone.claims.db")
				cdb, err := db.OpenClaimsDB(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := cdb.CreateSchema(); err != nil {
					t.Fatal(err)
				}
				if err := cdb.SetExtractionMeta(db.ExtractionMeta{
					CommitSHA:   "abc123",
					ExtractedAt: "2025-01-01T00:00:00Z",
					RepoRoot:    filepath.Join(dir, "nonexistent-dir-12345"),
				}); err != nil {
					t.Fatal(err)
				}
				cdb.Close()
			},
			wantLen: 0,
		},
		{
			name: "valid RepoRoot is included",
			setup: func(t *testing.T, dir string) {
				repoDir := filepath.Join(dir, "my-repo")
				if err := os.Mkdir(repoDir, 0o755); err != nil {
					t.Fatal(err)
				}

				path := filepath.Join(dir, "myrepo.claims.db")
				cdb, err := db.OpenClaimsDB(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := cdb.CreateSchema(); err != nil {
					t.Fatal(err)
				}
				if err := cdb.SetExtractionMeta(db.ExtractionMeta{
					CommitSHA:   "abc123",
					ExtractedAt: "2025-01-01T00:00:00Z",
					RepoRoot:    repoDir,
				}); err != nil {
					t.Fatal(err)
				}
				cdb.Close()
			},
			wantLen: 1,
			wantKey: "myrepo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			roots, err := discoverRepoRoots(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(roots) != tt.wantLen {
				t.Fatalf("got %d roots, want %d; roots=%v", len(roots), tt.wantLen, roots)
			}
			if tt.wantKey != "" {
				if _, ok := roots[tt.wantKey]; !ok {
					t.Fatalf("expected key %q in roots map; got %v", tt.wantKey, roots)
				}
			}
		})
	}
}
