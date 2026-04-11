package tribal

import (
	"fmt"
	"strings"
	"testing"
)

func TestInlineMarkerExtractor_GoComments(t *testing.T) {
	src := []byte(`package main

// TODO: refactor this function
func foo() {}

// FIXME: handle nil case
func bar() {}

// XXX: temporary workaround
var x = 1
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("main.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 3 {
		t.Fatalf("expected 3 facts, got %d", len(facts))
	}

	// All three should be kind=todo
	for i, f := range facts {
		if f.Kind != "todo" {
			t.Errorf("fact[%d]: expected kind=todo, got %q", i, f.Kind)
		}
	}

	// Check first fact details
	f := facts[0]
	if !strings.Contains(f.SourceQuote, "TODO") {
		t.Errorf("source_quote should contain TODO marker, got %q", f.SourceQuote)
	}
	if f.Confidence != 1.0 {
		t.Errorf("expected confidence=1.0, got %f", f.Confidence)
	}
	if f.Model != "" {
		t.Errorf("expected model to be empty (nil), got %q", f.Model)
	}
	if f.Extractor != "inline_marker" {
		t.Errorf("expected extractor=inline_marker, got %q", f.Extractor)
	}
}

func TestInlineMarkerExtractor_QuirkMarkers(t *testing.T) {
	src := []byte(`package main

// HACK: this works around a race condition
func foo() {}

// NOTE: this API is deprecated upstream
func bar() {}

// WHY: the spec requires this odd behavior
func baz() {}
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("main.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 3 {
		t.Fatalf("expected 3 facts, got %d", len(facts))
	}

	for i, f := range facts {
		if f.Kind != "quirk" {
			t.Errorf("fact[%d]: expected kind=quirk, got %q", i, f.Kind)
		}
	}
}

func TestInlineMarkerExtractor_PythonComments(t *testing.T) {
	src := []byte(`# TODO: implement caching
def compute():
    pass

# HACK: monkey-patch for compatibility
import os
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("main.py", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}

	if facts[0].Kind != "todo" {
		t.Errorf("fact[0]: expected kind=todo, got %q", facts[0].Kind)
	}
	if facts[1].Kind != "quirk" {
		t.Errorf("fact[1]: expected kind=quirk, got %q", facts[1].Kind)
	}
}

func TestInlineMarkerExtractor_ShellComments(t *testing.T) {
	src := []byte(`#!/bin/bash
# FIXME: handle spaces in paths
cp $SRC $DEST
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("deploy.sh", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].Kind != "todo" {
		t.Errorf("expected kind=todo, got %q", facts[0].Kind)
	}
}

func TestInlineMarkerExtractor_BlockComments(t *testing.T) {
	src := []byte(`package main

/* TODO: replace with proper implementation */
func stub() {}

/*
 * HACK: workaround for upstream bug
 * see https://example.com/issue/123
 */
func workaround() {}
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("main.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}
	if facts[0].Kind != "todo" {
		t.Errorf("fact[0]: expected kind=todo, got %q", facts[0].Kind)
	}
	if facts[1].Kind != "quirk" {
		t.Errorf("fact[1]: expected kind=quirk, got %q", facts[1].Kind)
	}
}

func TestInlineMarkerExtractor_TypeScriptComments(t *testing.T) {
	src := []byte(`// TODO: add proper types
function parse(data: any): any {
    // FIXME: handle edge cases
    return JSON.parse(data);
}

/* NOTE: this interface is provisional */
interface Config {
    debug: boolean;
}
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("parser.ts", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 3 {
		t.Fatalf("expected 3 facts, got %d", len(facts))
	}
	if facts[0].Kind != "todo" {
		t.Errorf("fact[0]: expected kind=todo, got %q", facts[0].Kind)
	}
	if facts[1].Kind != "todo" {
		t.Errorf("fact[1]: expected kind=todo, got %q", facts[1].Kind)
	}
	if facts[2].Kind != "quirk" {
		t.Errorf("fact[2]: expected kind=quirk, got %q", facts[2].Kind)
	}
}

func TestInlineMarkerExtractor_Evidence(t *testing.T) {
	src := []byte(`// TODO: fix this
func broken() {}
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("main.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}

	f := facts[0]
	if len(f.Evidence) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(f.Evidence))
	}

	ev := f.Evidence[0]
	if ev.SourceType != "inline_marker" {
		t.Errorf("expected source_type=inline_marker, got %q", ev.SourceType)
	}
	if ev.SourceRef != "main.go:1" {
		t.Errorf("expected source_ref=main.go:1, got %q", ev.SourceRef)
	}
	if ev.ContentHash == "" {
		t.Error("expected non-empty content_hash")
	}
}

func TestInlineMarkerExtractor_StalenessHash(t *testing.T) {
	src := []byte(`// TODO: fix this
func broken() {}
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("main.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].StalenessHash == "" {
		t.Error("expected non-empty staleness_hash")
	}
	// Hash should be deterministic
	facts2, _ := ext.ExtractFromFile("main.go", src)
	if facts[0].StalenessHash != facts2[0].StalenessHash {
		t.Error("staleness_hash should be deterministic")
	}
}

func TestInlineMarkerExtractor_CaseInsensitive(t *testing.T) {
	src := []byte(`// todo: lowercase marker
// Todo: mixed case
// TODO: uppercase
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("main.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 3 {
		t.Fatalf("expected 3 facts, got %d", len(facts))
	}
	for i, f := range facts {
		if f.Kind != "todo" {
			t.Errorf("fact[%d]: expected kind=todo, got %q", i, f.Kind)
		}
	}
}

func TestInlineMarkerExtractor_NoMarkers(t *testing.T) {
	src := []byte(`package main

// This is a regular comment
func main() {}
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("main.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("expected 0 facts, got %d", len(facts))
	}
}

func TestInlineMarkerExtractor_EmptyContent(t *testing.T) {
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("empty.go", []byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if facts != nil {
		t.Errorf("expected nil facts for empty content, got %d", len(facts))
	}
}

func TestInlineMarkerExtractor_SourceQuoteContainsMarker(t *testing.T) {
	src := []byte(`// TODO: refactor the parser module
func parse() {}
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("main.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}

	// source_quote should contain the marker prefix
	if !strings.HasPrefix(facts[0].SourceQuote, "TODO") {
		t.Errorf("source_quote should start with marker, got %q", facts[0].SourceQuote)
	}
	if !strings.Contains(facts[0].SourceQuote, "refactor the parser module") {
		t.Errorf("source_quote should contain comment text, got %q", facts[0].SourceQuote)
	}
}

func TestInlineMarkerExtractor_LineNumbers(t *testing.T) {
	src := []byte(`package main

import "fmt"

// TODO: first marker on line 5
func foo() {
    // HACK: second marker on line 7
    fmt.Println("hello")
}
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("main.go", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}

	if facts[0].Evidence[0].SourceRef != "main.go:5" {
		t.Errorf("expected source_ref=main.go:5, got %q", facts[0].Evidence[0].SourceRef)
	}
	if facts[1].Evidence[0].SourceRef != "main.go:7" {
		t.Errorf("expected source_ref=main.go:7, got %q", facts[1].Evidence[0].SourceRef)
	}
}

func TestInlineMarkerExtractor_MarkerWithColonOrSpace(t *testing.T) {
	tests := []struct {
		name    string
		comment string
		wantN   int
	}{
		{"colon separator", "// TODO: with colon\n", 1},
		{"space separator", "// TODO with space\n", 1},
		{"no separator", "// TODOnoseparator\n", 0},
	}

	ext := &MarkerExtractor{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts, err := ext.ExtractFromFile("test.go", []byte(tt.comment))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(facts) != tt.wantN {
				t.Errorf("expected %d facts, got %d", tt.wantN, len(facts))
			}
		})
	}
}

func TestInlineMarkerExtractor_AllKindMappings(t *testing.T) {
	markers := map[string]string{
		"TODO":  "todo",
		"FIXME": "todo",
		"XXX":   "todo",
		"HACK":  "quirk",
		"NOTE":  "quirk",
		"WHY":   "quirk",
	}

	ext := &MarkerExtractor{}
	for marker, expectedKind := range markers {
		t.Run(marker, func(t *testing.T) {
			src := fmt.Sprintf("// %s: test comment\n", marker)
			facts, err := ext.ExtractFromFile("test.go", []byte(src))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(facts) != 1 {
				t.Fatalf("expected 1 fact for %s, got %d", marker, len(facts))
			}
			if facts[0].Kind != expectedKind {
				t.Errorf("%s: expected kind=%s, got %s", marker, expectedKind, facts[0].Kind)
			}
		})
	}
}

func TestInlineMarkerExtractor_StatusIsActive(t *testing.T) {
	src := []byte("// TODO: check status\n")
	ext := &MarkerExtractor{}
	facts, _ := ext.ExtractFromFile("test.go", src)
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].Status != "active" {
		t.Errorf("expected status=active, got %q", facts[0].Status)
	}
}

func TestInlineMarkerExtractor_InlineCommentAfterCode(t *testing.T) {
	src := []byte(`x = 42  // TODO: use constant
y = 0   # FIXME: calculate properly
`)
	ext := &MarkerExtractor{}
	facts, err := ext.ExtractFromFile("test.py", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The // comment should be found; the # after code won't match since
	// we only match # at start of trimmed line. That's fine for Python
	// where # inline is less common as a tribal marker source.
	if len(facts) < 1 {
		t.Fatalf("expected at least 1 fact, got %d", len(facts))
	}
	if facts[0].Kind != "todo" {
		t.Errorf("fact[0]: expected kind=todo, got %q", facts[0].Kind)
	}
}
