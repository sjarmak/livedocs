package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/live-docs/live_docs/gitdiff"
)

// ErrDailyBudgetExceeded is returned when the daily MCP call budget has been reached.
var ErrDailyBudgetExceeded = errors.New("daily MCP call budget exceeded")

// CostRecord represents the daily cost tracking data persisted to JSON.
type CostRecord struct {
	Date             string  `json:"date"`
	CallCount        int     `json:"call_count"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

// costPerCall is the estimated USD cost per MCP call.
const costPerCall = 0.003

// CostTracker wraps a FileSource and tracks MCP call counts per day,
// enforcing an optional daily budget cap.
type CostTracker struct {
	inner        FileSource
	costFilePath string
	dailyBudget  int
	mu           sync.Mutex
	record       CostRecord
}

// NewCostTracker creates a CostTracker wrapping inner. If dailyBudget is 0,
// no limit is enforced. The cost file at costFilePath is loaded on creation
// (reset if the date has changed) and persisted after each call.
func NewCostTracker(inner FileSource, costFilePath string, dailyBudget int) (*CostTracker, error) {
	ct := &CostTracker{
		inner:        inner,
		costFilePath: costFilePath,
		dailyBudget:  dailyBudget,
	}
	if err := ct.load(); err != nil {
		return nil, fmt.Errorf("loading cost file: %w", err)
	}
	return ct, nil
}

func (ct *CostTracker) load() error {
	today := time.Now().UTC().Format("2006-01-02")

	data, err := os.ReadFile(ct.costFilePath)
	if errors.Is(err, os.ErrNotExist) {
		ct.record = CostRecord{Date: today}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading cost file: %w", err)
	}

	var rec CostRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("parsing cost file: %w", err)
	}

	if rec.Date != today {
		ct.record = CostRecord{Date: today}
	} else {
		ct.record = rec
	}
	return nil
}

func (ct *CostTracker) persist() error {
	data, err := json.Marshal(ct.record)
	if err != nil {
		return fmt.Errorf("marshaling cost record: %w", err)
	}
	if err := os.WriteFile(ct.costFilePath, data, 0644); err != nil {
		return fmt.Errorf("writing cost file: %w", err)
	}
	return nil
}

// increment checks the budget and increments the call counter. Must be called
// with ct.mu held.
func (ct *CostTracker) increment() error {
	if ct.dailyBudget > 0 && ct.record.CallCount >= ct.dailyBudget {
		return fmt.Errorf("%w: %d calls made, budget is %d",
			ErrDailyBudgetExceeded, ct.record.CallCount, ct.dailyBudget)
	}
	ct.record.CallCount++
	ct.record.EstimatedCostUSD = float64(ct.record.CallCount) * costPerCall
	return ct.persist()
}

// ReadFile implements FileSource, tracking the call and enforcing budget.
func (ct *CostTracker) ReadFile(ctx context.Context, repo, revision, path string) ([]byte, error) {
	ct.mu.Lock()
	if err := ct.increment(); err != nil {
		ct.mu.Unlock()
		return nil, err
	}
	ct.mu.Unlock()
	return ct.inner.ReadFile(ctx, repo, revision, path)
}

// ListFiles implements FileSource, tracking the call and enforcing budget.
func (ct *CostTracker) ListFiles(ctx context.Context, repo, revision, pattern string) ([]string, error) {
	ct.mu.Lock()
	if err := ct.increment(); err != nil {
		ct.mu.Unlock()
		return nil, err
	}
	ct.mu.Unlock()
	return ct.inner.ListFiles(ctx, repo, revision, pattern)
}

// DiffBetween implements FileSource, tracking the call and enforcing budget.
func (ct *CostTracker) DiffBetween(ctx context.Context, repo, fromRev, toRev string) ([]gitdiff.FileChange, error) {
	ct.mu.Lock()
	if err := ct.increment(); err != nil {
		ct.mu.Unlock()
		return nil, err
	}
	ct.mu.Unlock()
	return ct.inner.DiffBetween(ctx, repo, fromRev, toRev)
}

// CallCount returns the current daily call count.
func (ct *CostTracker) CallCount() int {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.record.CallCount
}
