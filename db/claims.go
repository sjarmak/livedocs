// Package db provides SQLite-backed storage for claims DB operations.
// Uses per-repo SQLite files with a lightweight cross-repo xref index.
package db

import (
	"database/sql"
	"fmt"
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

// ClaimsDB wraps a per-repo SQLite database.
type ClaimsDB struct {
	db *sql.DB
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
	return &ClaimsDB{db: db}, nil
}

// Close closes the database connection.
func (c *ClaimsDB) Close() error {
	return c.db.Close()
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
	_, err := c.db.Exec(schema)
	return err
}

// UpsertSymbol inserts or updates a symbol, returning its ID.
// On conflict (repo, import_path, symbol_name), updates mutable fields.
func (c *ClaimsDB) UpsertSymbol(s Symbol) (int64, error) {
	_, err := c.db.Exec(`
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
	err = c.db.QueryRow(
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
	result, err := c.db.Exec(`
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
	_, err := c.db.Exec(`
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
	err = c.db.QueryRow(
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
	err := c.db.QueryRow(`
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
	rows, err := c.db.Query(`
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
	_, err := c.db.Exec(
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
	err := c.db.QueryRow(`
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
	rows, err := c.db.Query(`
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
	result, err := c.db.Exec(
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
	rows, err := c.db.Query(`
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
	rows, err := c.db.Query(`
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
	rows, err := c.db.Query(`
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

// IsCacheHit checks whether a source file can be skipped based on content hash
// and extractor/grammar version matching. Returns true if all three match.
func (c *ClaimsDB) IsCacheHit(repo, relativePath, contentHash, extractorVersion, grammarVersion string) bool {
	var count int
	err := c.db.QueryRow(`
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
	rows, err := c.db.Query(`
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
	rows, err := c.db.Query(`
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

	rows, err := c.db.Query(query, args...)
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
	_, err := c.db.Exec(`
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
	result, err := c.db.Exec(`
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
	result, err := c.db.Exec(`
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
