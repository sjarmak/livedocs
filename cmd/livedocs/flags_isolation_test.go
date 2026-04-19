package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestNoFlagStateLeakBetweenInvocations is the converged sentinel for
// live_docs-m7v.28 / m7v.33 / m7v.35: invoking the same subcommand twice in
// the same process with different args must not leak flag state between
// invocations.
//
// The historical bug: pflag.Parse only mutates flags named in the current
// args. Without an explicit reset, a flag set by invocation 1 retains its
// value during invocation 2 even when invocation 2 omits it. This test
// runs `verify` twice — once with --json (producing JSON output) and once
// without (producing the human-readable "Verification Report" text). If the
// --json setting leaks, both calls would emit JSON and the second call's
// human-text assertion would fail.
//
// One sentinel test is enough for the class of standard pflag types —
// every command's RunE installs the same `defer resetCmdFlags(cmd)`
// defense. The companion TestTribalFlagValue_ResetClearsState below
// covers the one custom pflag.Value (tribalFlagValue) that needs its own
// regression guard because it validates Set() arguments and would
// otherwise reject the "" reset call from resetCmdFlags.
func TestNoFlagStateLeakBetweenInvocations(t *testing.T) {
	dir := t.TempDir()

	// Invocation 1: explicitly set --json.
	bufJSON := new(bytes.Buffer)
	rootCmd.SetOut(bufJSON)
	rootCmd.SetErr(bufJSON)
	rootCmd.SetArgs([]string{"verify", "--json", dir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("first invocation (--json) failed: %v", err)
	}
	if !strings.Contains(bufJSON.String(), `"verdict"`) {
		t.Fatalf("expected JSON verdict field in first invocation; got: %q", bufJSON.String())
	}

	// Invocation 2: omit --json. With state leak this would still emit JSON.
	bufHuman := new(bytes.Buffer)
	rootCmd.SetOut(bufHuman)
	rootCmd.SetErr(bufHuman)
	rootCmd.SetArgs([]string{"verify", dir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("second invocation (no --json) failed: %v", err)
	}
	got := bufHuman.String()
	if strings.Contains(got, `"verdict"`) {
		t.Errorf("flag state leaked: second invocation emitted JSON despite omitting --json\noutput: %q", got)
	}
	// The human/no-files-found path emits one of two specific strings —
	// either the report header or the empty-dir message.
	if !strings.Contains(got, "Verification Report") && !strings.Contains(got, "No AI context files found") {
		t.Errorf("expected human output (report header or no-files message) in second invocation; got: %q", got)
	}
}

// TestTribalFlagValue_ResetClearsState guards the custom pflag.Value used
// for --tribal: resetCmdFlags calls f.Value.Set(f.DefValue), and pflag
// captures DefValue from String() at registration — which is "" because
// zero-valued tribalFlagValue stores "". If Set("") errors (the original
// bug surfaced by wave-2 review), resetCmdFlags logs a warning and clears
// Changed but does NOT revert val, so a previously-set --tribal=llm leaks
// into the next invocation. This test pins the reset semantics directly
// on the type so the leak cannot regress.
func TestTribalFlagValue_ResetClearsState(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"deterministic", "llm"} {
		t.Run("after_"+mode, func(t *testing.T) {
			t.Parallel()
			tv := &tribalFlagValue{}
			if err := tv.Set(mode); err != nil {
				t.Fatalf("Set(%q) failed: %v", mode, err)
			}
			if tv.String() != mode {
				t.Fatalf("String() after Set(%q) = %q, want %q", mode, tv.String(), mode)
			}
			// The reset path resetCmdFlags exercises:
			if err := tv.Set(""); err != nil {
				t.Fatalf("Set(\"\") (the reset path) failed: %v", err)
			}
			if tv.String() != "" {
				t.Errorf("after reset, String() = %q, want \"\" (state leaked)", tv.String())
			}
		})
	}

	t.Run("invalid_still_rejected", func(t *testing.T) {
		t.Parallel()
		tv := &tribalFlagValue{}
		if err := tv.Set("bogus"); err == nil {
			t.Fatalf("Set(\"bogus\") should error; the empty-string allow-list must not relax non-empty validation")
		}
	})
}
