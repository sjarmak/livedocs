package evergreen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// defaultMaxRevisions is the per-document cap for preserved revisions.
// Matches the value documented in the PRD's Phase 1 Must-Have.
const defaultMaxRevisions = 5

// ErrInvalidDocument is the implementation-defined error this store returns
// when Save is called with an empty ID, which the Document contract forbids.
var ErrInvalidDocument = errors.New("evergreen: document is invalid (empty ID)")

// SQLiteStore is the default OSS-path DocumentStore implementation, backed
// by a single SQLite database. It is safe for concurrent use; the underlying
// *sql.DB manages the connection pool.
//
// Schema: the store owns two tables (deep_search_documents and
// deep_search_document_revisions) plus two indexes. Migrate creates them
// idempotently; callers can run it on every startup.
//
// Revision history: each Save that overwrites an existing document captures
// the previous row into the revisions table (append-only). Revisions beyond
// maxRevisions are pruned oldest-first in the same transaction.
type SQLiteStore struct {
	db           *sql.DB
	maxRevisions int
	ownsDB       bool
}

// SQLiteOption configures a SQLiteStore.
type SQLiteOption func(*SQLiteStore)

// WithMaxRevisions sets the per-document revision-history cap. Non-positive
// values are ignored. Default is 5.
func WithMaxRevisions(n int) SQLiteOption {
	return func(s *SQLiteStore) {
		if n > 0 {
			s.maxRevisions = n
		}
	}
}

