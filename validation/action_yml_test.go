package validation_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// actionYML mirrors the subset of action.yml we want to validate.
type actionYML struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Inputs      map[string]struct {
		Description string `yaml:"description"`
		Required    bool   `yaml:"required"`
		Default     string `yaml:"default"`
	} `yaml:"inputs"`
	Outputs map[string]struct {
		Description string `yaml:"description"`
		Value       string `yaml:"value"`
	} `yaml:"outputs"`
	Runs struct {
		Using string `yaml:"using"`
		Steps []struct {
			Name string `yaml:"name"`
			ID   string `yaml:"id"`
			Uses string `yaml:"uses"`
		} `yaml:"steps"`
	} `yaml:"runs"`
}

func loadActionYML(t *testing.T) actionYML {
	t.Helper()
	data, err := os.ReadFile("../action.yml")
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}
	var a actionYML
	if err := yaml.Unmarshal(data, &a); err != nil {
		t.Fatalf("parse action.yml: %v", err)
	}
	return a
}

func TestActionYML_ValidYAML(t *testing.T) {
	a := loadActionYML(t)
	if a.Name == "" {
		t.Error("action name must not be empty")
	}
	if a.Description == "" {
		t.Error("action description must not be empty")
	}
}

func TestActionYML_CompositeAction(t *testing.T) {
	a := loadActionYML(t)
	if a.Runs.Using != "composite" {
		t.Errorf("runs.using = %q, want %q", a.Runs.Using, "composite")
	}
}

func TestActionYML_RequiredInputs(t *testing.T) {
	a := loadActionYML(t)

	requiredInputs := []string{"version", "path", "format", "fail-threshold", "github-token"}
	for _, name := range requiredInputs {
		if _, ok := a.Inputs[name]; !ok {
			t.Errorf("missing input %q", name)
		}
	}
}

func TestActionYML_InputDefaults(t *testing.T) {
	a := loadActionYML(t)

	defaults := map[string]string{
		"version": "latest",
		"path":    ".",
		"format":  "json",
	}
	for name, want := range defaults {
		inp, ok := a.Inputs[name]
		if !ok {
			t.Errorf("missing input %q", name)
			continue
		}
		if inp.Default != want {
			t.Errorf("input %q default = %q, want %q", name, inp.Default, want)
		}
	}
}

func TestActionYML_FailThresholdDefault(t *testing.T) {
	a := loadActionYML(t)
	inp, ok := a.Inputs["fail-threshold"]
	if !ok {
		t.Fatal("missing input fail-threshold")
	}
	if inp.Default != "0" {
		t.Errorf("fail-threshold default = %q, want %q", inp.Default, "0")
	}
}

func TestActionYML_RequiredOutputs(t *testing.T) {
	a := loadActionYML(t)

	requiredOutputs := []string{
		"drift-score",
		"total-stale",
		"total-stale-packages",
		"total-undocumented",
		"has-drift",
	}
	for _, name := range requiredOutputs {
		if _, ok := a.Outputs[name]; !ok {
			t.Errorf("missing output %q", name)
		}
	}
}

func TestActionYML_Steps(t *testing.T) {
	a := loadActionYML(t)

	if len(a.Runs.Steps) == 0 {
		t.Fatal("action has no steps")
	}

	// Verify key steps exist by name
	stepNames := make(map[string]bool)
	for _, s := range a.Runs.Steps {
		stepNames[s.Name] = true
	}

	expectedSteps := []string{
		"Determine version",
		"Download livedocs binary",
		"Cache claims database",
		"Run livedocs check",
		"Upload drift report",
	}
	for _, name := range expectedSteps {
		if !stepNames[name] {
			t.Errorf("missing step %q", name)
		}
	}
}

func TestActionYML_CachesClaimsDB(t *testing.T) {
	a := loadActionYML(t)

	found := false
	for _, s := range a.Runs.Steps {
		if strings.HasPrefix(s.Uses, "actions/cache@") && s.Name == "Cache claims database" {
			found = true
			break
		}
	}
	if !found {
		t.Error("action must cache claims database using actions/cache")
	}
}

