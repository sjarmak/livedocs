package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// CreateTribalSearchIndex creates the FTS5 virtual table and content-sync
// triggers for full-text search over tribal_facts. The FTS index covers the
// body and source_quote columns and is kept in sync via INSERT/DELETE/UPDATE
// triggers on the tribal_facts table.
//
// This method is idempotent — IF NOT EXISTS is used for the virtual table and
// triggers are created with IF NOT EXISTS semantics via DROP IF EXISTS + CREATE.
func (c *ClaimsDB) CreateTribalSearchIndex() error {
	// FTS5 virtual tables do not support IF NOT EXISTS in all SQLite builds,
	// so we check for existence first.
	var count int
	err := c.exec.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'tribal_facts_fts'
	`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check tribal_facts_fts existence: %w", err)
	}

	if count == 0 {
		_, err := c.exec.Exec(`
			CREATE VIRTUAL TABLE tribal_facts_fts USING fts5(
				body,
				source_quote,
				content='tribal_facts',
				content_rowid='id'
			)
		`)
		if err != nil {
			return fmt.Errorf("create tribal_facts_fts: %w", err)
		}
	}

	// Content-sync triggers keep the FTS index in sync with tribal_facts.
	// We use DROP IF EXISTS + CREATE to make this idempotent.
	triggers := `
DROP TRIGGER IF EXISTS tribal_facts_ai;
CREATE TRIGGER tribal_facts_ai AFTER INSERT ON tribal_facts BEGIN
    INSERT INTO tribal_facts_fts(rowid, body, source_quote)
    VALUES (new.id, new.body, new.source_quote);
END;

DROP TRIGGER IF EXISTS tribal_facts_ad;
CREATE TRIGGER tribal_facts_ad AFTER DELETE ON tribal_facts BEGIN
    INSERT INTO tribal_facts_fts(tribal_facts_fts, rowid, body, source_quote)
    VALUES('delete', old.id, old.body, old.source_quote);
END;

DROP TRIGGER IF EXISTS tribal_facts_au_del;
CREATE TRIGGER tribal_facts_au_del BEFORE UPDATE ON tribal_facts BEGIN
    INSERT INTO tribal_facts_fts(tribal_facts_fts, rowid, body, source_quote)
    VALUES('delete', old.id, old.body, old.source_quote);
END;

DROP TRIGGER IF EXISTS tribal_facts_au_ins;
CREATE TRIGGER tribal_facts_au_ins AFTER UPDATE ON tribal_facts BEGIN
    INSERT INTO tribal_facts_fts(rowid, body, source_quote)
    VALUES (new.id, new.body, new.source_quote);
END;
`
	_, err = c.exec.Exec(triggers)
	if err != nil {
		return fmt.Errorf("create tribal_facts_fts triggers: %w", err)
	}

	return nil
}

// RebuildTribalSearchIndex rebuilds the FTS5 index from the current contents
// of the tribal_facts table. This is useful after bulk loading data that
// bypassed the content-sync triggers.
func (c *ClaimsDB) RebuildTribalSearchIndex() error {
	_, err := c.exec.Exec(`INSERT INTO tribal_facts_fts(tribal_facts_fts) VALUES('rebuild')`)
	if err != nil {
		return fmt.Errorf("rebuild tribal_facts_fts: %w", err)
	}
	return nil
}

// SearchTribalFactsBM25 performs a full-text search over tribal facts using
// BM25 ranking. Results are filtered to status='active' and scoped to the
// given repo via a JOIN on symbols. An optional kind filter narrows results
// to a specific fact kind. The limit parameter caps the number of results.
//
// Returns facts with evidence populated.
func (c *ClaimsDB) SearchTribalFactsBM25(repo, query, kind string, limit int) ([]TribalFact, error) {
	if query == "" {
		return nil, fmt.Errorf("search tribal facts: query must not be empty")
	}
	if limit <= 0 {
		limit = 10
	}

	var args []interface{}
	args = append(args, query, repo)

	kindClause := ""
	if kind != "" {
		kindClause = "AND tf.kind = ?"
		args = append(args, kind)
	}

	args = append(args, limit)

	q := fmt.Sprintf(`
		SELECT tf.id, tf.subject_id, tf.kind, tf.body, tf.source_quote,
		       tf.confidence, tf.corroboration, tf.extractor, tf.extractor_version,
		       tf.model, tf.staleness_hash, tf.status, tf.created_at, tf.last_verified
		FROM tribal_facts tf
		JOIN tribal_facts_fts fts ON tf.id = fts.rowid
		JOIN symbols s ON s.id = tf.subject_id
		WHERE tribal_facts_fts MATCH ?
		  AND tf.status = 'active'
		  AND s.repo = ?
		  %s
		ORDER BY bm25(tribal_facts_fts)
		LIMIT ?
	`, kindClause)

	rows, err := c.exec.Query(q, args...)
	if err != nil {
		// Check for common FTS5 query syntax errors and wrap them.
		if strings.Contains(err.Error(), "fts5") {
			return nil, fmt.Errorf("search tribal facts (bad query syntax): %w", err)
		}
		return nil, fmt.Errorf("search tribal facts: %w", err)
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
			return nil, fmt.Errorf("scan tribal search result: %w", err)
		}
		f.Model = model.String
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tribal search rows: %w", err)
	}

	if err := c.populateEvidence(facts); err != nil {
		return nil, err
	}

	return facts, nil
}