// NewSQLiteStore wraps an existing *sql.DB. The caller retains ownership of
// the DB handle; Close is a no-op in this mode. Use OpenSQLiteStore for a
// self-contained store that owns its DB lifecycle.
//
// Enables PRAGMA foreign_keys = ON before returning. SQLite's default is OFF,
// and without it the ON DELETE CASCADE on the revisions table silently fails
// (leaving orphaned revisions and no error signal). This ensures a
// caller-supplied DB behaves the same as one opened via OpenSQLiteStore.
func NewSQLiteStore(db *sql.DB, opts ...SQLiteOption) (*SQLiteStore, error) {
	if db == nil {
		return nil, errors.New("evergreen: NewSQLiteStore requires a non-nil *sql.DB")
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("evergreen: enable foreign_keys: %w", err)
	}
	s := &SQLiteStore{db: db, maxRevisions: defaultMaxRevisions}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// OpenSQLiteStore opens (or creates) a SQLite database at path, applies WAL
// mode and sensible pragmas, runs Migrate, and returns a store that owns
// the DB handle. Call Close to release it.
func OpenSQLiteStore(ctx context.Context, path string, opts ...SQLiteOption) (*SQLiteStore, error) {
	dsn := path + "?_pragma=busy_timeout%3d5000&_pragma=foreign_keys%3d1&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("evergreen: open sqlite %q: %w", path, err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("evergreen: set WAL mode: %w", err)
	}
	s, err := NewSQLiteStore(db, opts...)
	if err != nil {
		db.Close()
		return nil, err
	}
	s.ownsDB = true
	if err := s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle if this store owns it
// (i.e. was constructed via OpenSQLiteStore). Stores built with
// NewSQLiteStore leave the handle to the caller.
func (s *SQLiteStore) Close() error {
	if s.ownsDB {
		return s.db.Close()
	}
	return nil
}

// Migrate idempotently creates the schema. It is safe to call on every
// startup. Forward-only; rollback is "drop tables".
func (s *SQLiteStore) Migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS deep_search_documents (
			id TEXT PRIMARY KEY,
			query TEXT NOT NULL,
			rendered_answer TEXT NOT NULL,
			manifest_json TEXT NOT NULL,
			status TEXT NOT NULL,
			refresh_policy TEXT NOT NULL,
			max_age_days INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			last_refreshed_at TEXT NOT NULL,
			external_id TEXT,
			backend TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_deep_search_documents_status
			ON deep_search_documents(status)`,
		`CREATE INDEX IF NOT EXISTS idx_deep_search_documents_last_refreshed
			ON deep_search_documents(last_refreshed_at)`,
		`CREATE TABLE IF NOT EXISTS deep_search_document_revisions (
			document_id TEXT NOT NULL,
			revision_num INTEGER NOT NULL,
			rendered_answer TEXT NOT NULL,
			manifest_json TEXT NOT NULL,
			recorded_at TEXT NOT NULL,
			PRIMARY KEY (document_id, revision_num),
			FOREIGN KEY (document_id) REFERENCES deep_search_documents(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_deep_search_document_revisions_doc
			ON deep_search_document_revisions(document_id, revision_num DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("evergreen: migrate: %w", err)
		}
	}
	return nil
}

// Save implements DocumentStore.
func (s *SQLiteStore) Save(ctx context.Context, doc *Document) error {
	if doc == nil {
		return errors.New("evergreen: Save requires a non-nil Document")
	}
	if doc.ID == "" {
		return ErrInvalidDocument
	}

	manifestJSON, err := json.Marshal(doc.Manifest)
	if err != nil {
		return fmt.Errorf("evergreen: marshal manifest: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("evergreen: begin tx: %w", err)
	}
	defer tx.Rollback() // safe no-op after Commit

	// Capture the existing row (if any) into revisions before overwriting.
	if err := captureRevision(ctx, tx, doc.ID, s.maxRevisions); err != nil {
		return err
	}

	// True UPSERT via ON CONFLICT DO UPDATE. We deliberately avoid
	// INSERT OR REPLACE because REPLACE performs DELETE-then-INSERT on
	// conflict, which fires ON DELETE CASCADE on the revisions foreign
	// key and wipes the row we just captured above.
	const upsert = `INSERT INTO deep_search_documents
		(id, query, rendered_answer, manifest_json, status, refresh_policy,
		 max_age_days, created_at, last_refreshed_at, external_id, backend)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			query = excluded.query,
			rendered_answer = excluded.rendered_answer,
			manifest_json = excluded.manifest_json,
			status = excluded.status,
			refresh_policy = excluded.refresh_policy,
			max_age_days = excluded.max_age_days,
			created_at = excluded.created_at,
			last_refreshed_at = excluded.last_refreshed_at,
			external_id = excluded.external_id,
			backend = excluded.backend`
	_, err = tx.ExecContext(ctx, upsert,
		doc.ID,
		doc.Query,
		doc.RenderedAnswer,
		string(manifestJSON),
		string(doc.Status),
		string(doc.RefreshPolicy),
		doc.MaxAgeDays,
		doc.CreatedAt.UTC().Format(time.RFC3339Nano),
		doc.LastRefreshedAt.UTC().Format(time.RFC3339Nano),
		nullableString(doc.ExternalID),
		doc.Backend,
	)
	if err != nil {
		return fmt.Errorf("evergreen: upsert document: %w", err)
	}

	return tx.Commit()
}

// captureRevision copies the existing document row (if any) into the
// revisions table, then prunes the oldest revisions beyond maxRevisions.
// No-op when the document doesn't yet exist.
func captureRevision(ctx context.Context, tx *sql.Tx, id string, maxRevisions int) error {
	var (
		prevAnswer   string
		prevManifest string
	)
	err := tx.QueryRowContext(ctx,
		`SELECT rendered_answer, manifest_json FROM deep_search_documents WHERE id = ?`,
		id,
	).Scan(&prevAnswer, &prevManifest)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil // first save for this ID; nothing to capture
	case err != nil:
		return fmt.Errorf("evergreen: read existing document: %w", err)
	}

	// Next revision number = max + 1. Starting at 1 for the first revision
	// keeps downstream queries intuitive.
	var nextRev int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(revision_num), 0) + 1
		 FROM deep_search_document_revisions WHERE document_id = ?`,
		id,
	).Scan(&nextRev); err != nil {
		return fmt.Errorf("evergreen: compute next revision: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO deep_search_document_revisions
		 (document_id, revision_num, rendered_answer, manifest_json, recorded_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, nextRev, prevAnswer, prevManifest,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("evergreen: write revision: %w", err)
	}

	// Prune: keep only the most recent maxRevisions rows.
	//
	// Delete rows with revision_num <= (MAX - maxRevisions). When MAX <=
	// maxRevisions the cutoff is zero or negative and nothing is deleted.
	// Once MAX exceeds maxRevisions, the cutoff advances by exactly one
	// each additional save, dropping the single oldest row and leaving
	// exactly maxRevisions behind. Not (MAX - maxRevisions + 1) — that
	// would keep only maxRevisions-1 rows, which is off-by-one small.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM deep_search_document_revisions
		 WHERE document_id = ?
		   AND revision_num <= (
		     SELECT COALESCE(MAX(revision_num), 0) - ?
		     FROM deep_search_document_revisions
		     WHERE document_id = ?)`,
		id, maxRevisions, id,
	); err != nil {
		return fmt.Errorf("evergreen: prune revisions: %w", err)
	}
	return nil
}

