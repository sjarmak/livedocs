package mcpserver

import (
	"crypto/sha256"
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
