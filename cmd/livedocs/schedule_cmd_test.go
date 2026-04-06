package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCron(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{name: "every minute", expr: "* * * * *"},
		{name: "every 6 hours", expr: "0 */6 * * *"},
		{name: "specific time", expr: "30 2 * * *"},
		{name: "weekdays only", expr: "0 9 * * 1,2,3,4,5"},
		{name: "monthly", expr: "0 0 1 * *"},
		{name: "too few fields", expr: "* * *", wantErr: true},
		{name: "too many fields", expr: "* * * * * *", wantErr: true},
		{name: "invalid minute", expr: "60 * * * *", wantErr: true},
		{name: "invalid hour", expr: "0 25 * * *", wantErr: true},
		{name: "invalid step", expr: "*/0 * * * *", wantErr: true},
		{name: "invalid value", expr: "abc * * * *", wantErr: true},
		{name: "empty", expr: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseCron(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseCron(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
		})
	}
}

func TestParseCronField(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		min     int
		max     int
		want    []int
		wantErr bool
	}{
		{name: "wildcard", field: "*", min: 0, max: 5, want: []int{0, 1, 2, 3, 4, 5}},
		{name: "step", field: "*/2", min: 0, max: 9, want: []int{0, 2, 4, 6, 8}},
		{name: "single", field: "5", min: 0, max: 59, want: []int{5}},
		{name: "comma", field: "1,3,5", min: 0, max: 6, want: []int{1, 3, 5}},
		{name: "out of range", field: "99", min: 0, max: 59, wantErr: true},
		{name: "bad step", field: "*/abc", min: 0, max: 59, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCronField(tt.field, tt.min, tt.max)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseCronField(%q, %d, %d) error = %v, wantErr %v",
					tt.field, tt.min, tt.max, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Errorf("got %v, want %v", got, tt.want)
					return
				}
				for i := range got {
					if got[i] != tt.want[i] {
						t.Errorf("got %v, want %v", got, tt.want)
						return
					}
				}
			}
		})
	}
}

func TestCronNextAfter(t *testing.T) {
	// Fixed reference time: 2025-01-15 10:30:00 UTC (Wednesday)
	ref := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		expr string
		want time.Time
	}{
		{
			name: "every minute - next minute",
			expr: "* * * * *",
			want: time.Date(2025, 1, 15, 10, 31, 0, 0, time.UTC),
		},
		{
			name: "top of hour - next hour",
			expr: "0 * * * *",
			want: time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC),
		},
		{
			name: "specific time today already passed",
			expr: "0 8 * * *",
			want: time.Date(2025, 1, 16, 8, 0, 0, 0, time.UTC),
		},
		{
			name: "specific time today not yet",
			expr: "0 14 * * *",
			want: time.Date(2025, 1, 15, 14, 0, 0, 0, time.UTC),
		},
		{
			name: "every 6 hours",
			expr: "0 */6 * * *",
			want: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
		},
		{
			name: "first of month",
			expr: "0 0 1 * *",
			want: time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, err := parseCron(tt.expr)
			if err != nil {
				t.Fatalf("parseCron(%q): %v", tt.expr, err)
			}
			got := sched.NextAfter(ref)
			if !got.Equal(tt.want) {
				t.Errorf("NextAfter(%v) = %v, want %v", ref, got, tt.want)
			}
		})
	}
}

func TestCronNextAfterSequential(t *testing.T) {
	// Verify sequential NextAfter calls produce increasing times.
	sched, err := parseCron("0 */6 * * *")
	if err != nil {
		t.Fatal(err)
	}

	ref := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	expected := []time.Time{
		time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC), // first match from 00:00
		time.Date(2025, 1, 15, 6, 0, 0, 0, time.UTC),
		time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2025, 1, 15, 18, 0, 0, 0, time.UTC),
		time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC),
	}

	// Start one minute before midnight to get midnight as first result.
	current := ref.Add(-time.Minute)
	for i, want := range expected {
		got := sched.NextAfter(current)
		if !got.Equal(want) {
			t.Errorf("iteration %d: NextAfter(%v) = %v, want %v", i, current, got, want)
		}
		current = got
	}
}

