package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTribalReportFact_WrongReason(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalReportFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"fact_id":  1,
		"reason":   "wrong",
		"details":  "This fact is factually incorrect",
		"reporter": "test-user",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp tribalReportResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Reason != "wrong" {
		t.Errorf("expected reason 'wrong', got %q", resp.Reason)
	}
	if resp.Status != "recorded" {
		t.Errorf("expected status 'recorded', got %q", resp.Status)
	}
	if resp.FeedbackID == 0 {
		t.Error("expected non-zero feedback ID")
	}
}

func TestTribalReportFact_StaleReason(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalReportFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"fact_id": 1,
		"reason":  "stale",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp tribalReportResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Reason != "stale" {
		t.Errorf("expected reason 'stale', got %q", resp.Reason)
	}
}

func TestTribalReportFact_MisleadingReason(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalReportFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"fact_id": 1,
		"reason":  "misleading",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp tribalReportResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Reason != "misleading" {
		t.Errorf("expected reason 'misleading', got %q", resp.Reason)
	}
}

func TestTribalReportFact_OffensiveReason(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalReportFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"fact_id": 1,
		"reason":  "offensive",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp tribalReportResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Reason != "offensive" {
		t.Errorf("expected reason 'offensive', got %q", resp.Reason)
	}
}

func TestTribalReportFact_InvalidReason(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalReportFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"fact_id": 1,
		"reason":  "invalid-reason",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result for invalid reason, got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "invalid reason") {
		t.Errorf("expected error mentioning 'invalid reason', got: %s", result.Text())
	}
}

func TestTribalReportFact_MissingFactID(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalReportFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"reason": "wrong",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result for missing fact_id, got: %s", result.Text())
	}
}

func TestTribalReportFact_MissingReason(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalReportFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"fact_id": 1,
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result for missing reason, got: %s", result.Text())
	}
}

func TestTribalReportFact_NonexistentFact(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalReportFactHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"fact_id": 99999,
		"reason":  "wrong",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result for nonexistent fact, got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "not found") {
		t.Errorf("expected error mentioning 'not found', got: %s", result.Text())
	}
}

func TestTribalReportFact_DetailsTooLong(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalReportFactHandler(pool)

	longDetails := strings.Repeat("x", 4097)
	req := &tribalFakeRequest{args: map[string]any{
		"fact_id": 1,
		"reason":  "wrong",
		"details": longDetails,
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError() {
		t.Fatalf("expected error result for details too long, got: %s", result.Text())
	}
	if !strings.Contains(result.Text(), "too long") {
		t.Errorf("expected error mentioning 'too long', got: %s", result.Text())
	}
}

func TestTribalReportFact_DefaultReporter(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalReportFactHandler(pool)

	// No reporter specified — should default to "anonymous".
	req := &tribalFakeRequest{args: map[string]any{
		"fact_id": 1,
		"reason":  "wrong",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp tribalReportResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Status != "recorded" {
		t.Errorf("expected status 'recorded', got %q", resp.Status)
	}
}

// TestDegradedFlag_LLMLowCorroboration verifies that LLM-extracted facts
// with corroboration < 3 have the degraded flag set in the envelope.
func TestDegradedFlag_LLMLowCorroboration(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalContextForSymbolHandler(pool)

	// Explicitly pass min_confidence=0.0 to disable corroboration gate
	// and see all facts including uncorroborated ones.
	req := &tribalFakeRequest{args: map[string]any{
		"symbol":         "NewServer",
		"min_confidence": 0.0,
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError() {
		t.Fatalf("unexpected error result: %s", result.Text())
	}

	var resp tribalResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	for _, fact := range resp.Facts {
		isLLM := fact.Model != ""
		lowCorr := fact.Corroboration < 3
		if isLLM && lowCorr && !fact.Degraded {
			t.Errorf("fact kind=%q model=%q corr=%d: expected degraded=true", fact.Kind, fact.Model, fact.Corroboration)
		}
		if !isLLM && fact.Degraded {
			t.Errorf("fact kind=%q model=%q: deterministic fact should not be degraded", fact.Kind, fact.Model)
		}
	}

	// The invariant fact (model="", corroboration=3) should NOT be degraded.
	// The ownership fact (model=LLM, corroboration=2) should be degraded.
	// The rationale/quirk facts (model=LLM, corroboration=1) should be degraded.
	degradedCount := 0
	for _, fact := range resp.Facts {
		if fact.Degraded {
			degradedCount++
		}
	}
	// 3 LLM facts all have corroboration < 3, so 3 should be degraded.
	if degradedCount != 3 {
		t.Errorf("expected 3 degraded facts, got %d", degradedCount)
	}
}

// TestDegradedFlag_DeterministicNotDegraded verifies that deterministic
// facts (model="") are never marked as degraded regardless of corroboration.
func TestDegradedFlag_DeterministicNotDegraded(t *testing.T) {
	pool := setupTribalTestDB(t)
	handler := TribalContextForSymbolHandler(pool)

	req := &tribalFakeRequest{args: map[string]any{
		"symbol": "NewServer",
		"kinds":  "invariant",
	}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp tribalResponse
	if err := json.Unmarshal([]byte(result.Text()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	for _, fact := range resp.Facts {
		if fact.Degraded {
			t.Errorf("deterministic fact should not be degraded: kind=%q", fact.Kind)
		}
	}
}
