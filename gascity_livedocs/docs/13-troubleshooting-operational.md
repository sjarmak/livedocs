# Troubleshooting: Operational Issues

Memory, zombies, lock files, race conditions, panics, and recovery procedures.

## OOM & Memory Issues

### Storage Cache Memory Leak

**Symptom**: Memory and file descriptor count grow unboundedly over daemon lifetime.
**Root cause**: No eviction policy on daemon storage connections.
**Fix**: TTL-based (30min idle) and LRU (max 50 repos) cache eviction with cleanup goroutine.

- **Ref**: `259e994` (beads)

### SHOW DATABASES Thundering Herd

**Symptom**: Server pinned at 99% CPU. Multiple callers each spawn ~200MB dolt sql subprocesses.
**Root cause**: No caching — status-line, daemon, bd list, and health checks all fire SHOW DATABASES simultaneously.
**Fix**: 30-second TTL cache with single-flight deduplication on `ListDatabases`.

- **Ref**: `31ea6e4` (gastown)

### Unbounded SearchIssues OOM

**Symptom**: OOM crash during wisp gc, wisp list, or purge-closed on large stores.
**Root cause**: Unbounded queries loaded all ephemeral issues at once.
**Fix**: Add `Limit:5000` to all filters.

- **Ref**: `8380155` (beads)

### Context Accumulation From Resume Mode

**Symptom**: Token usage ~2x compared to expected. Context compounds across sleep/wake cycles.
**Root cause**: Default `wake_mode="resume"` reconnects to old Claude conversations.
**Fix**: Set `wake_mode="fresh"` on agents that don't need conversation continuity.

- **Ref**: `a722063` (gascity)

### Per-Operation Auto-Push Bloat

**Symptom**: 22GB of `git-remote-cache` bloat.
**Root cause**: Every `bd create` triggered automatic push to git remotes, with dozens of agents creating wisps constantly.
**Fix**: Push only via daemon periodic task (every 15min) or explicit `bd dolt push`.

- **Ref**: `a22da6f` (beads)

### Write Amplification From Per-Operation bd Calls

**Symptom**: Continuous btrfs write thrashing on large sweeps.
**Root cause**: Shell scripts loop over individual `bd` invocations, each opening a connection and creating one DOLT_COMMIT.
**Fix**: `bd batch` command collapses N operations into a single dolt transaction.

- **Refs**: `e39f041` (beads), `3d0afcd` (gascity)

---

## Zombie Processes

### Deacon Patrol Process Leak

**Symptom**: 12+ accumulated claude processes consuming resources. New process every patrol cycle (1-3 min) but old ones never killed.
**Root cause**: `tmux respawn-pane -k` sends SIGHUP to the shell but doesn't kill descendant processes (claude and its node children).
**Fix**: Kill pane processes explicitly before respawn.

- **Ref**: `1b036aa` (gastown)

### Hung Dogs Consuming 500MB Each

**Symptom**: Dogs finish work but sit idle at Claude prompt forever, consuming ~500MB RAM each.
**Root cause**: Dogs that finish but fail to call `gt dog done` are never reclaimed.
**Fix**: Auto-clear hung dogs (idle > 10m) and orphan sessions with `--auto-clear`.

- **Ref**: `15d5d5e` (gastown)

### Dolt Zombie Process Detection

**Symptom**: Zombie (Z state) dolt processes counted against limits and mistakenly adopted.
**Root cause**: `isDoltProcess()` matched zombie processes via `ps -p PID -o command=`.
**Fix**: Check process state via `ps -o state=` first; reject Z (zombie) and X (dead) states.

- **Ref**: `b21ec9e` (beads)

### Container Zombie Reaping

**Symptom**: Zombie child processes accumulating in Docker containers.
**Fix**: Add tini as PID 1 init process to Dockerfile.

- **Ref**: `9c2f0d0` (gastown)

---

## Lock File Issues

### Stale Noms LOCK Files After Crash

**Symptom**: SIGSEGV or "database is locked" on restart after OOM, SIGKILL, or tmux kill.
**Root cause**: Unclean process death leaves `.dolt/noms/LOCK` files that prevent reopening.
**Fix**:

```bash
# Remove stale locks manually
find .beads/ -name LOCK -path '*/noms/*' -delete

# Or use doctor
bd doctor --fix
```

`CleanStaleNomsLocks()` now runs pre-flight before connecting.

- **Ref**: `76e01b2` (beads)

### Flock File Removal Breaking Mutual Exclusion

**Symptom**: Lock protection silently broken. Concurrent processes both acquire "exclusive" locks.
**Root cause**: Removing a flock file while another process waits causes the waiter to acquire lock on the deleted inode.
**Fix**: Never remove flock files. They should persist.

- **Ref**: `4184029` (gastown)

### bd migrate Self-Deadlock

**Symptom**: `bd migrate` hangs indefinitely.
**Root cause**: `PersistentPreRun` opens a global store, but migrate manages its own. Both deadlock against each other's noms LOCK.
**Fix**: Added `migrate` to `noDbCommands` so PersistentPreRun skips global store open.

- **Ref**: `f9f585f` (beads)

