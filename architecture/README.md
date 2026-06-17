# Architecture diagram (LikeC4)

Architecture-as-code model of `livedocs`, rendered with [LikeC4](https://likec4.dev).
The model is the source of truth across [`spec.c4`](spec.c4) (element kinds,
tags, deployment node kinds), [`model.c4`](model.c4) (the system), and
[`views.c4`](views.c4) (structure, walkthrough, and risk views), with the
deployment model in [`deployment.c4`](deployment.c4). The narrative companions
are the repo-root [`README.md`](../README.md) and [`AGENTS.md`](../AGENTS.md).

Every element `link`s to its source (`extractor/…`, `mcpserver/…`, `db/…`) and,
where one exists, to the PRD / premortem behind it ([`docs/design/`](../docs/design))
— so any box in the explorer is one click from the code and the rationale.

## Delivery state is tagged, not guessed

Every element carries a tag so **planned and research work renders distinctly
from what is already built** (legend in `spec.c4`). Verdicts are drawn from the
working tree — code path, tests, and design docs — never guessed:

| Tag | Meaning | Render |
|---|---|---|
| `#built` | code path exists and is exercised by tests | solid |
| `#evolving` | built, but the contract / corpus coverage is still moving | solid |
| `#planned` | designed (PRD / premortem); not yet implemented, or gated | **dashed, dimmed** |
| `#research` | speculative research track in the design docs | **dashed, indigo** |

Built core: CLI, extractor (deep-Go + tree-sitter for Go/Python/TypeScript/Bash),
tribal deterministic miners, pipeline + content-hash cache, SCIP symbol layer,
claims store, renderer, structural/cross-repo/tribal drift, MCP server (stdio +
HTTP/SSE), watcher, and the supporting utilities. Evolving: semantic (LLM)
claim generation + semantic drift (Kubernetes-corpus-validated only), the LLM
PR-comment tribal miner (opt-in, gated), Sourcegraph enrichment (mock-tested),
and evergreen documents (Phase-1, alert-first). Planned: per-symbol tribal
fingerprinting and evergreen auto-refresh + external Sourcegraph adapter.
Research: persistent tribal mining from Slack / Jira / incidents.

## Views

**Structure** — the static map:

| View | Scope |
|---|---|
| `index` | system landscape — `livedocs` in context of Sourcegraph, GitHub, the LLM, tree-sitter |
| `livedocsSystem` | the `livedocs` system decomposed into containers (built vs planned) |
| `extractorContainer` | deep-Go / tree-sitter / tribal-miner internals |
| `pipelineContainer` | file sources (local / Sourcegraph) + content-hash cache |
| `dbContainer` | claims schema, tribal facts/evidence, cross-repo xref |
| `mcpContainer` | transports, DB pool, routing index, claim + tribal tools, staleness |
| `driftContainer` | structural / cross-repo / semantic / tribal drift detection |
| `semanticContainer` | LLM claim generation, adversarial verification, LLM clients |
| `sgContainer` | Sourcegraph MCP client + enrichment |
| `evergreenContainer` | document detector, store, refresh executor, MCP tools/webhook |
| `supportContainer` | check / prbot / aicontext / gitdiff / audit / init / config |
| `planned` | planned + research work, with built dependencies dimmed |
| `deployment` | where each piece runs — CLI host, hosted MCP container, CI runner, remote substrates |

**Walkthrough flows** (dynamic / numbered-step views) — the narrative spine for
a design-review walkthrough:

| View | Flow |
|---|---|
| `extractFlow` | extracting a repo into claims (cache → extractors → store) |
| `mcpQueryFlow` | an agent querying claims over MCP (describe_package + tribal, ~50x context reduction) |
| `driftFlow` | drift detection in CI (fast manifest scan → structural → semantic) |
| `remoteFlow` | remote extraction + enrichment via Sourcegraph, no clone |

**Risk lens:**

| View | Scope |
|---|---|
| `risks` | the `#risk`-flagged elements with each open question stated in-box (file-anchored tribal liveness, LLM-miner PII/non-determinism, semantic drift not intent-aware, semantic claims k8s-only, mock-tested enrichment, MCP+watch WAL contention) |

### Running the walkthrough

For a design review, present in this order: `index` → `livedocsSystem` (orient on
structure) → the four walkthrough flows in sequence (what actually happens) →
`deployment` (where it runs) → `risks` (what to probe) → `planned` (what's next).
In `npx likec4 start`, the dynamic views animate step-by-step and each view's
notes panel carries the gotchas (CGO build requirement, the 5s staleness budget,
SRC_ACCESS_TOKEN for remote work, the `--tribal=llm` gate).

## Viewing & regenerating

```bash
# Interactive, hot-reloading explorer (recommended)
npx likec4 start architecture

# Re-export the static PNGs in exports/ (needs a one-time browser download:
#   npx playwright install chromium-headless-shell)
npx likec4 export png architecture -o architecture/exports

# Validate the model (strict — the source of truth for correctness)
npx likec4 validate architecture
```

### Viewing the interactive explorer over SSH (headless remote)

`likec4 start` serves a Vite dev server on `localhost:5173`. From a headless
remote, forward that port to your laptop and open it locally — three options,
easiest first:

1. **VS Code / Cursor Remote-SSH** — run `npx likec4 start architecture` in the
   integrated terminal; the editor auto-forwards 5173 and offers "Open in
   Browser". Nothing else to configure.
2. **SSH local port-forward** — on your laptop:
   ```bash
   ssh -N -L 5173:localhost:5173 user@remote   # leave running
   ```
   then on the remote `npx likec4 start architecture` and open
   <http://localhost:5173> locally. (Already in an SSH session? Add the tunnel
   without reconnecting: press `~C` then type `-L 5173:localhost:5173`.)
3. **Bind + reach directly** — `npx likec4 start architecture --listen 0.0.0.0`
   and browse to `http://<remote-ip>:5173` (only if that port is reachable /
   firewall-open; the tunnel in option 2 is safer).

No browser at all? Export the PNGs with `npx likec4 export png` (needs no
display) — `scp` them down, or view inline if your terminal supports images.
