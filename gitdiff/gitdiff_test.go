package gitdiff

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseNameStatus(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []FileChange
		wantErr bool
	}{
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name:  "single added file",
			input: "A\tfoo.go\n",
			want: []FileChange{
				{Status: StatusAdded, Path: "foo.go"},
			},
		},
		{
			name:  "mixed statuses",
			input: "A\tnew.go\nM\tchanged.go\nD\tremoved.go\n",
			want: []FileChange{
				{Status: StatusAdded, Path: "new.go"},
				{Status: StatusModified, Path: "changed.go"},
				{Status: StatusDeleted, Path: "removed.go"},
			},
		},
		{
			name:  "renamed file",
			input: "R100\told.go\tnew.go\n",
			want: []FileChange{
				{Status: StatusRenamed, Path: "new.go", OldPath: "old.go"},
			},
		},
		{
			name:  "copied file",
			input: "C080\tsrc.go\tdst.go\n",
			want: []FileChange{
				{Status: StatusCopied, Path: "dst.go", OldPath: "src.go"},
			},
		},
		{
			name:    "malformed line",
			input:   "X\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseNameStatus(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d changes, want %d", len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i].Status != tt.want[i].Status || got[i].Path != tt.want[i].Path || got[i].OldPath != tt.want[i].OldPath {
					t.Errorf("change[%d]: got %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDiffBetween(t *testing.T) {
	// Create a temp git repo with two commits.
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "checkout", "-b", "main")

	// Commit 1: add file_a.go
	if err := os.WriteFile(filepath.Join(dir, "file_a.go"), []byte("package a"), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "initial")

	// Get first commit SHA.
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	sha1Out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	sha1 := string(sha1Out[:len(sha1Out)-1]) // trim newline

	// Commit 2: add file_b.go, modify file_a.go, delete nothing yet.
	if err := os.WriteFile(filepath.Join(dir, "file_b.go"), []byte("package b"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file_a.go"), []byte("package a // modified"), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "second")

	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	sha2Out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	sha2 := string(sha2Out[:len(sha2Out)-1])

	// Test DiffBetween.
	changes, err := DiffBetween(dir, sha1, sha2)
	if err != nil {
		t.Fatalf("DiffBetween: %v", err)
	}

	// Expect: file_a.go modified, file_b.go added.
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2: %+v", len(changes), changes)
	}

	byPath := make(map[string]FileChange)
	for _, c := range changes {
		byPath[c.Path] = c
	}

	if c, ok := byPath["file_a.go"]; !ok || c.Status != StatusModified {
		t.Errorf("file_a.go: got %+v, want Modified", c)
	}
	if c, ok := byPath["file_b.go"]; !ok || c.Status != StatusAdded {
		t.Errorf("file_b.go: got %+v, want Added", c)
	}
}

func TestDiffBetween_WithDeletes(t *testing.T) {
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "checkout", "-b", "main")

	// Commit 1: add two files.
	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte("package keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "remove.go"), []byte("package remove"), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "initial")

	sha1 := getHEAD(t, dir)

	// Commit 2: delete remove.go.
	run("git", "rm", "remove.go")
	run("git", "commit", "-m", "delete")

	sha2 := getHEAD(t, dir)

	changes, err := DiffBetween(dir, sha1, sha2)
	if err != nil {
		t.Fatalf("DiffBetween: %v", err)
	}

	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %+v", len(changes), changes)
	}
	if changes[0].Status != StatusDeleted || changes[0].Path != "remove.go" {
		t.Errorf("got %+v, want Deleted remove.go", changes[0])
	}
}

