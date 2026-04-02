// Package check — manifest-based stateless drift detection.
//
// The manifest maps source file glob patterns to documentation file paths.
// A post-commit hook can compare git diff output against the manifest to
// quickly identify which docs may need updating, without touching SQLite.
package check

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ManifestFileName is the path relative to the repo root.
const ManifestFileName = ".livedocs/manifest"

// ManifestEntry maps a source file glob pattern to documentation paths.
type ManifestEntry struct {
	// Source is a glob pattern relative to the repo root (e.g. "pkg/auth/*.go").
	Source string `yaml:"source"`
	// Docs lists documentation file paths affected when Source files change.
	Docs []string `yaml:"docs"`
}

// Manifest holds the full set of source-to-doc mappings.
type Manifest struct {
	Entries []ManifestEntry `yaml:"entries"`
}

// ManifestResult is the output of a manifest-based check.
type ManifestResult struct {
	AffectedDocs []string `json:"affected_docs"`
	ChangedFiles []string `json:"changed_files"`
	HasAffected  bool     `json:"has_affected"`
}

// LoadManifest reads the manifest file from root/.livedocs/manifest.
func LoadManifest(root string) (*Manifest, error) {
	path := filepath.Join(root, ManifestFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

// SaveManifest writes the manifest to root/.livedocs/manifest.
func SaveManifest(root string, m *Manifest) error {
	dir := filepath.Join(root, ".livedocs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create .livedocs dir: %w", err)
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	path := filepath.Join(root, ManifestFileName)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

// GenerateManifest auto-discovers source-to-doc mappings by walking the repo.
// For each markdown file, it creates entries mapping sibling source files
// (by extension glob) to that markdown file.
func GenerateManifest(root string) (*Manifest, error) {
	mdFiles, err := FindMarkdownFiles(root)
	if err != nil {
		return nil, fmt.Errorf("find markdown files: %w", err)
	}

	m := &Manifest{}
	seen := make(map[string]bool)

	for _, mdPath := range mdFiles {
		rel, err := filepath.Rel(root, mdPath)
		if err != nil {
			continue
		}
		dir := filepath.Dir(mdPath)
		relDir, err := filepath.Rel(root, dir)
		if err != nil {
			continue
		}

		// Find source file extensions in the same directory.
		extensions := discoverExtensions(dir)
		for _, ext := range extensions {
			var pattern string
			if relDir == "." {
				pattern = "*" + ext
			} else {
				pattern = relDir + "/*" + ext
			}
			key := pattern + "→" + rel
			if seen[key] {
				continue
			}
			seen[key] = true
			m.Entries = append(m.Entries, ManifestEntry{
				Source: pattern,
				Docs:   []string{rel},
			})
		}
	}

	// Sort entries for deterministic output.
	sort.Slice(m.Entries, func(i, j int) bool {
		if m.Entries[i].Source == m.Entries[j].Source {
			return m.Entries[i].Docs[0] < m.Entries[j].Docs[0]
		}
		return m.Entries[i].Source < m.Entries[j].Source
	})

	return m, nil
}

// discoverExtensions returns unique file extensions found in dir,
// excluding .md files.
func discoverExtensions(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	extSet := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext == "" || strings.EqualFold(ext, ".md") {
			continue
		}
		extSet[ext] = true
	}
	result := make([]string, 0, len(extSet))
	for ext := range extSet {
		result = append(result, ext)
	}
	sort.Strings(result)
	return result
}

// AffectedDocs returns documentation paths affected by the given changed files.
// Changed files should be relative to the repo root.
func (m *Manifest) AffectedDocs(changedFiles []string) []string {
	docSet := make(map[string]bool)
	for _, entry := range m.Entries {
		for _, changed := range changedFiles {
			matched, err := filepath.Match(entry.Source, changed)
			if err != nil {
				continue
			}
			if matched {
				for _, doc := range entry.Docs {
					docSet[doc] = true
				}
			}
		}
	}

	result := make([]string, 0, len(docSet))
	for doc := range docSet {
		result = append(result, doc)
	}
	sort.Strings(result)
	return result
}

// GitChangedFiles returns files changed in the most recent commit,
// relative to the repo root.
func GitChangedFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", "HEAD~1")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// Fallback: try diff against empty tree (first commit case).
		cmd2 := exec.Command("git", "diff", "--name-only", "--cached", "HEAD")
		cmd2.Dir = root
		out, err = cmd2.Output()
		if err != nil {
			return nil, fmt.Errorf("git diff: %w", err)
		}
	}
	return parseLines(string(out)), nil
}

// parseLines splits output into non-empty trimmed lines.
func parseLines(s string) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// RunManifest performs a stateless manifest-based check.
// It loads the manifest, gets changed files from git, and returns affected docs.
// This function does NOT touch SQLite.
func RunManifest(ctx context.Context, root string) (*ManifestResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	manifest, err := LoadManifest(root)
	if err != nil {
		return nil, fmt.Errorf("load manifest: %w", err)
	}

	changedFiles, err := GitChangedFiles(root)
	if err != nil {
		return nil, fmt.Errorf("get changed files: %w", err)
	}

	affected := manifest.AffectedDocs(changedFiles)

	return &ManifestResult{
		AffectedDocs: affected,
		ChangedFiles: changedFiles,
		HasAffected:  len(affected) > 0,
	}, nil
}

// RunManifestWithFiles performs a manifest-based check with explicitly provided
// changed files instead of calling git. Useful for testing and scripting.
func RunManifestWithFiles(ctx context.Context, root string, changedFiles []string) (*ManifestResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	manifest, err := LoadManifest(root)
	if err != nil {
		return nil, fmt.Errorf("load manifest: %w", err)
	}

	affected := manifest.AffectedDocs(changedFiles)

	return &ManifestResult{
		AffectedDocs: affected,
		ChangedFiles: changedFiles,
		HasAffected:  len(affected) > 0,
	}, nil
}

// FormatManifestResult formats a ManifestResult as human-readable text.
func FormatManifestResult(r *ManifestResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Manifest Check\n\n")
	fmt.Fprintf(&b, "Changed files: %d\n", len(r.ChangedFiles))
	fmt.Fprintf(&b, "Affected docs: %d\n\n", len(r.AffectedDocs))

	if len(r.AffectedDocs) > 0 {
		fmt.Fprintf(&b, "Documentation that may need updating:\n")
		for _, doc := range r.AffectedDocs {
			fmt.Fprintf(&b, "  - %s\n", doc)
		}
		fmt.Fprintf(&b, "\n")
	} else {
		fmt.Fprintf(&b, "No documentation affected by recent changes.\n\n")
	}

	return b.String()
}
