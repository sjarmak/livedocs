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
// One sentinel test is enough — every command's RunE installs the same
// `defer resetCmdFlags(cmd)` defense, so verifying the pattern once on the
// most-leak-prone command (verify, which has a boolean shorthand flag with
// historical leak reports) covers the class.
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
