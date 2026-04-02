// Package versionnorm handles version normalization for Go staging packages.
//
// The kubernetes monorepo uses replace directives to map module paths like
// k8s.io/client-go to local staging directories (./staging/src/k8s.io/client-go).
// Standalone repos reference these same modules via pseudo-versions
// (v0.0.0-YYYYMMDDHHMMSS-hash) or release versions (v0.28.0).
//
// This package normalizes these version differences so cross-repo symbol
// joins work on module_path+symbol_name without version conflicts.
package versionnorm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ReplaceDirective represents a single replace directive from go.mod.
type ReplaceDirective struct {
	ModulePath string // Original module path (e.g., "k8s.io/client-go")
	OldVersion string // Version constraint on old path (may be empty)
	NewPath    string // Replacement path (may be local or remote)
	NewVersion string // Version of replacement (empty for local paths)
	LocalPath  string // Set to NewPath if IsLocal is true
	IsLocal    bool   // True if replacement is a local directory (starts with . or /)
}

// StagingMap maps canonical module paths to their local staging directories.
// Only local replace directives are included.
type StagingMap map[string]string

// goModJSON mirrors the subset of go mod edit -json output we need.
type goModJSON struct {
	Module  goModModule    `json:"Module"`
	Replace []goModReplace `json:"Replace"`
}

type goModModule struct {
	Path string `json:"Path"`
}

type goModReplace struct {
	Old goModModuleVersion `json:"Old"`
	New goModModuleVersion `json:"New"`
}

type goModModuleVersion struct {
	Path    string `json:"Path"`
	Version string `json:"Version,omitempty"`
}

// ParseReplaceDirectives parses the JSON output of `go mod edit -json` and
// returns all replace directives.
func ParseReplaceDirectives(data []byte) ([]ReplaceDirective, error) {
	var mod goModJSON
	if err := json.Unmarshal(data, &mod); err != nil {
		return nil, fmt.Errorf("parsing go mod JSON: %w", err)
	}

	directives := make([]ReplaceDirective, 0, len(mod.Replace))
	for _, r := range mod.Replace {
		isLocal := strings.HasPrefix(r.New.Path, ".") || strings.HasPrefix(r.New.Path, "/")

		d := ReplaceDirective{
			ModulePath: r.Old.Path,
			OldVersion: r.Old.Version,
			NewPath:    r.New.Path,
			NewVersion: r.New.Version,
			IsLocal:    isLocal,
		}
		if isLocal {
			d.LocalPath = r.New.Path
		}

		directives = append(directives, d)
	}

	return directives, nil
}

// BuildStagingMap parses go mod edit -json output and returns a StagingMap
// containing only local (staging) replace directives.
func BuildStagingMap(data []byte) (StagingMap, error) {
	directives, err := ParseReplaceDirectives(data)
	if err != nil {
		return nil, err
	}

	sm := make(StagingMap, len(directives))
	for _, d := range directives {
		if d.IsLocal {
			sm[d.ModulePath] = d.LocalPath
		}
	}

	return sm, nil
}

// StripVersion removes version suffixes from import paths.
// Handles both "module@version" and "module@version/subpkg" patterns.
func StripVersion(path string) string {
	if path == "" {
		return ""
	}

	atIdx := strings.Index(path, "@")
	if atIdx < 0 {
		return path
	}

	prefix := path[:atIdx]

	// Find end of version: next "/" after @
	rest := path[atIdx+1:]
	slashIdx := strings.Index(rest, "/")
	if slashIdx < 0 {
		// No subpackage after version
		return prefix
	}

	// Rejoin prefix with subpackage path after version
	return prefix + rest[slashIdx:]
}

// Normalizer provides version-aware import path normalization using a
// staging map built from the monorepo's go.mod replace directives.
type Normalizer struct {
	staging StagingMap
	// reverse maps local staging paths back to canonical module paths
	reverse map[string]string
}

// NewNormalizer creates a Normalizer with the given staging map.
// A nil staging map is valid and creates a normalizer with no staging awareness.
func NewNormalizer(sm StagingMap) *Normalizer {
	if sm == nil {
		sm = make(StagingMap)
	}

	reverse := make(map[string]string, len(sm))
	for mod, local := range sm {
		reverse[local] = mod
	}

	return &Normalizer{
		staging: sm,
		reverse: reverse,
	}
}

// NormalizeModulePath returns the canonical module path. For staging modules
// this is the public import path (e.g., "k8s.io/client-go"). For non-staging
// modules the path is returned unchanged.
func (n *Normalizer) NormalizeModulePath(path string) string {
	return path
}

// IsStagingModule reports whether the given import path belongs to a staging
// module (or is a subpackage of one).
func (n *Normalizer) IsStagingModule(path string) bool {
	if path == "" {
		return false
	}

	for mod := range n.staging {
		if path == mod || strings.HasPrefix(path, mod+"/") {
			return true
		}
	}

	return false
}

// CanonicalImportPath strips version information and normalizes staging paths
// to produce a version-agnostic canonical import path suitable for cross-repo
// joins.
func (n *Normalizer) CanonicalImportPath(path string) string {
	return StripVersion(path)
}

// ResolveStagingPath takes a local staging directory path (e.g.,
// "./staging/src/k8s.io/client-go/kubernetes") and resolves it to the
// canonical module import path ("k8s.io/client-go/kubernetes").
func (n *Normalizer) ResolveStagingPath(path string) (string, bool) {
	// Try exact match first
	if mod, ok := n.reverse[path]; ok {
		return mod, true
	}

	// Try prefix match for subpackages
	for local, mod := range n.reverse {
		if strings.HasPrefix(path, local+"/") {
			suffix := path[len(local):]
			return mod + suffix, true
		}
	}

	return "", false
}
