package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDiffHelpShowsRepoFlag(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"diff", "--help"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("diff --help failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "--repo") {
		t.Errorf("diff help output missing --repo flag: %q", out)
	}
	if !strings.Contains(out, "repository name") {
		t.Errorf("diff help output missing repo flag description: %q", out)
	}
}

func TestDiffRepoFlagDefault(t *testing.T) {
	// Verify the diffRepo variable defaults to empty string,
	// which triggers the filepath.Base fallback in runDiff.
	if diffRepo != "" {
		t.Errorf("diffRepo default should be empty, got %q", diffRepo)
	}

	// Check the flag is registered with empty default.
	flag := diffCmd.Flags().Lookup("repo")
	if flag == nil {
		t.Fatal("--repo flag not registered on diff command")
	}
	if flag.DefValue != "" {
		t.Errorf("--repo default value should be empty, got %q", flag.DefValue)
	}
}
