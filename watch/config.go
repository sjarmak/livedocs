package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RepoEntry describes a single repository to watch.
type RepoEntry struct {
	Path   string `json:"path"`             // absolute or relative path to the git repo
	Name   string `json:"name,omitempty"`   // repository identifier (default: directory basename)
	Output string `json:"output,omitempty"` // output SQLite file path (default: <name>.claims.db)
}

// MultiRepoConfig is the top-level structure of a watch config file.
type MultiRepoConfig struct {
	Repos []RepoEntry `json:"repos"`
}

// LoadConfig reads a JSON config file listing repositories to watch.
// Each entry's Path is resolved to an absolute path relative to the config
// file's directory. Name and Output are defaulted if empty.
func LoadConfig(path string) ([]RepoEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg MultiRepoConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	if len(cfg.Repos) == 0 {
		return nil, fmt.Errorf("config %s: no repos defined", path)
	}

	configDir := filepath.Dir(path)
	entries := make([]RepoEntry, 0, len(cfg.Repos))
	for _, entry := range cfg.Repos {
		resolved, err := resolveEntry(entry, configDir)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		entries = append(entries, resolved)
	}
	return entries, nil
}

// ScanReposDir scans a directory for subdirectories that are git repositories
// (contain a .git/ directory). Returns a RepoEntry for each discovered repo.
func ScanReposDir(dir string) ([]RepoEntry, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve repos-dir %s: %w", dir, err)
	}

	dirEntries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("read repos-dir %s: %w", absDir, err)
	}

	var entries []RepoEntry
	for _, d := range dirEntries {
		if !d.IsDir() {
			continue
		}

		subDir := filepath.Join(absDir, d.Name())
		gitDir := filepath.Join(subDir, ".git")
		info, err := os.Stat(gitDir)
		if err != nil || !info.IsDir() {
			continue
		}

		name := d.Name()
		entries = append(entries, RepoEntry{
			Path:   subDir,
			Name:   name,
			Output: name + ".claims.db",
		})
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("repos-dir %s: no git repositories found", absDir)
	}
	return entries, nil
}

// resolveEntry fills in defaults and resolves relative paths for a RepoEntry.
func resolveEntry(entry RepoEntry, baseDir string) (RepoEntry, error) {
	if entry.Path == "" {
		return RepoEntry{}, fmt.Errorf("repo entry missing path")
	}

	// Resolve path relative to baseDir.
	if !filepath.IsAbs(entry.Path) {
		entry.Path = filepath.Join(baseDir, entry.Path)
	}
	absPath, err := filepath.Abs(entry.Path)
	if err != nil {
		return RepoEntry{}, fmt.Errorf("resolve path %s: %w", entry.Path, err)
	}
	entry.Path = absPath

	// Default name to directory basename.
	if entry.Name == "" {
		entry.Name = filepath.Base(entry.Path)
	}

	// Default output to <name>.claims.db.
	if entry.Output == "" {
		entry.Output = entry.Name + ".claims.db"
	}

	return entry, nil
}
