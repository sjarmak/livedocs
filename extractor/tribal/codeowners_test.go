package tribal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sjarmak/livedocs/db"
)

// newTestDB creates a fresh ClaimsDB with core + tribal schemas.
func newTestDB(t *testing.T) *db.ClaimsDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	cdb, err := db.OpenClaimsDB(path)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := cdb.CreateTribalSchema(); err != nil {
		t.Fatalf("create tribal schema: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	return cdb
}

// writeFile is a test helper that writes content to a file, creating dirs.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestCodeownersParseBasic(t *testing.T) {
	dir := t.TempDir()
	coPath := filepath.Join(dir, "CODEOWNERS")
	writeFile(t, coPath, `# Top-level owners
* @global-owner

# Frontend
/src/frontend/ @frontend-team @designer

# Backend
/src/backend/ @backend-team
`)

	rules, err := ParseCodeowners(coPath)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}

	// Rule 1: * @global-owner
	if rules[0].Pattern != "*" {
		t.Errorf("rule 0 pattern = %q, want %q", rules[0].Pattern, "*")
	}
	if len(rules[0].Owners) != 1 || rules[0].Owners[0] != "@global-owner" {
		t.Errorf("rule 0 owners = %v, want [@global-owner]", rules[0].Owners)
	}

	// Rule 2: /src/frontend/ @frontend-team @designer
	if rules[1].Pattern != "/src/frontend/" {
		t.Errorf("rule 1 pattern = %q, want %q", rules[1].Pattern, "/src/frontend/")
	}
	if len(rules[1].Owners) != 2 {
		t.Errorf("rule 1 owners count = %d, want 2", len(rules[1].Owners))
	}

	// Rule 3: /src/backend/ @backend-team
	if rules[2].Pattern != "/src/backend/" {
		t.Errorf("rule 2 pattern = %q, want %q", rules[2].Pattern, "/src/backend/")
	}
}

func TestCodeownersParseCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	coPath := filepath.Join(dir, "CODEOWNERS")
	writeFile(t, coPath, `
# This is a comment


# Another comment
*.go @go-team

# Trailing comment
*.js @js-team
`)

	rules, err := ParseCodeowners(coPath)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
}

func TestCodeownersParseInlineComment(t *testing.T) {
	dir := t.TempDir()
	coPath := filepath.Join(dir, "CODEOWNERS")
	writeFile(t, coPath, `*.go @go-team # Go files owned by go-team
`)

	rules, err := ParseCodeowners(coPath)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if len(rules[0].Owners) != 1 || rules[0].Owners[0] != "@go-team" {
		t.Errorf("owners = %v, want [@go-team]", rules[0].Owners)
	}
}

func TestFindCodeownersFiles(t *testing.T) {
	dir := t.TempDir()

	// Create CODEOWNERS in root, docs/, and .github/
	writeFile(t, filepath.Join(dir, "CODEOWNERS"), "* @root\n")
	writeFile(t, filepath.Join(dir, "docs", "CODEOWNERS"), "* @docs\n")
	writeFile(t, filepath.Join(dir, ".github", "CODEOWNERS"), "* @gh\n")

	files := FindCodeownersFiles(dir)
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(files), files)
	}
}

func TestFindCodeownersFilesNone(t *testing.T) {
	dir := t.TempDir()
	files := FindCodeownersFiles(dir)
	if len(files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(files))
	}
}

