package db

import (
	"database/sql"
	"fmt"
	"strings"
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
	// ClusterKey is the deterministic structural hash used by UpsertTribalFact
	// to merge facts that differ only in cosmetic noise. Callers SHOULD NOT set
	// this directly — UpsertTribalFact computes it from Body via the normalize
	// package. InsertTribalFact leaves this field empty (backward compatible).
	ClusterKey string
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

// ValidFactKind reports whether kind is a recognised tribal fact kind.
func ValidFactKind(kind string) bool {
	return validFactKinds[kind]
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
// that already has the core claims schema. Phase 3 additive migrations
// (cluster_key column, idx_tribal_facts_cluster index, source_files
// PR-mining columns) run at the end so that pre-Phase-3 databases are
// upgraded in place without dropping or rewriting existing rows.
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
    last_verified     TEXT NOT NULL,
    cluster_key       TEXT NOT NULL DEFAULT ''
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
-- idx_tribal_facts_cluster is created by migrateTribalFactsPhase3 after
-- ensuring the cluster_key column exists on pre-Phase-3 databases.

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

	// Create FTS5 search index over tribal_facts for BM25-ranked search.
	if err := c.CreateTribalSearchIndex(); err != nil {
		return fmt.Errorf("create tribal search index: %w", err)
	}

	// Apply Phase 3 additive migrations for databases that pre-date the
	// Phase 3 DDL above. Each step is idempotent — it only runs if the
	// target column/index is missing.
	if err := c.migrateTribalFactsPhase3(); err != nil {
		return fmt.Errorf("migrate tribal_facts phase3: %w", err)
	}
	if err := c.migrateSourceFilesPhase3(); err != nil {
		return fmt.Errorf("migrate source_files phase3: %w", err)
	}

	return nil
}

// migrateTribalFactsPhase3 adds the cluster_key column and the
// idx_tribal_facts_cluster index to tribal_facts on databases that
// pre-date the Phase 3 DDL. It is idempotent: both steps are guarded
// by existence checks so calling CreateTribalSchema twice is a no-op.
func (c *ClaimsDB) migrateTribalFactsPhase3() error {
	cols, err := c.columnsForTable("tribal_facts")
	if err != nil {
		return fmt.Errorf("inspect tribal_facts columns: %w", err)
	}
	if _, ok := cols["cluster_key"]; !ok {
		if _, err := c.exec.Exec(
			`ALTER TABLE tribal_facts ADD COLUMN cluster_key TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("add tribal_facts.cluster_key: %w", err)
		}
	}
	// CREATE INDEX IF NOT EXISTS is naturally idempotent. We still issue it
	// in the migration path because the main schema string above only runs
	// its index statement via exec.Exec on a fresh DB — calling it again
	// here guarantees the index exists after migration on older DBs that
	// saw an older schema string.
	if _, err := c.exec.Exec(
		`CREATE INDEX IF NOT EXISTS idx_tribal_facts_cluster ON tribal_facts(subject_id, kind, cluster_key)`,
	); err != nil {
		return fmt.Errorf("create idx_tribal_facts_cluster: %w", err)
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

// GetTribalFactByID returns a single tribal fact by its ID, with evidence populated.
// Returns an error if the fact is not found.
func (c *ClaimsDB) GetTribalFactByID(factID int64) (TribalFact, error) {
	rows, err := c.exec.Query(`
		SELECT id, subject_id, kind, body, source_quote, confidence,
		       corroboration, extractor, extractor_version, model,
		       staleness_hash, status, created_at, last_verified, cluster_key
		FROM tribal_facts WHERE id = ?
		ORDER BY id
	`, factID)
	if err != nil {
		return TribalFact{}, fmt.Errorf("get tribal fact by id: %w", err)
	}
	facts, err := scanTribalFactRows(rows)
	if err != nil {
		return TribalFact{}, fmt.Errorf("get tribal fact by id: %w", err)
	}
	if len(facts) == 0 {
		return TribalFact{}, fmt.Errorf("tribal fact %d not found", factID)
	}
	if err := c.populateEvidence(facts); err != nil {
		return TribalFact{}, err
	}
	return facts[0], nil
}

// GetTribalFactsBySubject returns all tribal facts for a given symbol ID,
// with their evidence rows populated.
func (c *ClaimsDB) GetTribalFactsBySubject(subjectID int64) ([]TribalFact, error) {
	rows, err := c.exec.Query(`
		SELECT id, subject_id, kind, body, source_quote, confidence,
		       corroboration, extractor, extractor_version, model,
		       staleness_hash, status, created_at, last_verified, cluster_key
		FROM tribal_facts WHERE subject_id = ?
		ORDER BY id
	`, subjectID)
	if err != nil {
		return nil, fmt.Errorf("get tribal facts by subject: %w", err)
	}
	facts, err := scanTribalFactRows(rows)
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
	rows, err := c.exec.Query(`
		SELECT id, subject_id, kind, body, source_quote, confidence,
		       corroboration, extractor, extractor_version, model,
		       staleness_hash, status, created_at, last_verified, cluster_key
		FROM tribal_facts WHERE kind = ?
		ORDER BY id
	`, kind)
	if err != nil {
		return nil, fmt.Errorf("get tribal facts by kind: %w", err)
	}
	facts, err := scanTribalFactRows(rows)
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

// UpdateFactLastVerified updates the last_verified timestamp for a tribal fact.
func (c *ClaimsDB) UpdateFactLastVerified(factID int64, lastVerified string) error {
	result, err := c.exec.Exec(
		"UPDATE tribal_facts SET last_verified = ? WHERE id = ?",
		lastVerified, factID,
	)
	if err != nil {
		return fmt.Errorf("update fact last_verified: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("update fact last_verified: fact %d not found", factID)
	}
	return nil
}

// UpdateFactConfidence updates the confidence score for a tribal fact.
func (c *ClaimsDB) UpdateFactConfidence(factID int64, confidence float64) error {
	result, err := c.exec.Exec(
		"UPDATE tribal_facts SET confidence = ? WHERE id = ?",
		confidence, factID,
	)
	if err != nil {
		return fmt.Errorf("update fact confidence: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("update fact confidence: fact %d not found", factID)
	}
	return nil
}

// GetActiveLLMFactsOlderThan returns active tribal facts where model is non-empty
// and last_verified is older than the given cutoff time. Results are ordered by
// last_verified ASC (oldest first) for deterministic sampling — this uses a
// dedicated query rather than queryTribalFacts to control the ORDER BY clause.
func (c *ClaimsDB) GetActiveLLMFactsOlderThan(cutoff string) ([]TribalFact, error) {
	rows, err := c.exec.Query(`
		SELECT id, subject_id, kind, body, source_quote, confidence,
		       corroboration, extractor, extractor_version, model,
		       staleness_hash, status, created_at, last_verified, cluster_key
		FROM tribal_facts
		WHERE status = 'active' AND model != '' AND model IS NOT NULL AND last_verified < ?
		ORDER BY last_verified ASC
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("get active LLM facts older than %s: %w", cutoff, err)
	}
	return scanTribalFactRows(rows)
}

// scanTribalFactRows scans rows from a tribal_facts query into a slice of
// TribalFact. The caller is responsible for issuing the query with a fully
// literal SQL string and parameterized arguments — this helper only handles
// the row-scanning logic. It closes the rows when done.
func scanTribalFactRows(rows *sql.Rows) ([]TribalFact, error) {
	defer rows.Close()

	var facts []TribalFact
	for rows.Next() {
		var f TribalFact
		var model sql.NullString
		err := rows.Scan(
			&f.ID, &f.SubjectID, &f.Kind, &f.Body, &f.SourceQuote,
			&f.Confidence, &f.Corroboration, &f.Extractor, &f.ExtractorVersion,
			&model, &f.StalenessHash, &f.Status, &f.CreatedAt, &f.LastVerified,
			&f.ClusterKey,
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
// statuses, with their evidence rows populated. The placeholder list is built
// from the length of the statuses slice — no caller-supplied strings are
// interpolated into the SQL.
func (c *ClaimsDB) GetTribalFactsByStatuses(statuses ...string) ([]TribalFact, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	// placeholders is purely "?,?,?" — only literal characters, never user input.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")

	args := make([]interface{}, len(statuses))
	for i, s := range statuses {
		args[i] = s
	}

	rows, err := c.exec.Query(
		"SELECT id, subject_id, kind, body, source_quote, confidence,"+
			" corroboration, extractor, extractor_version, model,"+
			" staleness_hash, status, created_at, last_verified, cluster_key"+
			" FROM tribal_facts WHERE status IN ("+placeholders+")"+
			" ORDER BY id",
		args...)
	if err != nil {
		return nil, fmt.Errorf("get tribal facts by statuses: %w", err)
	}
	facts, err := scanTribalFactRows(rows)
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

// validCorrectionActions is the set of allowed tribal correction actions.
var validCorrectionActions = map[string]bool{
	"correct":   true,
	"delete":    true,
	"supersede": true,
}

// InsertTribalCorrection inserts a correction row into tribal_corrections.
// Returns the correction ID on success. The Action field must be one of
// correct, delete, or supersede.
func (c *ClaimsDB) InsertTribalCorrection(correction TribalCorrection) (int64, error) {
	if !validCorrectionActions[correction.Action] {
		return 0, fmt.Errorf("insert tribal correction: invalid action %q", correction.Action)
	}
	if correction.Reason == "" {
		return 0, fmt.Errorf("insert tribal correction: reason is required")
	}
	if correction.Actor == "" {
		return 0, fmt.Errorf("insert tribal correction: actor is required")
	}

	result, err := c.exec.Exec(`
		INSERT INTO tribal_corrections (fact_id, action, new_body, reason, actor, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, correction.FactID, correction.Action,
		nullableString(correction.NewBody), correction.Reason,
		correction.Actor, correction.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("insert tribal correction: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get tribal correction id: %w", err)
	}
	return id, nil
}
