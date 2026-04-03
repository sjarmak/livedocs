package watch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewState(t *testing.T) {
	s := NewState()
	if s.Repos == nil {
		t.Fatal("NewState().Repos should not be nil")
	}
	if len(s.Repos) != 0 {
		t.Fatalf("NewState() should have empty repos, got %d", len(s.Repos))
	}
}

func TestGetSetSHA(t *testing.T) {
	s := NewState()
	if got := s.GetSHA("myrepo"); got != "" {
		t.Fatalf("expected empty SHA for unknown repo, got %q", got)
	}

	s.SetSHA("myrepo", "abc123")
	if got := s.GetSHA("myrepo"); got != "abc123" {
		t.Fatalf("expected abc123, got %q", got)
	}

	// Overwrite.
	s.SetSHA("myrepo", "def456")
	if got := s.GetSHA("myrepo"); got != "def456" {
		t.Fatalf("expected def456, got %q", got)
	}
}

func TestLoadState_MissingFile(t *testing.T) {
	s := LoadState("/nonexistent/path/state.json")
	if s.Repos == nil {
		t.Fatal("LoadState on missing file should return state with non-nil Repos")
	}
	if len(s.Repos) != 0 {
		t.Fatalf("expected empty repos, got %d", len(s.Repos))
	}
}

func TestLoadState_CorruptFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "state.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := LoadState(path)
	if s.Repos == nil {
		t.Fatal("LoadState on corrupt file should return state with non-nil Repos")
	}
	if len(s.Repos) != 0 {
		t.Fatalf("expected empty repos, got %d", len(s.Repos))
	}
}

func TestSaveAndLoadState(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "state.json")

	s := NewState()
	s.SetSHA("repo-a", "sha111")
	s.SetSHA("repo-b", "sha222")

	if err := SaveState(path, s); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// Verify the file exists.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file should exist: %v", err)
	}

	// Load and verify.
	loaded := LoadState(path)
	if loaded.GetSHA("repo-a") != "sha111" {
		t.Fatalf("expected sha111, got %q", loaded.GetSHA("repo-a"))
	}
	if loaded.GetSHA("repo-b") != "sha222" {
		t.Fatalf("expected sha222, got %q", loaded.GetSHA("repo-b"))
	}
}

func TestSaveState_Resume(t *testing.T) {
	// Simulate: save state, "restart", load state — SHA should persist.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "state.json")

	s1 := NewState()
	s1.SetSHA("myrepo", "commit_abc")
	if err := SaveState(path, s1); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// Simulate restart: load from file.
	s2 := LoadState(path)
	if got := s2.GetSHA("myrepo"); got != "commit_abc" {
		t.Fatalf("resume failed: expected commit_abc, got %q", got)
	}
}

func TestSaveState_CreatesDirectory(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "subdir", "nested", "state.json")

	s := NewState()
	s.SetSHA("repo", "sha")
	if err := SaveState(path, s); err != nil {
		t.Fatalf("SaveState should create directories: %v", err)
	}

	loaded := LoadState(path)
	if loaded.GetSHA("repo") != "sha" {
		t.Fatal("state not persisted through nested dir creation")
	}
}
