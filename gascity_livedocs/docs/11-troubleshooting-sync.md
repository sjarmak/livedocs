# Troubleshooting: Sync, Merges & Federation

Common issues with bead syncing, dolt push/pull, merge conflicts, federation, and bootstrap.

## Push/Pull Failures

### SSH Remotes Timing Out

**Symptom**: `bd dolt push` or `bd dolt pull` hangs and then times out for SSH remotes (`git+ssh://`, `ssh://`, `git@`).
**Root cause**: SQL path (`CALL DOLT_PUSH/PULL`) routes through the server's MySQL connection, which has a `readTimeout=10s` — far too short for SSH transfers.
**Fix**: beads now detects SSH remote URLs and shells out to CLI `dolt push`/`dolt pull` instead of using the SQL path.

- **Refs**: `2b354ff`, `c2e213c` (beads)

### git+https Remotes Also Timing Out

**Symptom**: Same timeout for `git+https://` remotes.
**Fix**: `isSSHRemote` generalized to `isGitProtocolRemote` to detect `git+https://`, `git+http://`, and `git://`.

- **Ref**: `bb6b712` (beads)

### Cloud Credentials Not Available

**Symptom**: Push/pull silently fails to authenticate with Azure, AWS, or GCS storage.
**Root cause**: `CALL DOLT_PUSH/PULL` runs inside the server process which only has env vars from startup. Credentials set later aren't available.
**Fix**: Detects cloud storage env var prefixes and routes through CLI subprocess that inherits current environment.

- **Ref**: `1c93599` (beads)

### "No Store Available" on bd dolt pull

**Symptom**: `bd dolt pull` fails with "no store available".
**Root cause**: Multiple issues with command nesting logic not properly falling through to store initialization for dolt subcommands.
**Fix**: Fixed command nesting; use positive allowlist (`needsStoreDoltSubcommands`) for push/pull/commit.

- **Refs**: `da508e8`, `3e04a92` (beads)

### CLI Operations From Wrong Directory

**Symptom**: CLI dolt operations fail to find remotes and database state.
**Root cause**: `cmd.Dir` set to server root (`.beads/dolt/`) instead of actual database directory (`.beads/dolt/{database}/`).
**Fix**: `cliDir()` helper returns correct path.

- **Ref**: `515a773` (beads)

### Remotes Lost After Server Restart

**Symptom**: `bd dolt push` fails with "remote origin not found" after server restart.
**Root cause**: `dolt_remotes` (in-memory SQL table) is empty after restart; CLI remotes persist in `.dolt/config` but SQL table doesn't.
**Fix**: `syncCLIRemotesToSQL` runs on every store `Open()` to reconcile.

- **Ref**: `2d3ce08` (beads)

### Diverged History Errors

**Symptom**: Push/pull fails with opaque "no common ancestor" error.
**Fix**: Now detects diverged history and prints actionable recovery options:

```bash
# Option 1: Re-bootstrap from remote (preserves remote history)
bd bootstrap

# Option 2: Force push local (overwrites remote)
bd dolt push --force

# Option 3: Re-init (last resort)
rm -rf .beads/dolt && bd init
```

- **Ref**: `64678bc`, `933253e` (beads)

---

## Merge Conflicts

### Auto-Push Metadata Causing Recurring Conflicts

**Symptom**: Merge conflicts on every pull in multi-machine setups.
**Root cause**: `dolt_auto_push_last` and `dolt_auto_push_commit` stored in the shared Dolt metadata table.
**Fix**: Auto-push tracking moved to `.beads/push-state.json` (gitignored, local-only). Migration auto-resolves existing metadata conflicts.

- **Refs**: `4591600`, `0ba22ae` (beads)

### DOLT_PULL Fails With "Autocommit Rollback"

**Symptom**: `DOLT_PULL` fails with "merge conflict detected, @autocommit transaction rolled back" even for clean merges.
**Root cause**: `DOLT_PULL` run without explicit transaction context.
**Fix**: Wrap in `BEGIN/COMMIT`. Later replaced `DOLT_PULL` with `DOLT_FETCH + DOLT_MERGE` to avoid nil-pointer bugs.

- **Refs**: `6615c71`, `d46a84b` (beads)

### issue_prefix Corruption From Concurrent Operations

**Symptom**: `issue_prefix` gets corrupted; "cannot merge with uncommitted changes" on every pull after fresh init.
**Root cause**: `DOLT_COMMIT('-Am')` stages ALL dirty tables, including stale config changes. And `bd init/restore/import` used `Commit()` which excludes the config table.
**Fix**: Selective staging that skips config table; `CommitWithConfig()` for intentional config changes.

- **Refs**: `bb45595`, `ebcb901` (beads)

### Deterministic Auto-Resolution Rules

For fields that do conflict, beads applies deterministic rules:

- **title/description**: latest `updated_at` wins
- **notes**: concatenated
- **priority**: higher priority (lower number) wins
- **issue_type**: local wins
- **status**: closed wins over open
- **Ref**: `7f13623` (beads)

### Polecat Close on Wrong Dolt Branch

