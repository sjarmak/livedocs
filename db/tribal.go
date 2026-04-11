package db

import (
	"database/sql"
	"fmt"
)

// TribalFact represents a tribal knowledge fact extracted from non-code sources.
type TribalFact struct {
	ID               int64
	SubjectID        int64
	Kind             string
	Body             string
	SourceQuote      string
	Confidence       float64
	Corroboration    int
	Extractor        string
	ExtractorVersion string
	Model            string
	StalenessHash    string
	Status           string
	CreatedAt        string
	LastVerified     string
	// Evidence is populated by query helpers; not stored directly in tribal_facts.
	Evidence []TribalEvidence
}

// TribalEvidence represents a piece of evidence supporting a tribal fact.
type TribalEvidence struct {
	ID          int64
	FactID      int64
	SourceType  string
	SourceRef   string
	Author      string
	AuthoredAt  string
	ContentHash string
}

// TribalCorrection represents a human correction applied to a tribal fact.
type TribalCorrection struct {
	ID        int64
	FactID    int64
	Action    string
	NewBody   string
	Reason    string
	Actor     string
	CreatedAt string
}

// validFactKinds is the set of allowed tribal fact kinds.
var validFactKinds = map[string]bool{
	"ownership":   true,
	"rationale":   true,
	"invariant":   true,
	"quirk":       true,
	"todo":        true,
	"deprecation": true,
}

// validFactStatuses is the set of allowed tribal fact statuses.
var validFactStatuses = map[string]bool{
	"active":      true,
	"stale":       true,
	"quarantined": true,
	"superseded":  true,
	"deleted":     true,
}

// validEvidenceSourceTypes is the set of allowed evidence source types.
var validEvidenceSourceTypes = map[string]bool{
	"blame":         true,
	"commit_msg":    true,
	"pr_comment":    true,
	"codeowners":    true,
	"inline_marker": true,
	"runbook":       true,
	"correction":    true,
}

