package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// resetCmdFlags restores every local flag on cmd to its declared default value
// and clears the Changed marker, so flag state does not leak between successive
// invocations of the same cobra.Command.
//
// Background: pflag.Parse only mutates flags named in the current args, so
// previously set values persist across Execute() calls. Without this reset,
// a test (or process) that runs `<cmd> --some-flag` leaves --some-flag set
// for every later invocation that omits it. See live_docs-m7v.28 for the
// original observation on verify-claims, and live_docs-m7v.36 for the
// extraction of this shared helper.
//
// On Set error the helper logs a warning to cmd.ErrOrStderr() and continues
// processing remaining flags. Changed is cleared unconditionally — preserving
// the behavior of the original per-command reset functions.
func resetCmdFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if err := f.Value.Set(f.DefValue); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to reset flag %q to default %q: %v\n", f.Name, f.DefValue, err)
		}
		f.Changed = false
	})
}
