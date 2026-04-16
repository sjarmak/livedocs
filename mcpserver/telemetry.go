package mcpserver

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DailyReport is the JSON structure written to each daily telemetry file.
type DailyReport struct {
	Date        string         `json:"date"`
	ToolCalls   map[string]int `json:"tool_calls"`
	UniqueRepos []string       `json:"unique_repos"`
}

// CollectorConfig configures telemetry collection.
type CollectorConfig struct {
	Enabled  bool
	StoreDir string           // defaults to ~/.livedocs/telemetry
	nowFunc  func() time.Time // for testing
}

// Collector accumulates tool-call metrics in memory and flushes to daily JSON files.
type Collector struct {
	cfg       CollectorConfig
	mu        sync.Mutex
	toolCalls map[string]int
	repos     map[string]struct{} // raw paths kept in memory, hashed on flush
}

// NewCollector creates a telemetry collector. If cfg.Enabled is false, all
// operations are no-ops.
func NewCollector(cfg CollectorConfig) *Collector {
	if cfg.StoreDir == "" {
		home, _ := os.UserHomeDir()
		cfg.StoreDir = filepath.Join(home, ".livedocs", "telemetry")
	}
	if cfg.nowFunc == nil {
		cfg.nowFunc = time.Now
	}
	return &Collector{
		cfg:       cfg,
		toolCalls: make(map[string]int),
		repos:     make(map[string]struct{}),
	}
}

// Enabled reports whether telemetry collection is active.
func (c *Collector) Enabled() bool {
	return c.cfg.Enabled
}

// Record logs a tool call. Safe for concurrent use. No-op if disabled.
func (c *Collector) Record(toolName, repoPath string) {
	if !c.cfg.Enabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolCalls[toolName]++
	if repoPath != "" {
		c.repos[repoPath] = struct{}{}
	}
}

// Flush writes accumulated metrics to the daily JSON file and resets counters.
// If a file for today already exists, metrics are merged. No-op if disabled.
func (c *Collector) Flush() error {
	if !c.cfg.Enabled {
		return nil
	}

	c.mu.Lock()
	calls := c.toolCalls
	repos := c.repos
	c.toolCalls = make(map[string]int)
	c.repos = make(map[string]struct{})
	c.mu.Unlock()

	if len(calls) == 0 && len(repos) == 0 {
		return nil
	}

	date := c.cfg.nowFunc().UTC().Format("2006-01-02")
	fpath := filepath.Join(c.cfg.StoreDir, date+".json")

	if err := os.MkdirAll(c.cfg.StoreDir, 0o755); err != nil {
		return fmt.Errorf("create telemetry dir: %w", err)
	}

	// Load existing report for today if present.
	report := DailyReport{
		Date:      date,
		ToolCalls: make(map[string]int),
	}
	existing := make(map[string]struct{})

	if data, err := os.ReadFile(fpath); err == nil {
		var prev DailyReport
		if json.Unmarshal(data, &prev) == nil {
			report.ToolCalls = prev.ToolCalls
			for _, h := range prev.UniqueRepos {
				existing[h] = struct{}{}
			}
		}
	}

	// Merge new data.
	for tool, count := range calls {
		report.ToolCalls[tool] += count
	}
	for path := range repos {
		h := hashRepo(path)
		existing[h] = struct{}{}
	}
	report.UniqueRepos = make([]string, 0, len(existing))
	for h := range existing {
		report.UniqueRepos = append(report.UniqueRepos, h)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal telemetry: %w", err)
	}
	return os.WriteFile(fpath, data, 0o644)
}

// hashRepo produces a truncated SHA-256 of the repo path for privacy.
func hashRepo(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:8])
}

