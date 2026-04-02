package versionnorm

import (
	"testing"
)

func TestParseReplaceDirectives(t *testing.T) {
	// Simulated go mod edit -json output for kubernetes/kubernetes
	goModJSON := []byte(`{
		"Module": {"Path": "k8s.io/kubernetes"},
		"Go": "1.26.0",
		"Replace": [
			{
				"Old": {"Path": "k8s.io/api"},
				"New": {"Path": "./staging/src/k8s.io/api"}
			},
			{
				"Old": {"Path": "k8s.io/client-go"},
				"New": {"Path": "./staging/src/k8s.io/client-go"}
			},
			{
				"Old": {"Path": "k8s.io/apimachinery"},
				"New": {"Path": "./staging/src/k8s.io/apimachinery"}
			}
		]
	}`)

	directives, err := ParseReplaceDirectives(goModJSON)
	if err != nil {
		t.Fatalf("ParseReplaceDirectives failed: %v", err)
	}

	if len(directives) != 3 {
		t.Fatalf("expected 3 directives, got %d", len(directives))
	}

	// Verify k8s.io/client-go maps to staging path
	found := false
	for _, d := range directives {
		if d.ModulePath == "k8s.io/client-go" {
			found = true
			if d.LocalPath != "./staging/src/k8s.io/client-go" {
				t.Errorf("expected local path ./staging/src/k8s.io/client-go, got %s", d.LocalPath)
			}
			if !d.IsLocal {
				t.Error("expected IsLocal to be true for staging replace")
			}
		}
	}
	if !found {
		t.Error("k8s.io/client-go not found in parsed directives")
	}
}

func TestParseReplaceDirectivesWithVersionedReplace(t *testing.T) {
	// Some replace directives point to a remote module with a version
	goModJSON := []byte(`{
		"Module": {"Path": "example.com/mymodule"},
		"Replace": [
			{
				"Old": {"Path": "github.com/old/pkg", "Version": "v1.0.0"},
				"New": {"Path": "github.com/new/pkg", "Version": "v2.0.0"}
			}
		]
	}`)

	directives, err := ParseReplaceDirectives(goModJSON)
	if err != nil {
		t.Fatalf("ParseReplaceDirectives failed: %v", err)
	}

	if len(directives) != 1 {
		t.Fatalf("expected 1 directive, got %d", len(directives))
	}

	d := directives[0]
	if d.IsLocal {
		t.Error("expected IsLocal to be false for remote replace")
	}
	if d.OldVersion != "v1.0.0" {
		t.Errorf("expected old version v1.0.0, got %s", d.OldVersion)
	}
	if d.NewVersion != "v2.0.0" {
		t.Errorf("expected new version v2.0.0, got %s", d.NewVersion)
	}
}

