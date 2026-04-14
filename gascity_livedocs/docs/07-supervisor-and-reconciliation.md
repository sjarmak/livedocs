# Supervisor & Reconciliation

## Architecture Overview

Gas City uses a **hierarchical reconciliation model** where a machine-wide supervisor daemon manages multiple cities, and each city runs its own reconciliation loop.

```
┌─────────────────────────────────────────────────────────────┐
│ Supervisor Process (gc supervisor run)                       │
│ - Lock: ~/.gc/supervisor.lock (flock)                        │
│ - Socket: ~/.gc/supervisor.sock                              │
│ - API: http://127.0.0.1:8372 (configurable)                 │
│ - Registry: ~/.gc/cities.toml                                │
│ - Patrol interval: 10s (configurable)                        │
└──────────────────┬──────────────────────────────────────────┘
                   │
    ┌──────────────┼──────────────┬──────────────────┐
    ▼              ▼              ▼                   ▼
┌───────────┐ ┌───────────┐ ┌───────────┐    ┌───────────┐
│ CityRuntime│ │ CityRuntime│ │ CityRuntime│    │ CityRuntime│
│ city: /a   │ │ city: /b   │ │ city: /c   │    │ city: /n   │
│ patrol: 30s│ │ patrol: 30s│ │ patrol: 30s│    │ patrol: 30s│
└─────┬──────┘ └────────────┘ └────────────┘    └────────────┘
      │
      │ Per-city reconciliation:
      ├─ Session bead sync → agent reconciliation
      ├─ Pool death detection
      ├─ Config hot-reload (fsnotify on city.toml)
      ├─ Wisp GC (purge expired molecules)
      ├─ Order dispatch (cron/interval/event gates)
      ├─ Service tick
      └─ Chat auto-suspend
```

## Supervisor Layer

### What the Supervisor Does

The supervisor is a single, machine-wide daemon. Its responsibilities:

1. **Registry reconciliation** — compare `~/.gc/cities.toml` with running CityRuntimes
2. **Start new cities** when they appear in the registry
3. **Stop removed cities** when they disappear from the registry
4. **Panic recovery** — backoff and restart for failing cities
5. **Signal handling** — `SIGHUP` triggers immediate reconciliation; `SIGINT`/`SIGTERM` trigger graceful shutdown

### Supervisor Loop

The main loop runs on a configurable `patrol_interval` (default 10s):

1. Read registry (`~/.gc/cities.toml`)
2. Compare with running CityRuntimes
3. Start/stop CityRuntimes as needed
4. Handle signals and control socket commands

### Managing the Supervisor

```bash
# Start (typically via systemd)
systemctl --user start gascity-supervisor

# Stop
systemctl --user stop gascity-supervisor

# Restart
systemctl --user restart gascity-supervisor

# Check status
systemctl --user status gascity-supervisor

# Trigger immediate reconciliation
kill -HUP $(cat ~/.gc/supervisor.pid)
```

Note: `gc start` / `gc stop` / `gc restart` manage **city registration** with the supervisor, not the supervisor process itself. Use `systemctl` to manage the actual supervisor process.

## Per-City Reconciliation (CityRuntime)

Each registered city gets a `CityRuntime` that runs its own tick loop.

### Tick Interval

Default: 30s (configurable via `[daemon] patrol_interval` in `city.toml`)

The CityRuntime also has a **poke channel** for event-driven immediate reconciliation, and a **config watcher** using fsnotify on `city.toml` with debounced reload.

### Per-Tick Operations

Each tick performs these operations in order:

1. **Pool death detection** — check if any agent pools have died
2. **Config reload** — if `city.toml` changed, reload and re-evaluate
3. **Session bead sync** — the core reconciliation (see below)
4. **Wisp GC** — purge expired ephemeral molecules
5. **Order dispatch** — evaluate gate conditions and fire ready orders
6. **Service tick** — workspace service health checks
7. **Chat auto-suspend** — idle chat session management

## Session Reconciliation

The session reconciler is the heart of Gas City's lifecycle management. It uses the **bead store as the source of truth** for session state.

### Reconciliation Flow

