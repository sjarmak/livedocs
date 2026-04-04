// Package db provides SQLite-backed storage for claims DB operations.
// Uses per-repo SQLite files with a lightweight cross-repo xref index.
package db

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Symbol represents a code symbol with composite primary key.
type Symbol struct {
	ID          int64
	Repo        string
	ImportPath  string
	SymbolName  string
	Language    string
	Kind        string
	Visibility  string
	DisplayName string
	SCIPSymbol  string // secondary index, may be empty
}

// Claim represents a structural or semantic assertion about a symbol.
type Claim struct {
	ID               int64
	SubjectID        int64
	Predicate        string
	ObjectText       string
	ObjectID         int64 // 0 means NULL
	SourceFile       string
	SourceLine       int
	Confidence       float64
	ClaimTier        string
	Extractor        string
	ExtractorVersion string
	LastVerified     string
}

// SourceFile tracks content-hash metadata for incremental indexing.
type SourceFile struct {
	ID               int64
	Repo             string
	RelativePath     string
	ContentHash      string
	ExtractorVersion string
	GrammarVersion   string // may be empty
	LastIndexed      string
	Deleted          bool
}

// XRef represents a cross-repo symbol reference in _xref.db.
type XRef struct {
	SymbolKey string
	Repo      string
	SymbolID  int64
}

// dbExecutor abstracts *sql.DB and *sql.Tx so ClaimsDB methods work inside
// or outside a transaction.
type dbExecutor interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

// ClaimsDB wraps a per-repo SQLite database.
type ClaimsDB struct {
	db   *sql.DB
	exec dbExecutor // defaults to db; swapped to tx inside RunInTransaction
	txMu sync.Mutex // serializes RunInTransaction to prevent concurrent c.exec swaps
}

// OpenClaimsDB opens or creates a per-repo claims database at the given path.
func OpenClaimsDB(path string) (*ClaimsDB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open claims db %s: %w", path, err)
	}
	// Enable WAL mode for concurrent reads during long writes.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	// Set busy timeout so concurrent writers retry instead of failing immediately.
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	return &ClaimsDB{db: db, exec: db}, nil
}

// DB returns the underlying *sql.DB for direct queries.
func (c *ClaimsDB) DB() *sql.DB {
	return c.db
}

// Close closes the database connection.
func (c *ClaimsDB) Close() error {
	return c.db.Close()
}