func TestParseReplaceDirectivesInvalidJSON(t *testing.T) {
	_, err := ParseReplaceDirectives([]byte(`{invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestBuildStagingMap(t *testing.T) {
	goModJSON := []byte(`{
		"Module": {"Path": "k8s.io/kubernetes"},
		"Replace": [
			{
				"Old": {"Path": "k8s.io/api"},
				"New": {"Path": "./staging/src/k8s.io/api"}
			},
			{
				"Old": {"Path": "k8s.io/client-go"},
				"New": {"Path": "./staging/src/k8s.io/client-go"}
			},
			{
				"Old": {"Path": "github.com/some/remote", "Version": "v1.0.0"},
				"New": {"Path": "github.com/some/fork", "Version": "v2.0.0"}
			}
		]
	}`)

	sm, err := BuildStagingMap(goModJSON)
	if err != nil {
		t.Fatalf("BuildStagingMap failed: %v", err)
	}

	// Only local replaces should be in the staging map
	if len(sm) != 2 {
		t.Fatalf("expected 2 staging entries, got %d", len(sm))
	}

	if path, ok := sm["k8s.io/api"]; !ok || path != "./staging/src/k8s.io/api" {
		t.Errorf("unexpected mapping for k8s.io/api: %s", path)
	}

	if _, ok := sm["github.com/some/remote"]; ok {
		t.Error("remote replace should not be in staging map")
	}
}

func TestNormalizeModulePath(t *testing.T) {
	sm := StagingMap{
		"k8s.io/api":          "./staging/src/k8s.io/api",
		"k8s.io/client-go":    "./staging/src/k8s.io/client-go",
		"k8s.io/apimachinery": "./staging/src/k8s.io/apimachinery",
	}

	normalizer := NewNormalizer(sm)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "staging module path unchanged",
			input:    "k8s.io/client-go",
			expected: "k8s.io/client-go",
		},
		{
			name:     "non-staging module path unchanged",
			input:    "github.com/google/go-cmp",
			expected: "github.com/google/go-cmp",
		},
		{
			name:     "subpackage of staging module",
			input:    "k8s.io/client-go/kubernetes",
			expected: "k8s.io/client-go/kubernetes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizer.NormalizeModulePath(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeModulePath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestStripPseudoVersion(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "pseudo-version stripped",
			input:    "k8s.io/api@v0.0.0-20260324094416-91061ea648b7",
			expected: "k8s.io/api",
		},
		{
			name:     "release version stripped",
			input:    "k8s.io/client-go@v0.28.0",
			expected: "k8s.io/client-go",
		},
		{
			name:     "no version suffix unchanged",
			input:    "k8s.io/api",
			expected: "k8s.io/api",
		},
		{
			name:     "subpackage with version",
			input:    "k8s.io/client-go@v0.28.0/kubernetes",
			expected: "k8s.io/client-go/kubernetes",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripVersion(tt.input)
			if result != tt.expected {
				t.Errorf("StripVersion(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsStagingModule(t *testing.T) {
	sm := StagingMap{
		"k8s.io/api":       "./staging/src/k8s.io/api",
		"k8s.io/client-go": "./staging/src/k8s.io/client-go",
	}

	normalizer := NewNormalizer(sm)

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"staging module", "k8s.io/api", true},
		{"staging subpackage", "k8s.io/api/core/v1", true},
		{"non-staging", "github.com/google/go-cmp", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizer.IsStagingModule(tt.input)
			if result != tt.expected {
				t.Errorf("IsStagingModule(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizerCanonicalImportPath(t *testing.T) {
	sm := StagingMap{
		"k8s.io/api":       "./staging/src/k8s.io/api",
		"k8s.io/client-go": "./staging/src/k8s.io/client-go",
	}

	normalizer := NewNormalizer(sm)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "versioned staging import normalized",
			input:    "k8s.io/api@v0.0.0-20260324094416-91061ea648b7",
			expected: "k8s.io/api",
		},
		{
			name:     "versioned staging subpackage",
			input:    "k8s.io/client-go@v0.28.0/kubernetes",
			expected: "k8s.io/client-go/kubernetes",
		},
		{
			name:     "non-staging versioned",
			input:    "github.com/google/go-cmp@v0.7.0",
			expected: "github.com/google/go-cmp",
		},
		{
			name:     "plain import path",
			input:    "k8s.io/api/core/v1",
			expected: "k8s.io/api/core/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizer.CanonicalImportPath(tt.input)
			if result != tt.expected {
				t.Errorf("CanonicalImportPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestResolveStagingToCanonical(t *testing.T) {
	sm := StagingMap{
		"k8s.io/api":       "./staging/src/k8s.io/api",
		"k8s.io/client-go": "./staging/src/k8s.io/client-go",
	}

	normalizer := NewNormalizer(sm)

	// Reverse lookup: given a staging local path, resolve to canonical module
	tests := []struct {
		name     string
		input    string
		expected string
		ok       bool
	}{
		{
			name:     "staging path resolves to module",
			input:    "./staging/src/k8s.io/api",
			expected: "k8s.io/api",
			ok:       true,
		},
		{
			name:     "staging subdir resolves to module subpackage",
			input:    "./staging/src/k8s.io/client-go/kubernetes",
			expected: "k8s.io/client-go/kubernetes",
			ok:       true,
		},
		{
			name:     "non-staging path returns empty",
			input:    "./vendor/github.com/foo",
			expected: "",
			ok:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := normalizer.ResolveStagingPath(tt.input)
			if ok != tt.ok {
				t.Errorf("ResolveStagingPath(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if result != tt.expected {
				t.Errorf("ResolveStagingPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizerWithEmptyStagingMap(t *testing.T) {
	normalizer := NewNormalizer(nil)

	// Should still work — just no staging awareness
	if normalizer.IsStagingModule("k8s.io/api") {
		t.Error("empty staging map should report nothing as staging")
	}

	result := normalizer.CanonicalImportPath("k8s.io/api@v0.28.0")
	if result != "k8s.io/api" {
		t.Errorf("expected k8s.io/api, got %s", result)
	}
}
