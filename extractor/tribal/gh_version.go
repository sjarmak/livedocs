package tribal

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrUnknownGhVersion is returned by CheckGhVersion when the detected gh CLI
// version is not in the advisory allowlist and the caller did not pass
// allowUnknown=true.
var ErrUnknownGhVersion = errors.New("unknown gh CLI version")

// knownGhVersions is an advisory allowlist of gh CLI versions that have been
// verified to work with the PR comment miner's `gh pr list --search ...` and
// `gh api repos/.../pulls/N/comments` call patterns.
//
// The list is intentionally short and advisory: if the user's gh binary is
// not listed, CheckGhVersion surfaces an ErrUnknownGhVersion and the
// `--accept-unknown-gh-version` CLI flag can override. Whenever the gh team
// changes pr-list search ranking or pulls/comments pagination semantics,
// either extend this list or bump to a newer pinned minimum.
var knownGhVersions = []string{
	"2.50.0",
	"2.52.0",
	"2.55.0",
	"2.58.0",
	"2.60.0",
	"2.62.0",
	"2.63.0",
	"2.65.0",
}

// ghVersionRegex matches the first line of `gh --version` output, e.g.
//
//	gh version 2.52.0 (2024-06-25)
//
// capturing the X.Y.Z triple.
var ghVersionRegex = regexp.MustCompile(`^gh version (\d+\.\d+\.\d+)`)

// CheckGhVersion runs `gh --version` via the provided runner and verifies the
// parsed version against the advisory allowlist.
//
// Returns the parsed version string on success. If the version is not in the
// allowlist and allowUnknown is false, returns ErrUnknownGhVersion so the
// caller can fail fast. If allowUnknown is true, the version is still
// returned (so callers can record it in source_files.pr_miner_version for
// audit).
func CheckGhVersion(ctx context.Context, runner CommandRunner, allowUnknown bool) (string, error) {
	if runner == nil {
		runner = defaultCommandRunner
	}
	out, err := runner(ctx, "gh", "--version")
	if err != nil {
		return "", fmt.Errorf("gh --version: %w", err)
	}

	version := parseGhVersion(string(out))
	if version == "" {
		if allowUnknown {
			return "unknown", nil
		}
		return "", fmt.Errorf("%w: could not parse `gh --version` output: %q", ErrUnknownGhVersion, strings.TrimSpace(string(out)))
	}

	for _, known := range knownGhVersions {
		if known == version {
			return version, nil
		}
	}

	if allowUnknown {
		return version, nil
	}
	return "", fmt.Errorf("%w: %s (allowlist: %v)", ErrUnknownGhVersion, version, knownGhVersions)
}

// parseGhVersion extracts the X.Y.Z version string from `gh --version`
// output. Returns "" if the first non-empty line does not match the
// expected format.
func parseGhVersion(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := ghVersionRegex.FindStringSubmatch(line)
		if len(m) == 2 {
			return m[1]
		}
		// Only inspect the first non-empty line.
		return ""
	}
	return ""
}