func TestActionYML_UploadsArtifact(t *testing.T) {
	a := loadActionYML(t)

	found := false
	for _, s := range a.Runs.Steps {
		if strings.HasPrefix(s.Uses, "actions/upload-artifact@") {
			found = true
			break
		}
	}
	if !found {
		t.Error("action must upload drift report as artifact")
	}
}

// TestActionYML_ChecksumVerification asserts that the "Download livedocs
// binary" step verifies release-asset integrity before `sudo mv`ing the
// binary into /usr/local/bin. Mirrors the two-guard pattern (grep presence
// pre-check + sha256sum --ignore-missing -c) applied to
// examples/workflows/livedocs-prbot.yml in 5vc.7.
//
// Security rationale: action.yml runs `sudo mv /tmp/livedocs
// /usr/local/bin/livedocs` on adopter runners. Without a checksum guard, a
// compromised release asset would get root on every consumer of
// `uses: sjarmak/livedocs@...`.
func TestActionYML_ChecksumVerification(t *testing.T) {
	data, err := os.ReadFile("../action.yml")
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}
	raw := string(data)

	// Guard 0: checksums.txt must be downloaded alongside the archive in
	// the same `gh release download` invocation. Assert via the literal
	// --pattern flag so a refactor that drops the download from the
	// release-download step is caught.
	if !strings.Contains(raw, `--pattern "checksums.txt"`) {
		t.Error("action.yml must download checksums.txt via `gh release download --pattern \"checksums.txt\"`")
	}

	// Guard 1: explicit archive-presence pre-check. `sha256sum --ignore-missing`
	// silently exits 0 when the archive is absent from the manifest, so we
	// must grep for it first.
	if !strings.Contains(raw, "grep -qF") {
		t.Error("action.yml must pre-check archive presence in checksums.txt via `grep -qF` (closes --ignore-missing bypass)")
	}

	// Guard 2: sha256sum -c verifies the archive bytes match the manifest
	// entry. --ignore-missing is correct here because the release-wide
	// checksums.txt lists every os/arch archive but we only downloaded one.
	if !strings.Contains(raw, "sha256sum --ignore-missing -c") {
		t.Error("action.yml must verify archive via `sha256sum --ignore-missing -c checksums.txt`")
	}

	// Fail-closed: the Download step must run under `set -euo pipefail` so
	// any guard failure aborts before `sudo mv`.
	downloadIdx := strings.Index(raw, "name: Download livedocs binary")
	if downloadIdx < 0 {
		t.Fatal("missing `Download livedocs binary` step")
	}
	// Scan only the Download step body (up to the next top-level step).
	nextStepIdx := strings.Index(raw[downloadIdx+len("name: Download livedocs binary"):], "\n    - name:")
	if nextStepIdx < 0 {
		t.Fatal("could not locate end of Download step")
	}
	downloadBody := raw[downloadIdx : downloadIdx+len("name: Download livedocs binary")+nextStepIdx]
	if !strings.Contains(downloadBody, "set -euo pipefail") {
		t.Error("Download step must run under `set -euo pipefail` to fail closed on guard failures")
	}

	// Ordering: verification must happen before the binary is installed.
	// Match the literal install command (not the same phrase in comments).
	sha256Idx := strings.Index(downloadBody, "sha256sum --ignore-missing -c")
	sudoMvIdx := strings.Index(downloadBody, "sudo mv /tmp/livedocs /usr/local/bin/livedocs")
	if sha256Idx < 0 || sudoMvIdx < 0 {
		t.Fatalf("missing sha256sum (%d) or install command (%d) in Download step", sha256Idx, sudoMvIdx)
	}
	if sha256Idx >= sudoMvIdx {
		t.Error("sha256sum verification must precede installation (otherwise unverified bytes reach /usr/local/bin)")
	}
}
