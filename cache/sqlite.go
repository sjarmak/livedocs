package cache

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore implements Store backed by a SQLite database.
type SQLiteStore struct {
	db       *sql.DB
	capBytes int64
}

// NewSQLiteStore opens (or creates) a SQLite database at dsn and returns a
// Store with the given size cap in bytes. Use ":memory:" for an in-memory DB.
func NewSQLiteStore(dsn string, capBytes int64) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cache: open db: %w", err)
	}
	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cache: set WAL mode: %w", err)
	}
	s := &SQLiteStore{db: db, capBytes: capBytes}
	if err := s.createTable(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) createTable() error {
	const ddl = `
	CREATE TABLE IF NOT EXISTS source_files (
		id                INTEGER PRIMARY KEY,
		repo              TEXT NOT NULL,
		relative_path     TEXT NOT NULL,
		content_hash      TEXT NOT NULL,
		extractor_version TEXT NOT NULL,
		grammar_version   TEXT NOT NULL DEFAULT '',
		last_indexed      TEXT NOT NULL,
		size_bytes        INTEGER NOT NULL DEFAULT 0,
		deleted           INTEGER NOT NULL DEFAULT 0,
		UNIQUE(repo, relative_path)
	);
	CREATE INDEX IF NOT EXISTS idx_sf_repo ON source_files(repo);
	CREATE INDEX IF NOT EXISTS idx_sf_deleted ON source_files(deleted);
	`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("cache: create table: %w", err)
	}
	return nil
}

// Hit checks whether a matching non-deleted cache entry exists.
func (s *SQLiteStore) Hit(repo, path, contentHash, extractorVersion, grammarVersion string) (bool, error) {
	const q = `
	SELECT COUNT(*) FROM source_files
	WHERE repo = ? AND relative_path = ?
	  AND content_hash = ? AND extractor_version = ? AND grammar_version = ?
	  AND deleted = 0
	`
	var count int
	err := s.db.QueryRow(q, repo, path, contentHash, extractorVersion, grammarVersion).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("cache: hit query: %w", err)
	}
	return count > 0, nil
}

// Put inserts or replaces a cache entry, clearing any tombstone flag.
func (s *SQLiteStore) Put(entry Entry) error {
	const upsert = `
	INSERT INTO source_files (repo, relative_path, content_hash, extractor_version, grammar_version, last_indexed, size_bytes, deleted)
	VALUES (?, ?, ?, ?, ?, ?, ?, 0)
	ON CONFLICT(repo, relative_path) DO UPDATE SET
		content_hash      = excluded.content_hash,
		extractor_version = excluded.extractor_version,
		grammar_version   = excluded.grammar_version,
		last_indexed      = excluded.last_indexed,
		size_bytes        = excluded.size_bytes,
		deleted           = 0
	`
	ts := entry.LastIndexed.UTC().Format(time.RFC3339)
	_, err := s.db.Exec(upsert, entry.Repo, entry.RelativePath, entry.ContentHash,
		entry.ExtractorVersion, entry.GrammarVersion, ts, entry.SizeBytes)
	if err != nil {
		return fmt.Errorf("cache: put: %w", err)
	}
	return nil
}

// MarkDeleted sets the tombstone flag on the entry for repo+path.
// No-op if the entry does not exist.
func (s *SQLiteStore) MarkDeleted(repo, path string) error {
	const stmt = `UPDATE source_files SET deleted = 1 WHERE repo = ? AND relative_path = ?`
	_, err := s.db.Exec(stmt, repo, path)
	if err != nil {
		return fmt.Errorf("cache: mark deleted: %w", err)
	}
	return nil
}

