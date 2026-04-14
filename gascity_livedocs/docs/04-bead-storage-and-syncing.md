# Bead Storage & Syncing

## Three Storage Modes

Beads supports three storage modes, each suited to different use cases:

### Embedded Mode (Default)

| Property             | Value                                    |
| -------------------- | ---------------------------------------- |
| **Data location**    | `.beads/embeddeddolt/`                   |
| **Writer model**     | Single-writer (file locking)             |
| **External process** | None                                     |
| **Config**           | Zero — works out of the box              |
| **Best for**         | Solo development, single-agent workflows |

The embedded Dolt engine runs in-process. No `dolt sql-server` needed. Each `bd` command opens the database, performs the operation, and closes it.

### Server Mode (Multi-Writer)

| Property             | Value                                            |
| -------------------- | ------------------------------------------------ |
| **Data location**    | `.beads/dolt/`                                   |
| **Writer model**     | Multi-writer via `dolt sql-server`               |
| **External process** | `dolt sql-server` (MySQL protocol)               |
| **Default port**     | Auto-allocated (hash of city path → 10000-60000) |
| **Best for**         | Multi-agent (Gas City), concurrent workloads     |

Multiple agents connect to the server simultaneously with full transaction isolation. This is the mode Gas City uses.

### Shared Server Mode

| Property             | Value                                                   |
| -------------------- | ------------------------------------------------------- |
| **Data location**    | `~/.beads/shared-server/`                               |
| **Writer model**     | Multi-writer, one server for all projects               |
| **External process** | Single shared `dolt sql-server`                         |
| **Isolation**        | Each project uses its own database (isolated by prefix) |
| **Best for**         | Many projects on one machine, port conservation         |

Eliminates port conflicts by running a single Dolt server with multiple databases.

## Directory Structure

```
.beads/
├── dolt/                    # Dolt database (server mode, gitignored)
│   ├── sql-server.pid       # Server process ID
│   └── sql-server.log       # Server logs
├── embeddeddolt/            # Dolt database (embedded mode, gitignored)
├── metadata.json            # Backend config (local, gitignored)
└── config.yaml              # Project config (optional, can be committed)
```

## Sync Mechanisms

### Dolt-Native Sync (Primary)

Beads uses Dolt remotes exclusively for sync. The legacy git-based JSONL sync has been removed.

```bash
# Configure a Dolt remote
bd dolt remote add origin https://doltremoteapi.dolthub.com/org/beads

# Push beads to remote
bd dolt push

# Pull beads from remote
bd dolt pull
```

Supported remote types:

| Remote Type | Example                                       |
| ----------- | --------------------------------------------- |
| DoltHub     | `https://doltremoteapi.dolthub.com/org/beads` |
| S3          | `s3://bucket/path`                            |
| GCS         | `gs://bucket/path`                            |
| Filesystem  | `/mnt/shared/beads`                           |
| git+ssh     | `git+ssh://host/path`                         |

### Federation (Peer-to-Peer)

For direct machine-to-machine sync without a central hub:

```bash
# Add a peer
bd federation add-peer town-beta 192.168.1.100:8080/beads

# Sync with all peers
bd federation sync
```

### How Merges Work

Dolt's cell-level merge handles most conflicts automatically:

| Scenario                                          | Resolution                                 |
| ------------------------------------------------- | ------------------------------------------ |
| Two agents edit different fields of the same bead | Auto-merged (no conflict)                  |
| Two agents edit the same field of the same bead   | Conflict — requires manual resolution      |
| Two agents create beads on different machines     | No conflict (hash-based IDs never collide) |
| One agent closes a bead while another edits it    | Auto-merged (different columns)            |

## Bootstrap & Contributor Onboarding

When a contributor clones a repository that uses beads:

```bash
cd cloned-project
bd bootstrap
```

The bootstrap process:

1. Probes `origin` for `refs/dolt/data`
2. Clones the Dolt database from the remote
3. Configures the Dolt remote for future push/pull
4. No manual steps required

This means beads data can travel alongside code in the same Git remote, using Dolt's ref namespace (`refs/dolt/data`) to avoid conflicts with code refs.

## Backup & Recovery

```bash
# Initialize backup destination
bd backup init /mnt/backup/beads

# Incremental sync to backup
bd backup sync

# Restore from backup
bd backup restore /mnt/backup/beads
```

Backups are Dolt-native, meaning they preserve full history and can be used as remotes.

## Health Checks

```bash
# Run diagnostics
bd doctor
```

The doctor command checks:

- Database integrity
- Server connectivity (in server mode)
- Remote reachability
- Schema version compatibility
- Orphaned processes
