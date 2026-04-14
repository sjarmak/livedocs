# Livedocs Setup Guide

## Prerequisites

Install the `livedocs` binary:

```bash
go install github.com/live-docs/live_docs/cmd/livedocs@latest
```

Or build from source (requires CGO for tree-sitter):

```bash
git clone https://github.com/live-docs/live_docs.git
cd live_docs
make build
# Binary at ./livedocs — move to your PATH
```

## 1. Local Repository Setup

Extract claims from a repo on your machine:

```bash
cd /path/to/your/repo
livedocs init
livedocs extract
```

This creates `.livedocs/claims.db` with all extracted symbols and claims.

### Include tribal knowledge

Extract ownership, rationale, invariants, and quirks from git history:

```bash
livedocs extract --tribal
```

This adds provenance-tracked facts from CODEOWNERS, git blame, commit messages, and inline markers (TODO/HACK/NOTE).

## 2. Connect to Your AI Assistant

### Claude Code

Single-repo mode:

```bash
claude mcp add livedocs -- livedocs mcp
```

Multi-repo mode (serve claims from multiple repos):

```bash
claude mcp add livedocs -- livedocs mcp --data-dir /path/to/claims/
```

### Cursor

Add to `.cursor/mcp.json` in your project root:

```json
{
  "mcpServers": {
    "livedocs": {
      "command": "livedocs",
      "args": ["mcp"]
    }
  }
}
```

For multi-repo mode:

```json
{
  "mcpServers": {
    "livedocs": {
      "command": "livedocs",
      "args": ["mcp", "--data-dir", "/path/to/claims/"]
    }
  }
}
```

### Windsurf

Add to your Windsurf MCP configuration (`~/.windsurf/mcp.json` or project-level):

```json
{
  "mcpServers": {
    "livedocs": {
      "command": "livedocs",
      "args": ["mcp"]
    }
  }
}
```

## 3. Sourcegraph Integration