func TestFilterHelpers(t *testing.T) {
	changes := []FileChange{
		{Status: StatusAdded, Path: "new.go"},
		{Status: StatusModified, Path: "changed.go"},
		{Status: StatusDeleted, Path: "removed.go"},
		{Status: StatusRenamed, Path: "renamed.go", OldPath: "old_name.go"},
		{Status: StatusCopied, Path: "copied.go", OldPath: "orig.go"},
	}

	t.Run("Added", func(t *testing.T) {
		got := Added(changes)
		if len(got) != 1 || got[0].Path != "new.go" {
			t.Errorf("Added: got %+v", got)
		}
	})

	t.Run("Modified", func(t *testing.T) {
		got := Modified(changes)
		if len(got) != 1 || got[0].Path != "changed.go" {
			t.Errorf("Modified: got %+v", got)
		}
	})

	t.Run("Deleted", func(t *testing.T) {
		got := Deleted(changes)
		if len(got) != 1 || got[0].Path != "removed.go" {
			t.Errorf("Deleted: got %+v", got)
		}
	})

	t.Run("ChangedPaths", func(t *testing.T) {
		got := ChangedPaths(changes)
		// Should include all non-deleted: new, changed, renamed, copied = 4
		if len(got) != 4 {
			t.Errorf("ChangedPaths: got %d, want 4: %v", len(got), got)
		}
	})

	t.Run("DeletedPaths", func(t *testing.T) {
		got := DeletedPaths(changes)
		// Should include removed.go + old_name.go (from rename) = 2
		if len(got) != 2 {
			t.Errorf("DeletedPaths: got %d, want 2: %v", len(got), got)
		}
	})
}

func TestDiffBetween_FromEmptyTree(t *testing.T) {
	// Verify that DiffBetween works when fromCommit is the empty tree SHA,
	// even if that object does not exist in the repository. This covers the
	// "fresh repo, no prior state" path in the watcher.
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "checkout", "-b", "main")

	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package foo"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bar.go"), []byte("package bar"), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "initial")

	head := getHEAD(t, dir)

	changes, err := DiffBetween(dir, emptyTreeSHA, head)
	if err != nil {
		t.Fatalf("DiffBetween from empty tree: %v", err)
	}

	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2: %+v", len(changes), changes)
	}
	for _, c := range changes {
		if c.Status != StatusAdded {
			t.Errorf("expected StatusAdded for %s, got %s", c.Path, c.Status)
		}
	}
	byPath := make(map[string]bool)
	for _, c := range changes {
		byPath[c.Path] = true
	}
	if !byPath["foo.go"] || !byPath["bar.go"] {
		t.Errorf("expected foo.go and bar.go in changes, got %+v", changes)
	}
}

