// Package drift — tribal facts drift detection.
//
// CheckTribal detects drift in tribal knowledge facts by comparing their
// evidence against the current state of the codebase. Unlike structural drift
// which deletes stale claims, tribal drift never deletes rows — it transitions
// facts to 'stale' or 'quarantined' status.
package drift

import (
	"fmt"

	"github.com/live-docs/live_docs/db"
)

// CheckTribal scans all active and stale tribal facts for drift.
//
// A fact transitions to 'quarantined' when its subject symbol no longer exists
// in the symbols table.
//
// A fact transitions from 'active' to 'stale' when any of its evidence rows'
// content_hash values differ from the fact's staleness_hash (indicating the
// underlying evidence has changed).
//
// Facts already in 'stale' status remain 'stale' (not deleted) when evidence
// changes again. Facts in 'superseded' or 'deleted' status are never touched.
//
// Returns the count of newly staled facts, newly quarantined facts, and any
// error encountered.
func CheckTribal(cdb *db.ClaimsDB) (staleCount int, quarantinedCount int, err error) {
	// Fetch only facts that are eligible for drift detection.
	facts, err := cdb.GetTribalFactsByStatuses("active", "stale")
	if err != nil {
		return 0, 0, fmt.Errorf("check tribal drift: %w", err)
	}

	for _, fact := range facts {
		// Check whether the subject symbol still exists.
		exists, err := cdb.SymbolExistsByID(fact.SubjectID)
		if err != nil {
			return staleCount, quarantinedCount, fmt.Errorf("check tribal drift: symbol %d: %w", fact.SubjectID, err)
		}

		if !exists {
			// Symbol disappeared — quarantine the fact (regardless of current status).
			if fact.Status != "quarantined" {
				if err := cdb.UpdateFactStatus(fact.ID, "quarantined"); err != nil {
					return staleCount, quarantinedCount, fmt.Errorf("check tribal drift: quarantine fact %d: %w", fact.ID, err)
				}
				quarantinedCount++
			}
			continue
		}

		// Symbol exists — check evidence hashes for staleness.
		// Only transition active facts to stale; already-stale facts stay stale.
		if fact.Status == "active" {
			changed := evidenceHashChanged(fact)
			if changed {
				if err := cdb.UpdateFactStatus(fact.ID, "stale"); err != nil {
					return staleCount, quarantinedCount, fmt.Errorf("check tribal drift: stale fact %d: %w", fact.ID, err)
				}
				staleCount++
			}
		}
	}

	return staleCount, quarantinedCount, nil
}

// evidenceHashChanged returns true if any evidence row's content_hash differs
// from the fact's staleness_hash, indicating the underlying evidence has been
// modified since the fact was last verified.
func evidenceHashChanged(fact db.TribalFact) bool {
	for _, ev := range fact.Evidence {
		if ev.ContentHash != fact.StalenessHash {
			return true
		}
	}
	return false
}