// CoverageBreadthMetrics holds on-demand coverage-breadth SLO gauges.
//
// These metrics are computed by reading a claims DB directly — the Collector
// tracks tool-call counts only and has no awareness of domain-specific
// gauges. Callers are expected to run ComputeCoverageBreadthMetrics on a
// schedule (or on demand) and forward the result to whatever downstream
// Prometheus/OpenMetrics exporter is in use.
//
// NeverMinedOldestAgeSeconds is the maximum age, in seconds, of any
// never-mined source file tracked in source_files for the given repo.
// A file is never-mined when its last_pr_id_set column is NULL or an
// empty blob. If no never-mined files exist, the value is 0.
//
// SourceFilesWithFactsFraction is the fraction of live source files whose
// relative_path is referenced by at least one active tribal_fact (joined
// through symbols). Range: [0.0, 1.0]. Returns 0.0 if source_files has
// no live rows.
type CoverageBreadthMetrics struct {
	NeverMinedOldestAgeSeconds   float64
	SourceFilesWithFactsFraction float64
}

// ComputeCoverageBreadthMetrics reads a claims DB and returns the two
// coverage-breadth gauges for the given repo. The nowSeconds argument is
// the reference wall clock used for age calculations (unit: seconds since
// the Unix epoch). Callers in production typically pass time.Now().Unix();
// tests inject a fixed value so age calculations are deterministic.
//
// A non-nil error is returned only for low-level DB failures. An
// absent/empty result set yields a zero-valued CoverageBreadthMetrics with
// a nil error.
func ComputeCoverageBreadthMetrics(sqlDB *sql.DB, repo string, nowSeconds int64) (CoverageBreadthMetrics, error) {
	var m CoverageBreadthMetrics

	// 1. Denominator: total live source_files for repo.
	var total int
	if err := sqlDB.QueryRow(
		`SELECT COUNT(*) FROM source_files WHERE repo = ? AND deleted = 0`,
		repo,
	).Scan(&total); err != nil {
		return m, fmt.Errorf("coverage-breadth: count source_files: %w", err)
	}
	if total == 0 {
		return m, nil
	}

	// 2. Numerator: source_files with at least one active fact attached
	// through a symbol whose import_path = sf.relative_path.
	var withFacts int
	if err := sqlDB.QueryRow(`
		SELECT COUNT(*) FROM source_files sf
		WHERE sf.repo = ? AND sf.deleted = 0
		  AND EXISTS (
		      SELECT 1 FROM tribal_facts tf
		      JOIN symbols s ON s.id = tf.subject_id
		      WHERE s.import_path = sf.relative_path
		        AND tf.status = 'active'
		  )
	`, repo).Scan(&withFacts); err != nil {
		return m, fmt.Errorf("coverage-breadth: count source_files with facts: %w", err)
	}
	m.SourceFilesWithFactsFraction = float64(withFacts) / float64(total)

	// 3. Never-mined oldest age: MAX(now - last_indexed_unix) over rows
	// where last_pr_id_set is NULL or empty. last_indexed is stored as an
	// RFC3339 string, so we parse in Go rather than relying on SQLite's
	// strftime semantics (which vary across drivers).
	rows, err := sqlDB.Query(`
		SELECT last_indexed FROM source_files
		WHERE repo = ? AND deleted = 0
		  AND (last_pr_id_set IS NULL OR LENGTH(last_pr_id_set) = 0)
	`, repo)
	if err != nil {
		return m, fmt.Errorf("coverage-breadth: query never-mined ages: %w", err)
	}
	defer rows.Close()

	var oldestAge float64
	for rows.Next() {
		var lastIndexed string
		if err := rows.Scan(&lastIndexed); err != nil {
			return m, fmt.Errorf("coverage-breadth: scan last_indexed: %w", err)
		}
		t, parseErr := time.Parse(time.RFC3339, lastIndexed)
		if parseErr != nil {
			// A malformed timestamp isn't a metric-calculation failure —
			// log would be nicer but the package has no logger. Skip the
			// row so one bad entry doesn't zero the gauge.
			continue
		}
		age := float64(nowSeconds - t.Unix())
		if age < 0 {
			age = 0
		}
		if age > oldestAge {
			oldestAge = age
		}
	}
	if err := rows.Err(); err != nil {
		return m, fmt.Errorf("coverage-breadth: iterate never-mined ages: %w", err)
	}
	m.NeverMinedOldestAgeSeconds = oldestAge
	return m, nil
}
