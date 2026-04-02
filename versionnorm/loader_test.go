package versionnorm

import (
	"os"
	"testing"
)

func TestLoadStagingMapFromRepo(t *testing.T) {
	kubeDir := os.ExpandEnv("$HOME/kubernetes/kubernetes")
	if _, err := os.Stat(kubeDir); err != nil {
		t.Skip("kubernetes repo not available, skipping integration test")
	}

	sm, err := LoadStagingMapFromRepo(kubeDir)
	if err != nil {
		t.Fatalf("LoadStagingMapFromRepo failed: %v", err)
	}

	// Kubernetes has ~33 staging replace directives
	if len(sm) < 20 {
		t.Errorf("expected at least 20 staging entries, got %d", len(sm))
	}

	// Verify well-known staging modules
	wellKnown := []string{
		"k8s.io/api",
		"k8s.io/apimachinery",
		"k8s.io/client-go",
		"k8s.io/kubectl",
	}

	for _, mod := range wellKnown {
		if _, ok := sm[mod]; !ok {
			t.Errorf("expected staging entry for %s", mod)
		}
	}
}

func TestLoadNormalizerFromRepo(t *testing.T) {
	kubeDir := os.ExpandEnv("$HOME/kubernetes/kubernetes")
	if _, err := os.Stat(kubeDir); err != nil {
		t.Skip("kubernetes repo not available, skipping integration test")
	}

	n, err := LoadNormalizerFromRepo(kubeDir)
	if err != nil {
		t.Fatalf("LoadNormalizerFromRepo failed: %v", err)
	}

	// client-go should be a staging module
	if !n.IsStagingModule("k8s.io/client-go") {
		t.Error("expected k8s.io/client-go to be a staging module")
	}

	// Subpackage should also be recognized
	if !n.IsStagingModule("k8s.io/client-go/kubernetes") {
		t.Error("expected k8s.io/client-go/kubernetes to be a staging module")
	}

	// Normalize a versioned import
	canonical := n.CanonicalImportPath("k8s.io/api@v0.0.0-20260324094416-91061ea648b7")
	if canonical != "k8s.io/api" {
		t.Errorf("expected k8s.io/api, got %s", canonical)
	}

	// Resolve staging path back to canonical
	resolved, ok := n.ResolveStagingPath("./staging/src/k8s.io/client-go")
	if !ok || resolved != "k8s.io/client-go" {
		t.Errorf("expected k8s.io/client-go, got %s (ok=%v)", resolved, ok)
	}
}

func TestLoadStagingMapFromRepoNoGoMod(t *testing.T) {
	_, err := LoadStagingMapFromRepo("/tmp/nonexistent-repo-dir-xyz")
	if err == nil {
		t.Error("expected error for nonexistent repo")
	}
}
