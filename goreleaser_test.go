package live_docs_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// goreleaserConfig mirrors the subset of .goreleaser.yaml we want to validate.
type goreleaserConfig struct {
	Version     int    `yaml:"version"`
	ProjectName string `yaml:"project_name"`
	Builds      []struct {
		ID     string   `yaml:"id"`
		Main   string   `yaml:"main"`
		Binary string   `yaml:"binary"`
		Env    []string `yaml:"env"`
		Goos   []string `yaml:"goos"`
		Goarch []string `yaml:"goarch"`
	} `yaml:"builds"`
	Archives []struct {
		Format          string `yaml:"format"`
		FormatOverrides []struct {
			Goos   string `yaml:"goos"`
			Format string `yaml:"format"`
		} `yaml:"format_overrides"`
	} `yaml:"archives"`
	Checksum struct {
		Algorithm string `yaml:"algorithm"`
	} `yaml:"checksum"`
	Brews []struct {
		Name string `yaml:"name"`
	} `yaml:"brews"`
}

func TestGoreleaserConfig(t *testing.T) {
	data, err := os.ReadFile(".goreleaser.yaml")
	if err != nil {
		t.Fatalf("failed to read .goreleaser.yaml: %v", err)
	}

	var cfg goreleaserConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse .goreleaser.yaml: %v", err)
	}

	t.Run("version is 2", func(t *testing.T) {
		if cfg.Version != 2 {
			t.Errorf("expected version 2, got %d", cfg.Version)
		}
	})

	t.Run("project name", func(t *testing.T) {
		if cfg.ProjectName != "livedocs" {
			t.Errorf("expected project_name 'livedocs', got %q", cfg.ProjectName)
		}
	})

	t.Run("build targets", func(t *testing.T) {
		if len(cfg.Builds) == 0 {
			t.Fatal("no builds defined")
		}
		b := cfg.Builds[0]

		if b.Binary != "livedocs" {
			t.Errorf("expected binary 'livedocs', got %q", b.Binary)
		}
		if b.Main != "./cmd/livedocs" {
			t.Errorf("expected main './cmd/livedocs', got %q", b.Main)
		}

		requiredOS := map[string]bool{"linux": false, "darwin": false}
		for _, os := range b.Goos {
			requiredOS[os] = true
		}
		for os, found := range requiredOS {
			if !found {
				t.Errorf("missing required OS target: %s", os)
			}
		}

		requiredArch := map[string]bool{"amd64": false, "arm64": false}
		for _, arch := range b.Goarch {
			requiredArch[arch] = true
		}
		for arch, found := range requiredArch {
			if !found {
				t.Errorf("missing required arch target: %s", arch)
			}
		}
	})

	t.Run("CGO enabled", func(t *testing.T) {
		if len(cfg.Builds) == 0 {
			t.Fatal("no builds defined")
		}
		found := false
		for _, env := range cfg.Builds[0].Env {
			if strings.Contains(env, "CGO_ENABLED=1") {
				found = true
				break
			}
		}
		if !found {
			t.Error("CGO_ENABLED=1 not set in build env")
		}
	})

	t.Run("archive formats", func(t *testing.T) {
		if len(cfg.Archives) == 0 {
			t.Fatal("no archives defined")
		}
		a := cfg.Archives[0]
		if a.Format != "tar.gz" {
			t.Errorf("expected default format 'tar.gz', got %q", a.Format)
		}
		darwinZip := false
		for _, o := range a.FormatOverrides {
			if o.Goos == "darwin" && o.Format == "zip" {
				darwinZip = true
			}
		}
		if !darwinZip {
			t.Error("expected darwin format override to zip")
		}
	})

	t.Run("checksum sha256", func(t *testing.T) {
		if cfg.Checksum.Algorithm != "sha256" {
			t.Errorf("expected checksum algorithm 'sha256', got %q", cfg.Checksum.Algorithm)
		}
	})

	t.Run("homebrew tap configured", func(t *testing.T) {
		if len(cfg.Brews) == 0 {
			t.Fatal("no homebrew tap configured")
		}
		if cfg.Brews[0].Name != "livedocs" {
			t.Errorf("expected brew name 'livedocs', got %q", cfg.Brews[0].Name)
		}
	})
}

func TestReleaseWorkflow(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("failed to read release workflow: %v", err)
	}
	content := string(data)

	t.Run("triggers on tag push", func(t *testing.T) {
		if !strings.Contains(content, `"v*"`) {
			t.Error("workflow does not trigger on v* tags")
		}
	})

	t.Run("uses goreleaser-cross", func(t *testing.T) {
		if !strings.Contains(content, "goreleaser-cross") {
			t.Error("workflow does not use goreleaser-cross Docker image")
		}
	})

	t.Run("has write permissions", func(t *testing.T) {
		if !strings.Contains(content, "contents: write") {
			t.Error("workflow missing contents: write permission")
		}
	})
}

func TestInstallScript(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatalf("failed to read install.sh: %v", err)
	}
	content := string(data)

	t.Run("supports linux and darwin", func(t *testing.T) {
		if !strings.Contains(content, `linux)`) {
			t.Error("install.sh missing linux support")
		}
		if !strings.Contains(content, `darwin)`) {
			t.Error("install.sh missing darwin support")
		}
	})

	t.Run("supports amd64 and arm64", func(t *testing.T) {
		if !strings.Contains(content, `amd64`) {
			t.Error("install.sh missing amd64 support")
		}
		if !strings.Contains(content, `arm64`) {
			t.Error("install.sh missing arm64 support")
		}
	})

	t.Run("verifies checksum", func(t *testing.T) {
		if !strings.Contains(content, "sha256") {
			t.Error("install.sh missing checksum verification")
		}
	})

	t.Run("set -e for safety", func(t *testing.T) {
		if !strings.Contains(content, "set -e") {
			t.Error("install.sh missing set -e")
		}
	})
}
