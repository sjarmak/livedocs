# Gas City Primitives

Gas City's power comes from a small set of composable primitives. None of them encode specific roles or workflows — that is left to configuration.

## Agents

An **agent** is a configured AI coding assistant process that runs in a session. Each agent is defined by an `[[agent]]` block in `city.toml`.

### Definition

```toml
[[agent]]
name = "worker"
provider = "claude"
description = "General-purpose coding agent"
prompt = "prompts/worker.md"
max_active_sessions = 5
min_active_sessions = 0
idle_timeout = "10m"
wake_mode = "resume"  # or "fresh"
```

Key fields:

| Field                 | Description                                                              |
| --------------------- | ------------------------------------------------------------------------ |
| `name`                | Agent identifier (becomes part of qualified name)                        |
| `provider`            | Which AI CLI to use (e.g., `claude`, `codex`, `gemini`)                  |
| `prompt`              | Go template file defining agent behavior                                 |
| `max_active_sessions` | Upper bound on concurrent sessions                                       |
| `min_active_sessions` | Minimum sessions kept alive even when idle                               |
| `idle_timeout`        | How long an idle session lives before being slept                        |
| `wake_mode`           | `"resume"` reuses session key; `"fresh"` creates a new session each wake |
| `work_query`          | Custom bead query for finding work                                       |
| `sling_query`         | Custom routing command for dispatching work                              |
| `depends_on`          | Other agents that must be running first                                  |

### Scaling

Agents scale via `min_active_sessions` and `max_active_sessions`:

| Configuration         | Behavior                                                 |
| --------------------- | -------------------------------------------------------- |
| `max = 1`             | Singleton (e.g., "mayor")                                |
| `max = 5`             | Bounded pool — creates "worker-1" through "worker-5"     |
| `max = -1` or omitted | Unlimited pool — discovers running instances dynamically |
| `min > 0`             | Keep minimum sessions alive even with no pending work    |

Inheritance hierarchy: **agent** → **rig** → **workspace** → **unlimited**

### Identity

- **City-scoped agent**: identity is just the agent `name`
- **Rig-scoped agent**: identity is `<rig-name>/<agent-name>` (e.g., `my-frontend/worker`)

## Named Sessions

A **named session** reserves a canonical identity backed by an agent template:

```toml
[[named_session]]
template = "mayor"
mode = "always"       # or "on_demand"
scope = "city"        # or "rig"
```

| Mode        | Behavior                                                              |
| ----------- | --------------------------------------------------------------------- |
| `always`    | Controller keeps the session alive continuously — restarts if it dies |
| `on_demand` | Materialized only when work exists or explicitly referenced           |

The controller creates a persistent session bead labeled with `configured_named_session = "true"` and `configured_named_identity = "<qualified-identity>"`.

## Providers

A **provider** defines how to launch an AI coding assistant CLI. Gas City ships with built-in definitions for common tools.

### Built-in Providers

| Provider   | Command    | Notes          |
| ---------- | ---------- | -------------- |
| `claude`   | `claude`   | Claude Code    |
| `codex`    | `codex`    | ChatGPT CLI    |
| `gemini`   | `gemini`   | Gemini CLI     |
| `cursor`   | `cursor`   | Cursor         |
| `copilot`  | `copilot`  | GitHub Copilot |
| `amp`      | `amp`      | Amp            |
| `opencode` | `opencode` | OpenCode       |
| `auggie`   | `auggie`   | Auggie         |
| `pi`       | `pi`       | Pi             |
| `omp`      | `omp`      | OMP            |

### Custom Provider Configuration

```toml
[providers.my-claude]
command = "claude"
args = ["--model", "sonnet"]
option_defaults = { permission_mode = "unrestricted", model = "sonnet" }
supports_acp = true
resume_flag = "--resume"
session_id_flag = "--session-id"
```

Configurable properties:

- Command and arguments
- Default model and permission mode
- Session resume behavior
- Hook and ACP support flags

### Account Switching

Multi-account management and quota rotation are **not yet implemented** in Gas City (marked as TODO). Each provider currently uses whatever account is configured in the underlying CLI's auth system.

## Rigs

A **rig** is an external project directory registered with the city.

```toml
[[rigs]]
name = "my-frontend"
path = "/Users/me/projects/my-frontend"
prefix = "fe"              # Override auto-derived bead prefix
suspended = false
formulas_dir = "local-formulas"
includes = ["packs/my-pack"]
max_active_sessions = 10   # Rig-level session cap
```

Each rig gets:

- **Its own bead prefix**: auto-derived from the name (`my-frontend` → `mf`) or explicitly set
- **Rig-scoped agents**: e.g., `my-frontend/polecat`
- **Independent formula/pack overrides**: rig-local formulas shadow city-level ones
- **Per-rig Dolt config**: optional `dolt_host` and `dolt_port` overrides

### Bead Prefix Derivation

The auto-derivation algorithm splits the rig name on `-` or `_` and takes the first letter of each part:

| Rig Name        | Derived Prefix |
| --------------- | -------------- |
| `my-frontend`   | `mf`           |
| `api-server`    | `as`           |
| `data_pipeline` | `dp`           |

Override with the `prefix` field if the auto-derived value collides.

## Formulas

A **formula** is a reusable workflow definition in `*.formula.toml`. Formulas define multi-step work (molecules) with beads, steps, and dependencies.

### Formula Layers (Priority Order)

Formulas are resolved from multiple layers, with later layers shadowing earlier ones by filename:

1. City pack formulas (from `workspace.includes`)
2. City local formulas (`formulas/` directory or `[formulas].dir`)
3. Rig pack formulas
4. Rig local formulas (`[[rigs]].formulas_dir`)

### Instantiation

Formulas are instantiated as:

- **Molecules**: persistent formula instances tracked as beads
- **Wisps**: ephemeral formula instances with TTL-based garbage collection

```bash
# Create a molecule from a formula
gc formula cook my-formula "Title for the work"
```

There are no hardcoded formulas — all formulas are config-driven. System formulas live in the `cmd/gc/system_formulas/` directory.

## Orders

An **order** pairs a **gate** (trigger condition) with an **action** (shell script or formula). Orders live in `formulas/orders/<name>/order.toml`.

### Gate Types

```toml
[order]
gate = "cooldown"           # Minimum interval between firings
interval = "5m"

# gate = "cron"             # Cron schedule
# schedule = "0 3 * * *"

# gate = "condition"        # Shell command exit code
# check = "test -f /tmp/flag"

# gate = "event"            # React to system events
# on = "bead.closed"

# gate = "manual"           # Only fires when explicitly triggered
```

### Action Types (Mutually Exclusive)

```toml
# Formula action: create a wisp and dispatch to a pool
formula = "mol-db-health"
pool = "worker"
timeout = "90s"

# OR exec action: run a shell script directly
# exec = "scripts/check.sh"
```

### Order Lifecycle

1. Gate is evaluated on each controller tick
2. If gate condition is met, a tracking bead is created synchronously (prevents cooldown re-fire)
3. Action is dispatched as a fire-and-forget goroutine
4. No automatic retry on failure

## Packs

A **pack** is a reusable bundle of agent configurations, formulas, and orders that can be included in a city or rig:

```toml
[workspace]
includes = ["packs/gastown"]

# Or per-rig:
[[rigs]]
name = "my-rig"
includes = ["packs/my-pack"]
```

Packs enable sharing and composing agent topologies across projects.
