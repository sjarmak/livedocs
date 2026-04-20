package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestAllSubcommands_HelpSmokeRunsWithoutPanic walks the entire rootCmd tree
// and invokes `--help` on every leaf command. The test asserts that:
//
//  1. No subcommand panics during flag registration or help rendering.
//  2. Every leaf emits non-empty help output that contains the leaf's name.
//
// Scope (honest framing for live_docs-m7v.47):
//
// This test catches **registration-class** bugs — e.g. a PersistentPreRun or
// init() that panics at load time, a malformed flag definition that only
// surfaces when cobra builds help text, or a broken subcommand wiring that
// makes the leaf unreachable from rootCmd.
//
// It does NOT catch **consumer-side typos** in RunE string literals like
// `mustGetString(cmd, "flag-nam")`. Those only surface when the actual RunE
// branch executes, and catching them requires per-command execution fixtures
// with valid args — a significantly heavier investment. The well-traveled
// commands (verify, verify-claims, extract, tribal, watch, enrich, export)
// already have execution-path coverage in their respective _test.go files.
//
// The `--help` flag is auto-registered by cobra and short-circuits RunE (help
// output prints, then exit 0 without executing the command body). That is the
// point: we exercise flag registration + help rendering across every leaf
// cheaply, without needing per-command fixtures.
func TestAllSubcommands_HelpSmokeRunsWithoutPanic(t *testing.T) {
	leaves := collectLeafCommands(rootCmd)
	if len(leaves) == 0 {
		t.Fatal("no leaf commands found on rootCmd; walker or rootCmd wiring is broken")
	}

	// Save and restore rootCmd streams and args so we do not bleed state into
	// other tests in this package.
	origOut := rootCmd.OutOrStdout()
	origErr := rootCmd.ErrOrStderr()
	t.Cleanup(func() {
		rootCmd.SetOut(origOut)
		rootCmd.SetErr(origErr)
		rootCmd.SetArgs(nil)
	})

	for _, leaf := range leaves {
		path := commandPath(leaf)
		t.Run(strings.Join(path, "_"), func(t *testing.T) {
			buf := new(bytes.Buffer)
			rootCmd.SetOut(buf)
			rootCmd.SetErr(buf)
			rootCmd.SetArgs(append(append([]string{}, path...), "--help"))

			panicMsg := runRecoveringPanic(func() {
				// Execute may return nil for --help on some cobra versions
				// and pflag.ErrHelp on others; we care about panics and
				// rendered help text, not the return value.
				_ = rootCmd.Execute()
			})
			// Always clear flag state on the leaf after Execute, regardless
			// of panic/pass. --help short-circuits RunE, so the package's
			// usual `defer resetCmdFlags(cmd)` does not fire — without this
			// the -h/--help flag (and any local flags parsed from the args)
			// stays set and leaks into other tests that share rootCmd.
			resetCmdFlags(leaf)
			if panicMsg != "" {
				t.Fatalf("rootCmd.Execute(%v --help) panicked: %s", path, panicMsg)
			}

			got := buf.String()
			if got == "" {
				t.Fatalf("rootCmd.Execute(%v --help) produced no output", path)
			}
			// Cobra's default help contains the full command path ending in
			// the leaf name. Asserting containment of the last path segment
			// pins that help was rendered for *this* command specifically.
			if !strings.Contains(got, leaf.Name()) {
				t.Errorf("help output for %v does not contain leaf name %q\noutput: %q",
					path, leaf.Name(), got)
			}
		})
	}
}

// collectLeafCommands returns every command in the rootCmd subtree that has no
// children of its own. The root itself is excluded because `livedocs --help`
// without a subcommand exercises only root-level flag registration, which
// offers no unique coverage beyond what individual leaves provide.
func collectLeafCommands(root *cobra.Command) []*cobra.Command {
	var leaves []*cobra.Command
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		children := c.Commands()
		// Cobra auto-injects a "help" subcommand on any command that has
		// children. Skip it: its help text is generic and not a real leaf.
		realChildren := make([]*cobra.Command, 0, len(children))
		for _, child := range children {
			if child.Name() == "help" {
				continue
			}
			realChildren = append(realChildren, child)
		}
		if len(realChildren) == 0 {
			if c != root {
				leaves = append(leaves, c)
			}
			return
		}
		for _, child := range realChildren {
			walk(child)
		}
	}
	walk(root)
	return leaves
}

// commandPath returns the chain of command names from (but excluding) the
// root down to cmd. For a leaf like `tribal status`, the returned slice is
// ["tribal", "status"].
func commandPath(cmd *cobra.Command) []string {
	var parts []string
	for c := cmd; c != nil && c.HasParent(); c = c.Parent() {
		parts = append([]string{c.Name()}, parts...)
	}
	return parts
}

// runRecoveringPanic executes fn and returns a formatted message if fn
// panicked, or "" if it returned normally.
func runRecoveringPanic(fn func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprintf("%v", r)
		}
	}()
	fn()
	return ""
}
