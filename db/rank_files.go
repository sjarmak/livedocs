// Package db — rank_files.go implements tribal mining file prioritization.
//
// No schema adaptations were required: source_files has `repo`, `deleted`,
// and `last_pr_id_set` columns (Phase 3 migration); symbols has `visibility`
// and `repo`; tribal_facts has `subject_id` joinable via symbols.id. The
// join key `symbols.import_path = source_files.relative_path` is exact for
// file-level symbols written by the tribal walker, and for Go deep-extracted
// symbols (package-path import_path) the join simply contributes no
// public_surface/fan_in rows — which is the correct behavior for file-scoped
// ranking.
package db

import (
	"fmt"
	"strings"
)

// miningExcludeGlobs is the canonical list of glob patterns to exclude from
// tribal mining. The SQL layer applies LIKE-based equivalents; this list is
// the second layer of defense, applied in Go via ShouldSkipForMining so that
// any caller of RankFilesForMining or any future file-selection code path
// uses the same set.
var miningExcludeGlobs = []string{
	"vendor/**",
	"third_party/**",
	"testdata/**",
	"*.pb.go",
	"*_gen.go",
	"*_test.go",
}

// ShouldSkipForMining reports whether the relative path matches any of the
// mining-exclusion glob patterns. Go's filepath.Match does not support `**`,
// so we implement a targeted matcher covering the fixed pattern set used by
// the tribal mining prioritizer.
func ShouldSkipForMining(relPath string) bool {
	// Normalize to forward slashes for cross-platform safety; all stored
	// relative_paths in the DB use forward slashes on Unix, but tests on
	// Windows hosts should not break over a stray backslash.
	p := strings.ReplaceAll(relPath, "\\", "/")
	if p == "" {
		return false
	}
	// Directory-prefix globs: "<dir>/**" matches any file whose path starts
	// with "<dir>/" OR whose path begins with "<dir>/" at any nesting level.
	// The SQL uses NOT LIKE 'vendor/%' which only matches top-level vendor;
	// to stay symmetric we match top-level only here as well. That keeps
	// both layers in sync: a nested `foo/vendor/bar.go` is NOT skipped by
	// either layer (no precedent in this codebase to treat nested vendor
	// specially).
	for _, prefix := range []string{"vendor/", "third_party/", "testdata/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	// Suffix globs.
	for _, suffix := range []string{".pb.go", "_gen.go", "_test.go"} {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}

// RankFilesForMining returns up to `limit` relative paths from the given repo,
// ordered by mining priority:
//
//  1. Tier 1 (structural): never_mined files come first (last_pr_id_set IS NULL
//     or empty BLOB).
//  2. Tier 2 (weighted): (public_surface * 3 + fan_in - existing_facts * 5) DESC.
//  3. Tiebreak: relative_path ASC for determinism.
//
// Paths matching the mining-exclusion globs (vendor/**, third_party/**,
// testdata/**, *.pb.go, *_gen.go, *_test.go) are filtered in SQL (LIKE)
// AND in Go (ShouldSkipForMining) as a two-layer guard.
//
// A limit of 0 or negative returns an empty slice (not an error).
func (c *ClaimsDB) RankFilesForMining(repo string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}

	const query = `
SELECT sf.relative_path,
       COUNT(DISTINCT s.id) FILTER (WHERE s.visibility = 'public') AS public_surface,
       COUNT(DISTINCT c.id) AS fan_in,
       COALESCE((SELECT COUNT(*) FROM tribal_facts tf
                 JOIN symbols s2 ON s2.id = tf.subject_id
                 WHERE s2.import_path = sf.relative_path
                   AND tf.status = 'active'), 0) AS existing_facts,
       (sf.last_pr_id_set IS NULL OR LENGTH(sf.last_pr_id_set) = 0) AS never_mined
FROM source_files sf
LEFT JOIN symbols s ON s.import_path = sf.relative_path AND s.repo = sf.repo
LEFT JOIN claims c ON c.subject_id = s.id
WHERE sf.repo = ? AND sf.deleted = 0
  AND sf.relative_path NOT LIKE 'vendor/%'
  AND sf.relative_path NOT LIKE 'third_party/%'
  AND sf.relative_path NOT LIKE 'testdata/%'
  AND sf.relative_path NOT LIKE '%_test.go'
  AND sf.relative_path NOT LIKE '%.pb.go'
  AND sf.relative_path NOT LIKE '%_gen.go'
GROUP BY sf.relative_path
ORDER BY never_mined DESC,
         (public_surface * 3 + fan_in - existing_facts * 5) DESC,
         sf.relative_path ASC
LIMIT ?
`
	rows, err := c.exec.Query(query, repo, limit)
	if err != nil {
		return nil, fmt.Errorf("rank files for mining: query: %w", err)
	}
	defer rows.Close()

	out := make([]string, 0, limit)
	for rows.Next() {
		var (
			relPath       string
			publicSurface int
			fanIn         int
			existingFacts int
			neverMined    int
		)
		if err := rows.Scan(&relPath, &publicSurface, &fanIn, &existingFacts, &neverMined); err != nil {
			return nil, fmt.Errorf("rank files for mining: scan: %w", err)
		}
		// Second layer: ShouldSkipForMining. If SQL-side LIKE missed anything
		// (e.g. a future caller that bypasses the canonical query), this
		// Go-side filter still catches it.
		if ShouldSkipForMining(relPath) {
			continue
		}
		out = append(out, relPath)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rank files for mining: iterate: %w", err)
	}
	return out, nil
}
