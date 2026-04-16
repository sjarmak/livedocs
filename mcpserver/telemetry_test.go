package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/live-docs/live_docs/db"
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

// TestComputeCoverageBreadthMetrics verifies the two coverage-breadth gauges
// return reasonable values on a fixture claims DB with a mix of never-mined
// and fact-annotated source files.
func TestComputeCoverageBreadthMetrics(t *testing.T) {
	const repo = "cov-repo"
	dir := t.TempDir()
	path := filepath.Join(dir, "cov.db")
	cdb, err := db.OpenClaimsDB(path)
	if err != nil {
		t.Fatalf("open claims db: %v", err)
	}
	defer cdb.Close()
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := cdb.CreateTribalSchema(); err != nil {
		t.Fatalf("create tribal schema: %v", err)
	}

	// Reference clock: 2026-04-15T12:00:00Z (matches current session date).
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	nowSec := now.Unix()

	// 5 source files, all for the same repo:
	//   sf0 — never-mined, indexed 3600 seconds ago (oldest age target).
	//   sf1 — never-mined, indexed 600 seconds ago.
	//   sf2 — mined, has 1 active tribal_fact attached.
	//   sf3 — mined, no facts.
	//   sf4 — mined, has 1 active tribal_fact attached.
	//
	// Expected:
	//   NeverMinedOldestAgeSeconds = 3600 (sf0).
	//   SourceFilesWithFactsFraction = 2/5 = 0.4.

	insertSF := func(relPath string, agoSec int64, mined bool) int64 {
		ts := time.Unix(nowSec-agoSec, 0).UTC().Format(time.RFC3339)
		id, err := cdb.UpsertSourceFile(db.SourceFile{
			Repo:             repo,
			RelativePath:     relPath,
			ContentHash:      "h-" + relPath,
			ExtractorVersion: "test",
			LastIndexed:      ts,
		})
		if err != nil {
			t.Fatalf("upsert source file %q: %v", relPath, err)
		}
		if mined {
			if err := cdb.SetPRIDSet(repo, relPath, []int{1}, "v1"); err != nil {
				t.Fatalf("set pr id set %q: %v", relPath, err)
			}
		}
		return id
	}

	insertSF("pkg/a/sf0.go", 3600, false)
	insertSF("pkg/a/sf1.go", 600, false)
	insertSF("pkg/b/sf2.go", 120, true)
	insertSF("pkg/b/sf3.go", 120, true)
	insertSF("pkg/c/sf4.go", 120, true)

	// Attach one active tribal_fact to sf2 and sf4 (via symbols).
	attachFact := func(relPath string, idx int) {
		symID, err := cdb.UpsertSymbol(db.Symbol{
			Repo:       repo,
			ImportPath: relPath,
			SymbolName: "sym_" + relPath,
			Language:   "file",
			Kind:       "file",
			Visibility: "public",
		})
		if err != nil {
			t.Fatalf("upsert symbol: %v", err)
		}
		_, err = cdb.InsertTribalFact(db.TribalFact{
			SubjectID:        symID,
			Kind:             "quirk",
			Body:             "body",
			SourceQuote:      "q",
			Confidence:       0.9,
			Corroboration:    1,
			Extractor:        "test",
			ExtractorVersion: "1",
			StalenessHash:    "h-" + relPath,
			Status:           "active",
			CreatedAt:        time.Now().UTC().Format(time.RFC3339),
			LastVerified:     time.Now().UTC().Format(time.RFC3339),
		}, []db.TribalEvidence{{
			SourceType:  "inline_marker",
			SourceRef:   relPath,
			ContentHash: "ev",
		}})
		if err != nil {
			t.Fatalf("insert fact %d: %v", idx, err)
		}
	}
	attachFact("pkg/b/sf2.go", 1)
	attachFact("pkg/c/sf4.go", 2)

	m, err := ComputeCoverageBreadthMetrics(cdb.DB(), repo, nowSec)
	if err != nil {
		t.Fatalf("compute metrics: %v", err)
	}
	if m.NeverMinedOldestAgeSeconds != 3600 {
		t.Errorf("NeverMinedOldestAgeSeconds = %.1f, want 3600", m.NeverMinedOldestAgeSeconds)
	}
	wantFrac := 2.0 / 5.0
	if m.SourceFilesWithFactsFraction < wantFrac-0.001 || m.SourceFilesWithFactsFraction > wantFrac+0.001 {
		t.Errorf("SourceFilesWithFactsFraction = %.4f, want ~%.4f", m.SourceFilesWithFactsFraction, wantFrac)
	}
}

// TestComputeCoverageBreadthMetricsEmpty verifies the gauges return
// zero-value metrics on a fresh DB with no rows.
func TestComputeCoverageBreadthMetricsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	cdb, err := db.OpenClaimsDB(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer cdb.Close()
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := cdb.CreateTribalSchema(); err != nil {
		t.Fatalf("tribal schema: %v", err)
	}
	m, err := ComputeCoverageBreadthMetrics(cdb.DB(), "nope", time.Now().Unix())
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if m.NeverMinedOldestAgeSeconds != 0 {
		t.Errorf("NeverMinedOldestAgeSeconds = %.1f, want 0", m.NeverMinedOldestAgeSeconds)
	}
	if m.SourceFilesWithFactsFraction != 0 {
		t.Errorf("SourceFilesWithFactsFraction = %.4f, want 0", m.SourceFilesWithFactsFraction)
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
