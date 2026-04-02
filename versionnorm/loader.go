package versionnorm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// LoadStagingMapFromRepo runs `go mod edit -json` in the given repo directory
// and builds a StagingMap from the replace directives.
func LoadStagingMapFromRepo(repoDir string) (StagingMap, error) {
	goModPath := filepath.Join(repoDir, "go.mod")
	if _, err := os.Stat(goModPath); err != nil {
		return nil, fmt.Errorf("no go.mod at %s: %w", goModPath, err)
	}

	cmd := exec.Command("go", "mod", "edit", "-json")
	cmd.Dir = repoDir

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running go mod edit -json in %s: %w", repoDir, err)
	}

	return BuildStagingMap(out)
}

// LoadNormalizerFromRepo creates a Normalizer by reading the go.mod replace
// directives from the given repo directory.
func LoadNormalizerFromRepo(repoDir string) (*Normalizer, error) {
	sm, err := LoadStagingMapFromRepo(repoDir)
	if err != nil {
		return nil, err
	}

	return NewNormalizer(sm), nil
}
