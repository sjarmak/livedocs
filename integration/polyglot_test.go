//go:build integration

package integration

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// hackRoot returns the path to ~/kubernetes/kubernetes/hack/ or skips.
func hackRoot(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot determine home directory: %v", err)
	}
	root := filepath.Join(home, "kubernetes", "kubernetes", "hack")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skipf("kubernetes/kubernetes/hack not found at %s", root)
	}
	return root
}

// TestPolyglotExtraction validates that livedocs extract produces claims
// for both Python (.py) and Shell (.sh) files in the kubernetes hack/ directory.
func TestPolyglotExtraction(t *testing.T) {
	bin := buildLivedocs(t)
	hackDir := hackRoot(t)

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "polyglot.claims.db")

	// Run extraction on the hack/ directory.
	cmd := exec.Command(bin, "extract", hackDir, "--repo", "kubernetes", "-o", dbPath)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	out, err := cmd.CombinedOutput()
	t.Logf("extract output:\n%s", string(out))
	if err != nil {
		t.Fatalf("livedocs extract failed: %v", err)
	}

	// Open the resulting database.
	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Query Python claims.
	var pyCount int
	err = database.QueryRow("SELECT COUNT(*) FROM claims WHERE source_file LIKE '%.py'").Scan(&pyCount)
	if err != nil {
		t.Fatalf("query python claims: %v", err)
	}
	t.Logf("Python claims: %d", pyCount)
	if pyCount == 0 {
		t.Error("expected >0 claims for .py files, got 0")
	}

	// Query Shell claims.
	var shCount int
	err = database.QueryRow("SELECT COUNT(*) FROM claims WHERE source_file LIKE '%.sh'").Scan(&shCount)
	if err != nil {
		t.Fatalf("query shell claims: %v", err)
	}
	t.Logf("Shell claims: %d", shCount)
	if shCount == 0 {
		t.Error("expected >0 claims for .sh files, got 0")
	}

	// Log total claims for context.
	var totalClaims int
	err = database.QueryRow("SELECT COUNT(*) FROM claims").Scan(&totalClaims)
	if err != nil {
		t.Fatalf("query total claims: %v", err)
	}
	t.Logf("Total claims in DB: %d (py=%d, sh=%d)", totalClaims, pyCount, shCount)
}
