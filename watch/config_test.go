package watch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_Valid(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "repos.json")

	// Create two fake repo directories.
	repoA := filepath.Join(tmp, "repo-a")
	repoB := filepath.Join(tmp, "repo-b")
	if err := os.MkdirAll(repoA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repoB, 0o755); err != nil {
		t.Fatal(err)
	}

	config := `{
		"repos": [
			{"path": "repo-a", "name": "alpha", "output": "alpha.claims.db"},
			{"path": "repo-b"}
		]
	}`
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// First entry: explicit name and output.
	if entries[0].Name != "alpha" {
		t.Errorf("entry[0].Name = %q, want alpha", entries[0].Name)
	}
	if entries[0].Output != "alpha.claims.db" {
		t.Errorf("entry[0].Output = %q, want alpha.claims.db", entries[0].Output)
	}
	if entries[0].Path != repoA {
		t.Errorf("entry[0].Path = %q, want %q", entries[0].Path, repoA)
	}

	// Second entry: defaults from path.
	if entries[1].Name != "repo-b" {
		t.Errorf("entry[1].Name = %q, want repo-b", entries[1].Name)
	}
	if entries[1].Output != "repo-b.claims.db" {
		t.Errorf("entry[1].Output = %q, want repo-b.claims.db", entries[1].Output)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := LoadConfig("/nonexistent/repos.json")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadConfig_EmptyRepos(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "empty.json")
	if err := os.WriteFile(path, []byte(`{"repos": []}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for empty repos list")
	}
}

func TestLoadConfig_MissingPath(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "no-path.json")
	config := `{"repos": [{"name": "foo"}]}`
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for entry missing path")
	}
}

func TestScanReposDir_FindsGitRepos(t *testing.T) {
	tmp := t.TempDir()

	// Create git repos (with .git/ dirs).
	for _, name := range []string{"repo-x", "repo-y"} {
		gitDir := filepath.Join(tmp, name, ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Create a non-git directory.
	if err := os.MkdirAll(filepath.Join(tmp, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a regular file (should be ignored).
	if err := os.WriteFile(filepath.Join(tmp, "readme.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ScanReposDir(tmp)
	if err != nil {
		t.Fatalf("ScanReposDir: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Verify names (sorted by filesystem order, but check both exist).
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
		if e.Output != e.Name+".claims.db" {
			t.Errorf("entry %s: output = %q, want %s.claims.db", e.Name, e.Output, e.Name)
		}
	}
	if !names["repo-x"] || !names["repo-y"] {
		t.Errorf("expected repo-x and repo-y, got %v", names)
	}
}

func TestScanReposDir_NoGitRepos(t *testing.T) {
	tmp := t.TempDir()
	// Create only non-git directories.
	if err := os.MkdirAll(filepath.Join(tmp, "plain-dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := ScanReposDir(tmp)
	if err == nil {
		t.Fatal("expected error when no git repos found")
	}
}

func TestScanReposDir_NonexistentDir(t *testing.T) {
	_, err := ScanReposDir("/nonexistent/dir")
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestLoadConfig_AbsolutePath(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "my-repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(tmp, "config.json")
	config := `{"repos": [{"path": "` + repoDir + `"}]}`
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if entries[0].Path != repoDir {
		t.Errorf("expected absolute path %q, got %q", repoDir, entries[0].Path)
	}
}
