package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectorDisabledByDefault(t *testing.T) {
	c := NewCollector(CollectorConfig{})
	if c.Enabled() {
		t.Fatal("collector should be disabled by default")
	}
	// Recording on a disabled collector should be a no-op.
	c.Record("query_claims", "/some/repo")
}

func TestCollectorEnabledViaConfig(t *testing.T) {
	dir := t.TempDir()
	c := NewCollector(CollectorConfig{
		Enabled:  true,
		StoreDir: dir,
	})
	if !c.Enabled() {
		t.Fatal("collector should be enabled when config says so")
	}
}

func TestRecordAndFlush(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	c := NewCollector(CollectorConfig{
		Enabled:  true,
		StoreDir: dir,
		nowFunc:  func() time.Time { return now },
	})

	c.Record("query_claims", "/home/user/repos/myapp")
	c.Record("query_claims", "/home/user/repos/myapp")
	c.Record("check_drift", "/home/user/repos/other")

	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Verify file exists with correct date.
	fpath := filepath.Join(dir, "2026-04-01.json")
	data, err := os.ReadFile(fpath)
	if err != nil {
		t.Fatalf("read telemetry file: %v", err)
	}

	var report DailyReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if report.Date != "2026-04-01" {
		t.Errorf("date = %q, want 2026-04-01", report.Date)
	}
	if report.ToolCalls["query_claims"] != 2 {
		t.Errorf("query_claims count = %d, want 2", report.ToolCalls["query_claims"])
	}
	if report.ToolCalls["check_drift"] != 1 {
		t.Errorf("check_drift count = %d, want 1", report.ToolCalls["check_drift"])
	}
	if len(report.UniqueRepos) != 2 {
		t.Errorf("unique repos = %d, want 2", len(report.UniqueRepos))
	}
}

func TestRepoPathsAreHashed(t *testing.T) {
	dir := t.TempDir()
	c := NewCollector(CollectorConfig{
		Enabled:  true,
		StoreDir: dir,
	})

	c.Record("query_claims", "/home/user/secret-project")
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) == 0 {
		t.Fatal("no telemetry files written")
	}
	data, _ := os.ReadFile(files[0])

	// The raw path must not appear in the output.
	if string(data) != "" && contains(string(data), "/home/user/secret-project") {
		t.Error("raw repo path leaked into telemetry file")
	}
}

func TestDailyFileRotation(t *testing.T) {
	dir := t.TempDir()
	day1 := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)

	current := day1
	c := NewCollector(CollectorConfig{
		Enabled:  true,
		StoreDir: dir,
		nowFunc:  func() time.Time { return current },
	})

	c.Record("query_claims", "/repo1")
	if err := c.Flush(); err != nil {
		t.Fatalf("flush day1: %v", err)
	}

	// Advance to day 2.
	current = day2
	c.Record("check_drift", "/repo2")
	if err := c.Flush(); err != nil {
		t.Fatalf("flush day2: %v", err)
	}

	// Both files should exist.
	for _, date := range []string{"2026-04-01", "2026-04-02"} {
		fpath := filepath.Join(dir, date+".json")
		if _, err := os.Stat(fpath); os.IsNotExist(err) {
			t.Errorf("missing telemetry file for %s", date)
		}
	}
}

func TestFlushMergesWithExistingFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	// First collector writes some data.
	c1 := NewCollector(CollectorConfig{
		Enabled:  true,
		StoreDir: dir,
		nowFunc:  func() time.Time { return now },
	})
	c1.Record("query_claims", "/repo1")
	if err := c1.Flush(); err != nil {
		t.Fatalf("flush c1: %v", err)
	}

	// Second collector writes more data for the same day.
	c2 := NewCollector(CollectorConfig{
		Enabled:  true,
		StoreDir: dir,
		nowFunc:  func() time.Time { return now },
	})
	c2.Record("check_drift", "/repo2")
	if err := c2.Flush(); err != nil {
		t.Fatalf("flush c2: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "2026-04-01.json"))
	var report DailyReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if report.ToolCalls["query_claims"] != 1 {
		t.Errorf("query_claims = %d, want 1", report.ToolCalls["query_claims"])
	}
	if report.ToolCalls["check_drift"] != 1 {
		t.Errorf("check_drift = %d, want 1", report.ToolCalls["check_drift"])
	}
	if len(report.UniqueRepos) != 2 {
		t.Errorf("unique repos = %d, want 2", len(report.UniqueRepos))
	}
}

func TestDisabledCollectorFlushIsNoop(t *testing.T) {
	c := NewCollector(CollectorConfig{})
	if err := c.Flush(); err != nil {
		t.Fatalf("flush on disabled collector should not error: %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
