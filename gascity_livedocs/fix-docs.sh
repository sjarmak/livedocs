#!/usr/bin/env bash
#
# fix-docs.sh — Detect stale documentation, rewrite it, commit and push.
#
# Runs the cross-repo semantic check, then for each stale section uses
# claude to rewrite it based on current code. Commits changes to a
# docs/auto-update branch and pushes.
#
# Usage:
#   ./fix-docs.sh              Detect, fix, commit, push
#   ./fix-docs.sh --dry-run    Detect and show fixes without committing
#
# Requires:
#   - livedocs on PATH
#   - claude CLI on PATH (uses OAuth)
#   - SOURCEGRAPH_ACCESS_TOKEN in ~/.env
#   - gh CLI (authenticated)

set -euo pipefail

# shellcheck disable=SC1090
[[ -f ~/.env ]] && source ~/.env
export SRC_ACCESS_TOKEN="${SRC_ACCESS_TOKEN:-${SOURCEGRAPH_ACCESS_TOKEN:-}}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOCS_DIR="${SCRIPT_DIR}/docs"
DOC_MAP="${SCRIPT_DIR}/doc-map.yaml"
LIVEDOCS="${LIVEDOCS_BIN:-livedocs}"
BRANCH="docs/auto-update-$(date +%Y-%m-%d)"
DRY_RUN=0

ts()  { date '+%Y-%m-%d %H:%M:%S'; }
log() { echo "[$(ts)] $*"; }
die() { echo "ERROR: $*" >&2; exit 1; }

# ── Prerequisites ────────────────────────────────────────────

check_prereqs() {
  command -v "$LIVEDOCS" >/dev/null 2>&1 || die "livedocs not found"
  command -v claude >/dev/null 2>&1 || die "claude CLI not found"
  command -v gh >/dev/null 2>&1 || die "gh CLI not found"
  command -v jq >/dev/null 2>&1 || die "jq not found (needed for JSON parsing)"
  [[ -n "${SRC_ACCESS_TOKEN:-}" ]] || die "SOURCEGRAPH_ACCESS_TOKEN not found in ~/.env"
}

# ── Phase 1: Detect stale sections ──────────────────────────

detect_drift() {
  log "Phase 1: Running cross-repo semantic check..."

  local json_output
  json_output=$("$LIVEDOCS" check \
    --cross-repo \
    --doc-map "$DOC_MAP" \
    --docs-dir "$DOCS_DIR" \
    --format json 2>/dev/null) || true

  if [[ -z "$json_output" ]]; then
    log "No output from semantic check"
    return 1
  fi

  # Count total stale sections.
  local total_stale
  total_stale=$(echo "$json_output" | jq '[.[].stale_sections] | add // 0')

  if [[ "$total_stale" -eq 0 ]]; then
    log "All documentation is current. Nothing to fix."
    return 1
  fi

  log "Found ${total_stale} stale section(s)"
  echo "$json_output"
}

# ── Phase 2: Fix stale sections ─────────────────────────────

fix_stale_sections() {
  local json_output="$1"
  local docs_fixed=0

  # Iterate over each doc that has stale sections.
  local doc_count
  doc_count=$(echo "$json_output" | jq 'length')

  for (( i=0; i<doc_count; i++ )); do
    local doc_path stale_count
    doc_path=$(echo "$json_output" | jq -r ".[$i].doc_path")
    stale_count=$(echo "$json_output" | jq ".[$i].stale_sections")

    [[ "$stale_count" -eq 0 ]] && continue

    # Resolve the actual file path.
    local file_path="${DOCS_DIR}/$(basename "$doc_path")"
    [[ -f "$file_path" ]] || continue

    log "Fixing ${doc_path} (${stale_count} stale section(s))..."

    local finding_count
    finding_count=$(echo "$json_output" | jq ".[$i].findings | length")

    for (( j=0; j<finding_count; j++ )); do
      local section_heading stale_claims_json
      section_heading=$(echo "$json_output" | jq -r ".[$i].findings[$j].Symbol")
      stale_claims_json=$(echo "$json_output" | jq -c ".[$i].findings[$j].stale_claims // []")

      [[ "$stale_claims_json" == "[]" || "$stale_claims_json" == "null" ]] && continue

      log "  Section: ${section_heading}"

      # Extract the current section text from the doc.
      local section_text
      section_text=$(extract_section "$file_path" "$section_heading")

      if [[ -z "$section_text" ]]; then
        log "    SKIP: Could not extract section"
        continue
      fi

      # Format the stale claims for the prompt.
      local claims_text
      claims_text=$(echo "$stale_claims_json" | jq -r '.[] | "- [\(.severity)] \(.claim) → Evidence: \(.evidence)"')

      # Call claude to rewrite the section.
      local rewritten
      rewritten=$(rewrite_section "$section_heading" "$section_text" "$claims_text" "$doc_path")

      if [[ -z "$rewritten" ]]; then
        log "    SKIP: Claude returned empty rewrite"
        continue
      fi

      if [[ "$DRY_RUN" -eq 1 ]]; then
        log "    DRY RUN — would rewrite section '${section_heading}'"
        echo "--- CURRENT ---"
        echo "$section_text" | head -10
        echo "--- REWRITTEN ---"
        echo "$rewritten" | head -10
        echo "---"
        continue
      fi

      # Replace the section in the file.
      replace_section "$file_path" "$section_heading" "$rewritten"
      log "    Updated"
      docs_fixed=1
    done
  done

  return $(( 1 - docs_fixed ))
}

