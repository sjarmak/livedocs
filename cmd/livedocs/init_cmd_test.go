package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestInitCommandRegistered(t *testing.T) {
	registered := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		registered[cmd.Name()] = true
	}
	if !registered["init"] {
		t.Error("init subcommand not registered on root command")
	}
}

func TestEnrichmentGuidanceWhenTokenUnset(t *testing.T) {
	// Ensure SRC_ACCESS_TOKEN is not set for this test.
	orig := os.Getenv("SRC_ACCESS_TOKEN")
	os.Unsetenv("SRC_ACCESS_TOKEN")
	t.Cleanup(func() {
		if orig != "" {
			os.Setenv("SRC_ACCESS_TOKEN", orig)
		}
	})

	dir := t.TempDir()

	var buf bytes.Buffer
	cmd := rootCmd
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"init", dir})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "To add semantic context:") {
		t.Errorf("expected enrichment guidance in output when SRC_ACCESS_TOKEN is unset, got:\n%s", output)
	}
	if !strings.Contains(output, "livedocs enrich --data-dir .livedocs/ --initial") {
		t.Errorf("expected enrich command example in output, got:\n%s", output)
	}
}

func TestEnrichmentGuidanceAbsentWhenTokenSet(t *testing.T) {
	// Set SRC_ACCESS_TOKEN.
	orig := os.Getenv("SRC_ACCESS_TOKEN")
	os.Setenv("SRC_ACCESS_TOKEN", "test-token-value")
	t.Cleanup(func() {
		if orig != "" {
			os.Setenv("SRC_ACCESS_TOKEN", orig)
		} else {
			os.Unsetenv("SRC_ACCESS_TOKEN")
		}
	})

	dir := t.TempDir()

	var buf bytes.Buffer
	cmd := rootCmd
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"init", dir})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	output := buf.String()
	if strings.Contains(output, "To add semantic context:") {
		t.Errorf("expected NO enrichment guidance when SRC_ACCESS_TOKEN is set, got:\n%s", output)
	}
}