```
1. Load session beads from bead store (labeled 'session')
       │
2. Build desired state from config:
   - Evaluate agent definitions
   - Pool scale_check (min/max sessions)
   - Work queries (are there beads waiting?)
       │
3. Compare desired state vs. running tmux sessions
       │
4. Execute lifecycle transitions:
   ┌──────────┬──────────┬──────────┬──────────┬──────────┐
   │  START   │   KILL   │  SLEEP   │   WAKE   │  DRAIN   │
   │          │          │          │          │          │
   │ Desired  │ Running  │ Idle     │ Work     │ Config   │
   │ but not  │ but not  │ timeout  │ assigned │ drift or │
   │ running  │ desired  │ exceeded │ to sleep │ graceful │
   │          │          │          │ session  │ shutdown │
   └──────────┴──────────┴──────────┴──────────┴──────────┘
```

### Lifecycle Transitions

| Transition | Trigger                                  | Action                                                               |
| ---------- | ---------------------------------------- | -------------------------------------------------------------------- |
| **Start**  | Session in desired state but not running | `Provider.Start()` — create tmux session                             |
| **Kill**   | Session running but not in desired state | `Provider.Stop()` — destroy tmux session                             |
| **Sleep**  | Idle timeout exceeded                    | Kill tmux session, keep bead (metadata: `sleep_reason=idle-timeout`) |
| **Wake**   | Work assigned to a sleeping session      | Restart session from existing bead                                   |
| **Drain**  | Config drift or graceful shutdown        | Send drain signal, wait for ack, then kill                           |

### Bead Metadata for Reconciliation

The reconciler stores metadata on session beads to track state:

| Metadata Key                | Purpose                                                                  |
| --------------------------- | ------------------------------------------------------------------------ |
| `config_hash`               | Detect configuration drift (if hash changes, drain and restart)          |
| `configured_named_session`  | Whether this is a named session                                          |
| `configured_named_identity` | Qualified identity for named sessions                                    |
| `configured_named_mode`     | `always` or `on_demand`                                                  |
| `sleep_reason`              | Why the session was put to sleep (`idle-timeout`, `suspended`, `killed`) |
| `gc.routed_to`              | Which agent work is routed to                                            |

## Tmux Session Management

The supervisor does **not** directly interact with tmux. Tmux management is delegated to each city's runtime provider.

### Provider Architecture

```
CityRuntime
  └─ runtime.Provider (interface)
       ├─ tmux.Provider (default)
       │    ├─ ListRunning()              → list tmux sessions
       │    ├─ IsRunning()                → check session liveness
       │    ├─ KillSessionWithProcesses() → kill + cleanup
       │    └─ tmux socket: -L <cityName> (isolated per city)
       │
       ├─ subprocess.Provider
       ├─ k8s.Provider
       ├─ acp.Provider
       └─ exec.Provider (script-backed)
```

### Tmux Socket Isolation

Each city gets its own tmux server via a named socket:

```bash
# City "ds-research" uses:
tmux -L ds-research list-sessions
tmux -L ds-research new-session -d -s worker "claude --resume ..."
```

This prevents session name collisions between cities. The socket name defaults to `workspace.name` and can be overridden via `[session] socket`.

### Session Naming

Default template: `{{.Agent}}` (just the sanitized agent name)

For pools: sessions are named `<agent>-1`, `<agent>-2`, etc.

### The Chicken-and-Egg Problem

The reconciler needs a running tmux server to create sessions, but it also **drains unrecognized tmux sessions as orphans**. If the tmux server dies (all managed sessions close), the server disappears and the reconciler can't create new sessions.

**Recovery sequence:**

1. Start a tmux server on the correct socket: `tmux -L <socket> new-session -d -s placeholder "sleep infinity"`
2. Restart the supervisor: `systemctl --user restart gascity-supervisor`
3. Wait ~30s for boot (scale checks run for all agents)
4. The reconciler drains the `placeholder` session as orphaned, but creates managed sessions first

### Symptoms of a Dead Tmux Socket

- `gc session new` times out with `tmux state cache: refresh failed ... no tmux server running`
- `gc status` shows agents as `stopped` with tmux errors
- Supervisor log shows `tmux state cache: refresh failed`
