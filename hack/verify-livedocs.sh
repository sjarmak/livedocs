#!/usr/bin/env bash
# verify-livedocs.sh — CI verification script for live documentation.
#
# Compatible with the Kubernetes hack/verify-*.sh pattern:
#   - Exit 0 on pass, non-zero on failure
#   - One-line-per-issue output, parseable by CI
#
# Usage:
#   hack/verify-livedocs.sh [repo-path]
#
# Arguments:
#   repo-path   Path to the repository to verify (default: current directory)
#
# Dependencies: jq

set -euo pipefail

REPO_PATH="${1:-.}"

# Resolve to absolute path.
REPO_PATH="$(cd "${REPO_PATH}" && pwd)"

# Require jq for JSON parsing.
if ! command -v jq &>/dev/null; then
    echo "FAIL: jq is required but not found on PATH" >&2
    exit 1
fi

# Locate the livedocs binary. Check common locations in order:
#   1. On PATH
#   2. In the repo root (development build)
#   3. In ./dist/ (goreleaser output)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

if command -v livedocs &>/dev/null; then
    LIVEDOCS="livedocs"
elif [[ -x "${PROJECT_ROOT}/livedocs" ]]; then
    LIVEDOCS="${PROJECT_ROOT}/livedocs"
elif [[ -x "${PROJECT_ROOT}/dist/livedocs" ]]; then
    LIVEDOCS="${PROJECT_ROOT}/dist/livedocs"
else
    echo "FAIL: livedocs binary not found on PATH or in ${PROJECT_ROOT}" >&2
    exit 1
fi

EXIT_CODE=0
ISSUE_COUNT=0

# --- Run livedocs check (documentation drift) ---
# Use JSON output and parse each finding into one line per issue.
CHECK_OUTPUT=$("${LIVEDOCS}" check --format=json "${REPO_PATH}" 2>/dev/null) || true

if echo "${CHECK_OUTPUT}" | jq -e '.has_drift == true' &>/dev/null; then
    # Iterate over each report's findings and emit one line per issue.
    # drift.Finding fields: Kind, Symbol, SourceFile, Detail (no JSON tags, so capitalized).
    while IFS= read -r line; do
        if [[ -n "${line}" ]]; then
            echo "${line}"
            ((ISSUE_COUNT++)) || true
        fi
    done < <(echo "${CHECK_OUTPUT}" | jq -r '
        .reports // [] | .[] |
        .ReadmePath as $file |
        .Findings // [] | .[] |
        select(.Kind != null) |
        "DRIFT: \($file): \(.Kind): \(.Symbol)"
    ')
    EXIT_CODE=1
fi

# --- Run livedocs verify (AI context file accuracy) ---
# Use JSON output and parse each stale ref into one line per issue.
VERIFY_OUTPUT=$("${LIVEDOCS}" verify --format=json "${REPO_PATH}" 2>/dev/null) || true

if echo "${VERIFY_OUTPUT}" | jq -e '.verdict == "fail"' &>/dev/null; then
    while IFS= read -r line; do
        if [[ -n "${line}" ]]; then
            echo "${line}"
            ((ISSUE_COUNT++)) || true
        fi
    done < <(echo "${VERIFY_OUTPUT}" | jq -r '
        .files // [] | .[] |
        .path as $file |
        .stale_refs // [] | .[] |
        "STALE: \($file): line \(.line): \(.kind): \(.value)"
    ')
    EXIT_CODE=1
fi

if [[ "${EXIT_CODE}" -eq 0 ]]; then
    echo "livedocs: all checks passed for ${REPO_PATH}"
fi

exit "${EXIT_CODE}"
