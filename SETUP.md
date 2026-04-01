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

One command:

```bash
claude mcp add livedocs -- livedocs mcp
```

Or with a custom database path:

```bash
claude mcp add livedocs -- livedocs mcp --db /path/to/claims.db
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

With a custom database path:

```json
{
  "mcpServers": {
    "livedocs": {
      "command": "livedocs",
      "args": ["mcp", "--db", "/path/to/claims.db"]
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

| Tool               | Description                                                             |
| ------------------ | ----------------------------------------------------------------------- |
| `query_claims`     | Search documentation claims by symbol name (supports wildcards)         |
| `check_drift`      | Detect stale symbol references in README files                          |
| `verify_section`   | Check if claims for a file and line range are still valid               |
| `check_ai_context` | Verify AI context files (CLAUDE.md, .cursorrules) for broken references |

## Usage Examples

After connecting, ask your AI assistant:

- "Query claims for the NewServer symbol"
- "Check drift on pkg/server/README.md"
- "Verify section server.go lines 40-80"
- "Check AI context for this repository"

## Troubleshooting

**"No symbols found"** - Run `livedocs extract` first to populate the claims database.

**"open claims db" error** - Ensure `.livedocs/claims.db` exists in the working directory, or pass `--db` with the correct path.

**Server not responding** - Verify `livedocs mcp` runs without errors: `echo '{}' | livedocs mcp` should produce JSON-RPC output.