// Reconcile synchronises the cache with the current file state of a repo.
// It returns the list of paths that need re-extraction (changed or new).
// Files in the cache but absent from currentFiles are tombstoned.
func (s *SQLiteStore) Reconcile(repo string, currentFiles map[string]string) ([]string, error) {
	// 1. Find cached entries for this repo.
	rows, err := s.db.Query(
		`SELECT relative_path, content_hash FROM source_files WHERE repo = ? AND deleted = 0`,
		repo,
	)
	if err != nil {
		return nil, fmt.Errorf("cache: reconcile query: %w", err)
	}
	defer rows.Close()

	cached := make(map[string]string) // path → hash
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			return nil, fmt.Errorf("cache: reconcile scan: %w", err)
		}
		cached[p] = h
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cache: reconcile rows: %w", err)
	}

	var changed []string

	// 2. Tombstone files that are no longer present.
	for path := range cached {
		if _, exists := currentFiles[path]; !exists {
			if err := s.MarkDeleted(repo, path); err != nil {
				return nil, err
			}
		}
	}

	// 3. Identify changed or new files.
	for path, hash := range currentFiles {
		cachedHash, exists := cached[path]
		if !exists || cachedHash != hash {
			changed = append(changed, path)
		}
	}

	return changed, nil
}

// Evict removes entries until total stored size (including tombstoned) is at or
// below the cap. Tombstoned entries are evicted first, then oldest by last_indexed.
func (s *SQLiteStore) Evict() (int, error) {
	total, err := s.rawTotalSize()
	if err != nil {
		return 0, err
	}
	if total <= s.capBytes {
		return 0, nil
	}

	evicted := 0

	// Phase 1: evict tombstoned entries.
	rows, err := s.db.Query(
		`SELECT id, size_bytes FROM source_files WHERE deleted = 1 ORDER BY last_indexed ASC`,
	)
	if err != nil {
		return 0, fmt.Errorf("cache: evict query tombstoned: %w", err)
	}
	var toDelete []int64
	for rows.Next() && total > s.capBytes {
		var id, sz int64
		if err := rows.Scan(&id, &sz); err != nil {
			rows.Close()
			return evicted, fmt.Errorf("cache: evict scan: %w", err)
		}
		toDelete = append(toDelete, id)
		total -= sz
		evicted++
	}
	rows.Close()

	for _, id := range toDelete {
		if _, err := s.db.Exec(`DELETE FROM source_files WHERE id = ?`, id); err != nil {
			return evicted, fmt.Errorf("cache: evict delete: %w", err)
		}
	}

	if total <= s.capBytes {
		return evicted, nil
	}

	// Phase 2: evict LRU non-deleted entries.
	rows, err = s.db.Query(
		`SELECT id, size_bytes FROM source_files WHERE deleted = 0 ORDER BY last_indexed ASC`,
	)
	if err != nil {
		return evicted, fmt.Errorf("cache: evict query LRU: %w", err)
	}
	toDelete = toDelete[:0]
	for rows.Next() && total > s.capBytes {
		var id, sz int64
		if err := rows.Scan(&id, &sz); err != nil {
			rows.Close()
			return evicted, fmt.Errorf("cache: evict scan LRU: %w", err)
		}
		toDelete = append(toDelete, id)
		total -= sz
		evicted++
	}
	rows.Close()

	for _, id := range toDelete {
		if _, err := s.db.Exec(`DELETE FROM source_files WHERE id = ?`, id); err != nil {
			return evicted, fmt.Errorf("cache: evict delete LRU: %w", err)
		}
	}

	return evicted, nil
}

// rawTotalSize returns the sum of size_bytes for ALL entries including tombstoned.
// Used internally by Evict to account for actual storage consumption.
func (s *SQLiteStore) rawTotalSize() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`SELECT SUM(size_bytes) FROM source_files`).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("cache: raw total size: %w", err)
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// TotalSize returns the sum of size_bytes for all non-deleted entries.
func (s *SQLiteStore) TotalSize() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(
		`SELECT SUM(size_bytes) FROM source_files WHERE deleted = 0`,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("cache: total size: %w", err)
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
