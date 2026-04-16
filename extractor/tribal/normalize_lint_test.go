package tribal_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNormalizeFunctionSinglePackage is a CI lint test that asserts normalize*
// functions exist in exactly one package: extractor/tribal/normalize.
// If any function named normalize* (case-insensitive) appears in a .go file
// outside that package, the test fails. This prevents the normalization drift
// described in premortem F7 (divergence point #6).
func TestNormalizeFunctionSinglePackage(t *testing.T) {
	// Walk up from the test's directory to find the repo root.
	// The test lives at extractor/tribal/ so we go up 2 levels.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Join(wd, "..", "..")

	// Regex matches Go function declarations with "normalize" (case-insensitive)
	// in the function name. Matches:
	//   func normalize...
	//   func (r *T) normalize...
	//   func Normalize...
	funcDeclRe := regexp.MustCompile(`(?i)^func\s+(?:\([^)]*\)\s+)?(normalize\w*)`)

	// Allowed path: only the normalize package itself.
	allowedDir := filepath.Join("extractor", "tribal", "normalize")

	// Scope the scan to the extractor/ tree where tribal normalization drift
	// is the actual risk. Other packages (e.g. versionnorm/) have unrelated
	// normalize* functions for Go module paths — those are not tribal.
	scanRoot := filepath.Join(repoRoot, "extractor")

	var violations []string

	walkErr := filepath.WalkDir(scanRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip test files — test helpers may reference normalize for setup.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return nil
		}

		// Skip the allowed package.
		if strings.HasPrefix(relPath, allowedDir+string(os.PathSeparator)) || relPath == allowedDir {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		for i, line := range strings.Split(string(content), "\n") {
			if m := funcDeclRe.FindStringSubmatch(line); m != nil {
				violations = append(violations, fmt.Sprintf(
					"%s:%d: func %s (normalize* functions must live in %s)",
					relPath, i+1, m[1], allowedDir,
				))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}

	for _, v := range violations {
		t.Errorf("LINT: %s", v)
	}
}