**Symptom**: Bead permanently stuck as HOOKED with no recovery.
**Root cause**: `ForceCloseWithReason` writes `status=closed` while `BD_BRANCH` is still set, writing to the polecat's branch instead of main.
**Fix**: Defer close to after branch merge and `BD_BRANCH` unset.

- **Ref**: `9f258dd` (gastown)

---

## Bootstrap Failures

### Wrong Database Name

**Symptom**: `bd bootstrap` creates database with wrong name ("beads" default instead of configured name).
**Root cause**: Config falls back to `DefaultConfig()` when local `.beads/metadata.json` is missing.
**Fix**: Walk up parent directories to find workspace-level `metadata.json`.

- **Ref**: `eee8514` (beads)

### Missing metadata.json After Sync Clone

**Symptom**: `bd status`, `bd where`, `bd dolt push` all fail after syncing from remote.
**Root cause**: `executeSyncAction` created `.beads/` and cloned data but never wrote `metadata.json` or `config.yaml`.
**Fix**: `finalizeSyncedBootstrap` writes both files after clone.

- **Ref**: `4aa573d` (beads)

### Wrong Path in Shared-Server Mode

**Symptom**: `bd bootstrap` clones database to wrong location, invisible to the shared dolt server.
**Fix**: Use `cfg.IsDoltServerMode()` combined with `doltserver.IsSharedServerMode()` for correct path.

- **Ref**: `f3c1551` (beads)

### Stale Noms LOCK After Clone

**Symptom**: `dolt sql-server` fails to start after `bd bootstrap`.
**Fix**: `CleanStaleNomsLocks` now runs recursively after clone completes.

- **Ref**: `dbdc955` (beads)

---

## Federation Issues

### Credential Race Condition

**Symptom**: Concurrent federation goroutines inherit wrong credentials.
**Root cause**: CLI operations used process-wide `os.Setenv` for credentials.
**Fix**: Use `cmd.Env` (subprocess env) instead of process-wide env. Always hold mutex for all `withPeerCredentials` calls.

- **Refs**: `893d7fb`, `729001a` (beads)

### Predictable Encryption Key

**Symptom**: Federation credentials encrypted with key derivable from filesystem path (`SHA256(dbPath)`).
**Fix**: Random 32-byte key stored in `.beads-credential-key` (0600 permissions). Auto-migrate existing credentials.

- **Ref**: `f3f50d3` (beads)

### SSH Fallback Missing

**Symptom**: Federation operations always use SQL, timing out for SSH remotes.
**Fix**: Added `isPeerSSHRemote()` detection and CLI fallback.

- **Ref**: `57e88d0` (beads)

### Doctor False Positive With No Peers

**Symptom**: `bd doctor` reports remotesapi port 8080 unreachable when no federation peers configured.
**Fix**: Return OK if no remotes (excluding origin) exist.

- **Ref**: `89f91cb` (beads)

---

## Wisp Garbage Collection

### Wisps Accumulating Indefinitely

**Symptom**: Thousands of wisps (~8,500+) clogging the bead store.
**Root cause**: Only abandoned wisps were cleaned; closed wisps from completed work never deleted.
**Fix**: Two GC passes per cycle: `--closed --force` for completed, then `--age 1h --force` for abandoned.

```bash
# Manual cleanup
bd mol wisp gc --closed --force
bd mol wisp gc --age 1h --force
```

- **Ref**: `31d17c3` (gastown)

### Orphaned Step Children

**Symptom**: Parent wisps cleaned but dependent step children become permanent orphans.
**Root cause**: Wisp-to-wisp cascade deletion was broken.
**Fix**: `FindWispDependentsRecursive` with batched BFS traversal expands the abandoned set before deletion.

- **Ref**: `6d7eb25` (beads)

### Sling Wisps Never Auto-Closed

**Symptom**: Sling wisps (formula molecules) accumulate forever.
**Fix**: `gc wisp autoclose` added to the `bd on_close` hook.

- **Ref**: `a603627` (gascity)

### Agent Identity Beads Garbage Collected

**Symptom**: `gt doctor` reports missing agent beads repeatedly — they keep getting recreated and then GC'd.
**Root cause**: Agent beads created as ephemeral wisps.
**Fix**: Switch from `--ephemeral` to `--no-history` so agent beads survive wisp GC.

- **Ref**: `6f577b0` (gastown)

---

## Schema Migration Issues

### Server-to-Embedded Migration Data Loss

**Symptom**: Migration creates fresh empty database, silently losing all data.
**Root cause**: Recipe deleted `metadata.json` before exporting data.
**Fix**: Reorder: export first, then clear metadata, then init, then verify.

- **Ref**: `af12d6f` (beads)

### UUID Primary Key Collisions in Federation

**Symptom**: Duplicate PK collisions during dolt federation sync.
**Root cause**: `BIGINT AUTO_INCREMENT` primary keys on events/comments.
**Fix**: Replaced with `CHAR(36)` UUID primary keys.

- **Ref**: `1ce765e` (beads)

### Configuration Drift Detection

**Symptom**: Git hooks, remotes, or server state silently diverge from declared config.
**Fix**: Use `bd config drift` (read-only diagnostic) and `bd config apply` (idempotent fix).

- **Ref**: `cc1cb9e` (beads)
