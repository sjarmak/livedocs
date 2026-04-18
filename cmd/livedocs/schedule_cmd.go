package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/sourcegraph"
)

var (
	scheduleConfigPath string
	scheduleDryRun     bool
)

var extractScheduleCmd = &cobra.Command{
	Use:   "extract-schedule",
	Short: "Run scheduled extractions based on cron expressions",
	Long: `Reads a JSON config file listing repositories with cron expressions
and triggers extraction on schedule. Supports both sourcegraph and clone
extraction modes per repository.

Config file format:
  [
    {
      "repo": "github.com/org/repo",
      "cron": "0 */6 * * *",
      "source": "sourcegraph",
      "data_dir": "/data/claims",
      "concurrency": 10
    }
  ]

The scheduler runs until interrupted (SIGINT/SIGTERM).
Use --dry-run to print the schedule without executing extractions.`,
	RunE: runExtractSchedule,
}

func init() {
	extractScheduleCmd.Flags().StringVar(&scheduleConfigPath, "config", "", "path to JSON schedule config file (required)")
	extractScheduleCmd.Flags().BoolVar(&scheduleDryRun, "dry-run", false, "print schedule without executing extractions")
	_ = extractScheduleCmd.MarkFlagRequired("config")
}

// scheduleEntry represents a single repo extraction schedule.
type scheduleEntry struct {
	Repo        string `json:"repo"`
	Cron        string `json:"cron"`
	Source      string `json:"source"`
	DataDir     string `json:"data_dir"`
	Concurrency int    `json:"concurrency"`
}

// loadScheduleConfig reads and parses the schedule config JSON file.
func loadScheduleConfig(path string) ([]scheduleEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}

	var entries []scheduleEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", path, err)
	}

	for i, e := range entries {
		if e.Repo == "" {
			return nil, fmt.Errorf("config entry %d: repo is required", i)
		}
		if e.Cron == "" {
			return nil, fmt.Errorf("config entry %d: cron is required", i)
		}
		if e.Source == "" {
			return nil, fmt.Errorf("config entry %d: source is required (sourcegraph or clone)", i)
		}
		if e.Source != "sourcegraph" && e.Source != "clone" {
			return nil, fmt.Errorf("config entry %d: source must be 'sourcegraph' or 'clone', got %q", i, e.Source)
		}
		if e.DataDir == "" {
			return nil, fmt.Errorf("config entry %d: data_dir is required", i)
		}
		if e.Concurrency <= 0 {
			entries[i].Concurrency = 10
		}
	}

	return entries, nil
}

// cronSchedule represents a parsed 5-field cron expression.
type cronSchedule struct {
	minutes     []int // 0-59
	hours       []int // 0-23
	daysOfMonth []int // 1-31
	months      []int // 1-12
	daysOfWeek  []int // 0-6 (0=Sunday)
}

// parseCron parses a standard 5-field cron expression: "minute hour dom month dow".
func parseCron(expr string) (cronSchedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cronSchedule{}, fmt.Errorf("cron expression must have 5 fields, got %d: %q", len(fields), expr)
	}

	minutes, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("minute field: %w", err)
	}

	hours, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("hour field: %w", err)
	}

	doms, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("day-of-month field: %w", err)
	}

	months, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("month field: %w", err)
	}

	dows, err := parseCronField(fields[4], 0, 6)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("day-of-week field: %w", err)
	}

	return cronSchedule{
		minutes:     minutes,
		hours:       hours,
		daysOfMonth: doms,
		months:      months,
		daysOfWeek:  dows,
	}, nil
}

// parseCronField parses a single cron field supporting: *, */N, N, N,N,N.
func parseCronField(field string, min, max int) ([]int, error) {
	if field == "*" {
		return rangeInts(min, max), nil
	}

	if strings.HasPrefix(field, "*/") {
		step, err := strconv.Atoi(field[2:])
		if err != nil || step <= 0 {
			return nil, fmt.Errorf("invalid step value in %q", field)
		}
		var vals []int
		for i := min; i <= max; i += step {
			vals = append(vals, i)
		}
		return vals, nil
	}

	// Comma-separated values.
	parts := strings.Split(field, ",")
	var vals []int
	for _, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("invalid value %q in field %q", p, field)
		}
		if v < min || v > max {
			return nil, fmt.Errorf("value %d out of range [%d, %d] in field %q", v, min, max, field)
		}
		vals = append(vals, v)
	}
	sort.Ints(vals)
	return vals, nil
}

