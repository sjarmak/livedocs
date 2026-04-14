# Beads Data Model

## What is a Bead?

A **bead** is a structured work item (issue/task) in a distributed, graph-based issue tracker designed for AI coding agents. The name evokes connected items forming a dependency graph — like beads on a string.

Beads replace messy markdown plans with a dependency-aware graph database. Each bead is self-contained with a content hash, enabling collision-free merging across machines.

## Core Fields

### Identification

| Field          | Type             | Description                                                              |
| -------------- | ---------------- | ------------------------------------------------------------------------ |
| `id`           | VARCHAR(255), PK | Hash-based identifier (e.g., `bd-a1b2`) — collision-free across machines |
| `content_hash` | VARCHAR(64)      | SHA256 hash for deduplication and change detection during merges         |

### Content

| Field                 | Type                   | Description                      |
| --------------------- | ---------------------- | -------------------------------- |
| `title`               | VARCHAR(500), required | Issue title                      |
| `description`         | TEXT                   | Detailed description             |
| `design`              | TEXT                   | Design notes                     |
| `acceptance_criteria` | TEXT                   | What "done" looks like           |
| `notes`               | TEXT                   | Additional notes                 |
| `spec_id`             | VARCHAR(1024)          | External specification reference |

### Status & Workflow

| Field        | Type        | Description                                                                                                          |
| ------------ | ----------- | -------------------------------------------------------------------------------------------------------------------- |
| `status`     | VARCHAR(32) | `open`, `in_progress`, `blocked`, `deferred`, `closed`, `tombstone`, `pinned`, `hooked`                              |
| `priority`   | INT (0-4)   | 0 = critical, 4 = backlog                                                                                            |
| `issue_type` | VARCHAR(32) | `bug`, `feature`, `task`, `epic`, `chore`, `message`, `merge-request`, `molecule`, `gate`, `agent`, `role`, `convoy` |

### Assignment

| Field               | Type         | Description                 |
| ------------------- | ------------ | --------------------------- |
| `assignee`          | VARCHAR(255) | Assigned user or agent      |
| `owner`             | VARCHAR(255) | Human owner for attribution |
| `estimated_minutes` | INT          | Time estimate               |

### Timestamps

| Field               | Type         | Description                        |
| ------------------- | ------------ | ---------------------------------- |
| `created_at`        | DATETIME     | Creation time                      |
| `updated_at`        | DATETIME     | Last modification                  |
| `created_by`        | VARCHAR(255) | Creator identifier                 |
| `closed_at`         | DATETIME     | When closed                        |
| `close_reason`      | TEXT         | Why it was closed                  |
| `closed_by_session` | VARCHAR(255) | Claude Code session that closed it |

### Metadata & Integration

| Field           | Type         | Description                                                                                      |
| --------------- | ------------ | ------------------------------------------------------------------------------------------------ |
| `metadata`      | JSON         | Arbitrary structured data for extensions (Gas City uses `gc.routed_to`, `gc.session_name`, etc.) |
| `external_ref`  | VARCHAR(255) | External system references (e.g., `gh-9`, `jira-ABC`)                                            |
| `source_system` | VARCHAR(255) | Federation/adapter identifier                                                                    |

### Specialized Fields

| Field         | Type    | Description                                                      |
| ------------- | ------- | ---------------------------------------------------------------- |
| `ephemeral`   | TINYINT | If true, this bead is a **wisp** — never syncs to git or remotes |
| `pinned`      | TINYINT | Persistent context marker                                        |
| `is_template` | TINYINT | Read-only template molecule                                      |

## Database Schema (5 Core Tables)

### 1. `issues` — Primary bead storage

The main table with 60+ columns. Indexed on: `status`, `priority`, `issue_type`, `assignee`, `created_at`, `spec_id`, `external_ref`.

### 2. `dependencies` — Bead relationships

```sql
(issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
```

