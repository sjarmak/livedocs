# Troubleshooting: Supervisor & Reconciler

Common issues with the supervisor daemon, session reconciler, agent lifecycle, and config hot-reload.

## Supervisor Crashes

### Panic Cascading From Single City

**Symptom**: Entire supervisor crashes, taking all managed cities down.
**Root cause**: No panic recovery around `reconcileCities`.
**Fix**: Each city's reconcile loop is wrapped in panic recovery with crash-loop backoff (10s to 5min cap).

- **Ref**: `1d38afd`, `cfe43d6` (gascity)

### Concurrent Map Crash

**Symptom**: `concurrent map read and map write` crash.
**Root cause**: `panicHistory` reads not protected by mutex.
**Fix**: Protected with `mu.Lock()`.

- **Ref**: `77f9568` (gascity)

### Socket Path Mismatch

**Symptom**: `gc supervisor stop/status` never finds the running supervisor.
**Root cause**: Supervisor socket handler reused the controller's path (`.gc/controller.sock` instead of `~/.gc/supervisor.sock`).
**Fix**: Dedicated `startSupervisorSocket` binding to the correct path.

- **Ref**: `164702d` (gascity)

### Init Failure Log Spam

**Symptom**: Supervisor log fills with repeated init failures every 10s indefinitely.
**Root cause**: No backoff on cities that fail initialization.
**Fix**: Exponential backoff (10s → 20s → 40s, capped at 5min). Auto-resets when `city.toml` is modified.

- **Ref**: `5cd17f2` (gascity)

### API Blocked During Shutdown

**Symptom**: API requests hang for seconds during shutdown or reconciliation.
**Root cause**: Single mutex shared between API reads and reconciliation writes. Reconciliation held the lock 21 times per tick.
**Fix**: Replaced with atomic snapshot pattern — API reads perform lock-free `atomic.Pointer` load.

- **Ref**: `2d7a457` (gascity)

### One City Panic Orphans Others

**Symptom**: If one city panics during shutdown, remaining cities are never stopped.
**Fix**: Wrapped `cr.shutdown()` in nested recovery in both shutdown and unregister loops.

- **Ref**: `ef5de64`, `77f9568` (gascity)

---

## Reconciler Issues

### Config-Drift Drain Storm

**Symptom**: All sessions drain simultaneously and restart, despite no config changes.
**Root cause**: `stageHookFiles` probes workDir-relative paths whose hashes change between pre_start and post-start. `CoreFingerprint` included these non-deterministic hashes.
**Fix**: Exclude pre_start-staged CopyFiles from config fingerprint. Add `Probed` field to prevent silent fingerprint fallback.

- **Refs**: `391222d`, `693cbe6`, `ef6c0b0`, `4a6c473` (gascity)

### Pool-Excess Sessions Permanently Drained

**Symptom**: Pool sessions drained when demand drops, but can never be rescued when demand returns.
**Root cause**: Pool instances (e.g., `polecat-1`) classified as "orphaned" (non-cancelable drain) instead of "pool-excess" (cancelable drain).
**Fix**: `classifyUndesiredSession()` distinguishes the two cases.

- **Ref**: `7af49b9` (gascity)

### Drained Pool Beads Never Closed

**Symptom**: New work cannot wake pool sessions — slots occupied by stale drained beads.
**Root cause**: After drain-ack, beads went to `asleep/drained` but were never closed. No handler for `!shouldWake && !target.alive`.
**Fix**: Fourth branch added to close drained+dead beads so fresh ones are created on next tick.

- **Ref**: `c8d348d` (gascity)

### Ctrl-C Drain Interrupts Working Agents

**Symptom**: Pool agents hang at "What should Claude do instead?" prompt. Working agents interrupted on transient bead store failures.
**Root cause**: `beginSessionDrain` sent Ctrl-C to agents.
**Fix**: Replaced with `GC_DRAIN_ACK` env var mechanism. Drain signal deferred by one tick for false-orphan recovery.

- **Ref**: `d6639ed` (gascity)

### Routed Queue Inflates Pool Demand

**Symptom**: Bulk graph materialization spawns one worker per routed bead instead of following `scale_check`.
**Root cause**: Reconciler mixed assigned actionable work with routed queue work for pool demand calculation.
**Fix**: Treat `AssignedWorkBeads` as actionable only; stop deriving pool requests from routed-but-unassigned beads.

- **Ref**: `dbeac76` (gascity)

### Phantom Polecat Spawns

**Symptom**: Phantom polecats spawned that cannot claim beads and sit idle.
**Root cause**: When polecat completed work and reassigned to refinery, `gc.routed_to` was not updated, so scale_check still counted the bead for the polecat pool.
**Fix**: Update `gc.routed_to` on polecat-to-refinery handoff.

- **Ref**: `addeb2d` (gascity)

### Named-Always Sessions Bypass Suspension

