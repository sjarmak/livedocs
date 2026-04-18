package tribal_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPRCommentMinerUnexported is a CI lint that enforces the encapsulation
// contract described in live_docs-m7v.13: the PR comment miner must only be
// reachable through TribalMiningService.
//
// Since the underlying miner type is unexported (`prCommentMiner`), the Go
// compiler already rejects external construction. This lint additionally
// catches two ways the encapsulation could silently regress:
//
//  1. Re-exporting the type (e.g. `type PRCommentMiner = prCommentMiner`)
//  2. Re-exporting a constructor (e.g. `func NewPRCommentMiner(...) ...`)
//     that external callers could use to bypass TribalMiningService.
//
// If either pattern appears in a non-test .go file inside extractor/tribal/
// the test fails. Tests are exempt because they may legitimately reference
// the identifier in function names (e.g. TestPRCommentMiner_GHOutputParsing).
func TestPRCommentMinerUnexported(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	pkgDir := wd // extractor/tribal

	// Match either:
	//   type PRCommentMiner ...
	//   func NewPRCommentMiner(...
	reExportedType := regexp.MustCompile(`^\s*type\s+PRCommentMiner(\s|=)`)
	reExportedCtor := regexp.MustCompile(`^\s*func\s+NewPRCommentMiner\s*\(`)

	var violations []string

	walkErr := filepath.WalkDir(pkgDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "testdata" || name == "normalize" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		relPath, _ := filepath.Rel(pkgDir, path)
		for i, line := range strings.Split(string(content), "\n") {
			if reExportedType.MatchString(line) {
				violations = append(violations, fmt.Sprintf(
					"%s:%d: exported type PRCommentMiner must stay unexported (use prCommentMiner) — "+
						"production callers must go through TribalMiningService",
					relPath, i+1,
				))
			}
			if reExportedCtor.MatchString(line) {
				violations = append(violations, fmt.Sprintf(
					"%s:%d: exported constructor NewPRCommentMiner would bypass TribalMiningService — "+
						"use NewTribalMiningService with PRMinerConfig instead",
					relPath, i+1,
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

// TestPRCommentMinerNotReferencedExternally is a CI lint that scans the rest
// of the repo (outside extractor/tribal/) for any reference to the exported
// symbols tribal.PRCommentMiner or tribal.NewPRCommentMiner. Such references
// would indicate someone re-exported the type or constructor and wired it
// into a caller — bypassing the service's budget and cursor bookkeeping.
//
// This is belt-and-suspenders: the compiler already rejects references to
// unexported identifiers across packages, but a future regression that
// re-exports the type (caught by TestPRCommentMinerUnexported in-package)
// would also show up here in caller code, making the failure site obvious.
func TestPRCommentMinerNotReferencedExternally(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Join(wd, "..", "..")

	// Anchor the match on "tribal." prefix so we don't false-positive on the
	// in-package uses or on test function names (TestPRCommentMiner_...).
	forbidden := regexp.MustCompile(`\btribal\.(PRCommentMiner|NewPRCommentMiner)\b`)

	// Paths that are allowed to mention these identifiers (docs only).
	allowedDirs := []string{
		filepath.Join("extractor", "tribal"),
		"docs",
		".beads",
	}

	var violations []string

	walkErr := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" ||
				name == "testdata" || name == "bin" || name == ".claude" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return nil
		}
		for _, allowed := range allowedDirs {
			if strings.HasPrefix(relPath, allowed+string(os.PathSeparator)) || relPath == allowed {
				return nil
			}
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		for i, line := range strings.Split(string(content), "\n") {
			if m := forbidden.FindString(line); m != "" {
				violations = append(violations, fmt.Sprintf(
					"%s:%d: external reference to %s — production callers must use tribal.NewTribalMiningService with tribal.PRMinerConfig",
					relPath, i+1, m,
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
