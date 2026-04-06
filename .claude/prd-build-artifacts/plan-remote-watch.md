# Plan: remote-watch

## Files to Modify

1. **cmd/livedocs/watch_cmd.go** - Add --source, --repos, --concurrency flags; add `runWatchSourcegraph()` function
2. **watch/watcher.go** - No changes needed (already supports GitOps interface + PipelineRunner)
3. **watch/config.go** - No changes needed (RepoEntry already has what we need)

## Implementation Steps

### Step 1: Add flags to watch_cmd.go

- `--source` flag (string, default "local") - "local" or "sourcegraph"
- `--repos` flag (string) - repo pattern for Sourcegraph discovery (e.g., "org/*")
- `--concurrency` flag (int, default 10) - max concurrent MCP calls
- `--data-dir` flag (string) - output directory for .claims.db files

### Step 2: Add runWatchSourcegraph() function

Flow:
1. Validate SRC_ACCESS_TOKEN is set
2. Validate --repos is provided
3. Create SourcegraphClient
4. Discover repos via list_repos MCP tool
5. For each repo:
   a. Create SourcegraphFileSource
   b. Create RemoteGitOps with sgClient as MCPCaller
   c. Create Pipeline with FileSource + remote registry
   d. Create Watcher with RemoteGitOps + Pipeline
6. Launch all watchers with shared state
7. Handle graceful shutdown

### Step 3: Add repo discovery helper

- `discoverRepos(ctx, sgClient, pattern)` - calls list_repos, parses response
- Returns slice of repo identifiers

### Step 4: Route --source flag in RunE

- If source == "sourcegraph", call runWatchSourcegraph()
- Otherwise, existing runWatchMulti() path

### Step 5: Tests

- Test repo discovery parsing
- Test that remote watchers are configured with RemoteGitOps
- Test SRC_ACCESS_TOKEN validation
- Test flag validation
