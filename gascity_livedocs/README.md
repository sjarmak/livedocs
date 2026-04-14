# Gas City LiveDocs

Automated documentation freshness monitoring for the Gas City ecosystem. Uses GitHub CLI to detect when `main` is updated on monitored repos, then identifies which docs may need updating via the doc-map. Optionally runs deep drift analysis via livedocs + Sourcegraph MCP.

## Monitored Repositories

| Repository            | Owner                                  |
| --------------------- | -------------------------------------- |
| `gastownhall/gastown` | Original multi-agent workspace manager |
| `gastownhall/gascity` | Orchestration-builder SDK              |
| `gastownhall/beads`   | Distributed graph-based issue tracker  |
| `dolthub/dolt`        | Git-for-data storage engine            |

## Architecture

```
cron (every 15m)
  │
  ▼
poll-repos.sh ─── gh api ──► GitHub API
  │                           (commits/main)
  │ compares SHA with .state/
  │
  ├─ no changes → exit
  │
  ├─ changes found:
  │   ├─ gh api compare → changed files list
  │   ├─ doc-map.yaml   → affected documents
  │   └─ report
  │
  └─ --deep mode:
      │
      ▼
  check-docs.sh ── livedocs ──► Sourcegraph MCP
      │                          (read remote files)
      ▼
  drift report (stale refs, undocumented exports)
```

**Two-layer approach:**

1. **Trigger** (`poll-repos.sh`): Lightweight `gh api` calls detect new commits. Fast, no special tokens needed beyond GitHub auth.
2. **Analysis** (`check-docs.sh`): Deep extraction via Sourcegraph MCP. Runs tree-sitter symbol extraction and compares against docs. Only triggered when changes are detected.

## Quick Start

```bash
# 1. Ensure gh is authenticated
gh auth status

# 2. Initialize state (records current SHAs without diffing)
./poll-repos.sh

# 3. Later, check for new commits
./poll-repos.sh

# 4. Check with deep drift analysis (needs livedocs + SRC_ACCESS_TOKEN)
./poll-repos.sh --deep
```

## Cron Setup

```bash
# Poll every 15 minutes, log output
*/15 * * * * cd /home/ds/projects/live_docs/gascity_livedocs && ./poll-repos.sh >> logs/poll.log 2>&1

# Deep check every 4 hours
0 */4 * * * cd /home/ds/projects/live_docs/gascity_livedocs && ./poll-repos.sh --deep >> logs/deep.log 2>&1
```

## Commands

### poll-repos.sh (trigger)

| Command                    | What it does                                   |
| -------------------------- | ---------------------------------------------- |
| `./poll-repos.sh`          | Check for new commits, report affected docs    |
| `./poll-repos.sh --deep`   | Same + run livedocs extraction and drift check |
| `./poll-repos.sh --status` | Show last-seen commit SHAs                     |
| `./poll-repos.sh --reset`  | Clear state (next run initializes fresh)       |

**Requires:** `gh` (GitHub CLI), authenticated

### check-docs.sh (deep analysis)

| Command                        | What it does                         |
| ------------------------------ | ------------------------------------ |
| `./check-docs.sh`              | Incremental extraction + drift check |
| `./check-docs.sh --full`       | Full re-extraction from scratch      |
| `./check-docs.sh --drift-only` | Check existing claims DBs only       |
| `./check-docs.sh --map`        | Show repo-to-document mapping        |

**Requires:** `livedocs` on PATH, `SRC_ACCESS_TOKEN` env var

## Document Map

Each doc is linked to repos via `doc-map.yaml`. When code changes, only mapped docs are flagged:

| Document                         | Repos                |
| -------------------------------- | -------------------- |
| 01-gastown-vs-gascity            | gastown, gascity     |
| 02-beads-data-model              | beads                |
| 03-why-dolt                      | beads, dolt          |
| 04-bead-storage-and-syncing      | beads, dolt          |
| 05-gascity-primitives            | gascity              |
| 06-port-coordination             | gascity              |
| 07-supervisor-and-reconciliation | gascity              |
| 08-customizable-vs-fixed         | gastown, gascity     |
| 09-troubleshooting-tmux          | gastown, gascity     |
| 10-troubleshooting-dolt          | gascity, beads, dolt |
| 11-troubleshooting-sync          | beads, dolt          |
| 12-troubleshooting-supervisor    | gascity              |
| 13-troubleshooting-operational   | gastown, gascity     |

## Files

| File            | Purpose                                            |
| --------------- | -------------------------------------------------- |
| `poll-repos.sh` | Cron-friendly trigger via GitHub CLI               |
| `check-docs.sh` | Deep drift analysis via livedocs + Sourcegraph MCP |
| `doc-map.yaml`  | Cross-repo source pattern to doc file mapping      |
| `docs/`         | The 13 Gas City documentation files                |
| `.state/`       | Last-seen commit SHAs per repo (gitignored)        |
| `data/`         | Generated claims DBs from livedocs (gitignored)    |
| `logs/`         | Cron output logs (gitignored)                      |

## Customizing

**Add a document:** Edit `doc-map.yaml`, add entries under the relevant repo's `mappings:` section.

**Change polling frequency:** Edit the cron interval. The `poll-repos.sh` script is stateless beyond the `.state/` directory.

**Add a repo:** Add an entry to the `REPOS` array in both scripts and add a repo block in `doc-map.yaml`.