func TestCodeownersExtractBasic(t *testing.T) {
	cdb := newTestDB(t)
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "CODEOWNERS"), `# Team ownership
* @global-owner
/src/api/ @api-team @lead
/docs/ @docs-team
`)

	n, err := ExtractCodeowners(cdb, dir, "test/repo")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 facts inserted, got %d", n)
	}

	// Verify facts via DB query.
	facts, err := cdb.GetTribalFactsByKind("ownership")
	if err != nil {
		t.Fatalf("get facts: %v", err)
	}
	if len(facts) != 3 {
		t.Fatalf("expected 3 facts in DB, got %d", len(facts))
	}

	for i, f := range facts {
		// All facts must be ownership kind.
		if f.Kind != "ownership" {
			t.Errorf("fact[%d] kind = %q, want %q", i, f.Kind, "ownership")
		}
		// Model must be empty (NULL in DB = empty string after scan).
		if f.Model != "" {
			t.Errorf("fact[%d] model = %q, want empty (NULL)", i, f.Model)
		}
		// Confidence must be 1.0.
		if f.Confidence != 1.0 {
			t.Errorf("fact[%d] confidence = %f, want 1.0", i, f.Confidence)
		}
		// Extractor metadata.
		if f.Extractor != "codeowners" {
			t.Errorf("fact[%d] extractor = %q, want %q", i, f.Extractor, "codeowners")
		}
		// Exactly 1 evidence row.
		if len(f.Evidence) != 1 {
			t.Errorf("fact[%d] evidence count = %d, want 1", i, len(f.Evidence))
			continue
		}
		ev := f.Evidence[0]
		if ev.SourceType != "codeowners" {
			t.Errorf("fact[%d] evidence source_type = %q, want %q", i, ev.SourceType, "codeowners")
		}
		if ev.SourceRef != "CODEOWNERS" {
			t.Errorf("fact[%d] evidence source_ref = %q, want %q", i, ev.SourceRef, "CODEOWNERS")
		}
	}
}

func TestCodeownersExtractMultipleOwners(t *testing.T) {
	cdb := newTestDB(t)
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "CODEOWNERS"), `/src/api/ @api-team @lead @pm
`)

	n, err := ExtractCodeowners(cdb, dir, "test/repo")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 fact, got %d", n)
	}

	facts, err := cdb.GetTribalFactsByKind("ownership")
	if err != nil {
		t.Fatalf("get facts: %v", err)
	}
	if facts[0].Body != "@api-team, @lead, @pm" {
		t.Errorf("body = %q, want %q", facts[0].Body, "@api-team, @lead, @pm")
	}
	if facts[0].SourceQuote != "/src/api/ @api-team @lead @pm" {
		t.Errorf("source_quote = %q, want %q", facts[0].SourceQuote, "/src/api/ @api-team @lead @pm")
	}
}

func TestCodeownersExtractGithubDir(t *testing.T) {
	cdb := newTestDB(t)
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, ".github", "CODEOWNERS"), `*.go @go-team
*.rs @rust-team
*.py @python-team
`)

	n, err := ExtractCodeowners(cdb, dir, "test/repo")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 facts, got %d", n)
	}

	facts, err := cdb.GetTribalFactsByKind("ownership")
	if err != nil {
		t.Fatalf("get facts: %v", err)
	}
	// Verify evidence points to .github/CODEOWNERS.
	for i, f := range facts {
		if len(f.Evidence) != 1 {
			t.Errorf("fact[%d] evidence count = %d, want 1", i, len(f.Evidence))
			continue
		}
		if f.Evidence[0].SourceRef != filepath.Join(".github", "CODEOWNERS") {
			t.Errorf("fact[%d] source_ref = %q, want %q", i, f.Evidence[0].SourceRef, filepath.Join(".github", "CODEOWNERS"))
		}
	}
}

func TestCodeownersExtractDocsDir(t *testing.T) {
	cdb := newTestDB(t)
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "docs", "CODEOWNERS"), `*.md @docs-team
`)

	n, err := ExtractCodeowners(cdb, dir, "test/repo")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 fact, got %d", n)
	}

	facts, err := cdb.GetTribalFactsByKind("ownership")
	if err != nil {
		t.Fatalf("get facts: %v", err)
	}
	if facts[0].Evidence[0].SourceRef != filepath.Join("docs", "CODEOWNERS") {
		t.Errorf("source_ref = %q, want %q", facts[0].Evidence[0].SourceRef, filepath.Join("docs", "CODEOWNERS"))
	}
}

func TestCodeownersExtractNoFiles(t *testing.T) {
	cdb := newTestDB(t)
	dir := t.TempDir()

	n, err := ExtractCodeowners(cdb, dir, "test/repo")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 facts, got %d", n)
	}
}

func TestCodeownersExtractEmptyFile(t *testing.T) {
	cdb := newTestDB(t)
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "CODEOWNERS"), `# Only comments
# No actual rules

`)

	n, err := ExtractCodeowners(cdb, dir, "test/repo")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 facts for comment-only file, got %d", n)
	}
}
