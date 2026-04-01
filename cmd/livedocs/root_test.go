package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSubcommandsRegistered(t *testing.T) {
	want := []string{"check", "init", "mcp", "version"}
	registered := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		registered[cmd.Name()] = true
	}
	for _, name := range want {
		if !registered[name] {
			t.Errorf("subcommand %q not registered on root command", name)
		}
	}
}

func TestVersionOutput(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"version"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "livedocs") {
		t.Errorf("version output missing 'livedocs': %q", out)
	}
	if !strings.Contains(out, "dev") {
		t.Errorf("version output missing default version 'dev': %q", out)
	}
}

func TestHelpContainsSubcommands(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"--help"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("help failed: %v", err)
	}
	out := buf.String()
	for _, sub := range []string{"check", "init", "mcp", "version"} {
		if !strings.Contains(out, sub) {
			t.Errorf("help output missing subcommand %q", sub)
		}
	}
}

func TestInitCommand(t *testing.T) {
	dir := t.TempDir()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"init", dir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("init command failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Created .livedocs.yaml") {
		t.Errorf("init output missing config creation message: %q", out)
	}
	if !strings.Contains(out, "Summary") {
		t.Errorf("init output missing summary: %q", out)
	}
}

func TestCheckCommand_Format(t *testing.T) {
	// Run check on a temp dir with no markdown files — should succeed with no drift.
	dir := t.TempDir()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"check", "--format=json", dir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("check --format=json failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "has_drift") {
		t.Errorf("JSON output missing 'has_drift': %q", out)
	}
}
