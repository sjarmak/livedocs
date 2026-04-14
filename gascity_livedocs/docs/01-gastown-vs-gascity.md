# Gas Town vs Gas City

## Relationship: Evolution, Not a Rename

Gas City is an **SDK extracted from Gas Town**. They relate as product to underlying infrastructure:

- **Gas Town** is a complete, opinionated multi-agent workspace manager — a product ready to use
- **Gas City** is the configurable orchestration toolkit extracted from Gas Town's internals

Gas Town's entire topology can be recreated as a Gas City "pack" (see `examples/gastown/`), but Gas City also supports building completely different multi-agent systems from the same primitives.

## History

| Project  | CLI  | Created  | Stars   | Role                                     |
| -------- | ---- | -------- | ------- | ---------------------------------------- |
| Gas Town | `gt` | Earlier  | ~14,000 | Production multi-agent workspace manager |
| Gas City | `gc` | Feb 2026 | ~210    | Orchestration-builder SDK                |

Gas City's README states:

> "Gas City is an orchestration-builder SDK for multi-agent systems. It extracts the reusable infrastructure from Gas Town into a configurable toolkit with runtime providers, work routing, formulas, orders, health patrol, and a declarative city configuration."

## Conceptual Differences

| Dimension         | Gas Town                                                            | Gas City                                               |
| ----------------- | ------------------------------------------------------------------- | ------------------------------------------------------ |
| **CLI**           | `gt`                                                                | `gc`                                                   |
| **Roles**         | Hardcoded in Go (Mayor, Deacon, Witness, Polecats, Refinery, Dogs)  | Config-driven `[[agent]]` blocks — no role names in Go |
| **Identity**      | Path-derived (your current directory implies your role)             | Explicit in `city.toml`                                |
| **Plugins**       | `gt plugin`                                                         | `gc order` (gates + actions)                           |
| **Layout**        | Rigid `~/gt/` directory structure with role-specific subdirectories | Flexible; directories are implementation details       |
| **Configuration** | Spread across role-specific directories and managers                | Declarative `city.toml` with pack composition          |
| **Philosophy**    | Product                                                             | SDK / toolkit                                          |

## Gas Town's Built-In Roles

Gas Town ships with a fixed role taxonomy:

| Role         | Symbol       | Purpose                                |
| ------------ | ------------ | -------------------------------------- |
| Mayor        | Crown        | Primary AI coordinator                 |
| Town         | Houses       | Workspace directory (e.g., `~/gt/`)    |
| Rigs         | Crane        | Project containers                     |
| Crew Members | Person       | Personal workspaces                    |
| Polecats     | Skunk        | Worker agents with persistent identity |
| Hooks        | Hook         | Git worktree-based persistent storage  |
| Convoys      | Truck        | Work tracking units                    |
| Beads        | Prayer beads | Git-backed issue tracking              |
| Molecules    | DNA          | Workflow templates                     |
| Witness      | —            | Per-rig lifecycle manager              |
| Deacon       | —            | Background supervisor                  |
| Dogs         | Dog          | Infrastructure workers                 |
| Refinery     | Factory      | Per-rig merge queue processor          |
| Wasteland    | Desert       | Federated work coordination network    |

## Gas City's Equivalent Primitives

Gas City replaces those hardcoded roles with generic, composable primitives:

| Primitive            | Description                                                     |
| -------------------- | --------------------------------------------------------------- |
| **Agents**           | Generic session workers (no hardcoded roles)                    |
| **Beads**            | Universal work units (tasks, mail, molecules, convoys)          |
| **Sessions**         | Runtime provider abstraction (tmux, subprocess, k8s, ACP, exec) |
| **Providers**        | Pluggable backends for sessions, beads, mail, events            |
| **Config**           | Declarative `city.toml` with multi-layer composition            |
| **Events**           | Append-only pub/sub log                                         |
| **Prompt Templates** | Go templates defining agent behavior                            |
| **Orders**           | Formula/shell dispatch triggered by gates                       |
| **Formulas**         | Workflow definitions (`.formula.toml`)                          |
| **Molecules**        | Formula instances at runtime                                    |
| **Wisps**            | Ephemeral molecules with TTL-based garbage collection           |
| **Convoys**          | Bead-backed work grouping                                       |
| **Gates**            | Trigger conditions (cooldown, cron, condition, event, manual)   |
| **Rigs**             | External project directories with their own beads databases     |
| **Packs**            | Reusable agent configuration bundles                            |
| **Controller**       | Long-running daemon for reconciliation and dispatch             |
| **Pools**            | Elastic scaling for agents                                      |

## Command Mapping

| Gas Town             | Gas City     | Notes                 |
| -------------------- | ------------ | --------------------- |
| `gt install`         | `gc init`    | Initialize workspace  |
| `gt start` / `gt up` | `gc start`   | Start the city        |
| `gt sling`           | `gc sling`   | Direct mapping        |
| `gt plugin`          | `gc order`   | Plugins become orders |
| `gt formula`         | `gc formula` | Similar but refined   |

## How Gas City Recreates Gas Town

Gas City includes a Gastown pack that recreates the familiar topology as a configuration:

```toml
# In Gas City, you can use Gas Town's familiar roles
[[rigs]]
name = "myproject"
path = "/path/to/myproject"
includes = ["packs/gastown"]  # Activates Mayor, Deacon, Witness, etc.
```

This means:

- Gas Town roles become a **pack** in Gas City
- Users can start with the familiar Gas Town topology
- They can build completely custom topologies
- They can mix and match packs

## The SDK Extraction Philosophy

From Gas City's CLAUDE.md:

> "Gas City extracts that insight into an SDK where Gas Town becomes one configuration among many."

Key principles:

1. **Primitive-first**: Everything built from small composable pieces
2. **Configuration over code**: Roles defined in TOML, not Go
3. **Pluggable providers**: Session, beads, mail, events all swappable
4. **No hardcoded roles**: Mayor, Deacon are pack conventions, not SDK types
5. **The Bitter Lesson**: Primitives must become MORE useful as models improve
