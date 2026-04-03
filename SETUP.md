# Livedocs MCP Server Setup

Connect livedocs to your AI assistant to query documentation claims, detect drift, and verify code sections.

## Prerequisites

Install the `livedocs` binary and ensure it is on your `PATH`:

```bash
go install github.com/live-docs/live_docs/cmd/livedocs@latest
```

Initialize the claims database in your repository:

```bash
cd /path/to/your/repo
livedocs init
livedocs extract
```

## Claude Code

Single-repo mode (one repository):

```bash
claude mcp add livedocs -- livedocs mcp
```

Multi-repo mode (corpus of repositories):

```bash
claude mcp add livedocs -- livedocs mcp --data-dir /path/to/claims/
```

## Cursor

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

## Windsurf

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

## Available Tools

### Single-Repo Mode (`--db`)

| Tool               | Description                                                             |
| ------------------ | ----------------------------------------------------------------------- |
| `query_claims`     | Search documentation claims by symbol name (supports wildcards)         |
| `check_drift`      | Detect stale symbol references in README files                          |
| `verify_section`   | Check if claims for a file and line range are still valid               |
| `check_ai_context` | Verify AI context files (CLAUDE.md, .cursorrules) for broken references |

### Multi-Repo Mode (`--data-dir`)

| Tool               | Description                                                           |
| ------------------ | --------------------------------------------------------------------- |
| `list_repos`       | List all repositories with symbol and claim counts                    |
| `list_packages`    | List import paths for a repository, with optional prefix filter       |
| `describe_package` | Render Markdown documentation for a package (interfaces, deps, types) |
| `search_symbols`   | Cross-repo symbol search with routing index                           |

## Static Context Generation

For tools without MCP support, generate static documentation files:

```bash
# Single package to stdout
livedocs context client-go tools/cache

# All packages in a repo (generates .livedocs/<repo>/<pkg>/CONTEXT.md)
livedocs context client-go
```

## Usage Examples

After connecting, ask your AI assistant:

- "List all available repos" (multi-repo)
- "Describe the tools/cache package in client-go" (multi-repo)
- "Search for NewInformer across all repos" (multi-repo)
- "Query claims for the NewServer symbol" (single-repo)
- "Check drift on pkg/server/README.md" (single-repo)
- "Verify section server.go lines 40-80" (single-repo)

## Troubleshooting

**"No symbols found"** - Run `livedocs extract` first to populate the claims database.

**"open claims db" error** - Ensure `.livedocs/claims.db` exists in the working directory, or pass `--db` with the correct path.

**"data directory ... no such file"** - The `--data-dir` path must exist and contain `*.claims.db` files.

**Server not responding** - Verify `livedocs mcp` runs without errors: `echo '{}' | livedocs mcp` should produce JSON-RPC output.
