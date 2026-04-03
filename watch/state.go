// Package watch provides a git-polling watcher that triggers incremental
// extraction when HEAD changes.
package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// State tracks the last-indexed commit SHA per repository.
type State struct {
	mu    sync.RWMutex
	Repos map[string]string `json:"repos"` // repo name -> last-indexed SHA
}

// NewState creates an empty State.
func NewState() *State {
	return &State{Repos: make(map[string]string)}
}

// GetSHA returns the last-indexed SHA for the given repo (empty if unknown).
func (s *State) GetSHA(repo string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Repos[repo]
}

// SetSHA updates the last-indexed SHA for the given repo.
func (s *State) SetSHA(repo string, sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Repos[repo] = sha
}

// LoadState reads state from a JSON file. Returns an empty State if the file
// does not exist or is corrupt.
func LoadState(path string) *State {
	data, err := os.ReadFile(path)
	if err != nil {
		return NewState()
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return NewState()
	}
	if s.Repos == nil {
		s.Repos = make(map[string]string)
	}
	return &s
}

// SaveState writes state to a JSON file atomically (write tmp, rename).
func SaveState(path string, s *State) error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("watch: marshal state: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("watch: create state dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("watch: create temp state file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("watch: write state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("watch: close temp state file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("watch: rename state file: %w", err)
	}
	return nil
}