// RunInTransaction executes fn inside a SQL transaction. All ClaimsDB methods
// called within fn will use the transaction. If fn returns an error, the
// transaction is rolled back; otherwise it is committed.
func (c *ClaimsDB) RunInTransaction(fn func() error) error {
	c.txMu.Lock()
	defer c.txMu.Unlock()

	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	prev := c.exec
	c.exec = tx
	defer func() { c.exec = prev }()

	if err := fn(); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// SetMaxOpenConns sets the maximum number of open connections to the database.
func (c *ClaimsDB) SetMaxOpenConns(n int) {
	c.db.SetMaxOpenConns(n)
}

// CreateSchema creates all required tables and indexes.
func (c *ClaimsDB) CreateSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS symbols (
    id              INTEGER PRIMARY KEY,
    repo            TEXT NOT NULL,
    import_path     TEXT NOT NULL,
    symbol_name     TEXT NOT NULL,
    language        TEXT NOT NULL,
    kind            TEXT NOT NULL,
    visibility      TEXT NOT NULL DEFAULT 'public'
                    CHECK(visibility IN ('public', 'internal', 'private', 're-exported', 'conditional')),
    display_name    TEXT,
    scip_symbol     TEXT,
    UNIQUE(repo, import_path, symbol_name)
);
CREATE INDEX IF NOT EXISTS idx_symbols_scip ON symbols(scip_symbol) WHERE scip_symbol IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_symbols_import_path ON symbols(import_path);

CREATE TABLE IF NOT EXISTS claims (
    id              INTEGER PRIMARY KEY,
    subject_id      INTEGER NOT NULL REFERENCES symbols(id),
    predicate       TEXT NOT NULL
                    CHECK(predicate IN (
                        'defines', 'imports', 'exports', 'has_doc', 'is_generated', 'is_test',
                        'has_kind', 'implements', 'has_signature', 'encloses',
                        'purpose', 'usage_pattern', 'complexity', 'stability'
                    )),
    object_text     TEXT,
    object_id       INTEGER REFERENCES symbols(id),
    source_file     TEXT NOT NULL,
    source_line     INTEGER,
    confidence      REAL NOT NULL DEFAULT 1.0,
    claim_tier      TEXT NOT NULL CHECK(claim_tier IN ('structural', 'semantic')),
    extractor       TEXT NOT NULL,
    extractor_version TEXT NOT NULL,
    last_verified   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_claims_subject ON claims(subject_id);
CREATE INDEX IF NOT EXISTS idx_claims_source_file ON claims(source_file);
CREATE INDEX IF NOT EXISTS idx_claims_predicate ON claims(predicate);

CREATE TABLE IF NOT EXISTS source_files (
    id              INTEGER PRIMARY KEY,
    repo            TEXT NOT NULL,
    relative_path   TEXT NOT NULL,
    content_hash    TEXT NOT NULL,
    extractor_version TEXT NOT NULL,
    grammar_version TEXT,
    last_indexed    TEXT NOT NULL,
    deleted         INTEGER NOT NULL DEFAULT 0,
    UNIQUE(repo, relative_path)
);
CREATE INDEX IF NOT EXISTS idx_source_files_deleted ON source_files(repo, deleted) WHERE deleted = 1;
`
	_, err := c.exec.Exec(schema)
	return err
}

// UpsertSymbol inserts or updates a symbol, returning its ID.
// On conflict (repo, import_path, symbol_name), updates mutable fields.
func (c *ClaimsDB) UpsertSymbol(s Symbol) (int64, error) {
	_, err := c.exec.Exec(`
		INSERT INTO symbols (repo, import_path, symbol_name, language, kind, visibility, display_name, scip_symbol)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo, import_path, symbol_name) DO UPDATE SET
			language = excluded.language,
			kind = excluded.kind,
			visibility = excluded.visibility,
			display_name = excluded.display_name,
			scip_symbol = excluded.scip_symbol
	`, s.Repo, s.ImportPath, s.SymbolName, s.Language, s.Kind, s.Visibility, s.DisplayName, nullableString(s.SCIPSymbol))
	if err != nil {
		return 0, fmt.Errorf("upsert symbol: %w", err)
	}
	// Always query the ID explicitly: LastInsertId() is unreliable for
	// INSERT ON CONFLICT UPDATE because SQLite's last_insert_rowid() is
	// connection-scoped across all tables and is not updated on the
	// conflict/update path.
	var id int64
	err = c.exec.QueryRow(
		"SELECT id FROM symbols WHERE repo = ? AND import_path = ? AND symbol_name = ?",
		s.Repo, s.ImportPath, s.SymbolName,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get symbol id: %w", err)
	}
	return id, nil
}

// InsertClaim inserts a new claim.
func (c *ClaimsDB) InsertClaim(cl Claim) (int64, error) {
	var objectID interface{}
	if cl.ObjectID != 0 {
		objectID = cl.ObjectID
	}
	result, err := c.exec.Exec(`
		INSERT INTO claims (subject_id, predicate, object_text, object_id, source_file, source_line,
		                     confidence, claim_tier, extractor, extractor_version, last_verified)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, cl.SubjectID, cl.Predicate, cl.ObjectText, objectID, cl.SourceFile, cl.SourceLine,
		cl.Confidence, cl.ClaimTier, cl.Extractor, cl.ExtractorVersion, cl.LastVerified)
	if err != nil {
		return 0, fmt.Errorf("insert claim: %w", err)
	}
	return result.LastInsertId()
}

