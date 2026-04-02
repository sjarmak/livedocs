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

set -euo pipefail

REPO_PATH="${1:-.}"

# Resolve to absolute path.
REPO_PATH="$(cd "${REPO_PATH}" && pwd)"

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

# --- Run livedocs check (documentation drift) ---
if ! CHECK_OUTPUT=$("${LIVEDOCS}" check --format=text "${REPO_PATH}" 2>&1); then
    # Extract one-line-per-issue from text output.
    # Lines that describe individual stale references start with whitespace or specific markers.
    while IFS= read -r line; do
        # Skip blank lines and header lines; emit substantive issue lines.
        [[ -z "${line}" ]] && continue
        echo "check: ${line}"
    done <<< "${CHECK_OUTPUT}"
    EXIT_CODE=1
fi

# --- Run livedocs verify (AI context file accuracy) ---
if ! VERIFY_OUTPUT=$("${LIVEDOCS}" verify --format=human "${REPO_PATH}" 2>&1); then
    while IFS= read -r line; do
        [[ -z "${line}" ]] && continue
        # Skip decorative header lines.
        [[ "${line}" == "="* ]] && continue
        echo "verify: ${line}"
    done <<< "${VERIFY_OUTPUT}"
    EXIT_CODE=1
fi

if [[ "${EXIT_CODE}" -eq 0 ]]; then
    echo "livedocs: all checks passed for ${REPO_PATH}"
fi

exit "${EXIT_CODE}"
