# Troubleshooting: Dolt Server & Ports

Common issues with dolt sql-server processes, port conflicts, ghost servers, and database stability.

## Ghost Dolt Servers

### Symptom

Orphaned `dolt sql-server` processes consuming memory (~80-90MB each). `bd` commands connect to wrong databases. Port conflicts on startup.

### Root Causes & Fixes

**Boot race — agents start before dolt is ready** — When agents and dolt start in parallel, agents call `bd` before the server is listening, causing `bd` to auto-spawn embedded dolt servers on random ports.

- **Fix**: Split startup into two phases: (1) start dolt synchronously, (2) start agents. In Gas City, set `BEADS_DOLT_AUTO_START=0` in agent environments.
- **Refs**: `70126b4` (gastown), `32ac71c` (gascity)

**Stale port files trigger embedded auto-start** — When a rig's port file is missing or stale, `bd` spawns an orphan embedded server instead of connecting to the city's dolt.

- **Fix**: Sync port files to all rig `.beads/` directories after server start.
- **Ref**: `0948af4` (gascity), `b97a04e` (gastown)

**`dolt.auto-start=false` not respected** — `currentDoltPort()` deletes the port file when the server is temporarily unreachable, causing `bd` to silently auto-start.

- **Fix**: Propagate stale port via env vars so `bd` gets "connection refused" instead of auto-starting.
- **Ref**: `d8b2038` (gascity)

**`gc doctor` triggers orphans on suspended rigs** — Doctor health-checks all rigs including suspended ones, triggering `bd` auto-start.

- **Fix**: Skip suspended rigs in per-rig check registration.
- **Ref**: `075b742` (gascity)

### Detection

```bash
# Find all dolt processes
ps aux | grep 'dolt sql-server'

# Check which port a process is using
ss -tlnp | grep <pid>

# Check the expected port for this city
cat .beads/dolt-server.port

# Verify the server identity
cat .beads/dolt-server.pid | xargs kill -0 2>/dev/null && echo "alive" || echo "dead"
```

### Cleanup

```bash
# Kill all orphan dolt processes (careful — verify they're orphans first)
ps aux | grep 'dolt sql-server' | grep -v grep | awk '{print $2}' | xargs -r kill

# Remove stale PID and port files
rm -f .beads/dolt-server.pid .beads/dolt-server.port

# Restart the managed server
bd dolt start
# Or for Gas City:
gc start
```

---

## Port Conflicts

### Symptom

`bd dolt start` fails with "port already in use". Or, another city's dolt server is silently treated as "ours".

### Diagnosis

```bash
# What's holding the port?
ss -tlnp | grep <port>

# What port does this city expect?
cat .beads/dolt-server.port

# Is it our server or an imposter?
# Compare the PID file with the actual process
cat .beads/dolt-server.pid
ps -p $(cat .beads/dolt-server.pid) -o command=
```

### Fix

**Another city's dolt on our port** — City-scoped detection now uses `.gc/dolt.pid` instead of the legacy path, preventing cross-city false positives.

```bash
# Override the port explicitly
export GC_DOLT_PORT=43678
# Or in city.toml:
# [dolt]
# port = 43678
```

- **Ref**: `f8657e6` (gascity)

**DDL operations creating databases in embedded mode** — When `buildDoltSQLCmd` omits `--host/--port`, dolt runs in embedded mode instead of connecting to the server. The database gets created on disk but the server never sees it.

- **Fix**: DDL operations now always use explicit `--host/--port` flags.
- **Ref**: `d792ec5` (gastown)

---

## Stale PID and Socket Files

### Symptom

- "unix socket set up failed: file already in use" after dolt crash
- `bd dolt start` fails despite no dolt process running
- `bd doctor` reports stale lock warnings

### Fix

