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
	Signs []struct {
		ID          string   `yaml:"id"`
		Cmd         string   `yaml:"cmd"`
		Artifacts   string   `yaml:"artifacts"`
		Signature   string   `yaml:"signature"`
		Certificate string   `yaml:"certificate"`
		Args        []string `yaml:"args"`
	} `yaml:"signs"`
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

	// Cosign keyless signing of checksums.txt (live_docs-5vc.11). Without a
	// signature, a compromised release repo could publish a matching
	// (tarball, checksums.txt) pair and bypass consumer integrity checks.
	t.Run("cosign keyless signing of checksums", func(t *testing.T) {
		if len(cfg.Signs) == 0 {
			t.Fatal("no signs block configured — checksums.txt would be unsigned")
		}
		// Find the checksum-signing entry (may coexist with future entries).
		sigIdx := -1
		for i := range cfg.Signs {
			if cfg.Signs[i].Artifacts == "checksum" {
				sigIdx = i
				break
			}
		}
		if sigIdx < 0 {
			t.Fatal("no signs entry targeting artifacts: checksum")
		}
		sig := cfg.Signs[sigIdx]

		if sig.Cmd != "cosign" {
			t.Errorf("signs.cmd = %q, want %q (GPG would require key management)", sig.Cmd, "cosign")
		}
		if sig.Signature == "" {
			t.Error("signs.signature must be set so the signature file lands in the release")
		}
		if sig.Certificate == "" {
			t.Error("signs.certificate must be set so the Fulcio cert lands in the release (consumers verify against it)")
		}
		// Keyless signing requires --yes (otherwise cosign prompts and blocks
		// non-interactive CI). sign-blob (not sign) targets a raw file, which
		// is what we need for checksums.txt. sign-blob MUST be the first arg —
		// cosign requires the subcommand to precede all flags.
		if len(sig.Args) == 0 || sig.Args[0] != "sign-blob" {
			t.Errorf("signs.args[0] must be 'sign-blob' (cosign requires subcommand first); got args=%v", sig.Args)
		}
		var foundYes bool
		for _, a := range sig.Args {
			if a == "--yes" {
				foundYes = true
			}
		}
		if !foundYes {
			t.Error("signs.args must include '--yes' (keyless signing prompts in the absence of this flag and will hang CI)")
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

	// Cosign keyless signing requires the workflow to request an OIDC token
	// from GitHub. Without id-token: write, the signs: block in .goreleaser.yaml
	// fails at release time. Least-privilege: scope to the release job, not the
	// whole workflow, so future jobs added here don't silently inherit OIDC
	// issuance rights. (live_docs-5vc.11)
	t.Run("id-token write scoped to release job, not workflow", func(t *testing.T) {
		if !strings.Contains(content, "id-token: write") {
			t.Error("workflow missing `id-token: write` permission (required for cosign keyless OIDC signing)")
		}
		// Top-level permissions block must be empty (`permissions: {}`) — any
		// id-token write at that scope bleeds into every job in the workflow.
		if !strings.Contains(content, "permissions: {}") {
			t.Error("top-level `permissions: {}` missing — id-token: write must be job-scoped, not workflow-scoped (least privilege)")
		}
	})

	// Cosign is not shipped inside goreleaser-cross. The host must install
	// it (via sigstore/cosign-installer) and bind-mount the binary into the
	// container; otherwise the signs: block fails with `cosign: not found`.
	t.Run("installs cosign on host", func(t *testing.T) {
		if !strings.Contains(content, "sigstore/cosign-installer@") {
			t.Error("workflow must install cosign via sigstore/cosign-installer so goreleaser can sign checksums.txt")
		}
	})

	// OIDC env vars must be forwarded into the goreleaser-cross container.
	t.Run("forwards OIDC env vars to container", func(t *testing.T) {
		if !strings.Contains(content, "ACTIONS_ID_TOKEN_REQUEST_TOKEN") {
			t.Error("workflow must forward ACTIONS_ID_TOKEN_REQUEST_TOKEN into goreleaser-cross (cosign keyless requires it)")
		}
		if !strings.Contains(content, "ACTIONS_ID_TOKEN_REQUEST_URL") {
			t.Error("workflow must forward ACTIONS_ID_TOKEN_REQUEST_URL into goreleaser-cross (cosign keyless requires it)")
		}
	})

	// Cosign binary must be bind-mounted into the container.
	t.Run("bind-mounts cosign binary", func(t *testing.T) {
		if !strings.Contains(content, "/usr/local/bin/cosign") {
			t.Error("workflow must bind-mount cosign into the goreleaser-cross container (the image does not ship cosign)")
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
