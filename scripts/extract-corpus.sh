#!/usr/bin/env bash
# extract-corpus.sh — Batch extraction of claims from all kubernetes repos.
#
# Usage:
#   scripts/extract-corpus.sh [--limit N] [--tier2]
#
# Must be run from the project root (/home/ds/live_docs/).
# Produces:
#   data/claims/<repo>.claims.db   — per-repo claims databases
#   data/corpus-summary.csv        — aggregate statistics

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
KUBERNETES_DIR="${KUBERNETES_DIR:-$HOME/kubernetes}"
PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LIVEDOCS="${PROJECT_ROOT}/livedocs"
CLAIMS_DIR="${PROJECT_ROOT}/data/claims"
SUMMARY_CSV="${PROJECT_ROOT}/data/corpus-summary.csv"
LIMIT=0          # 0 = no limit
TIER2_FLAG=""

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --limit)
            LIMIT="$2"
            shift 2
            ;;
        --tier2)
            TIER2_FLAG="--tier2"
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [--limit N] [--tier2]"
            echo ""
            echo "  --limit N   Process only the first N repos (for testing)"
            echo "  --tier2     Enable Tier 2 semantic extraction (requires ANTHROPIC_API_KEY)"
            exit 0
            ;;
        *)
            echo "Unknown argument: $1" >&2
            exit 1
            ;;
    esac
done

# ---------------------------------------------------------------------------
# Preflight checks
# ---------------------------------------------------------------------------
if [[ ! -x "${LIVEDOCS}" ]]; then
    echo "ERROR: livedocs binary not found at ${LIVEDOCS}" >&2
    echo "Build it first: go build -o livedocs ./cmd/livedocs" >&2
    exit 1
fi

if [[ ! -d "${KUBERNETES_DIR}" ]]; then
    echo "ERROR: Kubernetes directory not found at ${KUBERNETES_DIR}" >&2
    exit 1
fi

mkdir -p "${CLAIMS_DIR}"

# ---------------------------------------------------------------------------
# CSV header
# ---------------------------------------------------------------------------
echo "repo,symbols,structural_claims,semantic_claims,duration_ms,errors" > "${SUMMARY_CSV}"

# ---------------------------------------------------------------------------
# Collect repo directories (sorted for deterministic order)
# ---------------------------------------------------------------------------
mapfile -t REPOS < <(find "${KUBERNETES_DIR}" -mindepth 1 -maxdepth 1 -type d | sort)

TOTAL=${#REPOS[@]}
if [[ "${LIMIT}" -gt 0 && "${LIMIT}" -lt "${TOTAL}" ]]; then
    TOTAL="${LIMIT}"
fi

echo "Extracting claims from ${TOTAL} repositories..." >&2

# ---------------------------------------------------------------------------
# Main loop
# ---------------------------------------------------------------------------
COUNT=0
for REPO_PATH in "${REPOS[@]}"; do
    REPO_NAME="$(basename "${REPO_PATH}")"

    COUNT=$((COUNT + 1))
    if [[ "${LIMIT}" -gt 0 && "${COUNT}" -gt "${LIMIT}" ]]; then
        break
    fi

    # Check the repo directory actually exists and is non-empty.
    if [[ ! -d "${REPO_PATH}" ]]; then
        echo "[${COUNT}/${TOTAL}] WARN: ${REPO_NAME} — directory not found, skipping" >&2
        echo "${REPO_NAME},0,0,0,0,not_found" >> "${SUMMARY_CSV}"
        continue
    fi

    DB_PATH="${CLAIMS_DIR}/${REPO_NAME}.claims.db"
    echo "[${COUNT}/${TOTAL}] Extracting ${REPO_NAME}..." >&2

    # Capture start time in milliseconds.
    START_NS=$(date +%s%N)

    ERRORS=""
    if ! "${LIVEDOCS}" extract \
            --repo "${REPO_NAME}" \
            -o "${DB_PATH}" \
            ${TIER2_FLAG} \
            "${REPO_PATH}" \
            > /dev/null 2>&1; then
        ERRORS="extraction_failed"
        echo "[${COUNT}/${TOTAL}] WARN: ${REPO_NAME} — extraction failed" >&2
    fi

    END_NS=$(date +%s%N)
    DURATION_MS=$(( (END_NS - START_NS) / 1000000 ))

    # Query the DB for counts (default to 0 if DB is missing or query fails).
    SYMBOLS=0
    STRUCTURAL=0
    SEMANTIC=0

    if [[ -f "${DB_PATH}" ]]; then
        read -r SYMBOLS STRUCTURAL SEMANTIC < <(
            python3 -c "
import sqlite3, sys
try:
    conn = sqlite3.connect(sys.argv[1])
    c = conn.cursor()
    syms = c.execute('SELECT COUNT(*) FROM symbols').fetchone()[0]
    struct = c.execute(\"SELECT COUNT(*) FROM claims WHERE claim_tier='structural'\").fetchone()[0]
    sem = c.execute(\"SELECT COUNT(*) FROM claims WHERE claim_tier='semantic'\").fetchone()[0]
    conn.close()
    print(syms, struct, sem)
except Exception:
    print('0 0 0')
" "${DB_PATH}" 2>/dev/null || echo "0 0 0"
        )
    fi

    echo "${REPO_NAME},${SYMBOLS},${STRUCTURAL},${SEMANTIC},${DURATION_MS},${ERRORS}" >> "${SUMMARY_CSV}"
    echo "[${COUNT}/${TOTAL}] ${REPO_NAME}: ${SYMBOLS} symbols, ${STRUCTURAL} structural, ${SEMANTIC} semantic (${DURATION_MS}ms)" >&2
done

echo "" >&2
echo "Done. Summary written to ${SUMMARY_CSV}" >&2
echo "Claims databases in ${CLAIMS_DIR}/" >&2