Livedocs uses the [Sourcegraph MCP server](https://www.npmjs.com/package/@sourcegraph/mcp) to extract claims from remote repos, search code, and enrich claims with semantic context — all without cloning.

### Get a Sourcegraph token

1. Go to your Sourcegraph instance (e.g. `sourcegraph.com`) > Settings > Access Tokens
2. Create a token with `read` scope
3. Add it to your environment:

```bash
# Add to ~/.env or ~/.bashrc
export SOURCEGRAPH_ACCESS_TOKEN=sgp_...

# Livedocs reads SRC_ACCESS_TOKEN — you can alias it:
export SRC_ACCESS_TOKEN="$SOURCEGRAPH_ACCESS_TOKEN"
```

The Sourcegraph MCP server (`npx @sourcegraph/mcp@0.3`) is spawned automatically when needed. Requires `npx` (Node.js) on PATH.

### Extract claims from a remote repo

No cloning needed. Livedocs reads files via Sourcegraph's `read_file` and `list_files` MCP tools:

```bash
# Full extraction (estimates cost first, requires --confirm)
livedocs extract \
  --source sourcegraph \
  --repo github.com/kubernetes/client-go \
  -o client-go.claims.db \
  --confirm

# Incremental extraction (between two revisions)
livedocs extract \
  --source sourcegraph \
  --repo github.com/kubernetes/client-go \
  --from-rev abc123 \
  --to-rev def456 \
  -o client-go.claims.db
```

### Watch remote repos for changes

Poll Sourcegraph for new commits and incrementally extract:

```bash
# Watch all repos matching a pattern
livedocs watch \
  --source sourcegraph \
  --repos 'kubernetes/*' \
  --data-dir ./claims/ \
  --interval 5m

# Watch with semantic enrichment after each extraction
livedocs watch \
  --source sourcegraph \
  --repos 'kubernetes/*' \
  --data-dir ./claims/ \
  --enrich
```

### Schedule periodic extractions

Run extractions on a cron schedule (useful for background maintenance):

```bash
# Create a schedule config
cat > schedule.json << 'EOF'
[
  {
    "repo": "github.com/org/repo-a",
    "cron": "0 */6 * * *",
    "source": "sourcegraph",
    "data_dir": "./claims",
    "concurrency": 10
  },
  {
    "repo": "github.com/org/repo-b",
    "cron": "0 */6 * * *",
    "source": "sourcegraph",
    "data_dir": "./claims",
    "concurrency": 10
  }
]
EOF

# Start the scheduler
livedocs extract-schedule --config schedule.json

# Preview the schedule without running
livedocs extract-schedule --config schedule.json --dry-run
```

### Enrich claims with semantic context

After extraction, enrich claims with higher-level properties using Sourcegraph's code intelligence:

```bash
# Enrich all claims in a data directory
livedocs enrich --data-dir ./claims/ --repo org/repo

# Initial enrichment (processes all symbols, not just changed ones)
livedocs enrich --data-dir ./claims/ --repo org/repo --initial
```

Enrichment adds Tier 2 semantic claims: purpose, usage patterns, complexity, and stability assessments.

## 4. Documentation Drift Detection

### Symbol-level drift (local)

Compare symbols referenced in docs against code exports:

```bash
# Check all markdown files in the repo
livedocs check

# JSON output (CI-friendly)
livedocs check --format json

# Fast manifest-based check (no SQLite, good for git hooks)
livedocs check --manifest
```

### Cross-repo semantic drift

Validate documentation against code in other repositories. Useful for architectural docs that describe code living in multiple repos:

```bash
livedocs check \
  --cross-repo \
  --doc-map doc-map.yaml \
  --docs-dir ./docs/
```

The doc-map file maps source patterns in remote repos to documentation files:

```yaml
repos:
  - name: github.com/org/backend
    short: backend
    mappings:
      - source: "internal/api/**/*.go"
        docs:
          - docs/api-reference.md
      - source: "internal/auth/**/*.go"
        docs:
          - docs/authentication.md
          - docs/troubleshooting.md

  - name: github.com/org/frontend
    short: frontend
    mappings:
      - source: "src/components/**/*.tsx"
        docs:
          - docs/ui-components.md
```

The checker:

1. Extracts key terms from each doc section
2. Searches mapped repos via Sourcegraph for relevant code
3. Sends section + code context to an LLM for verification
4. Reports specific stale claims with evidence and severity

LLM priority: `claude` CLI (OAuth) > `ANTHROPIC_API_KEY` > Sourcegraph deepsearch.

### Generate or update the manifest

Auto-discover source-to-doc mappings for local repos:

```bash
livedocs check --update-manifest
```

### PR impact analysis

Check which docs are affected by a PR's changes:

```bash
livedocs prbot --diff <diff-file>
```

## 5. Continuous Documentation Maintenance

### Git hook (post-commit)

Run a fast manifest-based check after every commit:

```bash
# .git/hooks/post-commit
#!/bin/sh
livedocs check --manifest
```

### CI pipeline (GitHub Actions)

```yaml
name: Documentation Drift
on: [push, pull_request]

jobs:
  drift:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: live-docs/live_docs@v1
        with:
          fail-threshold: 0
```

### Automated doc updates (cron + Sourcegraph)

For documentation that tracks external repos, set up fully automated monitoring. See [gascity_livedocs/](gascity_livedocs/) for a working example that:

1. Polls 4 GitHub repos every 15 minutes via `gh api`
2. Identifies which docs may need updating via a doc-map
3. Runs cross-repo semantic drift detection every 6 hours
4. Auto-fixes stale sections using Claude and pushes a PR

## 6. Codebase Exploration with Sourcegraph MCP

Beyond documentation maintenance, livedocs + Sourcegraph MCP enables powerful codebase exploration workflows.

### Build a claims corpus for an organization

```bash
export SRC_ACCESS_TOKEN=sgp_...

# Extract claims from all repos in an org
for repo in $(gh repo list myorg --json nameWithOwner --jq '.[].nameWithOwner'); do
  echo "Extracting $repo..."
  livedocs extract \
    --source sourcegraph \
    --repo "github.com/$repo" \
    --data-dir ./claims/ \
    --concurrency 10 \
    --confirm
done

# Serve the corpus via MCP
claude mcp add livedocs -- livedocs mcp --data-dir ./claims/
```

Then ask your AI assistant:

- "List all repos" — see the full corpus
- "Search for AuthMiddleware across all repos" — cross-repo symbol search
- "Describe the auth package in the backend repo" — rendered docs with interfaces, deps, types
- "Who owns the payment module?" — tribal knowledge from CODEOWNERS and git blame
- "Why does the retry loop use exponential backoff?" — rationale from commit messages

### Keep the corpus fresh

```bash
# Watch all org repos, enrich after each extraction
livedocs watch \
  --source sourcegraph \
  --repos 'myorg/*' \
  --data-dir ./claims/ \
  --enrich \
  --interval 5m
```

### Explore a new codebase

When onboarding to an unfamiliar repo:

```bash
# One-shot extraction
livedocs extract --source sourcegraph --repo github.com/some/project -o project.claims.db --confirm

# Connect and ask questions
claude mcp add project -- livedocs mcp --db project.claims.db
```

Then: "What are the main packages?", "Describe the core module", "What interfaces does the server implement?"

## Available MCP Tools

### Single-Repo Mode (`--db`)

| Tool               | Description                                                             |
| ------------------ | ----------------------------------------------------------------------- |
| `query_claims`     | Search documentation claims by symbol name (supports wildcards)         |
| `check_drift`      | Detect stale symbol references in README files                          |
| `verify_section`   | Check if claims for a file and line range are still valid               |
| `check_ai_context` | Verify AI context files (CLAUDE.md, .cursorrules) for broken references |

### Multi-Repo Mode (`--data-dir`)

| Tool                        | Description                                                           |
| --------------------------- | --------------------------------------------------------------------- |
| `list_repos`                | List all repositories with symbol and claim counts                    |
| `list_packages`             | List import paths for a repository, with optional prefix filter       |
| `describe_package`          | Render Markdown documentation for a package (interfaces, deps, types) |
| `search_symbols`            | Cross-repo symbol search with routing index                           |
| `tribal_context_for_symbol` | All tribal facts for a symbol with full provenance envelope           |
| `tribal_owners`             | Ownership facts (CODEOWNERS + git blame) for a symbol                 |
| `tribal_why_this_way`       | Rationale and invariant facts explaining why code exists              |

## Troubleshooting

**"No symbols found"** — Run `livedocs extract` first to populate the claims database.

**"open claims db" error** — Ensure `.livedocs/claims.db` exists in the working directory, or pass `--db` with the correct path.

**"data directory ... no such file"** — The `--data-dir` path must exist and contain `*.claims.db` files.

**"SRC_ACCESS_TOKEN is not set"** — Set `SRC_ACCESS_TOKEN` (or `SOURCEGRAPH_ACCESS_TOKEN` in `~/.env`) for Sourcegraph features.

**Sourcegraph MCP server not starting** — Requires `npx` on PATH (install Node.js). The server command is `npx -y @sourcegraph/mcp@0.3`.

**Server not responding** — Verify `livedocs mcp` runs without errors: `echo '{}' | livedocs mcp` should produce JSON-RPC output.

**Cross-repo check shows 0 sections** — Ensure `--docs-dir` points to the directory containing the doc files, and doc paths in `doc-map.yaml` match the relative paths.
