# MCP Server Submission: livedocs

**Server Name:** livedocs
**GitHub URL:** https://github.com/sjarmak/livedocs
**Category:** Developer Tools
**License:** Apache 2.0

## Description

Livedocs extracts structural claims (exports, types, signatures, dependencies, interface implementations) from source code into per-repo SQLite databases and serves them to AI coding agents via MCP. It achieves ~50x context reduction compared to raw source reads, making codebase onboarding fast and cheap.

It also detects documentation drift — finding stale references in README files and AI context files (CLAUDE.md, .cursorrules) that no longer match the actual code.

## MCP Tools

### Single-Repo Mode

| Tool | Description |
|------|-------------|
| `query_claims` | Search documentation claims by symbol name (supports LIKE wildcards) |
| `check_drift` | Detect stale symbol references in documentation files |
| `verify_section` | Check if claims for a file/line range are still valid |
| `check_ai_context` | Verify AI context files (CLAUDE.md, .cursorrules) for broken references |

### Multi-Repo Mode

| Tool | Description |
|------|-------------|
| `list_repos` | List all repositories with symbol/claim counts |
| `list_packages` | List import paths for a repo with prefix filter |
| `describe_package` | Render Markdown docs for a package |
| `search_symbols` | Cross-repo symbol search with routing index |

## Installation

```bash
go install github.com/sjarmak/livedocs/cmd/livedocs@latest
cd /path/to/your/repo
livedocs init && livedocs extract
```

### Claude Code
```bash
claude mcp add livedocs -- livedocs mcp
```

### Cursor (.cursor/mcp.json)
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

### Windsurf
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

## Supported Languages

Go, Python, TypeScript, Shell (via tree-sitter)

## Compatibility

Claude Code, Claude Desktop, Cursor, Windsurf, any MCP-compatible client (stdio and HTTP/SSE transports)