// CreateTribalSchema creates the tribal knowledge tables and indexes.
// It is idempotent (uses IF NOT EXISTS) and can be called on a database
// that already has the core claims schema.
func (c *ClaimsDB) CreateTribalSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS tribal_facts (
    id                INTEGER PRIMARY KEY,
    subject_id        INTEGER NOT NULL REFERENCES symbols(id),
    kind              TEXT NOT NULL CHECK(kind IN ('ownership','rationale','invariant','quirk','todo','deprecation')),
    body              TEXT NOT NULL,
    source_quote      TEXT NOT NULL,
    confidence        REAL NOT NULL,
    corroboration     INTEGER NOT NULL DEFAULT 1,
    extractor         TEXT NOT NULL,
    extractor_version TEXT NOT NULL,
    model             TEXT,
    staleness_hash    TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stale','quarantined','superseded','deleted')),
    created_at        TEXT NOT NULL,
    last_verified     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tribal_evidence (
    id              INTEGER PRIMARY KEY,
    fact_id         INTEGER NOT NULL REFERENCES tribal_facts(id) ON DELETE CASCADE,
    source_type     TEXT NOT NULL CHECK(source_type IN ('blame','commit_msg','pr_comment','codeowners','inline_marker','runbook','correction')),
    source_ref      TEXT NOT NULL,
    author          TEXT,
    authored_at     TEXT,
    content_hash    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tribal_facts_subject ON tribal_facts(subject_id);
CREATE INDEX IF NOT EXISTS idx_tribal_evidence_fact ON tribal_evidence(fact_id);

CREATE TABLE IF NOT EXISTS tribal_corrections (
    id              INTEGER PRIMARY KEY,
    fact_id         INTEGER NOT NULL REFERENCES tribal_facts(id),
    action          TEXT NOT NULL CHECK(action IN ('correct','delete','supersede')),
    new_body        TEXT,
    reason          TEXT NOT NULL,
    actor           TEXT NOT NULL,
    created_at      TEXT NOT NULL
);
`
	_, err := c.exec.Exec(schema)
	if err != nil {
		return fmt.Errorf("create tribal schema: %w", err)
	}
	return nil
}

// InsertTribalFact inserts a tribal fact along with its evidence rows
// atomically. At least one evidence row is required; if the evidence slice
// is empty, the call returns an error and no data is written.
// Returns the fact ID on success.
func (c *ClaimsDB) InsertTribalFact(fact TribalFact, evidence []TribalEvidence) (int64, error) {
	if len(evidence) == 0 {
		return 0, fmt.Errorf("insert tribal fact: at least one evidence row is required")
	}

	var factID int64
	err := c.RunInTransaction(func() error {
		result, err := c.exec.Exec(`
			INSERT INTO tribal_facts (subject_id, kind, body, source_quote, confidence,
				corroboration, extractor, extractor_version, model, staleness_hash,
				status, created_at, last_verified)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, fact.SubjectID, fact.Kind, fact.Body, fact.SourceQuote, fact.Confidence,
			fact.Corroboration, fact.Extractor, fact.ExtractorVersion,
			nullableString(fact.Model), fact.StalenessHash,
			fact.Status, fact.CreatedAt, fact.LastVerified)
		if err != nil {
			return fmt.Errorf("insert tribal fact row: %w", err)
		}

		factID, err = result.LastInsertId()
		if err != nil {
			return fmt.Errorf("get tribal fact id: %w", err)
		}

		for i, ev := range evidence {
			_, err := c.exec.Exec(`
				INSERT INTO tribal_evidence (fact_id, source_type, source_ref, author, authored_at, content_hash)
				VALUES (?, ?, ?, ?, ?, ?)
			`, factID, ev.SourceType, ev.SourceRef,
				nullableString(ev.Author), nullableString(ev.AuthoredAt), ev.ContentHash)
			if err != nil {
				return fmt.Errorf("insert tribal evidence[%d]: %w", i, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return factID, nil
}

// GetTribalFactsBySubject returns all tribal facts for a given symbol ID,
// with their evidence rows populated.
func (c *ClaimsDB) GetTribalFactsBySubject(subjectID int64) ([]TribalFact, error) {
	facts, err := c.queryTribalFacts("subject_id = ?", subjectID)
	if err != nil {
		return nil, fmt.Errorf("get tribal facts by subject: %w", err)
	}
	if err := c.populateEvidence(facts); err != nil {
		return nil, err
	}
	return facts, nil
}

// GetTribalFactsByKind returns all tribal facts of a given kind,
// with their evidence rows populated.
func (c *ClaimsDB) GetTribalFactsByKind(kind string) ([]TribalFact, error) {
	facts, err := c.queryTribalFacts("kind = ?", kind)
	if err != nil {
		return nil, fmt.Errorf("get tribal facts by kind: %w", err)
	}
	if err := c.populateEvidence(facts); err != nil {
		return nil, err
	}
	return facts, nil
}

// UpdateFactStatus transitions a tribal fact to a new status.
// Valid statuses are: active, stale, quarantined, superseded, deleted.
func (c *ClaimsDB) UpdateFactStatus(factID int64, status string) error {
	if !validFactStatuses[status] {
		return fmt.Errorf("update fact status: invalid status %q", status)
	}
	result, err := c.exec.Exec(
		"UPDATE tribal_facts SET status = ? WHERE id = ?",
		status, factID,
	)
	if err != nil {
		return fmt.Errorf("update fact status: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("update fact status: fact %d not found", factID)
	}
	return nil
}

// queryTribalFacts is a shared helper that queries tribal_facts with an
// arbitrary WHERE clause.
func (c *ClaimsDB) queryTribalFacts(where string, args ...interface{}) ([]TribalFact, error) {
	query := fmt.Sprintf(`
		SELECT id, subject_id, kind, body, source_quote, confidence,
		       corroboration, extractor, extractor_version, model,
		       staleness_hash, status, created_at, last_verified
		FROM tribal_facts WHERE %s
		ORDER BY id
	`, where)

	rows, err := c.exec.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []TribalFact
	for rows.Next() {
		var f TribalFact
		var model sql.NullString
		err := rows.Scan(
			&f.ID, &f.SubjectID, &f.Kind, &f.Body, &f.SourceQuote,
			&f.Confidence, &f.Corroboration, &f.Extractor, &f.ExtractorVersion,
			&model, &f.StalenessHash, &f.Status, &f.CreatedAt, &f.LastVerified,
		)
		if err != nil {
			return nil, err
		}
		f.Model = model.String
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

// SymbolExistsByID returns true if a symbol with the given ID exists.
func (c *ClaimsDB) SymbolExistsByID(id int64) (bool, error) {
	var count int
	err := c.exec.QueryRow("SELECT COUNT(*) FROM symbols WHERE id = ?", id).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check symbol exists: %w", err)
	}
	return count > 0, nil
}

// GetTribalFactsByStatuses returns all tribal facts matching any of the given
// statuses, with their evidence rows populated.
func (c *ClaimsDB) GetTribalFactsByStatuses(statuses ...string) ([]TribalFact, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := ""
	args := make([]interface{}, len(statuses))
	for i, s := range statuses {
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
		args[i] = s
	}
	facts, err := c.queryTribalFacts(fmt.Sprintf("status IN (%s)", placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("get tribal facts by statuses: %w", err)
	}
	if err := c.populateEvidence(facts); err != nil {
		return nil, err
	}
	return facts, nil
}

// CountTribalFactsByKind returns the number of tribal facts grouped by kind.
func (c *ClaimsDB) CountTribalFactsByKind() (map[string]int, error) {
	rows, err := c.exec.Query(`
		SELECT kind, COUNT(*) FROM tribal_facts
		WHERE status = 'active'
		GROUP BY kind ORDER BY kind
	`)
	if err != nil {
		return nil, fmt.Errorf("count tribal facts by kind: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			return nil, fmt.Errorf("scan tribal fact count: %w", err)
		}
		counts[kind] = count
	}
	return counts, rows.Err()
}

// ListDistinctSourceFiles returns all distinct source_file values from the claims table.
func (c *ClaimsDB) ListDistinctSourceFiles() ([]string, error) {
	rows, err := c.exec.Query(`SELECT DISTINCT source_file FROM claims ORDER BY source_file`)
	if err != nil {
		return nil, fmt.Errorf("list distinct source files: %w", err)
	}
	defer rows.Close()

	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, fmt.Errorf("scan source file: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// GetImportPathsForSymbolName returns distinct import_path values from symbols
// whose symbol_name matches the given pattern (SQL LIKE).
// This enables resolving a symbol name like "ClaimsDB" to its package path
// "github.com/live-docs/live_docs/db" so tribal facts keyed by file path
// in that package directory can be found.
func (c *ClaimsDB) GetImportPathsForSymbolName(pattern string) ([]string, error) {
	rows, err := c.exec.Query(`
		SELECT DISTINCT import_path
		FROM symbols
		WHERE symbol_name LIKE ?
		ORDER BY import_path
	`, pattern)
	if err != nil {
		return nil, fmt.Errorf("get import paths for symbol name: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan import path: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// GetTribalFactsByPathPrefix returns all tribal facts (with evidence populated)
// whose subject symbol_name starts with the given path prefix, scoped to the
// given repo. This is the efficient one-query path used by the MCP fallback:
// it finds all file-level tribal subjects (e.g., "db/claims.go") in a package
// directory (e.g., "db/") and returns their facts in a single JOIN, avoiding
// the N+1 pattern of listing subjects then looking up each one.
//
// The prefix is passed to LIKE with ESCAPE '\' — callers must pre-escape any
// wildcard metacharacters in the prefix before calling.
func (c *ClaimsDB) GetTribalFactsByPathPrefix(repo, prefix string) ([]TribalFact, error) {
	rows, err := c.exec.Query(`
		SELECT tf.id, tf.subject_id, tf.kind, tf.body, tf.source_quote,
		       tf.confidence, tf.corroboration, tf.extractor, tf.extractor_version,
		       tf.model, tf.staleness_hash, tf.status, tf.created_at, tf.last_verified
		FROM tribal_facts tf
		JOIN symbols s ON s.id = tf.subject_id
		WHERE s.repo = ? AND s.symbol_name LIKE ? ESCAPE '\'
		ORDER BY tf.id
	`, repo, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("get tribal facts by path prefix: %w", err)
	}
	defer rows.Close()

	var facts []TribalFact
	for rows.Next() {
		var f TribalFact
		var model sql.NullString
		if err := rows.Scan(
			&f.ID, &f.SubjectID, &f.Kind, &f.Body, &f.SourceQuote,
			&f.Confidence, &f.Corroboration, &f.Extractor, &f.ExtractorVersion,
			&model, &f.StalenessHash, &f.Status, &f.CreatedAt, &f.LastVerified,
		); err != nil {
			return nil, fmt.Errorf("scan tribal fact: %w", err)
		}
		f.Model = model.String
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tribal fact rows: %w", err)
	}
	if err := c.populateEvidence(facts); err != nil {
		return nil, err
	}
	return facts, nil
}

// populateEvidence loads evidence rows for each fact in the slice.
func (c *ClaimsDB) populateEvidence(facts []TribalFact) error {
	for i := range facts {
		evidence, err := c.loadEvidenceForFact(facts[i].ID)
		if err != nil {
			return err
		}
		facts[i].Evidence = evidence
	}
	return nil
}

// loadEvidenceForFact fetches all evidence rows for one fact ID. Extracted
// from populateEvidence so that `defer rows.Close()` scopes correctly per
// query (deferring inside a loop would leak connections until the outer
// function returned).
func (c *ClaimsDB) loadEvidenceForFact(factID int64) ([]TribalEvidence, error) {
	rows, err := c.exec.Query(`
		SELECT id, fact_id, source_type, source_ref, author, authored_at, content_hash
		FROM tribal_evidence WHERE fact_id = ?
		ORDER BY id
	`, factID)
	if err != nil {
		return nil, fmt.Errorf("query tribal evidence for fact %d: %w", factID, err)
	}
	defer rows.Close()

	var evidence []TribalEvidence
	for rows.Next() {
		var ev TribalEvidence
		var author, authoredAt sql.NullString
		if err := rows.Scan(
			&ev.ID, &ev.FactID, &ev.SourceType, &ev.SourceRef,
			&author, &authoredAt, &ev.ContentHash,
		); err != nil {
			return nil, fmt.Errorf("scan tribal evidence: %w", err)
		}
		ev.Author = author.String
		ev.AuthoredAt = authoredAt.String
		evidence = append(evidence, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tribal evidence rows: %w", err)
	}
	return evidence, nil
}