- Composite PK on `(issue_id, depends_on_id)`
- Foreign key cascade on delete
- Dependency types:
  - `blocks` — A blocks B
  - `parent-child` — hierarchical nesting
  - `related` — loose association
  - `discovered-from` — provenance
  - `relates-to` — bidirectional link
  - `replies-to` — threaded discussion
  - `duplicates` — duplicate marker
  - `supersedes` — replacement

### 3. `labels` — Tag associations

```sql
(issue_id, label)
```

Composite PK, indexed on `label`. Used for routing and categorization.

### 4. `comments` — Discussion threads

```sql
(id UUID, issue_id, author, text, created_at)
```

### 5. `events` — Audit trail

```sql
(id UUID, issue_id, event_type, actor, old_value, new_value, comment, created_at)
```

Complete audit trail of every state change.

### Auxiliary Tables

| Table              | Purpose                                          |
| ------------------ | ------------------------------------------------ |
| `config`           | Configuration storage                            |
| `metadata`         | Project metadata                                 |
| `child_counters`   | Hierarchical ID generation (e.g., `bd-a3f8.1.2`) |
| `issue_counter`    | Sequential ID mode                               |
| `routes`           | Multi-repo routing                               |
| `federation_peers` | P2P sync configuration                           |
| `custom_statuses`  | User-defined status values                       |
| `custom_types`     | User-defined issue types                         |

### Views

| View                  | Purpose                                                |
| --------------------- | ------------------------------------------------------ |
| `ready_issues_view`   | Beads with no open blockers (optimized for `bd ready`) |
| `blocked_issues_view` | Beads blocked by open dependencies                     |

## Wisps (Ephemeral Beads)

**Wisps** are ephemeral beads stored in separate tables that mirror the main schema:

- `wisps`, `wisp_labels`, `wisp_dependencies`, `wisp_events`, `wisp_comments`
- **Dolt-ignored** — not version tracked, never sync to remotes
- Exist only in the local database
- Hard-deleted when compacted (no tombstones)
- Used for molecule execution steps and temporary scratch work

## The `bd` CLI

The `bd` command-line tool is the primary interface to beads.

### Installation

```bash
# Homebrew (macOS/Linux)
brew install beads

# npm
npm install -g @beads/bd

# Install script
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
```

### Essential Commands

| Command                       | Description                                              |
| ----------------------------- | -------------------------------------------------------- |
| `bd ready`                    | List beads with no open blockers                         |
| `bd create "Title"`           | Create a new bead (auto-generates hash ID + Dolt commit) |
| `bd update <id> --claim`      | Atomic assignee + in_progress transition                 |
| `bd show <id>`                | View bead details                                        |
| `bd close <id>`               | Close a bead                                             |
| `bd dep add <child> <parent>` | Add a dependency edge                                    |

### Dolt Operations

| Command                                             | Description          |
| --------------------------------------------------- | -------------------- |
| `bd dolt push` / `bd dolt pull`                     | Sync with remotes    |
| `bd dolt start` / `bd dolt stop` / `bd dolt status` | Server management    |
| `bd dolt remote add` / `list` / `remove`            | Remote configuration |

### Version Control

| Command        | Description              |
| -------------- | ------------------------ |
| `bd vc log`    | View commit history      |
| `bd vc diff`   | Show uncommitted changes |
| `bd vc commit` | Create a manual commit   |

### Data Management

| Command                               | Description                             |
| ------------------------------------- | --------------------------------------- |
| `bd export`                           | Export to JSONL                         |
| `bd backup init` / `sync` / `restore` | Dolt-native backups                     |
| `bd doctor`                           | Health checks and repair                |
| `bd compact`                          | Semantic memory decay (prune old wisps) |

### Auto-Commit Behavior

- **Embedded mode**: Each write command creates a Dolt commit automatically

  ```bash
  bd create "New issue"  # Creates issue + Dolt commit in one step
  ```

- **Server mode**: Auto-commit defaults to OFF (server manages transactions)
  ```bash
  bd --dolt-auto-commit off create "Issue 1"
  bd --dolt-auto-commit off create "Issue 2"
  bd vc commit -m "Batch: created issues"
  ```
