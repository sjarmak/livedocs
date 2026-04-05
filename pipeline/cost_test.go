package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/live-docs/live_docs/gitdiff"
)

// costTestFileSource is a minimal FileSource for testing CostTracker.
type costTestFileSource struct{}

func (m *costTestFileSource) ReadFile(_ context.Context, _, _, _ string) ([]byte, error) {
	return []byte("content"), nil
}

func (m *costTestFileSource) ListFiles(_ context.Context, _, _, _ string) ([]string, error) {
	return []string{"a.go", "b.go"}, nil
}

func (m *costTestFileSource) DiffBetween(_ context.Context, _, _, _ string) ([]gitdiff.FileChange, error) {
	return nil, nil
}

func TestCostTracker_BudgetEnforcement(t *testing.T) {
	dir := t.TempDir()
	costFile := filepath.Join(dir, "cost.json")

	ct, err := NewCostTracker(&costTestFileSource{}, costFile, 3)
	if err != nil {
		t.Fatalf("NewCostTracker: %v", err)
	}

	ctx := context.Background()

	// First 3 calls should succeed (one of each method type).
	if _, err := ct.ReadFile(ctx, "r", "rev", "f.go"); err != nil {
		t.Fatalf("call 1: %v", err)
	}
	if _, err := ct.ListFiles(ctx, "r", "rev", "*.go"); err != nil {
		t.Fatalf("call 2: %v", err)
	}
	if _, err := ct.DiffBetween(ctx, "r", "a", "b"); err != nil {
		t.Fatalf("call 3: %v", err)
	}

	// 4th call should fail with budget exceeded.
	_, err = ct.ReadFile(ctx, "r", "rev", "f.go")
	if err == nil {
		t.Fatal("expected budget exceeded error, got nil")
	}
	if !errors.Is(err, ErrDailyBudgetExceeded) {
		t.Fatalf("expected ErrDailyBudgetExceeded, got: %v", err)
	}

	if ct.CallCount() != 3 {
		t.Fatalf("expected call count 3, got %d", ct.CallCount())
	}
}

func TestCostTracker_Persistence(t *testing.T) {
	dir := t.TempDir()
	costFile := filepath.Join(dir, "cost.json")

	ct, err := NewCostTracker(&costTestFileSource{}, costFile, 0)
	if err != nil {
		t.Fatalf("NewCostTracker: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := ct.ReadFile(ctx, "r", "rev", "f.go"); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}

	// Read and verify the persisted cost file.
	data, err := os.ReadFile(costFile)
	if err != nil {
		t.Fatalf("reading cost file: %v", err)
	}

	var rec CostRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("parsing cost file: %v", err)
	}

	if rec.CallCount != 5 {
		t.Fatalf("expected call_count 5, got %d", rec.CallCount)
	}
	expectedCost := 5 * costPerCall
	if rec.EstimatedCostUSD != expectedCost {
		t.Fatalf("expected estimated_cost_usd %f, got %f", expectedCost, rec.EstimatedCostUSD)
	}
	if rec.Date == "" {
		t.Fatal("expected date to be set")
	}
}

func TestCostTracker_UnlimitedBudget(t *testing.T) {
	dir := t.TempDir()
	costFile := filepath.Join(dir, "cost.json")

	ct, err := NewCostTracker(&costTestFileSource{}, costFile, 0)
	if err != nil {
		t.Fatalf("NewCostTracker: %v", err)
	}

	ctx := context.Background()
	// With budget=0, many calls should succeed.
	for i := 0; i < 100; i++ {
		if _, err := ct.ReadFile(ctx, "r", "rev", "f.go"); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}

	if ct.CallCount() != 100 {
		t.Fatalf("expected 100 calls, got %d", ct.CallCount())
	}
}

func TestCostTracker_LoadExistingFile(t *testing.T) {
	dir := t.TempDir()
	costFile := filepath.Join(dir, "cost.json")

	// Pre-seed with today's record.
	ct1, err := NewCostTracker(&costTestFileSource{}, costFile, 0)
	if err != nil {
		t.Fatalf("NewCostTracker: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := ct1.ReadFile(ctx, "r", "rev", "f.go"); err != nil {
			t.Fatalf("seed call %d: %v", i+1, err)
		}
	}

	// Create a new tracker from the same file — should resume count.
	ct2, err := NewCostTracker(&costTestFileSource{}, costFile, 5)
	if err != nil {
		t.Fatalf("NewCostTracker reload: %v", err)
	}

	if ct2.CallCount() != 3 {
		t.Fatalf("expected resumed count 3, got %d", ct2.CallCount())
	}

	// Two more calls should succeed, then the 6th should fail.
	if _, err := ct2.ReadFile(ctx, "r", "rev", "f.go"); err != nil {
		t.Fatalf("call 4: %v", err)
	}
	if _, err := ct2.ReadFile(ctx, "r", "rev", "f.go"); err != nil {
		t.Fatalf("call 5: %v", err)
	}
	_, err = ct2.ReadFile(ctx, "r", "rev", "f.go")
	if !errors.Is(err, ErrDailyBudgetExceeded) {
		t.Fatalf("expected budget exceeded on call 6, got: %v", err)
	}
}