### Doctor False Positive on Own Locks

**Symptom**: `bd doctor` always reports LOCK file warnings.
**Root cause**: Doctor's embedded Dolt opens create LOCK files; `CheckLockHealth` runs after and finds them.
**Fix**: Run `CheckLockHealth` before any embedded Dolt opens.

- **Ref**: `da8a1bf` (beads)

---

## Race Conditions

### Port Allocation Race in Tests

**Symptom**: Zombie dolt sql-server leaks; concurrent tests grab the same port.
**Fix**: Cross-process flock coordination; `sync.Once` for in-process singleton.

- **Ref**: `0f7d7d2` (beads)

### Concurrent Schema Init Causes Data Loss

**Symptom**: Data loss from corrupted Dolt journal.
**Root cause**: Concurrent schema initialization from multiple processes.
**Fix**: Serialized with `GET_LOCK('bd_schema_init', 30)` on dedicated connection.

- **Ref**: `f3829c9` (beads)

### Sling Bead Assignment TOCTOU

**Symptom**: Two processes both assign the same bead; duplicate work.
**Root cause**: Check-then-write race on bead assignment.
**Fix**: Per-bead flock around `hookBeadWithRetry`.

- **Ref**: `576edda` (gastown)

### SIGTERM Cascading to Dolt Child

**Symptom**: SIGTERM to gc kills the dolt child, crashing the database.
**Fix**: `Setpgid` prevents signal cascade from gc to dolt.

- **Ref**: `5998273` (gascity)

---

## Common Panics (Now Fixed)

These panics have been fixed but may appear in older versions:

| Panic                                         | Root Cause                         | Fix                             | Ref       |
| --------------------------------------------- | ---------------------------------- | ------------------------------- | --------- |
| Nil stderr SIGSEGV in session materialization | `fmt.Fprintf(nil, ...)`            | Use `io.Discard`                | `ad56a00` |
| String slice `[:8]` on short hash             | No bounds check                    | `truncateID` helper             | `d1859a0` |
| `err.Error()[:20]` on short message           | No length check                    | `strings.HasPrefix`             | `1296242` |
| `time.NewTicker(0)` panic                     | No validation on `patrol_interval` | Reject non-positive durations   | `e783768` |
| `MustParse` on bad K8s quantity               | Panics on bad input                | `ParseQuantity` (returns error) | `3739e75` |
| `os.Executable()` fork bomb in tests          | Re-runs test binary                | PATH lookup instead             | `1122267` |
| Nil Cobra.Command in tests                    | Tests pass nil cmd                 | `context.Background()` fallback | `9f33b97` |

---

## Graceful Shutdown

### Two-Pass Shutdown Pattern

Gas City uses a two-pass shutdown:

1. Send Ctrl-C (Interrupt) to all agents → wait `shutdown_timeout` (default 5s)
2. Force-kill survivors

```toml
# city.toml
[daemon]
shutdown_timeout = "10s"  # Time for agents to save state
```

- **Ref**: `9f67e41`, `c2e4ca1` (gascity)

### Supervisor Shutdown Race

**Symptom**: Duplicate SIGTERM/kill sequences when both panic recovery and shutdown loop fire.
**Fix**: `sync.Once` guard on `CityRuntime.shutdown()`.

- **Ref**: `d153c5c` (gascity)

---

## Full Recovery Playbook

When everything is broken:

```bash
# 1. Stop supervisor
systemctl --user stop gascity-supervisor

# 2. Kill orphan dolt processes
ps aux | grep 'dolt sql-server' | grep -v grep | awk '{print $2}' | xargs -r kill

# 3. Clean stale locks and PID files
rm -f .gc/session-name-locks/*.lock
rm -f .beads/dolt-server.pid .beads/dolt-server.port
find .beads/ -name LOCK -path '*/noms/*' -delete
rm -f .beads/dolt-access.lock

# 4. Start tmux on the correct socket
tmux -L ds-research new-session -d -s placeholder "sleep infinity"

# 5. Check for zombie session beads in dolt
cd .beads/dolt/gc
dolt sql -q "SELECT id, status, JSON_EXTRACT(metadata, '\$.session_name') as sn
             FROM issues WHERE issue_type = 'session'
             AND status != 'closed'
             AND JSON_EXTRACT(metadata, '\$.session_name') != '';"
# Close any zombies found

# 6. Start supervisor (tmux must already be running)
systemctl --user start gascity-supervisor

# 7. Wait ~30s for boot, then verify
sleep 30
gc session list
gc doctor
```

---

## Diagnostic Commands

```bash
# Overall health
gc doctor
bd doctor

# Supervisor status
systemctl --user status gascity-supervisor
tail -50 ~/.gc/supervisor.log

# Tmux sessions
tmux -L ds-research list-sessions

# Dolt server
ss -tlnp | grep $(cat .beads/dolt-server.port 2>/dev/null || echo 43677)

# Bead store
bd ready        # What's actionable
bd stats        # Store statistics
bd dolt status  # Dolt connection status

# Config drift
bd config drift

# Process inventory
ps aux | grep -E '(dolt|claude|codex)' | grep -v grep
```
