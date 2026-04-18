package pipeline

import (
	"fmt"
	"strings"

	"github.com/sjarmak/livedocs/db"
)

// reverseDepPaths returns file paths that import symbols from any of the
// changed files. These files should be re-extracted to keep cross-file
// relationships (imports, implements) up to date.
//
// The lookup joins the symbols and claims tables:
//  1. Find distinct import_path values for symbols defined in changedPaths
//  2. Find "imports" claims whose subject symbol_name matches those import paths
//  3. Return the source_file values (excluding files already in changedPaths)
func reverseDepPaths(claimsDB *db.ClaimsDB, changedPaths []string) ([]string, error) {
	if len(changedPaths) == 0 {
		return nil, nil
	}

	changedSet := make(map[string]bool, len(changedPaths))
	for _, p := range changedPaths {
		changedSet[p] = true
	}

	// Step 1: Find import paths associated with the changed files.
	// A symbol's import_path identifies the package/module it belongs to.
	// We look for symbols that have "defines" claims sourced from changed files.
	placeholders := make([]string, len(changedPaths))
	args := make([]interface{}, len(changedPaths))
	for i, p := range changedPaths {
		placeholders[i] = "?"
		args[i] = p
	}
	inClause := strings.Join(placeholders, ", ")

	importPathQuery := fmt.Sprintf(`
		SELECT DISTINCT s.import_path
		FROM symbols s
		INNER JOIN claims c ON c.subject_id = s.id
		WHERE c.predicate = 'defines'
		  AND c.source_file IN (%s)
	`, inClause)

	rows, err := claimsDB.DB().Query(importPathQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("reverse-dep: query import paths: %w", err)
	}
	defer rows.Close()

	var importPaths []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, fmt.Errorf("reverse-dep: scan import path: %w", err)
		}
		importPaths = append(importPaths, ip)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reverse-dep: iterate import paths: %w", err)
	}

	if len(importPaths) == 0 {
		return nil, nil
	}

	// Step 2: Find files that have "imports" claims referencing those import paths.
	// The subject symbol_name of an import claim is the imported path.
	ipPlaceholders := make([]string, len(importPaths))
	ipArgs := make([]interface{}, len(importPaths))
	for i, ip := range importPaths {
		ipPlaceholders[i] = "?"
		ipArgs[i] = ip
	}
	ipInClause := strings.Join(ipPlaceholders, ", ")

	reverseDepQuery := fmt.Sprintf(`
		SELECT DISTINCT c.source_file
		FROM claims c
		INNER JOIN symbols s ON s.id = c.subject_id
		WHERE c.predicate = 'imports'
		  AND s.symbol_name IN (%s)
	`, ipInClause)

	depRows, err := claimsDB.DB().Query(reverseDepQuery, ipArgs...)
	if err != nil {
		return nil, fmt.Errorf("reverse-dep: query importers: %w", err)
	}
	defer depRows.Close()

	var result []string
	seen := make(map[string]bool)
	for depRows.Next() {
		var sf string
		if err := depRows.Scan(&sf); err != nil {
			return nil, fmt.Errorf("reverse-dep: scan source file: %w", err)
		}
		// Exclude files already in the changed set.
		if !changedSet[sf] && !seen[sf] {
			seen[sf] = true
			result = append(result, sf)
		}
	}
	if err := depRows.Err(); err != nil {
		return nil, fmt.Errorf("reverse-dep: iterate importers: %w", err)
	}

	return result, nil
}