# ── Section extraction and replacement ───────────────────────

# Extract a ## section (heading + body) from a markdown file.
extract_section() {
  local file="$1" heading="$2"
  awk -v heading="## ${heading}" '
    $0 == heading { found=1; print; next }
    found && /^## / { exit }
    found { print }
  ' "$file"
}

# Replace a ## section in a markdown file with new content.
replace_section() {
  local file="$1" heading="$2" new_content="$3"
  local tmp="${file}.tmp"

  awk -v heading="## ${heading}" -v replacement="$new_content" '
    $0 == heading { skip=1; printf "%s\n", replacement; next }
    skip && /^## / { skip=0 }
    !skip { print }
  ' "$file" > "$tmp"

  mv "$tmp" "$file"
}

# Call claude to rewrite a section based on stale claims and code evidence.
rewrite_section() {
  local heading="$1" current_text="$2" claims="$3" doc_path="$4"

  local prompt
  prompt="$(cat <<EOF
You are updating technical documentation. A section has been flagged as containing stale claims based on analysis of the current codebase.

DOCUMENT: ${doc_path}
SECTION: ${heading}

CURRENT SECTION TEXT:
${current_text}

STALE CLAIMS IDENTIFIED:
${claims}

INSTRUCTIONS:
- Rewrite ONLY this section to correct the stale claims
- Preserve the section heading (## ${heading})
- Keep the same style, tone, and level of detail as the original
- Keep all information that is still accurate
- Only change what the evidence shows is wrong
- Do NOT add disclaimers, notes about the update, or meta-commentary
- Output ONLY the rewritten section text, starting with the ## heading
EOF
)"

  claude -p \
    --model haiku \
    --output-format text \
    --system-prompt "You are a precise technical documentation editor. Output only the corrected markdown section, nothing else." \
    "$prompt" 2>/dev/null
}

# ── Phase 3: Commit and push ────────────────────────────────

commit_and_push() {
  log "Phase 3: Committing and pushing..."

  # Work in the live_docs repo root (parent of gascity_livedocs/).
  cd "${SCRIPT_DIR}/.."

  # Check if there are actual changes.
  if ! git diff --quiet -- gascity_livedocs/docs/; then
    # Create or switch to the update branch.
    local current_branch
    current_branch=$(git branch --show-current)

    if git show-ref --verify --quiet "refs/heads/${BRANCH}"; then
      git checkout "$BRANCH"
    else
      git checkout -b "$BRANCH"
    fi

    # Stage and commit.
    git add gascity_livedocs/docs/
    git commit -m "docs: auto-update stale gascity documentation

Cross-repo semantic check identified stale claims in documentation
and rewrote affected sections based on current code.

Generated by gascity_livedocs fix-docs.sh"

    # Push.
    git push -u origin "$BRANCH"

    log "Pushed to branch: ${BRANCH}"

    # Create PR if one doesn't exist for this branch.
    local existing_pr
    existing_pr=$(gh pr list --head "$BRANCH" --json number --jq '.[0].number' 2>/dev/null) || true

    if [[ -z "$existing_pr" ]]; then
      gh pr create \
        --title "docs: auto-update stale documentation" \
        --body "$(cat <<EOF
## Auto-generated documentation update

Cross-repo semantic drift check found stale claims and rewrote the affected sections.

Review the changes to verify accuracy before merging.

---
*Generated by gascity_livedocs*
EOF
)" \
        --head "$BRANCH" 2>/dev/null && \
        log "Created pull request" || \
        log "WARN: failed to create PR (push succeeded)"
    else
      log "PR #${existing_pr} already exists for ${BRANCH}"
    fi

    # Switch back to original branch.
    git checkout "$current_branch"
  else
    log "No changes to commit"
  fi
}

# ── Main ─────────────────────────────────────────────────────

main() {
  if [[ "${1:-}" == "--dry-run" ]]; then
    DRY_RUN=1
  fi

  check_prereqs

  # Phase 1: Detect.
  local drift_json
  drift_json=$(detect_drift) || exit 0

  # Phase 2: Fix.
  if ! fix_stale_sections "$drift_json"; then
    log "No sections were rewritten"
    exit 0
  fi

  # Phase 3: Commit and push (skip in dry-run).
  if [[ "$DRY_RUN" -eq 1 ]]; then
    log "DRY RUN complete. No changes committed."
  else
    commit_and_push
  fi
}

main "$@"
