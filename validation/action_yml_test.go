package validation_test

import (
	"os"
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
		if s.Uses == "actions/cache@v4" && s.Name == "Cache claims database" {
			found = true
			break
		}
	}
	if !found {
		t.Error("action must cache claims database using actions/cache@v4")
	}
}

func TestActionYML_UploadsArtifact(t *testing.T) {
	a := loadActionYML(t)

	found := false
	for _, s := range a.Runs.Steps {
		if s.Uses == "actions/upload-artifact@v4" {
			found = true
			break
		}
	}
	if !found {
		t.Error("action must upload drift report as artifact")
	}
}
