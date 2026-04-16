package db

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "modernc.org/sqlite"
)

// MaxClusterDebugRows caps the number of rows UpsertTribalFact will insert
// into the cluster_debug table for a single database. Prevents the
// calibration table from growing unbounded under high-volume pilots.
const MaxClusterDebugRows = 50000

// clusterDebugAlias is the SQLite ATTACH alias used when the
// cluster-debug database is attached to a main ClaimsDB connection.
const clusterDebugAlias = "cluster_debug"

// clusterDebugTable is the single table name inside the cluster-debug
// database file. It is intentionally the same as the alias so queries
// against the attached database read naturally as
// `cluster_debug.cluster_debug`.
const clusterDebugTable = "cluster_debug"

// ClusterDebugOpts controls optional opener behaviour.
type ClusterDebugOpts struct {
	// ExtendExpiryDays, when > 0, permits attach even if rows past their
	// expires_at exist. A boot warning is logged each time this flag is used.
	ExtendExpiryDays int
	// MaxRows overrides MaxClusterDebugRows for tests. Zero means use the
	// package default.
	MaxRows int
}

// ClusterDebugDB wraps a standalone SQLite database file that holds the
// cluster_debug calibration table. It lives at `<mainDBPath>.cluster-debug.db`
// and is intentionally kept out of the main claims DB so Phase 5 can drop
// the entire file with a single `rm` instead of a migration PR.
//
// Foreign keys across ATTACHed SQLite databases are NOT enforced — rows in
// cluster_debug that reference tribal_facts(id) become dangling if the
// tribal fact is deleted. SweepDanglingFactIDs provides the application-
// side enforcement equivalent.
type ClusterDebugDB struct {
	db      *sql.DB
	path    string
	maxRows int
}

// DebugPair is a single cluster-debug row observation, exposed for tests
// and calibration tooling. Instances are read-only snapshots.
type DebugPair struct {
	FactID           int64
	ClusterKey       string
	NearestMatchID   int64
	BodyTokenJaccard float64
	CreatedAt        int64
}

// OpenClusterDebugDB opens (or creates) the cluster-debug sidecar database
// next to mainDBPath. The resulting file is `<mainDBPath>.cluster-debug.db`.
// Uses default options: no expiry extension, default row cap.
func OpenClusterDebugDB(mainDBPath string) (*ClusterDebugDB, error) {
	return OpenClusterDebugDBWithOpts(mainDBPath, ClusterDebugOpts{})
}

// OpenClusterDebugDBWithOpts opens the cluster-debug sidecar with the
// supplied options. Callers opening a DB whose rows may have expired MUST
// set ExtendExpiryDays > 0 or the subsequent Attach will fail.
func OpenClusterDebugDBWithOpts(mainDBPath string, opts ClusterDebugOpts) (*ClusterDebugDB, error) {
	path := mainDBPath + ".cluster-debug.db"
	// foreign_keys is a no-op for this DB (no FKs defined) but we set it
	// anyway to satisfy the AC16 contract that every opened DB verifies
	// PRAGMA foreign_keys=ON at open time.
	dsn := path + "?_pragma=busy_timeout%3d5000&_pragma=foreign_keys%3d1"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open cluster debug db %s: %w", path, err)
	}
	// WAL mode so the main ClaimsDB (on a separate connection pool) can
	// read committed writes without blocking the attached writer.
	if _, err := sqldb.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	// Verify foreign_keys=ON actually landed on the connection (AC16).
	var fk int
	if err := sqldb.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("query foreign_keys pragma: %w", err)
	}
	if fk != 1 {
		sqldb.Close()
		return nil, fmt.Errorf("cluster debug db %s has foreign_keys=%d, expected 1", path, fk)
	}
	if _, err := sqldb.Exec(createClusterDebugSchema); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("create cluster_debug schema: %w", err)
	}
	maxRows := opts.MaxRows
	if maxRows == 0 {
		maxRows = MaxClusterDebugRows
	}
	cd := &ClusterDebugDB{db: sqldb, path: path, maxRows: maxRows}
	// Enforce the expiry policy: fail if any row is past expiry unless the
	// caller explicitly extended the window. The extension path logs a
	// boot warning every time (intentional — noisy by design).
	if err := cd.checkExpiry(opts.ExtendExpiryDays); err != nil {
		sqldb.Close()
		return nil, err
	}
	return cd, nil
}