func TestParseUnifiedDiff(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []FileChange
		wantErr bool
	}{
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name: "single modified file with one hunk",
			input: `diff --git a/handler.go b/handler.go
index abc1234..def5678 100644
--- a/handler.go
+++ b/handler.go
@@ -10,6 +10,8 @@ func handler() {
 	existing line
+	new line 1
+	new line 2
 	another existing
`,
			want: []FileChange{
				{
					Status: StatusModified,
					Path:   "handler.go",
					Hunks: []Hunk{
						{OldStart: 10, OldCount: 6, NewStart: 10, NewCount: 8},
					},
				},
			},
		},
		{
			name: "modified file with multiple hunks",
			input: `diff --git a/server.go b/server.go
index aaa..bbb 100644
--- a/server.go
+++ b/server.go
@@ -5,3 +5,4 @@ package server
 line
+added
 line
@@ -50,7 +51,6 @@ func foo() {
 line
-removed
 line
`,
			want: []FileChange{
				{
					Status: StatusModified,
					Path:   "server.go",
					Hunks: []Hunk{
						{OldStart: 5, OldCount: 3, NewStart: 5, NewCount: 4},
						{OldStart: 50, OldCount: 7, NewStart: 51, NewCount: 6},
					},
				},
			},
		},
		{
			name: "added file",
			input: `diff --git a/new.go b/new.go
new file mode 100644
index 0000000..abc1234
--- /dev/null
+++ b/new.go
@@ -0,0 +1,10 @@
+package new
+
+func hello() {}
`,
			want: []FileChange{
				{
					Status: StatusAdded,
					Path:   "new.go",
					Hunks: []Hunk{
						{OldStart: 0, OldCount: 0, NewStart: 1, NewCount: 10},
					},
				},
			},
		},
		{
			name: "deleted file",
			input: `diff --git a/old.go b/old.go
deleted file mode 100644
index abc1234..0000000
--- a/old.go
+++ /dev/null
@@ -1,5 +0,0 @@
-package old
-
-func bye() {}
`,
			want: []FileChange{
				{
					Status: StatusDeleted,
					Path:   "old.go",
					Hunks: []Hunk{
						{OldStart: 1, OldCount: 5, NewStart: 0, NewCount: 0},
					},
				},
			},
		},
		{
			name: "renamed file",
			input: `diff --git a/old_name.go b/new_name.go
similarity index 95%
rename from old_name.go
rename to new_name.go
index abc..def 100644
--- a/old_name.go
+++ b/new_name.go
@@ -10,3 +10,4 @@ func foo() {
 line
+added
 line
`,
			want: []FileChange{
				{
					Status:  StatusRenamed,
					Path:    "new_name.go",
					OldPath: "old_name.go",
					Hunks: []Hunk{
						{OldStart: 10, OldCount: 3, NewStart: 10, NewCount: 4},
					},
				},
			},
		},
		{
			name: "multiple files",
			input: `diff --git a/a.go b/a.go
index abc..def 100644
--- a/a.go
+++ b/a.go
@@ -1,3 +1,4 @@
 line
+new
 line
diff --git a/b.go b/b.go
index ghi..jkl 100644
--- a/b.go
+++ b/b.go
@@ -20,5 +20,5 @@ func bar() {
 line
-old
+new
 line
`,
			want: []FileChange{
				{
					Status: StatusModified,
					Path:   "a.go",
					Hunks: []Hunk{
						{OldStart: 1, OldCount: 3, NewStart: 1, NewCount: 4},
					},
				},
				{
					Status: StatusModified,
					Path:   "b.go",
					Hunks: []Hunk{
						{OldStart: 20, OldCount: 5, NewStart: 20, NewCount: 5},
					},
				},
			},
		},
		{
			name: "hunk header without function context",
			input: `diff --git a/simple.go b/simple.go
index abc..def 100644
--- a/simple.go
+++ b/simple.go
@@ -3,4 +3,5 @@
 line
+new
 line
`,
			want: []FileChange{
				{
					Status: StatusModified,
					Path:   "simple.go",
					Hunks: []Hunk{
						{OldStart: 3, OldCount: 4, NewStart: 3, NewCount: 5},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseUnifiedDiff(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d file changes, want %d\ngot: %+v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i].Status != tt.want[i].Status {
					t.Errorf("change[%d].Status = %q, want %q", i, got[i].Status, tt.want[i].Status)
				}
				if got[i].Path != tt.want[i].Path {
					t.Errorf("change[%d].Path = %q, want %q", i, got[i].Path, tt.want[i].Path)
				}
				if got[i].OldPath != tt.want[i].OldPath {
					t.Errorf("change[%d].OldPath = %q, want %q", i, got[i].OldPath, tt.want[i].OldPath)
				}
				if len(got[i].Hunks) != len(tt.want[i].Hunks) {
					t.Fatalf("change[%d]: got %d hunks, want %d\ngot: %+v", i, len(got[i].Hunks), len(tt.want[i].Hunks), got[i].Hunks)
				}
				for j := range tt.want[i].Hunks {
					if got[i].Hunks[j] != tt.want[i].Hunks[j] {
						t.Errorf("change[%d].Hunks[%d] = %+v, want %+v", i, j, got[i].Hunks[j], tt.want[i].Hunks[j])
					}
				}
			}
		})
	}
}

func getHEAD(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return string(out[:len(out)-1])
}
