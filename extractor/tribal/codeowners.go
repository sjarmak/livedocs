// Package tribal provides extractors for tribal knowledge from non-code sources.
package tribal

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sjarmak/livedocs/db"
)

const (
	codeownersExtractorName    = "codeowners"
	codeownersExtractorVersion = "0.1.0"
)

// CodeownersRule represents a single parsed rule from a CODEOWNERS file.
type CodeownersRule struct {
	Pattern string
	Owners  []string
	Line    int
}

// CodeownersFact holds the data needed to insert one ownership tribal fact.
type CodeownersFact struct {
	Rule       CodeownersRule
	FilePath   string // path to the CODEOWNERS file that produced this rule
	SourceLine string // the raw line text
}

// ParseCodeowners reads a CODEOWNERS file and returns the parsed rules.
// It skips blank lines, comment-only lines, and inline comments.
func ParseCodeowners(path string) ([]CodeownersRule, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open codeowners %s: %w", path, err)
	}
	defer f.Close()

	var rules []CodeownersRule
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Strip inline comments: find a # preceded by whitespace.
		if idx := strings.Index(line, " #"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			// A pattern with no owners — skip silently per GitHub spec.
			continue
		}

		rules = append(rules, CodeownersRule{
			Pattern: fields[0],
			Owners:  fields[1:],
			Line:    lineNum,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan codeowners %s: %w", path, err)
	}
	return rules, nil
}

// FindCodeownersFiles returns all CODEOWNERS files found in the standard
// locations (root, docs/, .github/, .gitlab/) relative to repoRoot.
func FindCodeownersFiles(repoRoot string) []string {
	candidates := []string{
		filepath.Join(repoRoot, "CODEOWNERS"),
		filepath.Join(repoRoot, "docs", "CODEOWNERS"),
		filepath.Join(repoRoot, ".github", "CODEOWNERS"),
		filepath.Join(repoRoot, ".gitlab", "CODEOWNERS"),
	}
	var found []string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			found = append(found, c)
		}
	}
	return found
}

// ExtractCodeowners parses CODEOWNERS files in the given repo root and
// inserts ownership tribal facts into the claims database.
//
// For each rule in each CODEOWNERS file, a synthetic symbol is upserted
// using the glob pattern as the symbol name (kind="path_pattern"), and
// one ownership tribal fact is created with:
//   - kind = "ownership"
//   - model = "" (deterministic — stored as NULL)
//   - confidence = 1.0
//   - source_quote = the raw CODEOWNERS line (pattern + owners)
//   - body = comma-separated list of owners
//   - exactly 1 tribal_evidence row with source_type="codeowners"
//
// Returns the number of facts inserted and any error encountered.
func ExtractCodeowners(cdb *db.ClaimsDB, repoRoot, repoName string) (int, error) {
	files := FindCodeownersFiles(repoRoot)
	if len(files) == 0 {
		return 0, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	inserted := 0

	for _, coFile := range files {
		rules, err := ParseCodeowners(coFile)
		if err != nil {
			return inserted, fmt.Errorf("extract codeowners: %w", err)
		}

		// Compute relative path for source_ref.
		relPath, err := filepath.Rel(repoRoot, coFile)
		if err != nil {
			relPath = coFile
		}

		for _, rule := range rules {
			// Upsert a synthetic symbol for the glob pattern.
			symID, err := cdb.UpsertSymbol(db.Symbol{
				Repo:       repoName,
				ImportPath: relPath,
				SymbolName: rule.Pattern,
				Language:   "codeowners",
				Kind:       "path_pattern",
				Visibility: "public",
			})
			if err != nil {
				return inserted, fmt.Errorf("upsert symbol for pattern %s: %w", rule.Pattern, err)
			}

			sourceLine := rule.Pattern + " " + strings.Join(rule.Owners, " ")
			bodyText := strings.Join(rule.Owners, ", ")
			contentHash := fmt.Sprintf("%x", sha256.Sum256([]byte(sourceLine)))

			fact := db.TribalFact{
				SubjectID:        symID,
				Kind:             "ownership",
				Body:             bodyText,
				SourceQuote:      sourceLine,
				Confidence:       1.0,
				Corroboration:    1,
				Extractor:        codeownersExtractorName,
				ExtractorVersion: codeownersExtractorVersion,
				Model:            "", // deterministic — NULL in DB
				StalenessHash:    contentHash,
				Status:           "active",
				CreatedAt:        now,
				LastVerified:     now,
			}

			evidence := []db.TribalEvidence{{
				SourceType:  "codeowners",
				SourceRef:   relPath,
				ContentHash: contentHash,
			}}

			if _, _, err := cdb.UpsertTribalFact(fact, evidence); err != nil {
				return inserted, fmt.Errorf("insert fact for pattern %s: %w", rule.Pattern, err)
			}
			inserted++
		}
	}

	return inserted, nil
}