// UpsertSourceFile inserts or updates a source file record.
func (c *ClaimsDB) UpsertSourceFile(sf SourceFile) (int64, error) {
	deleted := 0
	if sf.Deleted {
		deleted = 1
	}
	_, err := c.exec.Exec(`
		INSERT INTO source_files (repo, relative_path, content_hash, extractor_version, grammar_version, last_indexed, deleted)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo, relative_path) DO UPDATE SET
			content_hash = excluded.content_hash,
			extractor_version = excluded.extractor_version,
			grammar_version = excluded.grammar_version,
			last_indexed = excluded.last_indexed,
			deleted = excluded.deleted
	`, sf.Repo, sf.RelativePath, sf.ContentHash, sf.ExtractorVersion,
		nullableString(sf.GrammarVersion), sf.LastIndexed, deleted)
	if err != nil {
		return 0, fmt.Errorf("upsert source file: %w", err)
	}
	var id int64
	err = c.exec.QueryRow(
		"SELECT id FROM source_files WHERE repo = ? AND relative_path = ?",
		sf.Repo, sf.RelativePath,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get source file id: %w", err)
	}
	return id, nil
}

// GetSymbolByCompositeKey looks up a symbol by its composite primary key.
func (c *ClaimsDB) GetSymbolByCompositeKey(repo, importPath, symbolName string) (*Symbol, error) {
	s := &Symbol{}
	var displayName, scipSymbol sql.NullString
	err := c.exec.QueryRow(`
		SELECT id, repo, import_path, symbol_name, language, kind, visibility, display_name, scip_symbol
		FROM symbols WHERE repo = ? AND import_path = ? AND symbol_name = ?
	`, repo, importPath, symbolName).Scan(
		&s.ID, &s.Repo, &s.ImportPath, &s.SymbolName, &s.Language, &s.Kind, &s.Visibility,
		&displayName, &scipSymbol,
	)
	if err != nil {
		return nil, err
	}
	s.DisplayName = displayName.String
	s.SCIPSymbol = scipSymbol.String
	return s, nil
}

