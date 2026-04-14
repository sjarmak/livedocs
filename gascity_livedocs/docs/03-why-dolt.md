# Why Dolt

Beads uses Dolt as its storage backend instead of SQLite, flat files, or a traditional database. This is not incidental — the choice of Dolt is the key architectural decision that enables beads' distributed, multi-writer, version-controlled design.

## What is Dolt?

Dolt is "Git for data" — a SQL database that implements the Git version control model at the cell level. Every table modification creates a commit. Branches, merges, diffs, and push/pull all work like Git but operate on database rows and cells rather than text lines.

## Five Reasons Beads Needs Dolt

### 1. Cell-Level Version Control

Every write to the bead store automatically creates a Dolt commit. This provides:

- **Time-travel queries**: `SELECT * FROM issues AS OF 'commit-hash'`
- **Cell-level diffs**: changes are tracked per-cell, not per-line (unlike git on flat files)
- **Complete audit trail**: full history with zero extra application code
- **Branching**: experimental work on beads without affecting the main branch

With SQLite or flat files, you would need to build all of this manually — event sourcing, snapshot tables, diff logic. Dolt provides it as a storage primitive.

### 2. Distributed Sync Without a Central Server

Dolt has native push/pull to remotes, just like Git:

- **Remote types**: DoltHub, S3, GCS, filesystem, git+ssh
- **Cell-level merge**: automatically resolves most conflicts (two agents editing different fields of the same bead merge cleanly)
- **No coordination server**: machines converge to the same state independently
- **Issues travel with code**: bead data can live alongside the codebase

SQLite databases cannot be meaningfully merged. Flat files require line-level merge, which breaks on concurrent edits to the same record.

### 3. Multi-Writer Concurrency

In server mode (`dolt sql-server`), multiple agents and orchestrators write simultaneously with full transaction isolation.

This is critical for Gas City, where 5+ Claude agents may need concurrent write access to the bead store. SQLite is fundamentally single-writer with file locking — under concurrent load, writers queue behind a single lock, creating bottlenecks and timeout failures.

### 4. Hash-Based Collision Prevention

This is the key insight that enables distribution:

```
Sequential IDs (SQLite approach):
  Machine A: bd create → bd-10
  Machine B: bd create → bd-10    ← COLLISION

Hash-based IDs (Dolt approach):
  Machine A: bd create → bd-a1b2  (derived from UUID)
  Machine B: bd create → bd-f14c  (different UUID, no collision)
```

Each bead has a `content_hash` (SHA256) for change detection. During merge:

| Condition                | Action                      |
| ------------------------ | --------------------------- |
| Same ID + same hash      | Skip (already present)      |
| Same ID + different hash | Update (newer version wins) |
| No matching ID           | Create (new bead)           |

This eliminates the need for central coordination while guaranteeing convergence.

### 5. Full SQL with Indexing

Complex queries — like "find all beads with no open blockers" (`bd ready`) — run in milliseconds via optimized SQL views with dependency graph traversal and indexed lookups.

With flat files:

- Every query requires a full directory scan
- Dependency graph traversal requires loading and parsing every file
- No indexes, no joins, no aggregate functions
- Performance degrades linearly with bead count

With SQLite:

- Good query performance but no distributed sync
- No version control
- Single-writer limitation

Dolt gives you the SQL performance of a real database AND the distributed version control of Git.

## Trade-offs

Dolt is not without costs:

| Trade-off             | Impact                                                           |
| --------------------- | ---------------------------------------------------------------- |
| **Binary size**       | Dolt is a full database engine (~100MB binary)                   |
| **Complexity**        | Running `dolt sql-server` adds a process to manage               |
| **Port coordination** | Server mode needs a port; multiple projects need port allocation |
| **Ghost processes**   | Orphaned dolt servers can accumulate if not cleaned up           |

These costs are accepted because the alternatives (SQLite, flat files) cannot provide the combination of multi-writer concurrency, distributed sync, and version control that beads requires.
