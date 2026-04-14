# Troubleshooting: Tmux Sessions

Common issues with tmux session management, socket isolation, and agent lifecycle.

## Session Name Collisions

### Symptom

`gc session new` or supervisor fails with "session name already exists" or "duplicate session".

### Root Causes & Fixes

**Hardcoded session names across towns/cities** — Mayor/deacon sessions like `gt-mayor` collide when multiple instances run on the same machine.

- **Fix**: Session names now include the town/city name (e.g., `gt-ai-mayor`). In Gas City, per-city tmux socket isolation makes this automatic.
- **Ref**: `e7145cf` (gastown), `599fd6e` (gascity)

**Closed beads permanently reserving names** — After cold boot, closed session beads from previous runs still hold name reservations.

- **Fix**: Config-aware name check bypasses rejection when a legacy closed bead holds the name. Verify with:
  ```bash
  cd .beads/dolt/gc
  dolt sql -q "SELECT id, status, JSON_EXTRACT(metadata, '$.session_name') as sn FROM issues WHERE issue_type='session' AND status != 'closed';"
  ```
- **Ref**: `b864cbb`, `c13f6d1` (gascity)

**Reserved infrastructure names** — Polecats named "crew" or "polecats" collide with session name parsing markers.

- **Fix**: Both added to `ReservedInfraAgentNames`. Avoid naming agents with these words.
- **Ref**: `5425645` (gastown)

---

## Socket Split-Brain

### Symptom

Sessions exist but are invisible to commands. `gc status` shows agents as stopped. `gt nudge` silently fails. Orphan detection misses active sessions.

### Root Cause

Sessions created on one tmux socket (e.g., `default`) while commands query a different socket (e.g., `gt` or the city name). This happens during:

- Binary upgrades that change socket naming conventions
- Supervisor restarts without the socket env var
- Manual tmux operations on the wrong socket

### Diagnosis

```bash
# Check what's on the city socket
tmux -L ds-research list-sessions

# Check what's on the default socket
tmux list-sessions

# Gas Town: check doctor for split-brain
gt doctor
```

### Fix

```bash
# Gas City: socket defaults to workspace.name, auto-isolated
# Verify in city.toml:
# [session]
# socket = "ds-research"

# Gas Town: detect and clean cross-socket zombies
gt doctor --fix
```

- **Refs**: `33362a7`, `d09dc33`, `3a5980e`, `2af747f`, `0dd1eae` (gastown); `837d467`, `599fd6e` (gascity)

### Prevention

- Never use bare `tmux` commands — always use `tmux -L <socket>` or the `gc`/`gt` CLI
- Gas City auto-defaults socket to `workspace.name`; don't override unless you have a reason

---

## Dead Tmux Socket (Chicken-and-Egg)

### Symptom

- `gc session new` times out with `tmux state cache: refresh failed ... no tmux server running`
- `gc status` shows all agents as `stopped`
- Supervisor log shows `tmux state cache: refresh failed`

### Root Cause

The reconciler needs a running tmux server to create sessions, but it drains unrecognized sessions as orphans. If the tmux server dies (all managed sessions close), no sessions can be created.

### Recovery

```bash
# 1. Start a placeholder tmux session
tmux -L ds-research new-session -d -s placeholder "sleep infinity"

# 2. Restart the supervisor
systemctl --user restart gascity-supervisor

# 3. Wait ~30s for boot (scale checks run for all agents)
sleep 30

# 4. The reconciler drains "placeholder" as orphaned but creates managed sessions first
gc session list
```

---

## Zombie Sessions (Unkillable)

### Symptom

Sessions respawn 3 seconds after being killed. Infinite zombie loop.

### Root Cause

tmux's `remain-on-exit` + `pane-died` hook creates auto-respawn machinery. `KillSessionWithProcesses` killed the processes but didn't disarm the hooks first.

### Fix

```bash
# Disarm hooks first, then kill
tmux -L ds-research set-option -t <session> remain-on-exit off
tmux -L ds-research kill-session -t <session>
```

- **Ref**: `8358ade` (gastown), `d06ee7c` (gastown)

---

## Stale Session Key Crash Loop

### Symptom

Agent session starts, dies immediately ("No conversation found"), then the reconciler restarts it in an infinite loop.

### Root Cause

When a session dies unexpectedly, the `session_key` is preserved for resume. But if the Claude conversation was deleted, `--resume <key>` fails immediately. The reconciler keeps retrying with the same dead key.

### Fix

The reconciler now detects rapid death (2s grace period), clears the stale session key, and retries with a fresh start. If stuck:

```bash
# Manually clear the session key
gc session update <id> --set-metadata session_key=""
```

- **Refs**: `d0dd592`, `877ec9a`, `3732381`, `ba13237` (gascity)

---

## "No Current Target" Error

### Symptom

`gc crew start` or agent startup fails with `tmux has-session: no current target` instead of bootstrapping a new server.

### Root Cause

When no tmux server exists, `tmux has-session` returns "no current target". This error wasn't mapped to `ErrNoServer`, so the code didn't attempt to bootstrap.

### Fix

Upgraded in both Gas Town and Gas City. If running an older version:

```bash
# Start a tmux server first
tmux -L ds-research new-session -d -s bootstrap "sleep infinity"
# Then retry your command
```

- **Ref**: `371074c` (gastown)

---

## Window Size Locked to 80x24

### Symptom

Agent sessions have tiny 80x24 terminal windows, even when the real terminal is larger.

### Root Cause

tmux 3.3+ defaults detached sessions to manual sizing. Without `window-size=latest`, the session stays at 80x24.

### Fix

Applied in both projects. If your sessions still show 80x24:

```bash
tmux -L ds-research set-option -t <session> window-size latest
```

- **Ref**: `9471a7e` (gascity, ported from gastown)

---

## macOS Socket Path Mismatch

### Symptom

`gt status` shows wrong socket path. Session detection fails because tmux sockets are looked up in `/var/folders/.../T/` instead of `/tmp/tmux-<uid>/`.

### Root Cause

`os.TempDir()` on macOS returns `/var/folders/.../T/` but tmux always creates sockets under `/tmp/tmux-<uid>/`.

### Fix

Use `/tmp` directly for tmux socket paths, not `os.TempDir()`.

- **Ref**: `a19d1e6` (gastown)
