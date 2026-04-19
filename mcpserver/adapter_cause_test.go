// Package mcpserver — adapter_cause_test.go covers ResultCause across:
//  1. nil ToolResult input
//  2. results constructed via NewTextResult / NewErrorResult (no cause)
//  3. results constructed via NewErrorResultWithCause (cause attached)
//  4. foreign ToolResult implementations that satisfy the exported Causer
//     interface (they MUST have their cause returned)
//  5. foreign ToolResult implementations that do NOT satisfy Causer (cause
//     must be nil — preserves the prior "unknown impls have no cause"
//     behavior)
//
// See live_docs-m7v.38: prior implementation type-asserted to *resultAdapter
// directly, so foreign impls (test doubles in other packages, future
// alternative wrappers) silently could not carry a cause. The Causer
// interface fixes that without breaking ToolResult's existing contract.
package mcpserver

import (
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// sentinelErr is a per-test sentinel used to exercise errors.Is semantics
// through ResultCause without depending on production sentinels (e.g.
// ErrRateLimited) whose ownership is tracked under separate beads.
var sentinelErr = errors.New("adapter_cause_test: sentinel")

// foreignWithCause is a stand-in for a test double living in another
// package that wants to participate in the cause-propagation contract.
// It satisfies BOTH ToolResult and Causer. The only state it carries is
// the cause it should return — the other ToolResult methods return
// trivial values because the test only inspects ResultCause behavior.
type foreignWithCause struct {
	cause error
}

func (f *foreignWithCause) IsError() bool               { return f.cause != nil }
func (f *foreignWithCause) Text() string                { return "" }
func (f *foreignWithCause) Unwrap() *mcp.CallToolResult { return nil }
func (f *foreignWithCause) Cause() error                { return f.cause }

// foreignWithoutCause is a stand-in for a foreign test double that
// satisfies ToolResult but does NOT opt in to Causer. ResultCause must
// return nil for it (matching the documented "no cause attached" semantics
// for unknown implementations).
type foreignWithoutCause struct{}

func (f *foreignWithoutCause) IsError() bool               { return true }
func (f *foreignWithoutCause) Text() string                { return "" }
func (f *foreignWithoutCause) Unwrap() *mcp.CallToolResult { return nil }

func TestResultCause_NilResult(t *testing.T) {
	t.Parallel()
	if got := ResultCause(nil); got != nil {
		t.Fatalf("ResultCause(nil) = %v, want nil", got)
	}
}

func TestResultCause_TextResult(t *testing.T) {
	t.Parallel()
	r := NewTextResult("ok")
	if got := ResultCause(r); got != nil {
		t.Fatalf("ResultCause(NewTextResult) = %v, want nil", got)
	}
}

func TestResultCause_ErrorResultWithoutCause(t *testing.T) {
	t.Parallel()
	r := NewErrorResult("boom")
	if got := ResultCause(r); got != nil {
		t.Fatalf("ResultCause(NewErrorResult) = %v, want nil", got)
	}
}

func TestResultCause_ErrorResultfWithoutCause(t *testing.T) {
	t.Parallel()
	r := NewErrorResultf("boom %d", 42)
	if got := ResultCause(r); got != nil {
		t.Fatalf("ResultCause(NewErrorResultf) = %v, want nil", got)
	}
}

func TestResultCause_ErrorResultWithCause(t *testing.T) {
	t.Parallel()
	r := NewErrorResultWithCause("denied", sentinelErr)
	got := ResultCause(r)
	if !errors.Is(got, sentinelErr) {
		t.Fatalf("ResultCause(NewErrorResultWithCause) = %v, want %v", got, sentinelErr)
	}
}

func TestResultCause_ErrorResultWithNilCause(t *testing.T) {
	t.Parallel()
	// Constructing with nil cause is allowed (the godoc tells callers to
	// prefer NewErrorResult, but nil-safety is required).
	r := NewErrorResultWithCause("denied", nil)
	if got := ResultCause(r); got != nil {
		t.Fatalf("ResultCause(NewErrorResultWithCause(_, nil)) = %v, want nil", got)
	}
}

func TestResultCause_ForeignImplementationWithCauser(t *testing.T) {
	t.Parallel()
	r := &foreignWithCause{cause: sentinelErr}
	got := ResultCause(r)
	if !errors.Is(got, sentinelErr) {
		t.Fatalf("ResultCause(foreignWithCause) = %v, want %v", got, sentinelErr)
	}
}

func TestResultCause_ForeignImplementationWithCauserNilCause(t *testing.T) {
	t.Parallel()
	r := &foreignWithCause{cause: nil}
	if got := ResultCause(r); got != nil {
		t.Fatalf("ResultCause(foreignWithCause{nil}) = %v, want nil", got)
	}
}

func TestResultCause_ForeignImplementationWithoutCauser(t *testing.T) {
	t.Parallel()
	r := &foreignWithoutCause{}
	if got := ResultCause(r); got != nil {
		t.Fatalf("ResultCause(foreignWithoutCause) = %v, want nil", got)
	}
}

// TestResultAdapterImplementsCauser is a compile-time check that the
// concrete adapter satisfies the new Causer interface, so production code
// (NewErrorResultWithCause) keeps participating in the contract.
func TestResultAdapterImplementsCauser(t *testing.T) {
	t.Parallel()
	var _ Causer = (*resultAdapter)(nil)
}
