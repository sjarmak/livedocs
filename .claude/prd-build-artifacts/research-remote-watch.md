# Research: remote-watch

## Current Watch Architecture

- `watch_cmd.go` has `runWatchMulti()` which creates one `Watcher` per `RepoEntry` with local git paths
- `Watcher.Run()` polls via `GitOps.RevParseHEAD()`, triggers `PipelineRunner.Run()` on HEAD change
- `GitOps` interface has `LocalGitOps` (local git) and `RemoteGitOps` (Sourcegraph MCP `commit_search`)
- `Config` already has `Git GitOps` field -- nil defaults to `LocalGitOps{}`
- `State` persists last-indexed SHA per repo name to JSON file
- Pipeline `Config.FileSource` enables remote extraction without local clone

## Key Components for Remote Mode

1. **RemoteGitOps** (watch/gitops.go) - already exists, uses MCPCaller for commit_search
2. **SourcegraphFileSource** (pipeline/filesource_sourcegraph.go) - already exists, provides ReadFile/ListFiles/DiffBetween via MCP
3. **SourcegraphClient** (sourcegraph/client.go) - creates MCP subprocess, requires SRC_ACCESS_TOKEN
4. **sgToolLister** (cmd/livedocs/extract_cmd.go) - hardcoded tool list for NewSourcegraphFileSource
5. **buildRemoteRegistry** (cmd/livedocs/extraction_runner.go) - tree-sitter only registry for remote

## Repo Discovery

- Sourcegraph MCP `list_repos` tool can discover repos matching a pattern
- Need to call `sgClient.CallTool(ctx, "list_repos", {"query": pattern})` and parse response
- Response is newline-separated repo names

## Design Decisions

- Add `--source sourcegraph` flag to watch command (parallel to extract command pattern)
- Add `--repos` flag for repo pattern (e.g., "kubernetes/*")
- Default interval for remote: 5m (not 5s like local -- remote polling is expensive)
- Reuse existing Watcher.Run() loop with RemoteGitOps + SourcegraphFileSource pipeline
- Each remote repo gets its own Watcher with shared state
- RepoDir field used as repo identifier (e.g., "github.com/org/repo") for remote repos
