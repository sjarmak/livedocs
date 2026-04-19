package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestResetCmdFlags_RestoresDefaults verifies that string, bool, and int flags
// are restored to their declared default values after mutation.
func TestResetCmdFlags_RestoresDefaults(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("name", "default-name", "")
	cmd.Flags().Bool("enabled", false, "")
	cmd.Flags().Int("count", 42, "")

	// Mutate every flag and mark them as Changed.
	if err := cmd.Flags().Set("name", "mutated"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if err := cmd.Flags().Set("enabled", "true"); err != nil {
		t.Fatalf("set enabled: %v", err)
	}
	if err := cmd.Flags().Set("count", "99"); err != nil {
		t.Fatalf("set count: %v", err)
	}

	resetCmdFlags(cmd)

	if got, _ := cmd.Flags().GetString("name"); got != "default-name" {
		t.Errorf("name = %q, want %q", got, "default-name")
	}
	if got, _ := cmd.Flags().GetBool("enabled"); got != false {
		t.Errorf("enabled = %v, want false", got)
	}
	if got, _ := cmd.Flags().GetInt("count"); got != 42 {
		t.Errorf("count = %d, want 42", got)
	}
}

// TestResetCmdFlags_ClearsChanged verifies that Changed=true is reset to false
// after calling resetCmdFlags, so that subsequent Execute() calls do not see
// stale "user-supplied" markers.
func TestResetCmdFlags_ClearsChanged(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("name", "default", "")

	if err := cmd.Flags().Set("name", "user-supplied"); err != nil {
		t.Fatalf("set: %v", err)
	}
	flag := cmd.Flags().Lookup("name")
	if !flag.Changed {
		t.Fatalf("expected Changed=true after Set")
	}

	resetCmdFlags(cmd)

	if flag.Changed {
		t.Errorf("Changed = true, want false after reset")
	}
}

// erroringValue is a pflag.Value implementation that always errors on Set.
// Used to exercise the warning-log code path in resetCmdFlags.
type erroringValue struct{}

func (e *erroringValue) String() string { return "always-error" }
func (e *erroringValue) Type() string   { return "erroringValue" }
func (e *erroringValue) Set(string) error {
	return fmt.Errorf("synthetic set failure")
}

// TestResetCmdFlags_WarnsOnSetError verifies that when a flag's Set returns
// an error, resetCmdFlags writes a warning to cmd.ErrOrStderr() and continues
// processing remaining flags (does not panic or short-circuit).
func TestResetCmdFlags_WarnsOnSetError(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}

	// Register an erroring flag and a normal flag — the helper must process both.
	cmd.Flags().Var(&erroringValue{}, "broken", "always errors on Set")
	cmd.Flags().String("good", "default-good", "")

	if err := cmd.Flags().Set("good", "mutated"); err != nil {
		t.Fatalf("set good: %v", err)
	}

	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	resetCmdFlags(cmd)

	// Warning text should mention the broken flag name and default value.
	got := stderr.String()
	if !strings.Contains(got, `"broken"`) {
		t.Errorf("stderr missing broken flag name; got: %q", got)
	}
	if !strings.Contains(got, "warning: failed to reset flag") {
		t.Errorf("stderr missing expected warning prefix; got: %q", got)
	}
	if !strings.Contains(got, "synthetic set failure") {
		t.Errorf("stderr missing underlying error; got: %q", got)
	}

	// The good flag must still have been reset despite the broken one erroring.
	if s, _ := cmd.Flags().GetString("good"); s != "default-good" {
		t.Errorf("good flag = %q, want %q (helper short-circuited on error)", s, "default-good")
	}

	// Changed must be cleared on the broken flag too — this matches the
	// semantics needed to prevent leak between invocations.
	brokenFlag := cmd.Flags().Lookup("broken")
	if brokenFlag.Changed {
		t.Errorf("broken flag Changed = true, want false (must clear even on Set error)")
	}
}

// TestResetCmdFlags_NoFlags verifies the helper handles a command with no
// registered flags without panicking.
func TestResetCmdFlags_NoFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "empty"}
	// Should be a no-op.
	resetCmdFlags(cmd)
}

// Compile-time guard: erroringValue must satisfy pflag.Value.
var _ pflag.Value = (*erroringValue)(nil)

// --- mustGetX helper tests (live_docs-m7v.46) ---
//
// The mustGetX helpers replace the `_, _ := cmd.Flags().GetX(name)` discard
// convention used by every cmd/livedocs subcommand. They panic when the flag
// is unknown or has the wrong type — both are structural bugs that should crash
// the CLI immediately rather than be masked by a zero-value return.

// assertPanicContains runs fn and fails if it does not panic with a message
// containing every needle.
func assertPanicContains(t *testing.T, fn func(), needles ...string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic, got none")
		}
		msg := fmt.Sprintf("%v", r)
		for _, n := range needles {
			if !strings.Contains(msg, n) {
				t.Errorf("panic message %q missing %q", msg, n)
			}
		}
	}()
	fn()
}

func TestMustGetString(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("name", "default-name", "")
	if got := mustGetString(cmd, "name"); got != "default-name" {
		t.Errorf("mustGetString = %q, want %q", got, "default-name")
	}
	if err := cmd.Flags().Set("name", "v"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := mustGetString(cmd, "name"); got != "v" {
		t.Errorf("mustGetString after Set = %q, want %q", got, "v")
	}
	assertPanicContains(t, func() { mustGetString(cmd, "missing") },
		"missing", "test")
}

func TestMustGetBool(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Bool("enabled", false, "")
	if mustGetBool(cmd, "enabled") {
		t.Errorf("mustGetBool default = true, want false")
	}
	if err := cmd.Flags().Set("enabled", "true"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if !mustGetBool(cmd, "enabled") {
		t.Errorf("mustGetBool after Set = false, want true")
	}
	assertPanicContains(t, func() { mustGetBool(cmd, "missing") },
		"missing", "test")
}

func TestMustGetInt(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Int("count", 7, "")
	if got := mustGetInt(cmd, "count"); got != 7 {
		t.Errorf("mustGetInt = %d, want 7", got)
	}
	assertPanicContains(t, func() { mustGetInt(cmd, "missing") },
		"missing", "test")
}

func TestMustGetInt64(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Int64("count64", 1234567890123, "")
	if got := mustGetInt64(cmd, "count64"); got != 1234567890123 {
		t.Errorf("mustGetInt64 = %d, want 1234567890123", got)
	}
	assertPanicContains(t, func() { mustGetInt64(cmd, "missing") },
		"missing", "test")
}

func TestMustGetDuration(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Duration("interval", 30_000_000_000, "") // 30s
	got := mustGetDuration(cmd, "interval")
	if got.String() != "30s" {
		t.Errorf("mustGetDuration = %s, want 30s", got)
	}
	assertPanicContains(t, func() { mustGetDuration(cmd, "missing") },
		"missing", "test")
}

func TestMustGetStringSlice(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().StringSlice("items", []string{"a", "b"}, "")
	got := mustGetStringSlice(cmd, "items")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("mustGetStringSlice = %v, want [a b]", got)
	}
	assertPanicContains(t, func() { mustGetStringSlice(cmd, "missing") },
		"missing", "test")
}

// TestMustGet_WrongType verifies that asking for the wrong type also panics
// (same code path as unknown flag, but exercised through a real type mismatch).
func TestMustGet_WrongType(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("name", "x", "")
	assertPanicContains(t, func() { mustGetBool(cmd, "name") },
		"name", "test")
}
