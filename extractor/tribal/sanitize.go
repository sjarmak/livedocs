// Package tribal — sanitize.go defines the sanitization boundary for
// MiningResult.FailedErrors.
//
// Why this exists
// ---------------
// The Wave 1 convergence security review (live_docs-m7v.21) flagged that raw
// upsert errors surfaced on MiningResult may carry:
//
//   - SQLite schema details (table/column names, constraint identifiers)
//   - offending row values embedded in constraint-violation messages
//   - LLM-generated content echoed back inside wrapped error strings
//   - user-supplied tokens (file paths, symbol names) in error context
//
// Any of those could leak through MCP tool responses to an agent, or through
// log aggregation to a lower-trust system, once a caller calls .Error() on a
// retained error value.
//
// Contract
// --------
// sanitizeUpsertError maps an arbitrary upsert error to one of a fixed set of
// canonical category strings. Callers ONLY ever see these categories — never
// the raw error's .Error() output. Operators retain debuggability via a
// separate server-side log line emitted at the capture site (MineFile), which
// may include the full raw error; the data-surface field MiningResult
// .FailedErrors is strictly caller-facing.
//
// Adding a new category
// ---------------------
// Adding a new category is a conscious API change. It must be:
//  1. Added to the switch in sanitizeUpsertError.
//  2. Added to the canonicalCategories set in sanitize_test.go.
//
// The test pins the set so accidental drift is caught.
package tribal

import (
	"context"
	"errors"
	"strings"
)

// These constants are the complete vocabulary of sanitized FailedErrors
// entries. They are intentionally coarse so that a caller cannot use them
// to infer raw error content (e.g. which column violated a constraint).
const (
	catNilError                   = "nil_error"
	catUniqueConstraint           = "unique_constraint_violation"
	catCheckConstraint            = "check_constraint_violation"
	catForeignKeyConstraint       = "foreign_key_constraint"
	catNotNullConstraint          = "not_null_constraint"
	catDatabaseBusy               = "database_busy"
	catDatabaseLocked             = "database_locked"
	catDatabaseError              = "database_error"
	catProvenanceValidationFailed = "provenance_validation_failed"
	catContextCanceled            = "context_canceled"
	catUpsertFailed               = "upsert_failed"
)

// sanitizeUpsertError classifies a raw error from the fact-upsert path into a
// canonical category string. It never returns raw error text, wrapped
// messages, or any substring of the input.
//
// The function is deterministic and allocation-free for the happy paths:
// literal string constants are returned, and the only work performed on the
// input is a lower-cased substring match against stable SQLite diagnostic
// prefixes.
//
// The order of checks matters. SQLite returns composite error strings for
// some failures (e.g. a UNIQUE violation on a table that is simultaneously
// locked); we classify by the most specific signal first and fall back to
// catDatabaseError for anything that clearly originated from the DB but
// doesn't match a known constraint type.
func sanitizeUpsertError(err error) string {
	if err == nil {
		return catNilError
	}

	// Context cancellation is a canonical first-class case because callers
	// often retry on cancellation but should not retry on a constraint
	// violation. We expose this explicitly so partial-mining code can make
	// that distinction without reading the raw error.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return catContextCanceled
	}

	// Lower-case once; all match substrings below are ASCII lowercase.
	msg := strings.ToLower(err.Error())

	// SQLite diagnostic prefixes are stable across versions:
	//   "UNIQUE constraint failed: ..."
	//   "CHECK constraint failed: ..."
	//   "FOREIGN KEY constraint failed..."
	//   "NOT NULL constraint failed: ..."
	// See SQLite source: src/build.c, src/vdbe.c.
	switch {
	case strings.Contains(msg, "unique constraint"):
		return catUniqueConstraint
	case strings.Contains(msg, "check constraint"):
		return catCheckConstraint
	case strings.Contains(msg, "foreign key constraint"):
		return catForeignKeyConstraint
	case strings.Contains(msg, "not null constraint"):
		return catNotNullConstraint
	}

	// Busy / locked are operational (retryable) signals, distinct from
	// constraint violations which are usually bugs.
	if strings.Contains(msg, "database is busy") || strings.Contains(msg, "sqlite_busy") {
		return catDatabaseBusy
	}
	if strings.Contains(msg, "is locked") || strings.Contains(msg, "sqlite_locked") {
		return catDatabaseLocked
	}

	// Generic database signals without a more specific classifier. The match
	// is intentionally narrow — we do NOT fall into this branch for any
	// error that mentions "sql", since the upsert call path itself mentions
	// SQL in logs; we want only errors that *originated* from the driver.
	if strings.Contains(msg, "sqlite") || strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "no such column") || strings.Contains(msg, "syntax error") {
		return catDatabaseError
	}

	// Provenance-envelope validation failures are already coarse and
	// name-mangled ("missing source_quote", etc.) but they may echo field
	// names from user content, so route them through the sanitizer too.
	if strings.Contains(msg, "provenance") || strings.Contains(msg, "missing source_quote") ||
		strings.Contains(msg, "invalid evidence") {
		return catProvenanceValidationFailed
	}

	// Unknown failure. Collapse to the coarsest possible category.
	return catUpsertFailed
}