func TestLoadScheduleConfig(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		entries := []scheduleEntry{
			{Repo: "github.com/org/repo1", Cron: "0 */6 * * *", Source: "sourcegraph", DataDir: "/data"},
			{Repo: "https://github.com/org/repo2.git", Cron: "30 2 * * *", Source: "clone", DataDir: "/data"},
		}
		data, _ := json.Marshal(entries)
		path := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(path, data, 0o644)

		got, err := loadScheduleConfig(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d entries, want 2", len(got))
		}
		if got[0].Repo != "github.com/org/repo1" {
			t.Errorf("got repo %q, want %q", got[0].Repo, "github.com/org/repo1")
		}
		// Default concurrency should be set.
		if got[0].Concurrency != 10 {
			t.Errorf("got concurrency %d, want 10", got[0].Concurrency)
		}
	})

	t.Run("missing repo", func(t *testing.T) {
		entries := []scheduleEntry{
			{Cron: "* * * * *", Source: "sourcegraph", DataDir: "/data"},
		}
		data, _ := json.Marshal(entries)
		path := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(path, data, 0o644)

		_, err := loadScheduleConfig(path)
		if err == nil || !strings.Contains(err.Error(), "repo is required") {
			t.Errorf("expected repo required error, got: %v", err)
		}
	})

	t.Run("missing cron", func(t *testing.T) {
		entries := []scheduleEntry{
			{Repo: "org/repo", Source: "sourcegraph", DataDir: "/data"},
		}
		data, _ := json.Marshal(entries)
		path := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(path, data, 0o644)

		_, err := loadScheduleConfig(path)
		if err == nil || !strings.Contains(err.Error(), "cron is required") {
			t.Errorf("expected cron required error, got: %v", err)
		}
	})

	t.Run("invalid source", func(t *testing.T) {
		entries := []scheduleEntry{
			{Repo: "org/repo", Cron: "* * * * *", Source: "ftp", DataDir: "/data"},
		}
		data, _ := json.Marshal(entries)
		path := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(path, data, 0o644)

		_, err := loadScheduleConfig(path)
		if err == nil || !strings.Contains(err.Error(), "source must be") {
			t.Errorf("expected source validation error, got: %v", err)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(path, []byte("not json"), 0o644)

		_, err := loadScheduleConfig(path)
		if err == nil || !strings.Contains(err.Error(), "parse config file") {
			t.Errorf("expected parse error, got: %v", err)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := loadScheduleConfig("/nonexistent/config.json")
		if err == nil || !strings.Contains(err.Error(), "read config file") {
			t.Errorf("expected read error, got: %v", err)
		}
	})

	t.Run("missing data_dir", func(t *testing.T) {
		entries := []scheduleEntry{
			{Repo: "org/repo", Cron: "* * * * *", Source: "sourcegraph"},
		}
		data, _ := json.Marshal(entries)
		path := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(path, data, 0o644)

		_, err := loadScheduleConfig(path)
		if err == nil || !strings.Contains(err.Error(), "data_dir is required") {
			t.Errorf("expected data_dir required error, got: %v", err)
		}
	})
}

func TestDryRunOutput(t *testing.T) {
	entries := []scheduleEntry{
		{Repo: "github.com/org/repo1", Cron: "0 */6 * * *", Source: "sourcegraph", DataDir: "/data"},
	}

	sched, err := parseCron(entries[0].Cron)
	if err != nil {
		t.Fatal(err)
	}

	runs := []scheduledRun{
		{entryIndex: 0, nextRun: sched.NextAfter(time.Now()), schedule: sched},
	}

	var buf bytes.Buffer
	err = printDryRun(&buf, entries, runs)
	if err != nil {
		t.Fatalf("printDryRun error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Dry-run schedule:") {
		t.Error("missing dry-run header")
	}
	if !strings.Contains(output, "github.com/org/repo1") {
		t.Error("missing repo name in output")
	}
	if !strings.Contains(output, "source=sourcegraph") {
		t.Error("missing source in output")
	}
	// Should contain 5 numbered entries.
	for i := 1; i <= 5; i++ {
		prefix := "  " + string(rune('0'+i)) + "."
		if !strings.Contains(output, prefix) {
			t.Errorf("missing scheduled time entry %d", i)
		}
	}
}

func TestSchedulerShutdown(t *testing.T) {
	// Override the extraction function to track calls.
	callCount := 0
	original := schedulerExtractionFn
	schedulerExtractionFn = func(_ context.Context, _ scheduleEntry) (int, error) {
		callCount++
		return 42, nil
	}
	defer func() { schedulerExtractionFn = original }()

	entries := []scheduleEntry{
		{Repo: "test/repo", Cron: "* * * * *", Source: "clone", DataDir: "/tmp"},
	}

	sched, _ := parseCron("* * * * *")
	// Set next run far in the future so the scheduler sleeps.
	futureTime := time.Now().Add(1 * time.Hour)
	runs := []scheduledRun{
		{entryIndex: 0, nextRun: futureTime, schedule: sched},
	}

	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- runSchedulerLoop(ctx, &buf, entries, runs)
	}()

	// Cancel the context to trigger shutdown.
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not shut down within 5 seconds")
	}

	output := buf.String()
	if !strings.Contains(output, "shutting down") {
		t.Error("missing shutdown message in output")
	}

	// Should not have executed any extractions.
	if callCount != 0 {
		t.Errorf("expected 0 extraction calls, got %d", callCount)
	}
}

func TestSchedulerExecutesExtraction(t *testing.T) {
	// Override extraction function.
	callCount := 0
	var lastEntry scheduleEntry
	original := schedulerExtractionFn
	schedulerExtractionFn = func(_ context.Context, e scheduleEntry) (int, error) {
		callCount++
		lastEntry = e
		return 100, nil
	}
	defer func() { schedulerExtractionFn = original }()

	entries := []scheduleEntry{
		{Repo: "test/repo", Cron: "* * * * *", Source: "sourcegraph", DataDir: "/tmp", Concurrency: 5},
	}

	sched, _ := parseCron("* * * * *")
	// Set next run in the past so it fires immediately.
	runs := []scheduledRun{
		{entryIndex: 0, nextRun: time.Now().Add(-time.Minute), schedule: sched},
	}

	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- runSchedulerLoop(ctx, &buf, entries, runs)
	}()

	// Wait a bit for the extraction to fire, then cancel.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not shut down")
	}

	if callCount < 1 {
		t.Errorf("expected at least 1 extraction call, got %d", callCount)
	}
	if lastEntry.Repo != "test/repo" {
		t.Errorf("expected repo test/repo, got %s", lastEntry.Repo)
	}
}

func TestIntSliceContains(t *testing.T) {
	tests := []struct {
		slice []int
		val   int
		want  bool
	}{
		{[]int{1, 3, 5}, 3, true},
		{[]int{1, 3, 5}, 2, false},
		{[]int{0}, 0, true},
		{[]int{}, 1, false},
	}

	for _, tt := range tests {
		got := intSliceContains(tt.slice, tt.val)
		if got != tt.want {
			t.Errorf("intSliceContains(%v, %d) = %v, want %v", tt.slice, tt.val, got, tt.want)
		}
	}
}