// createClusterDebugSchema is the DDL executed on open. Any change here
// requires bumping the schema comment in the Phase 3 PRD.
const createClusterDebugSchema = `
CREATE TABLE IF NOT EXISTS cluster_debug (
  fact_id               INTEGER,
  cluster_key           TEXT,
  nearest_body_match_id INTEGER,
  body_token_jaccard    REAL,
  expires_at            INTEGER NOT NULL DEFAULT (strftime('%s','now') + 7776000),
  created_at            INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
`

// Path returns the on-disk path of the cluster-debug database file.
func (c *ClusterDebugDB) Path() string { return c.path }

// MaxRows returns the row cap in effect for this handle.
func (c *ClusterDebugDB) MaxRows() int { return c.maxRows }

// Close closes the underlying database handle.
func (c *ClusterDebugDB) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// DB exposes the underlying *sql.DB for integration tests only.
func (c *ClusterDebugDB) DB() *sql.DB { return c.db }

// checkExpiry refuses to proceed if any row is past its expires_at unless
// extendDays > 0, in which case a boot warning is logged.
func (c *ClusterDebugDB) checkExpiry(extendDays int) error {
	now := time.Now().Unix()
	var expired int
	err := c.db.QueryRow(
		`SELECT COUNT(*) FROM cluster_debug WHERE expires_at < ?`, now,
	).Scan(&expired)
	if err != nil {
		return fmt.Errorf("check expiry: %w", err)
	}
	if expired == 0 {
		return nil
	}
	if extendDays <= 0 {
		return fmt.Errorf(
			"cluster debug db %s has %d rows past expiry; pass ExtendExpiryDays > 0 to proceed",
			c.path, expired,
		)
	}
	log.Printf("[cluster_debug] expiry extended by %d days at %s (%d rows past expiry)",
		extendDays, c.path, expired)
	return nil
}

// Attach runs `ATTACH DATABASE` on the main ClaimsDB connection so
// subsequent writes can reference the cluster-debug table via the
// attached alias.
//
// IMPORTANT: ATTACH is connection-local in SQLite. Go's database/sql
// manages a connection pool, so an ATTACH run on one connection is NOT
// visible from another. To make the attach globally visible for the
// duration of the sidecar's use, Attach pins the main pool to a single
// connection via SetMaxOpenConns(1). Callers must Detach (or Close the
// sidecar) when done, after which MaxOpenConns can be restored via the
// regular ClaimsDB.SetMaxOpenConns helper.
//
// Safe to call multiple times — ATTACH returns "already in use" on the
// second call, which we treat as a no-op.
func (c *ClusterDebugDB) Attach(main *ClaimsDB) error {
	if main == nil {
		return fmt.Errorf("attach: main ClaimsDB is nil")
	}
	// Pin to a single connection so every subsequent Exec/Query sees the
	// same ATTACH state. This is the pragmatic workaround for SQLite's
	// per-connection ATTACH semantics under Go's sql.DB pool.
	main.db.SetMaxOpenConns(1)
	sql := fmt.Sprintf("ATTACH DATABASE '%s' AS %s", c.path, clusterDebugAlias)
	if _, err := main.exec.Exec(sql); err != nil {
		if isAlreadyAttachedErr(err) {
			return nil
		}
		return fmt.Errorf("attach cluster debug: %w", err)
	}
	return nil
}

// Detach runs `DETACH DATABASE` on the main ClaimsDB connection. Callers
// should detach before closing the ClusterDebugDB.
func (c *ClusterDebugDB) Detach(main *ClaimsDB) error {
	if main == nil {
		return nil
	}
	sql := fmt.Sprintf("DETACH DATABASE %s", clusterDebugAlias)
	if _, err := main.exec.Exec(sql); err != nil {
		return fmt.Errorf("detach cluster debug: %w", err)
	}
	return nil
}

