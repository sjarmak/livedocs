#!/usr/bin/env bash
# verify-livedocs.sh — CI verification script for live documentation.
#
# Compatible with the Kubernetes hack/verify-*.sh pattern:
#   - Exit 0 on pass, non-zero on failure
#   - One-line-per-issue output, parseable by CI
#
# Usage:
#   hack/verify-livedocs.sh <repo-path> [flags]
#
# Arguments:
#   repo-path   Path to the repository to verify (required)
#
# Flags:
#   --tier2            Also run tier-2 semantic extraction before verification
#   --severity=LEVEL   Minimum severity to report: LOW, MEDIUM, HIGH (default: LOW)
#
# Dependencies: jq

set -euo pipefail

# --- Parse arguments ---
REPO_PATH=""
TIER2=false
MIN_SEVERITY="LOW"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --tier2)
            TIER2=true
            shift
            ;;
        --severity=*)
            MIN_SEVERITY="${1#*=}"
            shift
            ;;
        --severity)
            MIN_SEVERITY="$2"
            shift 2
            ;;
        -*)
            echo "FAIL: unknown flag: $1" >&2
            exit 2
            ;;
        *)
            if [[ -z "${REPO_PATH}" ]]; then
                REPO_PATH="$1"
            else
                echo "FAIL: unexpected argument: $1" >&2
                exit 2
            fi
            shift
            ;;
    esac
done

if [[ -z "${REPO_PATH}" ]]; then
    echo "FAIL: repo-path argument is required" >&2
    echo "Usage: hack/verify-livedocs.sh <repo-path> [--tier2] [--severity=LEVEL]" >&2
    exit 2
fi

# Validate severity level.
case "${MIN_SEVERITY}" in
    LOW|MEDIUM|HIGH) ;;
    *)
        echo "FAIL: invalid severity level: ${MIN_SEVERITY} (must be LOW, MEDIUM, or HIGH)" >&2
        exit 2
        ;;
esac

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

# --- Severity filter helper ---
# Returns 0 (true) if the given severity meets or exceeds the minimum threshold.
severity_rank() {
    case "$1" in
        HIGH)   echo 3 ;;
        MEDIUM) echo 2 ;;
        LOW)    echo 1 ;;
        *)      echo 0 ;;
    esac
}

meets_severity() {
    local issue_sev="$1"
    local min_rank
    local issue_rank
    min_rank=$(severity_rank "${MIN_SEVERITY}")
    issue_rank=$(severity_rank "${issue_sev}")
    [[ "${issue_rank}" -ge "${min_rank}" ]]
}

EXIT_CODE=0
ISSUE_COUNT=0
TOTAL_CHECKS=0

# --- Optional: Run tier-2 extraction before verification ---
if [[ "${TIER2}" == "true" ]]; then
    echo "Running tier-2 semantic extraction on ${REPO_PATH}..."
    if ! "${LIVEDOCS}" extract --tier2 "${REPO_PATH}" 2>&1; then
        echo "FAIL: tier-2 extraction failed" >&2
        exit 1
    fi
fi

# --- Run livedocs check (documentation drift) ---
# Use JSON output and parse each finding into one line per issue.
CHECK_OUTPUT=$("${LIVEDOCS}" check --format=json "${REPO_PATH}" 2>/dev/null) || true
((TOTAL_CHECKS++)) || true

if echo "${CHECK_OUTPUT}" | jq -e '.has_drift == true' &>/dev/null; then
    # Iterate over each report's findings and emit one line per issue.
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
        "DRIFT [HIGH]: \($file): \(.Kind): \(.Symbol)"
    ')
    EXIT_CODE=1
fi

# --- Run livedocs verify-claims --check-existing (claims-based verification) ---
# Only run if a claims DB exists for the repo.
REPO_NAME="$(basename "${REPO_PATH}")"
CLAIMS_DB=""
if [[ -f "${REPO_NAME}.claims.db" ]]; then
    CLAIMS_DB="${REPO_NAME}.claims.db"
elif [[ -f "${REPO_PATH}/${REPO_NAME}.claims.db" ]]; then
    CLAIMS_DB="${REPO_PATH}/${REPO_NAME}.claims.db"
fi

if [[ -n "${CLAIMS_DB}" ]]; then
    ((TOTAL_CHECKS++)) || true
    VERIFY_OUTPUT=$("${LIVEDOCS}" verify-claims --check-existing --db "${CLAIMS_DB}" "${REPO_PATH}" 2>&1) || true

    # Parse one-line-per-issue output from verify-claims and apply severity filter.
    while IFS= read -r line; do
        if [[ -z "${line}" ]]; then
            continue
        fi
        # Extract severity from lines like "DRIFT [HIGH]: ..."
        if [[ "${line}" =~ ^DRIFT\ \[([A-Z]+)\]: ]]; then
            sev="${BASH_REMATCH[1]}"
            if meets_severity "${sev}"; then
                echo "${line}"
                ((ISSUE_COUNT++)) || true
                EXIT_CODE=1
            fi
        elif [[ "${line}" =~ ^(STALE|FAIL): ]]; then
            echo "${line}"
            ((ISSUE_COUNT++)) || true
            EXIT_CODE=1
        fi
    done <<< "${VERIFY_OUTPUT}"
fi

# --- Summary ---
echo ""
if [[ "${EXIT_CODE}" -eq 0 ]]; then
    echo "livedocs: all checks passed for ${REPO_PATH} (${TOTAL_CHECKS} check(s), 0 issues)"
else
    echo "livedocs: ${ISSUE_COUNT} issue(s) found for ${REPO_PATH} (severity >= ${MIN_SEVERITY})"
fi

exit "${EXIT_CODE}"