// Get implements DocumentStore.
func (s *SQLiteStore) Get(ctx context.Context, id string) (*Document, error) {
	const q = `SELECT id, query, rendered_answer, manifest_json, status,
		refresh_policy, max_age_days, created_at, last_refreshed_at,
		external_id, backend
		FROM deep_search_documents WHERE id = ?`
	row := s.db.QueryRowContext(ctx, q, id)
	doc, err := scanDocument(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("evergreen: Get(%q): %w", id, err)
	}
	return doc, nil
}

// List implements DocumentStore. Ordering: most-recently-refreshed first so
// callers can page without extra sorting.
func (s *SQLiteStore) List(ctx context.Context) ([]*Document, error) {
	const q = `SELECT id, query, rendered_answer, manifest_json, status,
		refresh_policy, max_age_days, created_at, last_refreshed_at,
		external_id, backend
		FROM deep_search_documents
		ORDER BY last_refreshed_at DESC, id ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("evergreen: List: %w", err)
	}
	defer rows.Close()

	var out []*Document
	for rows.Next() {
		doc, err := scanDocument(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("evergreen: List scan: %w", err)
		}
		out = append(out, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("evergreen: List iterate: %w", err)
	}
	return out, nil
}

// Delete implements DocumentStore. Foreign-key cascade removes the revision
// history in the same transaction.
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM deep_search_documents WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("evergreen: Delete(%q): %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("evergreen: Delete rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateStatus implements DocumentStore.
func (s *SQLiteStore) UpdateStatus(ctx context.Context, id string, status DocStatus) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE deep_search_documents SET status = ? WHERE id = ?`,
		string(status), id,
	)
	if err != nil {
		return fmt.Errorf("evergreen: UpdateStatus(%q): %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("evergreen: UpdateStatus rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanFn matches both *sql.Row.Scan and *sql.Rows.Scan so we can share
// row-assembly code between Get and List.
type scanFn func(dest ...any) error

func scanDocument(scan scanFn) (*Document, error) {
	var (
		doc            Document
		manifestJSON   string
		createdAt      string
		lastRefreshed  string
		externalID     sql.NullString
		status         string
		policy         string
		backend        sql.NullString
	)
	if err := scan(
		&doc.ID,
		&doc.Query,
		&doc.RenderedAnswer,
		&manifestJSON,
		&status,
		&policy,
		&doc.MaxAgeDays,
		&createdAt,
		&lastRefreshed,
		&externalID,
		&backend,
	); err != nil {
		return nil, err
	}
	doc.Status = DocStatus(status)
	doc.RefreshPolicy = RefreshPolicy(policy)
	if backend.Valid {
		doc.Backend = backend.String
	}
	if externalID.Valid {
		s := externalID.String
		doc.ExternalID = &s
	}
	if err := json.Unmarshal([]byte(manifestJSON), &doc.Manifest); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	doc.CreatedAt = t
	t, err = time.Parse(time.RFC3339Nano, lastRefreshed)
	if err != nil {
		return nil, fmt.Errorf("parse last_refreshed_at %q: %w", lastRefreshed, err)
	}
	doc.LastRefreshedAt = t
	return &doc, nil
}

func nullableString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
