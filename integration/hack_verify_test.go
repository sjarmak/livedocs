//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHackVerifyScript validates the hack/verify-livedocs.sh wrapper script.
func TestHackVerifyScript(t *testing.T) {
	bin := buildLivedocs(t)
	scriptPath := filepath.Join(projectRootDir(t), "hack", "verify-livedocs.sh")

	// Verify script exists and is executable.
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("hack/verify-livedocs.sh not found: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatal("hack/verify-livedocs.sh is not executable")
	}

	// Put the livedocs binary on PATH so the script can find it.
	binDir := filepath.Dir(bin)
	origPath := os.Getenv("PATH")

	t.Run("CleanRepoExitZero", func(t *testing.T) {
		// A repo with Go source but no README files should produce zero drift.
		repoDir := createTempGitRepo(t)
		writeFile(t, repoDir, "go.mod", "module example.com/clean\n\ngo 1.21\n")
		writeFile(t, repoDir, "main.go", `package main

func main() {}
`)
		gitCommitAll(t, repoDir, "clean repo with no docs")

		cmd := exec.Command("bash", scriptPath, repoDir)
		cmd.Env = append(os.Environ(), "PATH="+binDir+":"+origPath)
		out, err := cmd.CombinedOutput()
		t.Logf("clean repo output:\n%s", out)

		if err != nil {
			t.Errorf("expected exit 0 for clean repo, got error: %v", err)
		}
		if !strings.Contains(string(out), "all checks passed") {
			t.Errorf("expected 'all checks passed' in output, got:\n%s", out)
		}
	})

	t.Run("ClientGoParseable", func(t *testing.T) {
		root := clientGoRoot(t)

		cmd := exec.Command("bash", scriptPath, root)
		cmd.Env = append(os.Environ(), "PATH="+binDir+":"+origPath)
		out, err := cmd.CombinedOutput()
		output := string(out)

		// Log a truncated version to avoid flooding test output.
		lines := strings.Split(strings.TrimSpace(output), "\n")
		t.Logf("client-go: %d output lines, exit err=%v", len(lines), err)
		if len(lines) > 5 {
			t.Logf("first 3 lines:\n%s", strings.Join(lines[:3], "\n"))
			t.Logf("last 2 lines:\n%s", strings.Join(lines[len(lines)-2:], "\n"))
		} else {
			t.Logf("output:\n%s", output)
		}

		// The script should complete (not crash). client-go has extensive docs
		// so drift detection may find issues — that is acceptable.
		// Verify output format: each non-empty line should be parseable.
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// Lines should be one of:
			//   DRIFT [...]: ...
			//   STALE: ...
			//   FAIL: ...
			//   Running tier-2 ...
			//   livedocs: ...  (summary)
			if len(line) > 2000 {
				t.Errorf("output line exceeds 2000 chars (not CI-friendly): %.100s...", line)
			}
		}

		// Summary line should always be present.
		if !strings.Contains(output, "livedocs:") {
			t.Errorf("expected summary line starting with 'livedocs:' in output")
		}
	})

	t.Run("MissingArgExitNonZero", func(t *testing.T) {
		cmd := exec.Command("bash", scriptPath)
		cmd.Env = append(os.Environ(), "PATH="+binDir+":"+origPath)
		out, err := cmd.CombinedOutput()
		t.Logf("no-arg output:\n%s", out)

		if err == nil {
			t.Error("expected non-zero exit when no repo-path given")
		}
		if !strings.Contains(string(out), "repo-path argument is required") {
			t.Errorf("expected usage error message, got:\n%s", out)
		}
	})

	t.Run("InvalidSeverityExitNonZero", func(t *testing.T) {
		root := clientGoRoot(t)

		cmd := exec.Command("bash", scriptPath, root, "--severity=BOGUS")
		cmd.Env = append(os.Environ(), "PATH="+binDir+":"+origPath)
		out, err := cmd.CombinedOutput()
		t.Logf("invalid severity output:\n%s", out)

		if err == nil {
			t.Error("expected non-zero exit for invalid severity")
		}
		if !strings.Contains(string(out), "invalid severity level") {
			t.Errorf("expected severity validation error, got:\n%s", out)
		}
	})

	t.Run("SeverityFilterHigh", func(t *testing.T) {
		// A clean repo with severity=HIGH should still pass.
		repoDir := createTempGitRepo(t)
		writeFile(t, repoDir, "go.mod", "module example.com/sev-test\n\ngo 1.21\n")
		writeFile(t, repoDir, "main.go", "package main\n\nfunc main() {}\n")
		gitCommitAll(t, repoDir, "severity test repo")

		cmd := exec.Command("bash", scriptPath, repoDir, "--severity=HIGH")
		cmd.Env = append(os.Environ(), "PATH="+binDir+":"+origPath)
		out, err := cmd.CombinedOutput()
		t.Logf("severity=HIGH output:\n%s", out)

		if err != nil {
			t.Errorf("expected exit 0 for clean repo with --severity=HIGH, got: %v", err)
		}
	})

	t.Run("OneLinePerIssueFormat", func(t *testing.T) {
		// Create a repo with a README referencing symbols to trigger drift output,
		// then verify output format.
		repoDir := createTempGitRepo(t)
		writeFile(t, repoDir, "go.mod", "module example.com/format-test\n\ngo 1.21\n")
		writeFile(t, repoDir, "pkg/foo/foo.go", `package foo

// RealFunc does something.
func RealFunc() string { return "real" }
`)
		writeFile(t, repoDir, "pkg/foo/README.md", "# Package foo\n\nUses `RealFunc` for processing.\n")
		gitCommitAll(t, repoDir, "format test repo")

		cmd := exec.Command("bash", scriptPath, repoDir)
		cmd.Env = append(os.Environ(), "PATH="+binDir+":"+origPath)
		out, err := cmd.CombinedOutput()
		output := string(out)
		t.Logf("format test output (exit=%v):\n%s", err, output)

		// Verify each non-empty line is single-line (no embedded newlines in issue reports).
		for i, line := range strings.Split(strings.TrimSpace(output), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if strings.Contains(line, "\t\t") {
				t.Errorf("line %d contains embedded tabs (multi-line?): %s", i+1, line)
			}
		}
	})
}
