# Port Coordination & Networking

Gas City coordinates multiple network services across the supervisor, per-city controllers, and Dolt database servers. Understanding port allocation is important for multi-city setups and debugging connectivity issues.

## Port Map

| Service           | Default Port                 | Configurable? | Config Location                                             | Protocol    |
| ----------------- | ---------------------------- | ------------- | ----------------------------------------------------------- | ----------- |
| Dolt sql-server   | Auto-allocated (10000-60000) | Yes           | `city.toml [dolt]`, `GC_DOLT_PORT` env, per-rig `dolt_port` | MySQL       |
| API/Dashboard     | **9443**                     | Yes           | `city.toml [api]`                                           | HTTP        |
| Supervisor API    | **8372**                     | Yes           | `~/.gc/supervisor.toml`                                     | HTTP        |
| Controller socket | `.gc/controller.sock`        | Fixed path    | N/A                                                         | Unix socket |
| Supervisor socket | `~/.gc/supervisor.sock`      | Fixed path    | N/A                                                         | Unix socket |

## Dolt Port Auto-Allocation

When no explicit port is configured, Gas City uses a deterministic algorithm to find a free port:

1. Check `GC_DOLT_PORT` environment variable (explicit override)
2. Check port file (`.beads/dolt-server.port`)
3. Check state file with live PID verification
4. **Hash the city path** to a value in the range 10000-60000
5. Probe with `lsof` until a free port is found

This hashing approach means the same city directory will consistently get the same port (unless it is already taken), reducing confusion across restarts.

### Dolt Configuration

```toml
# city.toml
[dolt]
port = 0             # 0 = auto-allocate (default)
host = "localhost"

# Per-rig override
[[rigs]]
name = "myrig"
dolt_host = "rig-host"
dolt_port = "3307"   # Override city-level Dolt config
```

## API/Dashboard Server

The API server provides HTTP access to city state, agent status, and bead queries.

```toml
# city.toml
[api]
port = 9443              # 0 = disabled
bind = "127.0.0.1"       # Bind address (localhost only by default)
allow_mutations = false   # Override read-only for non-localhost binds
```

Key behaviors:

- Started by `runController()` as part of the per-city controller
- Non-localhost binds are **read-only** unless `allow_mutations = true`
- When the supervisor is running, per-city `[api]` ports are **ignored** — the supervisor serves all cities on its single port

## Supervisor API

The supervisor is a machine-wide daemon that manages multiple cities.

```toml
# ~/.gc/supervisor.toml
[supervisor]
port = 8372
bind = "127.0.0.1"
patrol_interval = "10s"
allow_mutations = false
```

The supervisor API replaces per-city API servers when running — all queries go through port 8372.

## Control Sockets (Not TCP)

Gas City uses Unix domain sockets for local IPC, not TCP ports:

| Socket              | Path                                                             | Commands                 |
| ------------------- | ---------------------------------------------------------------- | ------------------------ |
| Per-city controller | `.gc/controller.sock`                                            | `stop`, `ping`, `reload` |
| Supervisor          | `~/.gc/supervisor.sock` or `$XDG_RUNTIME_DIR/gc/supervisor.sock` | `stop`, `ping`, `reload` |

These are not configurable — the paths are fixed conventions.

## Tmux Socket

Each city gets an isolated tmux server via a named socket:

```toml
# city.toml
[session]
socket = "my-custom-socket"  # Explicit socket name
```

If not set, defaults to `workspace.name`. All tmux commands for the city use `tmux -L <socket>`, which creates a separate tmux server instance. This prevents session name collisions between cities.

The socket lives at `/tmp/tmux-<uid>/<socket-name>`.

The `GC_TMUX_SOCKET` environment variable is passed to session setup scripts.

## Port Conflict Scenarios

### Multiple cities on one machine

Each city auto-allocates its own Dolt port via hashing. Conflicts are resolved by probing. The supervisor runs on a single fixed port (8372) and multiplexes.

### Dolt ghost servers

Orphaned `dolt sql-server` processes can hold ports after crashes. Symptoms:

- `bd dolt start` fails with "port already in use"
- `ss -tlnp | grep <port>` shows a process not associated with any running city

Fix: kill the orphaned process, remove stale `.beads/dolt-server.pid`.

### Supervisor port conflict

If another process holds port 8372:

```bash
# Check what holds the port
ss -tlnp | grep 8372

# Override in supervisor config
# ~/.gc/supervisor.toml
[supervisor]
port = 8373
```