```bash
# Remove stale Unix socket
rm -f /tmp/mysql.sock

# Remove stale PID file
rm -f .beads/dolt-server.pid

# Remove stale noms LOCK files (recursive — catches nested databases)
find .beads/ -name LOCK -path '*/noms/*' -exec rm {} \;

# Remove stale dolt-access.lock
rm -f .beads/dolt-access.lock

# Restart
bd dolt start
```

- **Refs**: `2e058fa` (gastown), `76e01b2`, `dbdc955` (beads)

---

## Dolt Server Crashes

### SIGTERM Race (NomsBlockStore.Close panic)

**Symptom**: Recurring panic with `NomsBlockStore.Close()` temp file race.
**Root cause**: SIGTERM arrives while storage I/O is active.
**Fix**: Pre-SIGTERM connection drain (wait 10s for queries to complete before SIGTERM). Guard restart if server running < 60s.

- **Ref**: `41384ab` (gastown)

### Corrupted Journal on Restart

**Symptom**: "corrupted journal at offset N" after crash.
**Root cause**: SIGKILL sent only 5s after SIGTERM, not enough time for journal flush under heavy load.
**Fix**: Increase SIGTERM-to-SIGKILL timeout from 5s to 30s.

- **Ref**: `5f8161d` (gastown)

### SELECT UNION Crash on Column Mismatch

**Symptom**: Every `bd`/`gt` command crashes. Circuit breaker blocks all operations.
**Root cause**: `SELECT * FROM issues UNION ALL SELECT * FROM wisps` panics when tables have different column counts after schema migration.
**Fix**: Export tables as separate queries instead of UNION ALL.

- **Ref**: `5f35dc7` (beads)

### Phantom Database Directories

**Symptom**: Corrupted phantom database dirs crash the entire dolt server on startup.
**Root cause**: `ListDatabases` didn't validate `.dolt/noms/manifest` exists.
**Fix**: Quarantine phantom directories before launching server.

- **Ref**: `7c0de7b` (gastown)

---

## Embedded vs Server Mode Confusion

### Symptom

Commands silently operate in embedded mode (filesystem-only) instead of connecting to the running server. Reads return stale data. Writes go to wrong database.

### Root Causes

**Missing `--host/--port` flags** — Several code paths omitted server connection flags, causing dolt to fall back to embedded mode.

```bash
# Verify you're connecting to the server, not embedded
bd dolt status
```

**Admission control using embedded mode** — `GetActiveConnectionCount` fell into embedded mode for local servers, loading all databases into memory and causing OOM.

- **Fix**: Always connect as TCP client with explicit flags.
- **Ref**: `f99edf6` (gastown)

**JSONL backup reads stale data** — Daemon's export used embedded mode, reading stale on-disk state instead of the live server.

- **Fix**: Connect to running server via `--host/--port` when available.
- **Ref**: `8a61d94` (gastown)

---

## Version Mismatches

### Symptom

"workspace bd_version 1.0.0 is newer than embedded beads 0.63.3" — daemon startup blocked.

### Fix

Update the embedded beads dependency to match the store version.

```bash
# Check versions
bd version
bd dolt status
```

- **Ref**: `746225e` (gastown)

---

## Dolt Server Ready Timeout

### Symptom

`bd init --shared-server` kills dolt mid-bootstrap with "server started but not accepting connections".

### Root Cause

Hardcoded 10-second timeout. First-run SQL engine bootstrap takes ~60s on slow hardware.

### Fix

```bash
export BEADS_DOLT_READY_TIMEOUT=120
bd init --shared-server
```

- **Ref**: `f09970f` (beads)

---

## Security: Dashboard CORS Vulnerability

### Symptom

Historical: dashboard bound to `0.0.0.0` with `Access-Control-Allow-Origin: *` and no authentication.

### Fix (applied)

Four defense layers: bind to 127.0.0.1, remove wildcard CORS, per-session CSRF token, server-side confirmation for dangerous commands.

- **Ref**: `7a484c5` (gastown)
