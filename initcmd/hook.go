package initcmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// hookMarkerBegin and hookMarkerEnd delimit the livedocs section in the hook file.
const (
	hookMarkerBegin = "# BEGIN livedocs post-commit hook"
	hookMarkerEnd   = "# END livedocs post-commit hook"
)

// hookScript returns the livedocs post-commit hook snippet.
func hookScript() string {
	return fmt.Sprintf(`%s
livedocs extract . 2>/dev/null || true
%s
`, hookMarkerBegin, hookMarkerEnd)
}

// InstallPostCommitHook installs a git post-commit hook that runs livedocs extract.
// If a post-commit hook already exists, it appends the livedocs section.
// If the livedocs section already exists, it is a no-op.
// Returns true if the hook was created or modified, false if already present.
func InstallPostCommitHook(repoRoot string) (bool, error) {
	gitDir := filepath.Join(repoRoot, ".git")
	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return false, fmt.Errorf("not a git repository: %s", repoRoot)
	}

	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return false, fmt.Errorf("create hooks dir: %w", err)
	}

	hookPath := filepath.Join(hooksDir, "post-commit")

	existing, err := os.ReadFile(hookPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read existing hook: %w", err)
	}

	content := string(existing)

	// Already installed — no-op.
	if strings.Contains(content, hookMarkerBegin) {
		return false, nil
	}

	// Build new content.
	var builder strings.Builder
	if len(content) == 0 {
		builder.WriteString("#!/bin/sh\n")
	} else {
		builder.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			builder.WriteString("\n")
		}
	}
	builder.WriteString(hookScript())

	if err := os.WriteFile(hookPath, []byte(builder.String()), 0755); err != nil {
		return false, fmt.Errorf("write hook: %w", err)
	}

	return true, nil
}
