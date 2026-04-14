#!/usr/bin/env bash
#
# check-docs.sh — Run deep drift analysis on Gas City documentation.
#
# Uses livedocs + Sourcegraph MCP to extract claims from repos and compare
# against the documentation. Typically called after poll-repos.sh detects
# changes, or manually for a full audit.
#
# Usage:
#   ./check-docs.sh                 Incremental extraction + structural drift check
#   ./check-docs.sh --full          Full re-extraction (ignores cache)
#   ./check-docs.sh --drift-only    Skip extraction, check existing DBs
#   ./check-docs.sh --semantic      Cross-repo semantic check (validates content accuracy)
#   ./check-docs.sh --semantic-json Same as --semantic but JSON output
#   ./check-docs.sh --map           Show repo-to-document mapping
#
# Requires:
#   - livedocs binary on PATH (or LIVEDOCS_BIN set)
#   - SOURCEGRAPH_ACCESS_TOKEN in ~/.env (for Sourcegraph MCP code search)
#   - claude CLI on PATH (for --semantic; uses existing OAuth, no extra API key)

set -euo pipefail

# Source ~/.env for tokens (SOURCEGRAPH_ACCESS_TOKEN, etc.)
# shellcheck disable=SC1090
[[ -f ~/.env ]] && source ~/.env

# Bridge env var name: livedocs expects SRC_ACCESS_TOKEN.
export SRC_ACCESS_TOKEN="${SRC_ACCESS_TOKEN:-${SOURCEGRAPH_ACCESS_TOKEN:-}}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DATA_DIR="${SCRIPT_DIR}/data"
DOCS_DIR="${SCRIPT_DIR}/docs"
DOC_MAP="${SCRIPT_DIR}/doc-map.yaml"
LIVEDOCS="${LIVEDOCS_BIN:-livedocs}"

# Fully qualified repo names (Sourcegraph format).
REPOS=(
  "github.com/gastownhall/gastown"
  "github.com/gastownhall/gascity"
  "github.com/gastownhall/beads"
  "github.com/dolthub/dolt"
)

# ── Helpers ──────────────────────────────────────────────────

die() { echo "ERROR: $*" >&2; exit 1; }

check_prereqs() {
  command -v "$LIVEDOCS" >/dev/null 2>&1 || die "livedocs not found. Set LIVEDOCS_BIN or add to PATH."
  if [[ -z "${SRC_ACCESS_TOKEN:-}" ]]; then
    die "SOURCEGRAPH_ACCESS_TOKEN not found. Add it to ~/.env"
  fi
}

repo_short() { basename "$1"; }

# ── Extraction ───────────────────────────────────────────────

extract_all() {
  local mode="${1:-incremental}"
  echo "=== Extracting claims (mode: ${mode}) ==="
  mkdir -p "$DATA_DIR"

  for repo in "${REPOS[@]}"; do
    local short db_path
    short=$(repo_short "$repo")
    db_path="${DATA_DIR}/${short}.claims.db"

    echo ""
    echo "--- ${repo} ---"

    if [[ "$mode" == "full" ]] && [[ -f "$db_path" ]]; then
      echo "  Removing existing DB..."
      rm -f "$db_path"
    fi

    "$LIVEDOCS" extract \
      --source sourcegraph \
      --repo "$repo" \
      -o "$db_path" \
      --concurrency 10 \
      2>&1 | sed 's/^/  /'

    echo "  Done: ${db_path}"
  done

  echo ""
  echo "=== Extraction complete ==="
}

# ── Drift check ─────────────────────────────────────────────

check_drift() {
  echo ""
  echo "=== Checking documentation drift ==="

  local has_drift=0

  for repo in "${REPOS[@]}"; do
    local short db_path
    short=$(repo_short "$repo")
    db_path="${DATA_DIR}/${short}.claims.db"

    if [[ ! -f "$db_path" ]]; then
      echo "  SKIP: No claims DB for ${short} (run extraction first)"
      continue
    fi

    echo ""
    echo "--- ${short} ---"

    local drift_output
    drift_output=$("$LIVEDOCS" check \
      --db "$db_path" \
      --docs-dir "$DOCS_DIR" \
      --format json 2>/dev/null) || true

    if [[ -n "$drift_output" ]]; then
      local stale undoc
      stale=$(echo "$drift_output" | grep -o '"total_stale":[0-9]*' | head -1 | cut -d: -f2)
      undoc=$(echo "$drift_output" | grep -o '"total_undocumented":[0-9]*' | head -1 | cut -d: -f2)

      if [[ "${stale:-0}" -gt 0 ]] || [[ "${undoc:-0}" -gt 0 ]]; then
        has_drift=1
        echo "  DRIFT: stale=${stale:-0} undocumented=${undoc:-0}"
      else
        echo "  OK"
      fi
    fi
  done

  echo ""
  if [[ "$has_drift" -eq 1 ]]; then
    echo "=== RESULT: Drift detected — review flagged docs ==="
    return 1
  else
    echo "=== RESULT: Documentation is current ==="
    return 0
  fi
}

# ── Cross-repo semantic check ────────────────────────────────

check_semantic() {
  local fmt="${1:-text}"
  echo ""
  echo "=== Cross-repo semantic drift check ==="
  echo "  Validating doc content against live code in mapped repos..."
  echo ""

  "$LIVEDOCS" check \
    --cross-repo \
    --doc-map "$DOC_MAP" \
    --docs-dir "$DOCS_DIR" \
    --format "$fmt" \
    2>&1

  local exit_code=$?
  if [[ "$exit_code" -ne 0 ]]; then
    echo ""
    echo "=== RESULT: Semantic drift detected — review flagged sections ==="
    return 1
  else
    echo ""
    echo "=== RESULT: Documentation content appears current ==="
    return 0
  fi
}

# ── Doc map report ───────────────────────────────────────────

report_map() {
  echo "=== Document → Repository Map ==="
  echo ""

  local -A doc_repos
  for repo in "${REPOS[@]}"; do
    local short
    short=$(repo_short "$repo")
    local docs
    docs=$(grep -A200 "short: ${short}" "$DOC_MAP" \
      | grep -oP '(?<=- )docs/.*\.md' \
      | sort -u 2>/dev/null) || continue
    while IFS= read -r doc; do
      [[ -z "$doc" ]] && continue
      doc_repos["$doc"]="${doc_repos[$doc]:-} ${short}"
    done <<< "$docs"
  done

  for doc in $(echo "${!doc_repos[@]}" | tr ' ' '\n' | sort); do
    echo "  ${doc}"
    echo "    repos:${doc_repos[$doc]}"
    echo ""
  done
}

# ── Main ─────────────────────────────────────────────────────

main() {
  case "${1:-}" in
    --full)
      check_prereqs
      extract_all full
      check_drift
      ;;
    --drift-only)
      check_drift
      ;;
    --semantic)
      check_prereqs
      check_semantic text
      ;;
    --semantic-json)
      check_prereqs
      check_semantic json
      ;;
    --map)
      report_map
      ;;
    --help|-h)
      head -17 "${BASH_SOURCE[0]}" | tail -15
      ;;
    *)
      check_prereqs
      extract_all incremental
      check_drift
      ;;
  esac
}

main "$@"
