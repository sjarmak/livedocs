# Customizable vs Fixed

This document catalogs what you can configure in Gas City vs what is hardcoded in the Go source.

## Fully Customizable (via `city.toml`)

### Workspace

```toml
[workspace]
name = "my-city"                  # City name (also default tmux socket)
provider = "claude"               # Default AI provider
max_active_sessions = 50          # Global session cap
includes = ["packs/gastown"]      # Pack composition
```

### Sessions

```toml
[session]
provider = "tmux"                 # Runtime: tmux, subprocess, acp, k8s, exec:<script>
socket = "my-socket"              # Tmux socket name (default: workspace.name)
startup_timeout = "60s"           # How long to wait for session ready
session_template = "{{.Agent}}"   # Session naming template
```

### Beads

```toml
[beads]
provider = "bd"                   # Backend: bd, file, exec:<script>
```

### Daemon

```toml
[daemon]
patrol_interval = "30s"           # Reconciliation tick interval
max_restarts = 5                  # Max restart attempts before backoff
formula_v2 = true                 # Enable graph workflows
```

### Dolt

```toml
[dolt]
port = 0                          # 0 = auto-allocate
host = "localhost"
```

### API

```toml
[api]
port = 9443                       # 0 = disabled
bind = "127.0.0.1"               # Bind address
allow_mutations = false           # Override read-only
```

### Formulas

```toml
[formulas]
dir = "formulas"                  # Formula directory
```

### Orders

```toml
[orders]
skip = ["noisy-order"]            # Orders to skip
max_timeout = "120s"              # Global timeout cap
```

### Agents

```toml
[[agent]]
name = "worker"                   # Agent name
provider = "claude"               # Override workspace provider
description = "..."               # Human description
prompt = "prompts/worker.md"      # Prompt template (Go template)
max_active_sessions = 5           # Upper session bound
min_active_sessions = 0           # Lower session bound
idle_timeout = "10m"              # Sleep after this idle time
wake_mode = "resume"              # "resume" or "fresh"
work_query = "..."                # Custom bead query
sling_query = "..."               # Custom routing command
depends_on = ["mayor"]            # Agent dependencies
env = { KEY = "value" }           # Environment variables
setup_commands = ["..."]          # Run before agent starts
```

### Named Sessions

```toml
[[named_session]]
template = "mayor"                # Agent template to use
mode = "always"                   # "always" or "on_demand"
scope = "city"                    # "city" or "rig"
```

### Rigs

```toml
[[rigs]]
name = "my-rig"                   # Rig name
path = "/path/to/rig"             # Project directory
prefix = "mr"                     # Override auto-derived bead prefix
suspended = false                 # Pause rig
formulas_dir = "local-formulas"   # Rig-local formulas
includes = ["packs/my-pack"]      # Rig-specific packs
max_active_sessions = 10          # Rig-level session cap
dolt_host = "custom-host"         # Per-rig Dolt override
dolt_port = "3307"                # Per-rig Dolt port override
```

### Providers

```toml
[providers.my-claude]
command = "claude"                # CLI command
args = ["--model", "sonnet"]      # CLI arguments
option_defaults = { ... }         # Default options
supports_acp = true               # ACP support flag
resume_flag = "--resume"          # Session resume flag
session_id_flag = "--session-id"  # Session ID flag
```

### Supervisor (separate file)

```toml
# ~/.gc/supervisor.toml
[supervisor]
port = 8372                       # API port
bind = "127.0.0.1"               # Bind address
patrol_interval = "10s"           # Registry check interval
allow_mutations = false           # API mutation control
```

## Fixed / Hardcoded (Cannot Override)

### Identity & Naming

| What                       | Fixed Value                                                                                      | Notes                            |
| -------------------------- | ------------------------------------------------------------------------------------------------ | -------------------------------- |
| Rig-scoped agent identity  | `<rig-name>/<agent-name>`                                                                        | Always qualified with rig prefix |
| City-scoped agent identity | `<agent-name>`                                                                                   | Plain name                       |
| Bead prefix derivation     | First letter of each hyphen/underscore-split part                                                | e.g., `my-frontend` → `mf`       |
| Session bead metadata keys | `configured_named_session`, `configured_named_identity`, `configured_named_mode`, `gc.routed_to` | Fixed key names in JSON metadata |

### Work Routing

| What                | Fixed Value                                                                                             |
| ------------------- | ------------------------------------------------------------------------------------------------------- |
| Default work query  | 3-tier priority: (1) in_progress + assigned to me, (2) ready + assigned to me, (3) ready + routed_to me |
| Default sling query | `bd update {} --set-metadata gc.routed_to=<qualified-name>`                                             |

### Providers

| What                          | Fixed Value                                                                                                           |
| ----------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| Built-in provider definitions | Command paths, process names, ready delays for claude, codex, gemini, cursor, copilot, amp, opencode, auggie, pi, omp |
| Provider detection order      | `claude`, `codex`, `gemini`, `cursor`, `copilot`, ...                                                                 |
| Hook support flags            | `SupportsHooks`, `SupportsACP`, `NeedsNudgePoller` per provider                                                       |
| Instructions file format      | CLAUDE.md for Claude, AGENTS.md for others                                                                            |

### Formulas & Orders

| What                         | Fixed Value                                             |
| ---------------------------- | ------------------------------------------------------- |
| Formula resolution algorithm | Last-wins by filename across layers                     |
| Order gate evaluation logic  | `CheckGate()` in `internal/orders/gates.go`             |
| Order scoped name format     | `<name>` (city) or `<name>:rig:<rigName>` (rig)         |
| Tracking bead creation       | Synchronous before dispatch (prevents cooldown re-fire) |
| Order dispatch model         | Fire-and-forget goroutines (no automatic retry)         |

### Infrastructure

| What                          | Fixed Value                                                      |
| ----------------------------- | ---------------------------------------------------------------- |
| Tmux socket isolation pattern | `tmux -L <socketName>`                                           |
| Controller socket path        | `.gc/controller.sock`                                            |
| Supervisor socket path        | `~/.gc/supervisor.sock` or `$XDG_RUNTIME_DIR/gc/supervisor.sock` |
| Supervisor lock file          | `~/.gc/supervisor.lock`                                          |
| Control-dispatcher agent      | Injected automatically when `formula_v2 = true`                  |

## Summary

| Category           | Customizable                              | Fixed                           |
| ------------------ | ----------------------------------------- | ------------------------------- |
| **Agent topology** | Fully (names, counts, providers, prompts) | Identity format only            |
| **Scaling**        | Fully (min/max, idle timeout, wake mode)  | Inheritance hierarchy           |
| **Providers**      | Fully (override built-ins, add custom)    | Built-in detection order        |
| **Rigs**           | Fully (paths, prefixes, packs, sessions)  | Prefix derivation algorithm     |
| **Formulas**       | Fully (layers, composition, shadowing)    | Resolution algorithm            |
| **Orders**         | Fully (gates, actions, timeouts)          | Gate evaluation, dispatch model |
| **Ports**          | Dolt (auto or explicit), API, supervisor  | Control socket paths            |
| **Tmux**           | Socket name, session template             | Isolation pattern               |
| **Work routing**   | Custom queries and sling commands         | Default query structure         |