// rangeInts returns a slice of integers from min to max inclusive.
func rangeInts(min, max int) []int {
	vals := make([]int, 0, max-min+1)
	for i := min; i <= max; i++ {
		vals = append(vals, i)
	}
	return vals
}

// intSliceContains checks if a sorted slice contains the value.
func intSliceContains(sorted []int, val int) bool {
	idx := sort.SearchInts(sorted, val)
	return idx < len(sorted) && sorted[idx] == val
}

// NextAfter returns the next time after t that matches the cron schedule.
// It searches up to 1 year ahead and returns zero time if no match is found.
func (c cronSchedule) NextAfter(t time.Time) time.Time {
	// Start from the next minute.
	candidate := t.Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(366 * 24 * time.Hour)

	for candidate.Before(limit) {
		if !intSliceContains(c.months, int(candidate.Month())) {
			candidate = time.Date(candidate.Year(), candidate.Month()+1, 1, 0, 0, 0, 0, candidate.Location())
			continue
		}
		if !intSliceContains(c.daysOfMonth, candidate.Day()) || !intSliceContains(c.daysOfWeek, int(candidate.Weekday())) {
			candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day()+1, 0, 0, 0, 0, candidate.Location())
			continue
		}
		if !intSliceContains(c.hours, candidate.Hour()) {
			candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day(), candidate.Hour()+1, 0, 0, 0, candidate.Location())
			continue
		}
		if !intSliceContains(c.minutes, candidate.Minute()) {
			candidate = candidate.Add(time.Minute)
			continue
		}
		return candidate
	}

	return time.Time{}
}

// scheduledRun tracks the next run time for an entry.
type scheduledRun struct {
	entryIndex int
	nextRun    time.Time
	schedule   cronSchedule
}

func runExtractSchedule(cmd *cobra.Command, _ []string) error {
	entries, err := loadScheduleConfig(scheduleConfigPath)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		return fmt.Errorf("config file contains no entries")
	}

	out := cmd.OutOrStdout()
	now := time.Now()

	// Parse cron expressions and compute initial next-run times.
	runs := make([]scheduledRun, len(entries))
	for i, e := range entries {
		sched, parseErr := parseCron(e.Cron)
		if parseErr != nil {
			return fmt.Errorf("entry %d (%s): %w", i, e.Repo, parseErr)
		}
		runs[i] = scheduledRun{
			entryIndex: i,
			nextRun:    sched.NextAfter(now),
			schedule:   sched,
		}
	}

	// Dry-run mode: print next 5 scheduled times per entry and exit.
	if scheduleDryRun {
		return printDryRun(out, entries, runs)
	}

	// Set up signal-based context cancellation.
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	fmt.Fprintf(out, "extract-schedule: starting scheduler with %d entries\n", len(entries))
	for i, e := range entries {
		fmt.Fprintf(out, "  [%d] %s (%s) next=%s\n", i, e.Repo, e.Cron, runs[i].nextRun.Format(time.RFC3339))
	}

	return runSchedulerLoop(ctx, out, entries, runs)
}

// printDryRun prints the next 5 run times for each entry.
func printDryRun(out io.Writer, entries []scheduleEntry, runs []scheduledRun) error {
	fmt.Fprintf(out, "Dry-run schedule:\n\n")
	for i, e := range entries {
		fmt.Fprintf(out, "[%d] %s (source=%s, cron=%q)\n", i, e.Repo, e.Source, e.Cron)
		t := time.Now()
		for j := 0; j < 5; j++ {
			next := runs[i].schedule.NextAfter(t)
			if next.IsZero() {
				fmt.Fprintf(out, "  %d. (no more runs within 1 year)\n", j+1)
				break
			}
			fmt.Fprintf(out, "  %d. %s\n", j+1, next.Format(time.RFC3339))
			t = next
		}
		fmt.Fprintln(out)
	}
	return nil
}

// schedulerExtractionFn is the function called for each extraction.
// Overridable in tests to avoid real network calls.
var schedulerExtractionFn = executeScheduledExtraction

