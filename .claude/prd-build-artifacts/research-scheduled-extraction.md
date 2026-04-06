# Research: Scheduled Extraction

## Command Registration
- root.go uses `rootCmd.AddCommand(cmd)` in `init()`
- Commands are `*cobra.Command` vars declared at package level

## Extraction Modes
- `extract_cmd.go` supports local, clone, sourcegraph via `--source` flag
- `extraction_runner.go` has `extractionRunner` with `RunExtraction(ctx, repo, importPath)` method
- `extractionRunner` needs an `sgClient pipeline.MCPCaller`, `dataDir`, `concurrency`
- For clone mode, extraction shells out to `git clone --depth=1` then runs local pipeline

## Signal Handling (watch_cmd.go)
- Uses `context.WithCancel` + manual `signal.Notify(sigCh, SIGINT, SIGTERM)`
- Goroutine reads signal channel, calls cancel()
- Main loop blocks on `wg.Wait()`

## Cron Library
- No cron library in go.mod
- Will implement minimal 5-field cron parser (minute hour dom month dow)
- Supports: exact values, wildcards (*), step values (*/N), ranges not needed for MVP

## Config Format
```json
[
  {
    "repo": "github.com/org/repo",
    "cron": "0 */6 * * *",
    "source": "sourcegraph",
    "data_dir": "/data/claims"
  }
]
```

## Key Decisions
- Use `extractionRunner` for sourcegraph mode (already handles full/incremental)
- For clone mode, shell out to `livedocs extract --source clone`
- Minimal cron: parse 5 fields, compute next run from time.Now()
- `--dry-run` prints next 5 scheduled times per entry
