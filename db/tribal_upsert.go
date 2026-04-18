package db

import (
	"database/sql"
	"fmt"
	"log"

	"github.com/sjarmak/livedocs/extractor/tribal/normalize"
)

// recentBodyScanLimit caps how many existing fact bodies UpsertTribalFact
// inspects when computing the nearest-match calibration row. Bounded so
// a high-volume pilot does not turn every insert into an O(N) scan.
const recentBodyScanLimit = 200

// UpsertTribalFact inserts a tribal fact OR merges it into an existing fact
// sharing the same (subject_id, kind, cluster_key). The cluster key is
// computed from fact.Body via the normalize package — callers must leave
// fact.ClusterKey empty. Evidence rows are deduplicated by source_ref; if
// at least one new evidence row is inserted on the merge path, the fact's
// corroboration counter is incremented by exactly 1 (NEVER more, regardless
// of how many new evidence rows the call contributed).
//
// Returns (factID, merged, err). On the insert path `merged` is false and
// factID is the new row id. On the merge path `merged` is true and factID
// is the existing row id.
//
// Source quote stability (M6): merge NEVER rewrites the existing fact's
// source_quote, body, confidence, or model. The earliest-inserted version
// wins. The newer quote is recorded on the new evidence row only.
//
// Calibration side effect (N4): when the caller has wired a sidecar via
// EnableClusterDebug, UpsertTribalFact records a calibration row
// containing the nearest recent-body Jaccard match for every successful
// insert or merge. Failures to write the sidecar row are logged but never
// fail the main upsert — the calibration data is strictly observational.
func (c *ClaimsDB) UpsertTribalFact(
	fact TribalFact,
	evidence []TribalEvidence,
) (factID int64, merged bool, err error) {
	if len(evidence) == 0 {
		return 0, false, fmt.Errorf("upsert tribal fact: at least one evidence row is required")
	}
	if fact.ClusterKey != "" {
		return 0, false, fmt.Errorf("upsert tribal fact: ClusterKey must be empty; UpsertTribalFact computes it internally")
	}
	clusterKey := normalize.ScrubAndHash(fact.Body)
	fact.ClusterKey = clusterKey

	txErr := c.RunInTransaction(func() error {
		var existingID int64
		row := c.exec.QueryRow(`
			SELECT id FROM tribal_facts
			WHERE subject_id = ? AND kind = ? AND cluster_key = ?
			ORDER BY id
			LIMIT 1
		`, fact.SubjectID, fact.Kind, clusterKey)
		switch err := row.Scan(&existingID); err {
		case nil:
			// Merge path.
			merged = true
			factID = existingID
			newEvidence, mergeErr := c.mergeEvidence(existingID, evidence)
			if mergeErr != nil {
				return mergeErr
			}
			if newEvidence > 0 {
				if _, err := c.exec.Exec(
					`UPDATE tribal_facts SET corroboration = corroboration + 1, last_verified = ? WHERE id = ?`,
					fact.LastVerified, existingID,
				); err != nil {
					return fmt.Errorf("increment corroboration: %w", err)
				}
			}
		case sql.ErrNoRows:
			// Insert path.
			result, err := c.exec.Exec(`
				INSERT INTO tribal_facts (subject_id, kind, body, source_quote, confidence,
					corroboration, extractor, extractor_version, model, staleness_hash,
					status, created_at, last_verified, cluster_key)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, fact.SubjectID, fact.Kind, fact.Body, fact.SourceQuote, fact.Confidence,
				fact.Corroboration, fact.Extractor, fact.ExtractorVersion,
				nullableString(fact.Model), fact.StalenessHash,
				fact.Status, fact.CreatedAt, fact.LastVerified, clusterKey)
			if err != nil {
				return fmt.Errorf("insert tribal fact row: %w", err)
			}
			factID, err = result.LastInsertId()
			if err != nil {
				return fmt.Errorf("get tribal fact id: %w", err)
			}
			for i, ev := range evidence {
				if _, err := c.exec.Exec(`
					INSERT INTO tribal_evidence (fact_id, source_type, source_ref, author, authored_at, content_hash)
					VALUES (?, ?, ?, ?, ?, ?)
				`, factID, ev.SourceType, ev.SourceRef,
					nullableString(ev.Author), nullableString(ev.AuthoredAt), ev.ContentHash); err != nil {
					return fmt.Errorf("insert tribal evidence[%d]: %w", i, err)
				}
			}
		default:
			return fmt.Errorf("lookup tribal fact by cluster: %w", err)
		}

		// Compute nearest-body calibration and write it through the
		// attached sidecar alias inside the SAME transaction, so a
		// sidecar failure does NOT corrupt tribal_facts (either both
		// succeed or both roll back). Failures are logged and swallowed
		// so an attach mishap never takes down the main writer.
		if c.clusterDebug != nil {
			var nearestID int64
			var nearestJaccard float64
			recent, err := recentDifferentClusterBodies(c, clusterKey, recentBodyScanLimit)
			if err != nil {
				log.Printf("[tribal_upsert] recent scan failed for fact %d: %v", factID, err)
				return nil
			}
			for _, r := range recent {
				j := normalize.TokenJaccard(fact.Body, r.Body)
				if j > nearestJaccard {
					nearestJaccard = j
					nearestID = r.ID
				}
			}
			if _, writeErr := c.clusterDebug.writeDebugRow(c, factID, clusterKey, nearestID, nearestJaccard); writeErr != nil {
				log.Printf("[tribal_upsert] calibration write failed for fact %d: %v", factID, writeErr)
			}
		}
		return nil
	})
	if txErr != nil {
		return 0, false, txErr
	}
	return factID, merged, nil
}

// mergeEvidence inserts only evidence rows whose source_ref does not
// already exist for factID. Returns the number of rows actually inserted.
// Runs inside the caller's transaction.
func (c *ClaimsDB) mergeEvidence(factID int64, evidence []TribalEvidence) (int, error) {
	inserted := 0
	for _, ev := range evidence {
		var exists int
		err := c.exec.QueryRow(
			`SELECT 1 FROM tribal_evidence WHERE fact_id = ? AND source_ref = ? LIMIT 1`,
			factID, ev.SourceRef,
		).Scan(&exists)
		switch err {
		case nil:
			// duplicate; skip
			continue
		case sql.ErrNoRows:
			// new row; insert below
		default:
			return inserted, fmt.Errorf("check duplicate evidence: %w", err)
		}
		if _, err := c.exec.Exec(`
			INSERT INTO tribal_evidence (fact_id, source_type, source_ref, author, authored_at, content_hash)
			VALUES (?, ?, ?, ?, ?, ?)
		`, factID, ev.SourceType, ev.SourceRef,
			nullableString(ev.Author), nullableString(ev.AuthoredAt), ev.ContentHash); err != nil {
			return inserted, fmt.Errorf("insert merged evidence: %w", err)
		}
		inserted++
	}
	return inserted, nil
}