// runSchedulerLoop is the main scheduler loop. It finds the soonest next run,
// sleeps until that time (or context cancellation), then executes the extraction.
func runSchedulerLoop(ctx context.Context, out io.Writer, entries []scheduleEntry, runs []scheduledRun) error {
	for {
		// Find the soonest next run.
		soonestIdx := 0
		for i := 1; i < len(runs); i++ {
			if runs[i].nextRun.Before(runs[soonestIdx].nextRun) {
				soonestIdx = i
			}
		}

		nextRun := runs[soonestIdx].nextRun
		if nextRun.IsZero() {
			fmt.Fprintf(out, "extract-schedule: no more scheduled runs\n")
			return nil
		}

		sleepDuration := time.Until(nextRun)
		if sleepDuration < 0 {
			sleepDuration = 0
		}

		entry := entries[runs[soonestIdx].entryIndex]
		fmt.Fprintf(out, "extract-schedule: next run for %s at %s (in %s)\n",
			entry.Repo, nextRun.Format(time.RFC3339), sleepDuration.Round(time.Second))

		// Sleep until next run or context cancellation.
		select {
		case <-ctx.Done():
			fmt.Fprintf(out, "extract-schedule: shutting down\n")
			return nil
		case <-time.After(sleepDuration):
		}

		// Check if context was cancelled during sleep.
		if ctx.Err() != nil {
			fmt.Fprintf(out, "extract-schedule: shutting down\n")
			return nil
		}

		// Execute extraction.
		start := time.Now()
		claimsCount, extractErr := schedulerExtractionFn(ctx, entry)
		duration := time.Since(start)

		if extractErr != nil {
			log.Printf("extract-schedule: FAILED %s (source=%s) duration=%s error=%v",
				entry.Repo, entry.Source, duration.Round(time.Millisecond), extractErr)
		} else {
			log.Printf("extract-schedule: SUCCESS %s (source=%s) duration=%s claims=%d",
				entry.Repo, entry.Source, duration.Round(time.Millisecond), claimsCount)
		}

		// Update next run time for this entry.
		runs[soonestIdx].nextRun = runs[soonestIdx].schedule.NextAfter(time.Now())
	}
}

// executeScheduledExtraction runs one extraction for the given entry.
// Returns the number of claims extracted and any error.
func executeScheduledExtraction(ctx context.Context, entry scheduleEntry) (int, error) {
	switch entry.Source {
	case "sourcegraph":
		return executeSourcegraphExtraction(ctx, entry)
	case "clone":
		return executeCloneExtraction(ctx, entry)
	default:
		return 0, fmt.Errorf("unknown source: %q", entry.Source)
	}
}

// executeSourcegraphExtraction runs extraction via the ExtractionRunner pattern.
func executeSourcegraphExtraction(ctx context.Context, entry scheduleEntry) (int, error) {
	sgClient, err := sourcegraph.NewSourcegraphClient()
	if err != nil {
		return 0, fmt.Errorf("create sourcegraph client: %w", err)
	}
	defer sgClient.Close()

	runner := newExtractionRunner(sgClient, entry.DataDir, entry.Concurrency)
	if err := runner.RunExtraction(ctx, entry.Repo, ""); err != nil {
		return 0, err
	}

	// Count claims from the resulting DB.
	repoName := repoNameFromPath(entry.Repo)
	count := countClaimsFromDB(entry.DataDir, repoName)
	return count, nil
}

// executeCloneExtraction runs extraction by shelling out to livedocs extract --source clone.
func executeCloneExtraction(ctx context.Context, entry scheduleEntry) (int, error) {
	selfPath, err := os.Executable()
	if err != nil {
		selfPath = "livedocs"
	}

	repoName := repoNameFromURL(entry.Repo)
	outputPath := filepath.Join(entry.DataDir, repoName+".claims.db")

	args := []string{
		"extract",
		"--source", "clone",
		"--repo", entry.Repo,
		"-o", outputPath,
	}

	cmd := exec.CommandContext(ctx, selfPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("clone extraction for %s: %w", entry.Repo, err)
	}

	count := countClaimsFromDB(entry.DataDir, repoName)
	return count, nil
}

// countClaimsFromDB opens the claims DB and counts symbols as a proxy for claims.
func countClaimsFromDB(dataDir, repoName string) int {
	dbPath := filepath.Join(dataDir, repoName+".claims.db")
	claimsDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		return 0
	}
	defer claimsDB.Close()
	return countSymbols(claimsDB)
}