**Symptom**: Named sessions with `mode='always'` stay awake even when rig/city is suspended.
**Root cause**: Only agent-level suspension checked, ignoring rig and city hierarchy.
**Fix**: Use `isAgentEffectivelySuspended()` for full hierarchy check.

- **Ref**: `794e3e7` (gascity)

### scale_check Concurrency Overload

**Symptom**: Pool workers stuck in `creating` state. scale_check times out (30s).
**Root cause**: One goroutine per agent (40+ concurrent `bd` calls) against shared dolt server caused contention.
**Fix**: Bound scale_check concurrency with semaphore. Raise timeout to 180s. Add creating-to-active transition.

- **Ref**: `561881c` (gascity)

### Idle Recovery Bypassing Sleep Timers

**Symptom**: Sessions drained after 2 minutes regardless of configured `sleep_after_idle`/`idle_timeout`.
**Root cause**: `idle_recovery.go` bypassed existing timers and `ComputeAwakeSet`.
**Fix**: Removed idle recovery from reconciler tick entirely.

- **Ref**: `25faa72` (gascity)

---

## Config Hot-Reload Issues

### Reload Race Conditions

**Symptom**: Config updates partially applied, causing split-brain state.
**Fix**: Parse → validate → swap atomically. On validation failure, preserve old config.

- **Ref**: `f4f30cd` (gascity)

### Provider Not Swapped

**Symptom**: Old provider persists after config change. Agents use stale provider settings.
**Root cause**: Old provider captured in closure at setup time.
**Fix**: Pass session provider to `buildFn` on each tick. Hot-reload triggers graceful stop → new provider → restart.

- **Refs**: `57b3389`, `0d0adf3` (gascity)

### Store Not Refreshed

**Symptom**: Auto-suspend reads stale city bead store after config reload.
**Fix**: Refresh standalone city store on config reload.

- **Ref**: `e656d27` (gascity)

### Typo Detection

Config now uses Levenshtein distance to suggest corrections for unknown fields ("did you mean?").

- **Ref**: `cfaf959` (gascity)

---

## Formula & Order Issues

### Formula Search Path Mismatches

**Symptom**: Formulas not found despite existing in the expected directory.
**Root cause**: Formula resolution didn't account for worktree isolation mode or cross-rig contexts.
**Fix**: 4-layer resolution (system → topology → rig → local) with worktree awareness.

- **Refs**: `9b4896f` (beads), `9af14fa` (gastown), `d3f8d0d` (gascity)

### Graph Workflow Routing Missing

**Symptom**: Order-dispatched graph.v2 formula step beads have no `gc.routed_to` metadata. Workers can't find the work.
**Fix**: Order dispatch now applies graph workflow routing to step beads.

- **Ref**: `909d2e3` (gascity)

### Orders Fire During Suspension

**Symptom**: Orders still dispatched during `gc suspend`.
**Fix**: Gate order dispatch on city-level suspend state.

- **Ref**: `c190eaf` (gascity)

---

## Provider Issues

### Env Vars Not Propagated

**Symptom**: Agents don't get provider-specific env vars (e.g., `OPENCODE_PERMISSION`).
**Fix**: Provider presets with Kubernetes-inspired resolution chain.

- **Ref**: `fd64ac1` (gascity)

### Non-Claude Resume Silently Fails

**Symptom**: "Resume" for non-Claude providers silently starts a fresh session.
**Root cause**: `ResumeFlag`/`SessionIDFlag` missing for non-Claude providers.
**Status**: Documented as show-stopper in parity audit. Requires per-provider resume support.

- **Ref**: `b722e81` (gascity)

---

## Bead Prefix Collisions

### Symptom

Two rigs silently share the same prefix, causing routing confusion.

### Fix

```bash
# Check for collisions
gc doctor

# Explicitly set prefix in city.toml
[[rigs]]
name = "my-rig"
prefix = "mr"  # Override auto-derived prefix
```

Prefix collision checking now runs on `gc rig add`. When re-adding a rig, existing prefix is preserved.

- **Refs**: `ed19b7e` (gastown), `9b422c6` (gascity)

---

## Work Routing Failures

### Cross-Rig Bead Routing Broken

**Symptom**: `bd` commands don't find beads in other rig databases.
**Root cause**: `BEADS_DIR` not set correctly for rig-scoped agents.
**Fix**: Set `BEADS_DIR` explicitly for all rig-scoped contexts. Stamp `gc.routed_to` on all dispatched work.

- **Refs**: `b9a823b` (gastown), `d43ff5c`, `65ef432` (gascity)

### Inherited BEADS_DIR Causes Prefix Mismatch

**Symptom**: Prefix mismatch errors when creating agent beads for rigs.
**Root cause**: BEADS_DIR inherited from parent session (e.g., mayor at `/home/erik/gt/.beads`) used when creating rig beads.
**Fix**: Always explicitly set BEADS_DIR based on working directory.

- **Ref**: `598a39e` (gastown)
