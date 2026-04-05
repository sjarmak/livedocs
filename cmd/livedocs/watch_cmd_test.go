package main

import (
	"testing"
	"time"
)

func TestWatchCmd_EnrichFlagExists(t *testing.T) {
	f := watchCmd.Flags().Lookup("enrich")
	if f == nil {
		t.Fatal("--enrich flag not registered on watch command")
	}
	if f.DefValue != "false" {
		t.Fatalf("--enrich default should be false, got %s", f.DefValue)
	}
}

func TestWatchCmd_EnrichDebounceFlagExists(t *testing.T) {
	f := watchCmd.Flags().Lookup("enrich-debounce")
	if f == nil {
		t.Fatal("--enrich-debounce flag not registered on watch command")
	}
	expected := (5 * time.Second).String()
	if f.DefValue != expected {
		t.Fatalf("--enrich-debounce default should be %s, got %s", expected, f.DefValue)
	}
}

func TestWatchCmd_RegisteredOnRoot(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "watch" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("watch command not registered on root command")
	}
}
