# Gas City & Beads — Architectural Primer

This documentation provides an in-depth primer on the Gas City multi-agent orchestration SDK, the Beads work-tracking system it depends on, and their relationship to the original Gas Town project.

## Contents

| Document                                                           | Description                                                                  |
| ------------------------------------------------------------------ | ---------------------------------------------------------------------------- |
| [Gas Town vs Gas City](01-gastown-vs-gascity.md)                   | History, relationship, and conceptual differences between the two projects   |
| [Beads Data Model](02-beads-data-model.md)                         | What a bead is, its fields, schema, and the `bd` CLI                         |
| [Why Dolt](03-why-dolt.md)                                         | Why beads uses Dolt instead of SQLite or flat files                          |
| [Bead Storage & Syncing](04-bead-storage-and-syncing.md)           | Storage modes, sync mechanisms, and the bootstrap flow                       |
| [Gas City Primitives](05-gascity-primitives.md)                    | Agents, providers, rigs, formulas, orders, and named sessions                |
| [Port Coordination & Networking](06-port-coordination.md)          | Port assignments, auto-allocation, and what is configurable vs fixed         |
| [Supervisor & Reconciliation](07-supervisor-and-reconciliation.md) | Supervisor architecture, tmux isolation, and the session reconciliation loop |
| [Customizable vs Fixed](08-customizable-vs-fixed.md)               | What you can configure in city.toml vs what is hardcoded                     |

### Troubleshooting Guides

| Document                                                    | Description                                                        |
| ----------------------------------------------------------- | ------------------------------------------------------------------ |
| [Tmux Sessions](09-troubleshooting-tmux.md)                 | Socket split-brain, session collisions, zombies, dead sockets      |
| [Dolt Server & Ports](10-troubleshooting-dolt.md)           | Ghost servers, port conflicts, stale PIDs, crashes, version issues |
| [Sync, Merges & Federation](11-troubleshooting-sync.md)     | Push/pull failures, merge conflicts, bootstrap, wisp GC            |
| [Supervisor & Reconciler](12-troubleshooting-supervisor.md) | Supervisor crashes, drain storms, config reload, routing failures  |
| [Operational Issues](13-troubleshooting-operational.md)     | OOM, zombies, lock files, race conditions, panics, full recovery   |

## Quick orientation

- **Gas Town** (`gt` CLI) — the original opinionated multi-agent workspace manager with hardcoded roles (Mayor, Polecat, Deacon, etc.)
- **Gas City** (`gc` CLI) — the configurable SDK extracted from Gas Town; roles become config, not code
- **Beads** (`bd` CLI) — distributed, graph-based work tracker backed by Dolt (a version-controlled SQL database)
- **Dolt** — "Git for data"; provides cell-level version control, distributed sync, and multi-writer concurrency for the bead store

## Source repositories

| Repository            | Stars   | Purpose                                           |
| --------------------- | ------- | ------------------------------------------------- |
| `gastownhall/gastown` | ~14,000 | Original multi-agent workspace manager            |
| `gastownhall/gascity` | ~210    | Orchestration-builder SDK extracted from Gas Town |
| `gastownhall/beads`   | —       | Distributed graph-based issue tracker             |
