# Plan: Scheduled Extraction

## Files
1. `cmd/livedocs/schedule_cmd.go` — command, config parsing, cron parsing, scheduler loop
2. `cmd/livedocs/schedule_cmd_test.go` — tests for config parsing, cron, dry-run, lifecycle
3. `cmd/livedocs/root.go` — register `extractScheduleCmd`

## Implementation Steps

### 1. Cron Parser
- `cronSchedule` struct with 5 fields: minute, hour, dom, month, dow
- `parseCron(expr string) (cronSchedule, error)` — parse "m h dom mon dow"
- `parseField(field string, min, max int) ([]int, error)` — handle *, */N, exact int
- `(c cronSchedule) NextAfter(t time.Time) time.Time` — compute next matching time

### 2. Config
- `scheduleEntry` struct: Repo, Cron, Source, DataDir, Concurrency
- `loadScheduleConfig(path string) ([]scheduleEntry, error)` — read + unmarshal JSON

### 3. Command
- `extractScheduleCmd` cobra command: `extract-schedule --config <path> [--dry-run]`
- Register in root.go init()
- Flags: `--config` (required), `--dry-run`

### 4. Scheduler Loop
- Parse config, parse cron for each entry
- If dry-run: print next 5 run times per entry, exit
- Main loop: find soonest next run across all entries, sleep until then (or ctx cancel)
- On trigger: run extraction (sourcegraph via extractionRunner, clone via exec)
- Log result (success/failure, duration, claims count)
- Signal handling via signal.NotifyContext

### 5. Tests
- `TestParseCron` — valid expressions, invalid expressions
- `TestCronNextAfter` — known time -> expected next run
- `TestLoadScheduleConfig` — valid JSON, invalid JSON, missing fields
- `TestDryRun` — capture output, verify schedule printed
- `TestSchedulerShutdown` — start scheduler, cancel context, verify clean exit