// isAlreadyAttachedErr reports whether err indicates the cluster-debug
// database was already attached to the target connection.
func isAlreadyAttachedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsAny(msg, []string{
		"already in use",
		"already attached",
		"is in use",
	})
}

// containsAny reports whether s contains any of the substrings.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if len(sub) == 0 {
			continue
		}
		if indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

// indexOf is a tiny stdlib-free substring search used by containsAny.
// Kept local so the file has zero dependencies beyond database/sql.
func indexOf(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// RowCount returns the number of rows currently in cluster_debug.
func (c *ClusterDebugDB) RowCount() (int, error) {
	var n int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM cluster_debug`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count cluster_debug rows: %w", err)
	}
	return n, nil
}

// recentDifferentClusterBodies returns up to limit bodies from tribal_facts
// whose cluster_key differs from excludeKey, most recent first. Used to
// compute nearest_body_match for a newly inserted fact. Runs against the
// main claims DB (not the attached cluster-debug DB).
func recentDifferentClusterBodies(main *ClaimsDB, excludeKey string, limit int) ([]recentBody, error) {
	rows, err := main.exec.Query(`
		SELECT id, body
		FROM tribal_facts
		WHERE cluster_key != ?
		ORDER BY id DESC
		LIMIT ?
	`, excludeKey, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent bodies: %w", err)
	}
	defer rows.Close()
	var out []recentBody
	for rows.Next() {
		var b recentBody
		if err := rows.Scan(&b.ID, &b.Body); err != nil {
			return nil, fmt.Errorf("scan recent body: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// recentBody is an internal row used by recentDifferentClusterBodies.
type recentBody struct {
	ID   int64
	Body string
}

// writeDebugRow inserts one row into the attached cluster-debug table
// unless the row cap has been reached. Returns true if the row was
// inserted, false if the cap refused the write. Logs a structured warning
// on the refusal path. Called from UpsertTribalFact after the main
// transaction has committed.
func (c *ClusterDebugDB) writeDebugRow(
	main *ClaimsDB,
	factID int64,
	clusterKey string,
	nearestID int64,
	jaccard float64,
) (bool, error) {
	n, err := c.RowCount()
	if err != nil {
		return false, err
	}
	if n >= c.maxRows {
		log.Printf("[cluster_debug] row cap reached (%d >= %d); skipping write for fact %d",
			n, c.maxRows, factID)
		return false, nil
	}
	// Write through the attached alias on the main connection so the
	// operation participates in the same transaction scope as the caller
	// decides to give it. All queries against cluster_debug go through the
	// main handle, not the sidecar handle, because only the main handle
	// knows about the ATTACH alias.
	query := fmt.Sprintf(
		`INSERT INTO %s.cluster_debug (fact_id, cluster_key, nearest_body_match_id, body_token_jaccard) VALUES (?, ?, ?, ?)`,
		clusterDebugAlias,
	)
	if _, err := main.exec.Exec(query, factID, clusterKey, nullableInt64(nearestID), jaccard); err != nil {
		return false, fmt.Errorf("insert cluster_debug row: %w", err)
	}
	return true, nil
}

// SweepDanglingFactIDs walks cluster_debug rows and sets fact_id and
// nearest_body_match_id to NULL when the referenced tribal_facts row no
// longer exists. Implements the application-side equivalent of the FK
// `ON DELETE SET NULL` constraint that SQLite cannot enforce across
// attached databases. Runs directly against the sidecar handle (no ATTACH
// required) so the caller does not need the main DB in a specific state.
func (c *ClusterDebugDB) SweepDanglingFactIDs(main *ClaimsDB) error {
	if main == nil {
		return fmt.Errorf("sweep: main ClaimsDB is nil")
	}
	rows, err := c.db.Query(
		`SELECT rowid, fact_id, nearest_body_match_id FROM cluster_debug`,
	)
	if err != nil {
		return fmt.Errorf("scan cluster_debug for sweep: %w", err)
	}
	type toClear struct {
		rowid        int64
		clearFactID  bool
		clearNearest bool
	}
	var updates []toClear
	for rows.Next() {
		var rowid int64
		var factID, nearest sql.NullInt64
		if err := rows.Scan(&rowid, &factID, &nearest); err != nil {
			rows.Close()
			return fmt.Errorf("scan sweep row: %w", err)
		}
		u := toClear{rowid: rowid}
		if factID.Valid {
			exists, existsErr := factExists(main, factID.Int64)
			if existsErr != nil {
				rows.Close()
				return existsErr
			}
			if !exists {
				u.clearFactID = true
			}
		}
		if nearest.Valid {
			exists, existsErr := factExists(main, nearest.Int64)
			if existsErr != nil {
				rows.Close()
				return existsErr
			}
			if !exists {
				u.clearNearest = true
			}
		}
		if u.clearFactID || u.clearNearest {
			updates = append(updates, u)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sweep rows: %w", err)
	}
	for _, u := range updates {
		switch {
		case u.clearFactID && u.clearNearest:
			_, err = c.db.Exec(
				`UPDATE cluster_debug SET fact_id = NULL, nearest_body_match_id = NULL WHERE rowid = ?`,
				u.rowid,
			)
		case u.clearFactID:
			_, err = c.db.Exec(
				`UPDATE cluster_debug SET fact_id = NULL WHERE rowid = ?`, u.rowid,
			)
		case u.clearNearest:
			_, err = c.db.Exec(
				`UPDATE cluster_debug SET nearest_body_match_id = NULL WHERE rowid = ?`, u.rowid,
			)
		}
		if err != nil {
			return fmt.Errorf("update sweep row %d: %w", u.rowid, err)
		}
	}
	return nil
}

// factExists reports whether a tribal_facts row with the given ID is
// present in the main DB.
func factExists(main *ClaimsDB, id int64) (bool, error) {
	var one int
	err := main.exec.QueryRow(
		`SELECT 1 FROM tribal_facts WHERE id = ? LIMIT 1`, id,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check tribal fact existence: %w", err)
	}
	return one == 1, nil
}

// nullableInt64 returns an interface{} suitable for INSERT with a nullable
// INTEGER column; zero maps to SQL NULL, matching the convention used by
// nullableString.
func nullableInt64(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

// ListDebugPairs returns up to limit rows from cluster_debug, ordered by
// insertion order. Intended for tests and calibration tooling — this is
// NOT part of any production read path.
func (c *ClusterDebugDB) ListDebugPairs(limit int) ([]DebugPair, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := c.db.Query(`
		SELECT COALESCE(fact_id, 0),
		       COALESCE(cluster_key, ''),
		       COALESCE(nearest_body_match_id, 0),
		       COALESCE(body_token_jaccard, 0),
		       created_at
		FROM cluster_debug
		ORDER BY rowid
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query cluster_debug pairs: %w", err)
	}
	defer rows.Close()
	var out []DebugPair
	for rows.Next() {
		var p DebugPair
		if err := rows.Scan(&p.FactID, &p.ClusterKey, &p.NearestMatchID, &p.BodyTokenJaccard, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan cluster_debug row: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- ClaimsDB hook points ---

// EnableClusterDebug wires a ClusterDebugDB sidecar into c so subsequent
// UpsertTribalFact calls write calibration rows. The sidecar must already
// be attached (via ClusterDebugDB.Attach) before enabling.
func (c *ClaimsDB) EnableClusterDebug(cd *ClusterDebugDB) {
	c.clusterDebug = cd
}

// DisableClusterDebug clears the sidecar hook.
func (c *ClaimsDB) DisableClusterDebug() {
	c.clusterDebug = nil
}

// ClusterDebugHandle returns the attached sidecar, or nil when disabled.
func (c *ClaimsDB) ClusterDebugHandle() *ClusterDebugDB {
	return c.clusterDebug
}
