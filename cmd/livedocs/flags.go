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