// GetClaimsBySubject returns all claims for a given symbol ID.
func (c *ClaimsDB) GetClaimsBySubject(subjectID int64) ([]Claim, error) {
	rows, err := c.exec.Query(`
		SELECT id, subject_id, predicate, object_text, object_id, source_file, source_line,
		       confidence, claim_tier, extractor, extractor_version, last_verified
		FROM claims WHERE subject_id = ?
	`, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var claims []Claim
	for rows.Next() {
		var cl Claim
		var objectText sql.NullString
		var objectID sql.NullInt64
		err := rows.Scan(
			&cl.ID, &cl.SubjectID, &cl.Predicate, &objectText, &objectID,
			&cl.SourceFile, &cl.SourceLine, &cl.Confidence, &cl.ClaimTier,
			&cl.Extractor, &cl.ExtractorVersion, &cl.LastVerified,
		)
		if err != nil {
			return nil, err
		}
		cl.ObjectText = objectText.String
		cl.ObjectID = objectID.Int64
		claims = append(claims, cl)
	}
	return claims, rows.Err()
}

// DeleteClaimsByExtractorAndFile removes all claims produced by a specific
// extractor for a given source file. Used for re-import idempotency.
func (c *ClaimsDB) DeleteClaimsByExtractorAndFile(extractor, sourceFile string) error {
	_, err := c.exec.Exec(
		"DELETE FROM claims WHERE extractor = ? AND source_file = ?",
		extractor, sourceFile,
	)
	return err
}

// GetSourceFile retrieves a source file record by repo and relative path.
// Returns sql.ErrNoRows if the file is not found.
func (c *ClaimsDB) GetSourceFile(repo, relativePath string) (*SourceFile, error) {
	sf := &SourceFile{}
	var grammarVersion sql.NullString
	var deleted int
	err := c.exec.QueryRow(`
		SELECT id, repo, relative_path, content_hash, extractor_version, grammar_version, last_indexed, deleted
		FROM source_files WHERE repo = ? AND relative_path = ?
	`, repo, relativePath).Scan(
		&sf.ID, &sf.Repo, &sf.RelativePath, &sf.ContentHash,
		&sf.ExtractorVersion, &grammarVersion, &sf.LastIndexed, &deleted,
	)
	if err != nil {
		return nil, err
	}
	sf.GrammarVersion = grammarVersion.String
	sf.Deleted = deleted != 0
	return sf, nil
}

// ListSymbolsByImportPath returns all symbols for a given import path.
func (c *ClaimsDB) ListSymbolsByImportPath(importPath string) ([]Symbol, error) {
	rows, err := c.exec.Query(`
		SELECT id, repo, import_path, symbol_name, language, kind, visibility, display_name, scip_symbol
		FROM symbols WHERE import_path = ?
		ORDER BY symbol_name
	`, importPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var symbols []Symbol
	for rows.Next() {
		var s Symbol
		var displayName, scipSymbol sql.NullString
		err := rows.Scan(
			&s.ID, &s.Repo, &s.ImportPath, &s.SymbolName, &s.Language, &s.Kind, &s.Visibility,
			&displayName, &scipSymbol,
		)
		if err != nil {
			return nil, err
		}
		s.DisplayName = displayName.String
		s.SCIPSymbol = scipSymbol.String
		symbols = append(symbols, s)
	}
	return symbols, rows.Err()
}

// MarkFileDeleted sets the deleted tombstone flag on a source file.
func (c *ClaimsDB) MarkFileDeleted(repo, relativePath string) error {
	result, err := c.exec.Exec(
		"UPDATE source_files SET deleted = 1 WHERE repo = ? AND relative_path = ?",
		repo, relativePath,
	)
	if err != nil {
		return fmt.Errorf("mark file deleted: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("source file not found: %s/%s", repo, relativePath)
	}
	return nil
}

// GetClaimsByFile returns all claims for a given source file path.
func (c *ClaimsDB) GetClaimsByFile(sourceFile string) ([]Claim, error) {
	rows, err := c.exec.Query(`
		SELECT id, subject_id, predicate, object_text, object_id, source_file, source_line,
		       confidence, claim_tier, extractor, extractor_version, last_verified
		FROM claims WHERE source_file = ?
	`, sourceFile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var claims []Claim
	for rows.Next() {
		var cl Claim
		var objectText sql.NullString
		var objectID sql.NullInt64
		err := rows.Scan(
			&cl.ID, &cl.SubjectID, &cl.Predicate, &objectText, &objectID,
			&cl.SourceFile, &cl.SourceLine, &cl.Confidence, &cl.ClaimTier,
			&cl.Extractor, &cl.ExtractorVersion, &cl.LastVerified,
		)
		if err != nil {
			return nil, err
		}
		cl.ObjectText = objectText.String
		cl.ObjectID = objectID.Int64
		claims = append(claims, cl)
	}
	return claims, rows.Err()
}

// GetClaimsByPredicate returns all claims with a specific predicate.
func (c *ClaimsDB) GetClaimsByPredicate(predicate string) ([]Claim, error) {
	rows, err := c.exec.Query(`
		SELECT id, subject_id, predicate, object_text, object_id, source_file, source_line,
		       confidence, claim_tier, extractor, extractor_version, last_verified
		FROM claims WHERE predicate = ?
	`, predicate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var claims []Claim
	for rows.Next() {
		var cl Claim
		var objectText sql.NullString
		var objectID sql.NullInt64
		err := rows.Scan(
			&cl.ID, &cl.SubjectID, &cl.Predicate, &objectText, &objectID,
			&cl.SourceFile, &cl.SourceLine, &cl.Confidence, &cl.ClaimTier,
			&cl.Extractor, &cl.ExtractorVersion, &cl.LastVerified,
		)
		if err != nil {
			return nil, err
		}
		cl.ObjectText = objectText.String
		cl.ObjectID = objectID.Int64
		claims = append(claims, cl)
	}
	return claims, rows.Err()
}

// ListDeletedFiles returns all source files marked as deleted for a given repo.
func (c *ClaimsDB) ListDeletedFiles(repo string) ([]SourceFile, error) {
	rows, err := c.exec.Query(`
		SELECT id, repo, relative_path, content_hash, extractor_version, grammar_version, last_indexed, deleted
		FROM source_files WHERE repo = ? AND deleted = 1
	`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []SourceFile
	for rows.Next() {
		var sf SourceFile
		var grammarVersion sql.NullString
		var deleted int
		err := rows.Scan(
			&sf.ID, &sf.Repo, &sf.RelativePath, &sf.ContentHash,
			&sf.ExtractorVersion, &grammarVersion, &sf.LastIndexed, &deleted,
		)
		if err != nil {
			return nil, err
		}
		sf.GrammarVersion = grammarVersion.String
		sf.Deleted = deleted != 0
		files = append(files, sf)
	}
	return files, rows.Err()
}

// GetSourceFilesByImportPath returns the distinct source files associated with
// symbols in the given import path. It joins claims back to source_files to
// find files that contributed claims for the package.
func (c *ClaimsDB) GetSourceFilesByImportPath(importPath string) ([]SourceFile, error) {
	rows, err := c.exec.Query(`
		SELECT DISTINCT sf.id, sf.repo, sf.relative_path, sf.content_hash,
		       sf.extractor_version, sf.grammar_version, sf.last_indexed, sf.deleted
		FROM source_files sf
		INNER JOIN claims cl ON cl.source_file = sf.relative_path
		INNER JOIN symbols sym ON cl.subject_id = sym.id AND sym.import_path = ?
		WHERE sf.deleted = 0
	`, importPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []SourceFile
	for rows.Next() {
		var sf SourceFile
		var grammarVersion sql.NullString
		var deleted int
		err := rows.Scan(
			&sf.ID, &sf.Repo, &sf.RelativePath, &sf.ContentHash,
			&sf.ExtractorVersion, &grammarVersion, &sf.LastIndexed, &deleted,
		)
		if err != nil {
			return nil, err
		}
		sf.GrammarVersion = grammarVersion.String
		sf.Deleted = deleted != 0
		files = append(files, sf)
	}
	return files, rows.Err()
}

// IsCacheHit checks whether a source file can be skipped based on content hash
// and extractor/grammar version matching. Returns true if all three match.
func (c *ClaimsDB) IsCacheHit(repo, relativePath, contentHash, extractorVersion, grammarVersion string) bool {
	var count int
	err := c.exec.QueryRow(`
		SELECT COUNT(*) FROM source_files
		WHERE repo = ? AND relative_path = ? AND content_hash = ?
		  AND extractor_version = ? AND COALESCE(grammar_version, '') = ?
		  AND deleted = 0
	`, repo, relativePath, contentHash, extractorVersion, grammarVersion).Scan(&count)
	if err != nil {
		return false
	}
	return count > 0
}

// SymbolWithClaims bundles a symbol with its structural claims for context assembly.
type SymbolWithClaims struct {
	Symbol Symbol
	Claims []Claim
}

// GetStructuralClaimsByImportPath returns all symbols in a package along with
// their structural claims. This is the primary query used by the semantic
// claim generator to build LLM context.
func (c *ClaimsDB) GetStructuralClaimsByImportPath(importPath string) ([]SymbolWithClaims, error) {
	symbols, err := c.ListSymbolsByImportPath(importPath)
	if err != nil {
		return nil, fmt.Errorf("list symbols for %s: %w", importPath, err)
	}
	result := make([]SymbolWithClaims, 0, len(symbols))
	for _, sym := range symbols {
		claims, err := c.GetClaimsBySubject(sym.ID)
		if err != nil {
			return nil, fmt.Errorf("get claims for symbol %d (%s): %w", sym.ID, sym.SymbolName, err)
		}
		// Filter to structural claims only.
		structural := make([]Claim, 0, len(claims))
		for _, cl := range claims {
			if cl.ClaimTier == "structural" {
				structural = append(structural, cl)
			}
		}
		if len(structural) > 0 {
			result = append(result, SymbolWithClaims{Symbol: sym, Claims: structural})
		}
	}
	return result, nil
}

// ListDistinctImportPaths returns all distinct import paths in the database.
func (c *ClaimsDB) ListDistinctImportPaths(limit int) ([]string, error) {
	rows, err := c.exec.Query(`
		SELECT DISTINCT import_path FROM symbols ORDER BY import_path LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// SearchSymbolsByName returns symbols whose symbol_name matches the given
// pattern using SQL LIKE. Use "%" as wildcard. For exact match, pass the name
// directly without wildcards.
func (c *ClaimsDB) SearchSymbolsByName(pattern string) ([]Symbol, error) {
	rows, err := c.exec.Query(`
		SELECT id, repo, import_path, symbol_name, language, kind, visibility, display_name, scip_symbol
		FROM symbols WHERE symbol_name LIKE ?
		ORDER BY symbol_name
	`, pattern)
	if err != nil {
		return nil, fmt.Errorf("search symbols by name: %w", err)
	}
	defer rows.Close()

	var symbols []Symbol
	for rows.Next() {
		var s Symbol
		var displayName, scipSymbol sql.NullString
		err := rows.Scan(
			&s.ID, &s.Repo, &s.ImportPath, &s.SymbolName, &s.Language, &s.Kind, &s.Visibility,
			&displayName, &scipSymbol,
		)
		if err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		s.DisplayName = displayName.String
		s.SCIPSymbol = scipSymbol.String
		symbols = append(symbols, s)
	}
	return symbols, rows.Err()
}

// GetClaimsByFileAndLineRange returns claims for a given source file whose
// source_line falls within [startLine, endLine]. If startLine and endLine
// are both 0, returns all claims for the file.
func (c *ClaimsDB) GetClaimsByFileAndLineRange(sourceFile string, startLine, endLine int) ([]Claim, error) {
	var query string
	var args []interface{}
	if startLine == 0 && endLine == 0 {
		query = `
			SELECT id, subject_id, predicate, object_text, object_id, source_file, source_line,
			       confidence, claim_tier, extractor, extractor_version, last_verified
			FROM claims WHERE source_file = ?`
		args = []interface{}{sourceFile}
	} else {
		query = `
			SELECT id, subject_id, predicate, object_text, object_id, source_file, source_line,
			       confidence, claim_tier, extractor, extractor_version, last_verified
			FROM claims WHERE source_file = ? AND source_line >= ? AND source_line <= ?`
		args = []interface{}{sourceFile, startLine, endLine}
	}

	rows, err := c.exec.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get claims by file and line range: %w", err)
	}
	defer rows.Close()

	var claims []Claim
	for rows.Next() {
		var cl Claim
		var objectText sql.NullString
		var objectID sql.NullInt64
		err := rows.Scan(
			&cl.ID, &cl.SubjectID, &cl.Predicate, &objectText, &objectID,
			&cl.SourceFile, &cl.SourceLine, &cl.Confidence, &cl.ClaimTier,
			&cl.Extractor, &cl.ExtractorVersion, &cl.LastVerified,
		)
		if err != nil {
			return nil, fmt.Errorf("scan claim: %w", err)
		}
		cl.ObjectText = objectText.String
		cl.ObjectID = objectID.Int64
		claims = append(claims, cl)
	}
	return claims, rows.Err()
}

// DeleteClaimsByExtractorAndImportPath removes all semantic claims produced by
// a specific extractor for all symbols in a given import path. Used for
// idempotent re-generation of semantic claims.
func (c *ClaimsDB) DeleteClaimsByExtractorAndImportPath(extractor, importPath string) error {
	_, err := c.exec.Exec(`
		DELETE FROM claims
		WHERE extractor = ?
		  AND claim_tier = 'semantic'
		  AND subject_id IN (SELECT id FROM symbols WHERE import_path = ?)
	`, extractor, importPath)
	return err
}

// DeleteLowConfidenceSemanticClaims removes semantic claims with confidence
// below the given threshold. Returns the number of rows deleted.
func (c *ClaimsDB) DeleteLowConfidenceSemanticClaims(threshold float64) (int64, error) {
	result, err := c.exec.Exec(`
		DELETE FROM claims
		WHERE claim_tier = 'semantic'
		  AND confidence < ?
	`, threshold)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteSensitiveClaims removes claims whose object_text contains sensitive
// patterns (password, secret, token, credential, api_key). Case-insensitive.
// Returns the number of rows deleted.
func (c *ClaimsDB) DeleteSensitiveClaims() (int64, error) {
	result, err := c.exec.Exec(`
		DELETE FROM claims
		WHERE LOWER(object_text) LIKE '%password%'
		   OR LOWER(object_text) LIKE '%secret%'
		   OR LOWER(object_text) LIKE '%token%'
		   OR LOWER(object_text) LIKE '%credential%'
		   OR LOWER(object_text) LIKE '%api_key%'
	`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// GetLatestLastIndexed returns the most recent last_indexed timestamp from the
// source_files table. Returns an empty string if no source files exist.
func (c *ClaimsDB) GetLatestLastIndexed() (string, error) {
	var ts sql.NullString
	err := c.exec.QueryRow("SELECT MAX(last_indexed) FROM source_files").Scan(&ts)
	if err != nil {
		return "", fmt.Errorf("get latest last_indexed: %w", err)
	}
	return ts.String, nil
}

// CountSymbols returns the total number of rows in the symbols table.
func (c *ClaimsDB) CountSymbols() (int, error) {
	var count int
	err := c.exec.QueryRow("SELECT COUNT(*) FROM symbols").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count symbols: %w", err)
	}
	return count, nil
}

// CountClaims returns the total number of rows in the claims table.
func (c *ClaimsDB) CountClaims() (int, error) {
	var count int
	err := c.exec.QueryRow("SELECT COUNT(*) FROM claims").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count claims: %w", err)
	}
	return count, nil
}

// ListDistinctImportPathsWithPrefix returns distinct import paths matching the
// given prefix, ordered alphabetically and capped at limit. It also returns the
// total count of matching distinct paths (ignoring the limit). When prefix is
// empty, all distinct import paths are returned.
func (c *ClaimsDB) ListDistinctImportPathsWithPrefix(prefix string, limit int) (paths []string, totalCount int, err error) {
	// Build the count query.
	var countQuery, listQuery string
	var args []interface{}
	if prefix == "" {
		countQuery = "SELECT COUNT(DISTINCT import_path) FROM symbols"
		listQuery = "SELECT DISTINCT import_path FROM symbols ORDER BY import_path LIMIT ?"
		args = []interface{}{limit}
	} else {
		likePattern := prefix + "%"
		countQuery = "SELECT COUNT(DISTINCT import_path) FROM symbols WHERE import_path LIKE ?"
		listQuery = "SELECT DISTINCT import_path FROM symbols WHERE import_path LIKE ? ORDER BY import_path LIMIT ?"
		// Count first.
		if err := c.exec.QueryRow(countQuery, likePattern).Scan(&totalCount); err != nil {
			return nil, 0, fmt.Errorf("count distinct import paths with prefix: %w", err)
		}
		rows, err := c.exec.Query(listQuery, likePattern, limit)
		if err != nil {
			return nil, 0, fmt.Errorf("list distinct import paths with prefix: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return nil, 0, fmt.Errorf("scan import path: %w", err)
			}
			paths = append(paths, p)
		}
		return paths, totalCount, rows.Err()
	}

	// No-prefix path.
	if err := c.exec.QueryRow(countQuery).Scan(&totalCount); err != nil {
		return nil, 0, fmt.Errorf("count distinct import paths: %w", err)
	}
	rows, err := c.exec.Query(listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list distinct import paths: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, 0, fmt.Errorf("scan import path: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, totalCount, rows.Err()
}

// DistinctSymbolPrefixes returns the distinct lowercase prefixes of length n
// from all symbol names. Only symbols with names at least n characters long
// are included. Used by the routing index to map prefixes to repos.
func (c *ClaimsDB) DistinctSymbolPrefixes(n int) ([]string, error) {
	rows, err := c.exec.Query(
		"SELECT DISTINCT LOWER(SUBSTR(symbol_name, 1, ?)) FROM symbols WHERE LENGTH(symbol_name) >= ?",
		n, n,
	)
	if err != nil {
		return nil, fmt.Errorf("distinct symbol prefixes: %w", err)
	}
	defer rows.Close()

	var prefixes []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan prefix: %w", err)
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, rows.Err()
}

// ExtractionMeta holds metadata about when and from which commit a repo was extracted.
type ExtractionMeta struct {
	CommitSHA   string
	ExtractedAt string
	RepoRoot    string
}

// SetExtractionMeta inserts or updates the extraction metadata for this database.
// Uses a single-row table keyed by id=1.
func (c *ClaimsDB) SetExtractionMeta(meta ExtractionMeta) error {
	_, err := c.exec.Exec(`
		CREATE TABLE IF NOT EXISTS extraction_meta (
			id              INTEGER PRIMARY KEY CHECK(id = 1),
			commit_sha      TEXT NOT NULL,
			extracted_at    TEXT NOT NULL,
			repo_root       TEXT NOT NULL DEFAULT ''
		)
	`)
	if err != nil {
		return fmt.Errorf("create extraction_meta table: %w", err)
	}
	// Add repo_root column if upgrading from an older schema.
	_, _ = c.exec.Exec(`ALTER TABLE extraction_meta ADD COLUMN repo_root TEXT NOT NULL DEFAULT ''`)
	_, err = c.exec.Exec(`
		INSERT INTO extraction_meta (id, commit_sha, extracted_at, repo_root)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			commit_sha = excluded.commit_sha,
			extracted_at = excluded.extracted_at,
			repo_root = excluded.repo_root
	`, meta.CommitSHA, meta.ExtractedAt, meta.RepoRoot)
	if err != nil {
		return fmt.Errorf("set extraction meta: %w", err)
	}
	return nil
}

// GetExtractionMeta reads the extraction metadata from the database.
// Returns a zero-value ExtractionMeta (empty strings) if the table does not
// exist or contains no rows.
func (c *ClaimsDB) GetExtractionMeta() (ExtractionMeta, error) {
	var meta ExtractionMeta
	// Check if table exists first to avoid errors on older DBs.
	var tableCount int
	err := c.exec.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='extraction_meta'",
	).Scan(&tableCount)
	if err != nil {
		return meta, fmt.Errorf("check extraction_meta table: %w", err)
	}
	if tableCount == 0 {
		return meta, nil
	}
	// Use COALESCE for backward compat with DBs lacking repo_root column.
	err = c.exec.QueryRow(
		"SELECT commit_sha, extracted_at, COALESCE(repo_root, '') FROM extraction_meta WHERE id = 1",
	).Scan(&meta.CommitSHA, &meta.ExtractedAt, &meta.RepoRoot)
	if err != nil {
		if err == sql.ErrNoRows {
			return ExtractionMeta{}, nil
		}
		return meta, fmt.Errorf("get extraction meta: %w", err)
	}
	return meta, nil
}

// Now returns the current time in RFC3339 format.
func Now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// XRefDB wraps the cross-repo index SQLite database.
type XRefDB struct {
	db *sql.DB
}

// OpenXRefDB opens or creates the cross-repo xref index at the given path.
func OpenXRefDB(path string) (*XRefDB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open xref db %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	return &XRefDB{db: db}, nil
}

// Close closes the database connection.
func (x *XRefDB) Close() error {
	return x.db.Close()
}

// CreateSchema creates the xref table.
func (x *XRefDB) CreateSchema() error {
	_, err := x.db.Exec(`
CREATE TABLE IF NOT EXISTS xref (
    symbol_key      TEXT NOT NULL,
    repo            TEXT NOT NULL,
    symbol_id       INTEGER NOT NULL,
    PRIMARY KEY(symbol_key, repo)
);
`)
	return err
}

// UpsertXRef inserts or updates a cross-repo reference.
func (x *XRefDB) UpsertXRef(ref XRef) error {
	_, err := x.db.Exec(`
		INSERT INTO xref (symbol_key, repo, symbol_id)
		VALUES (?, ?, ?)
		ON CONFLICT(symbol_key, repo) DO UPDATE SET
			symbol_id = excluded.symbol_id
	`, ref.SymbolKey, ref.Repo, ref.SymbolID)
	return err
}

// LookupRepos returns all repos that define a given symbol key.
func (x *XRefDB) LookupRepos(symbolKey string) ([]XRef, error) {
	rows, err := x.db.Query("SELECT symbol_key, repo, symbol_id FROM xref WHERE symbol_key = ?", symbolKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var refs []XRef
	for rows.Next() {
		var ref XRef
		if err := rows.Scan(&ref.SymbolKey, &ref.Repo, &ref.SymbolID); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}
