package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// resetCmdFlags restores every local flag on cmd to its declared default value
// and clears the Changed marker, so flag state does not leak between successive
// invocations of the same cobra.Command.
//
// Background: pflag.Parse only mutates flags named in the current args. Even
// though m7v.35 moved flag reads inside RunE via cmd.Flags().GetX(), the
// underlying pflag.Flag still stores the parsed value across Execute() calls.
// In a long-lived process (or test process), a previous invocation that set
// --some-flag leaves --some-flag set for every later invocation that omits it.
//
// The recommended pattern is to call this helper at the END of RunE (via defer)
// so the next invocation starts from defaults and only the args explicitly in
// that invocation take effect. See live_docs-m7v.28 for the original observation
// on verify-claims and live_docs-m7v.35 for the converged refactor.
//
// On Set error the helper logs a warning to cmd.ErrOrStderr() and continues
// processing remaining flags. Changed is cleared unconditionally.
func resetCmdFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if err := f.Value.Set(f.DefValue); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to reset flag %q to default %q: %v\n", f.Name, f.DefValue, err)
		}
		f.Changed = false
	})
}

// mustGetX helpers (live_docs-m7v.46)
//
// Each helper reads a typed flag value via cmd.Flags().GetX(name) and panics
// if the lookup fails. The error returned by pflag.GetX only fires when the
// flag is unknown to cmd or has been registered with an incompatible type —
// both are structural bugs (a typo in a flag name, or a mismatched registration)
// that should crash the CLI immediately rather than silently return a zero value.
//
// Why panic instead of returning the error: every caller in cmd/livedocs that
// reads a flag previously discarded the error with `_, _ := …`, which errcheck
// flags as a violation. Centralizing the discard behind a panic makes intent
// explicit (the lookup cannot fail at runtime) and removes 90+ errcheck
// suppressions across the package.
//
// All flag names passed to these helpers MUST match a flag registered on the
// same cmd via Flags().StringVar* / Flags().BoolVar* / etc., normally inside
// the package's init() functions.

func flagPanic(cmd *cobra.Command, name, kind string, err error) {
	panic(fmt.Sprintf("livedocs: missing or wrong-typed %s flag %q on command %q: %v",
		kind, name, cmd.Name(), err))
}

// mustGetString returns the value of a string flag and panics if name is not
// registered on cmd or is registered as a non-string flag.
func mustGetString(cmd *cobra.Command, name string) string {
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		flagPanic(cmd, name, "string", err)
	}
	return v
}

// mustGetBool returns the value of a bool flag (see mustGetString for panic
// semantics).
func mustGetBool(cmd *cobra.Command, name string) bool {
	v, err := cmd.Flags().GetBool(name)
	if err != nil {
		flagPanic(cmd, name, "bool", err)
	}
	return v
}

// mustGetInt returns the value of an int flag (see mustGetString for panic
// semantics).
func mustGetInt(cmd *cobra.Command, name string) int {
	v, err := cmd.Flags().GetInt(name)
	if err != nil {
		flagPanic(cmd, name, "int", err)
	}
	return v
}

// mustGetInt64 returns the value of an int64 flag (see mustGetString for panic
// semantics).
func mustGetInt64(cmd *cobra.Command, name string) int64 {
	v, err := cmd.Flags().GetInt64(name)
	if err != nil {
		flagPanic(cmd, name, "int64", err)
	}
	return v
}

// mustGetDuration returns the value of a duration flag (see mustGetString for
// panic semantics).
func mustGetDuration(cmd *cobra.Command, name string) time.Duration {
	v, err := cmd.Flags().GetDuration(name)
	if err != nil {
		flagPanic(cmd, name, "duration", err)
	}
	return v
}

// mustGetStringSlice returns the value of a string-slice flag (see
// mustGetString for panic semantics).
func mustGetStringSlice(cmd *cobra.Command, name string) []string {
	v, err := cmd.Flags().GetStringSlice(name)
	if err != nil {
		flagPanic(cmd, name, "string-slice", err)
	}
	return v
}
