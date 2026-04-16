package db

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestClusterDebugImportBoundary is a CI lint that prevents the
// calibration table name from leaking into the rest of the codebase.
// If a new caller wants to read or write cluster_debug it MUST go
// through the types defined in db/cluster_debug.go, not by copying
// the SQL table name around.
//
// The allowlist is intentionally tiny: every path below must exist
// either as a source file (or directory prefix) that is genuinely
// allowed to mention cluster_debug, or as a documentation tree that
// discusses the design.
func TestClusterDebugImportBoundary(t *testing.T) {
	root := repoRoot(t)
	re := regexp.MustCompile(`(?i)\bcluster_debug\b`)

	allowedFiles := map[string]bool{
		"extractor/tribal/upsert.go":        true,
		"db/cluster_debug.go":               true,
		"db/cluster_debug_test.go":          true,
		"db/cluster_debug_boundary_test.go": true,
		"db/phase5_readiness_test.go":       true,
	}
	allowedPrefixes := []string{
		".claude/",
		"docs/",
		"testdata/",
	}

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip vendor + hidden directories except .claude (which is allowlisted).
			name := info.Name()
			if path != root && strings.HasPrefix(name, ".") && name != ".claude" {
				return filepath.SkipDir
			}
			if name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		// Only scan Go source + related text files that could leak a reference.
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".sql" && ext != ".yaml" && ext != ".yml" && ext != ".toml" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if allowedFiles[filepath.ToSlash(rel)] {
			return nil
		}
		for _, prefix := range allowedPrefixes {
			if strings.HasPrefix(filepath.ToSlash(rel), prefix) || strings.Contains(filepath.ToSlash(rel), "/"+prefix) {
				return nil
			}
		}
		if strings.Contains(filepath.ToSlash(rel), "/testdata/") {
			return nil
		}

		f, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer f.Close()

		// Honor //go:build calibration exemption.
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		sawBuildTagCalibration := false
		var lineNo int
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if lineNo < 10 && strings.Contains(line, "//go:build") && strings.Contains(line, "calibration") {
				sawBuildTagCalibration = true
			}
			if sawBuildTagCalibration {
				continue
			}
			if re.MatchString(line) {
				t.Errorf("%s:%d references forbidden token cluster_debug outside allowlist: %s", rel, lineNo, line)
			}
		}
		return scanner.Err()
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
}

// repoRoot locates the repository root by walking up from the test file
// until a go.mod is found.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for dir := wd; dir != "/"; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
	}
	t.Fatalf("could not locate repo root from %s", wd)
	return ""
}
