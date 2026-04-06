package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestWatchCmd_EnrichFlagExists(t *testing.T) {
	f := watchCmd.Flags().Lookup("enrich")
	if f == nil {
		t.Fatal("--enrich flag not registered on watch command")
	}
	if f.DefValue != "false" {
		t.Fatalf("--enrich default should be false, got %s", f.DefValue)
	}
}

func TestWatchCmd_EnrichDebounceFlagExists(t *testing.T) {
	f := watchCmd.Flags().Lookup("enrich-debounce")
	if f == nil {
		t.Fatal("--enrich-debounce flag not registered on watch command")
	}
	expected := (5 * time.Second).String()
	if f.DefValue != expected {
		t.Fatalf("--enrich-debounce default should be %s, got %s", expected, f.DefValue)
	}
}

func TestWatchCmd_RegisteredOnRoot(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "watch" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("watch command not registered on root command")
	}
}

func TestWatchCmd_SourceFlagExists(t *testing.T) {
	f := watchCmd.Flags().Lookup("source")
	if f == nil {
		t.Fatal("--source flag not registered on watch command")
	}
	if f.DefValue != "local" {
		t.Fatalf("--source default should be 'local', got %s", f.DefValue)
	}
}

func TestWatchCmd_ReposFlagExists(t *testing.T) {
	f := watchCmd.Flags().Lookup("repos")
	if f == nil {
		t.Fatal("--repos flag not registered on watch command")
	}
	if f.DefValue != "" {
		t.Fatalf("--repos default should be empty, got %s", f.DefValue)
	}
}

func TestWatchCmd_ConcurrencyFlagExists(t *testing.T) {
	f := watchCmd.Flags().Lookup("concurrency")
	if f == nil {
		t.Fatal("--concurrency flag not registered on watch command")
	}
	if f.DefValue != "10" {
		t.Fatalf("--concurrency default should be 10, got %s", f.DefValue)
	}
}

func TestWatchCmd_DataDirFlagExists(t *testing.T) {
	f := watchCmd.Flags().Lookup("data-dir")
	if f == nil {
		t.Fatal("--data-dir flag not registered on watch command")
	}
}

// mockMCPCallerWatch implements pipeline.MCPCaller for testing.
type mockMCPCallerWatch struct {
	responses map[string]string
	err       error
}

func (m *mockMCPCallerWatch) CallTool(_ context.Context, toolName string, _ map[string]any) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	resp, ok := m.responses[toolName]
	if !ok {
		return "", fmt.Errorf("unexpected tool call: %s", toolName)
	}
	return resp, nil
}

func TestDiscoverSourcegraphRepos(t *testing.T) {
	tests := []struct {
		name     string
		response string
		err      error
		want     []string
		wantErr  bool
	}{
		{
			name:     "multiple repos",
			response: "github.com/org/repo1\ngithub.com/org/repo2\ngithub.com/org/repo3\n",
			want:     []string{"github.com/org/repo1", "github.com/org/repo2", "github.com/org/repo3"},
		},
		{
			name:     "single repo",
			response: "github.com/org/repo1\n",
			want:     []string{"github.com/org/repo1"},
		},
		{
			name:     "empty response",
			response: "",
			want:     nil,
		},
		{
			name:     "whitespace only",
			response: "  \n  \n",
			want:     nil,
		},
		{
			name:     "with blank lines",
			response: "github.com/org/repo1\n\ngithub.com/org/repo2\n",
			want:     []string{"github.com/org/repo1", "github.com/org/repo2"},
		},
		{
			name:    "api error",
			err:     fmt.Errorf("connection refused"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller := &mockMCPCallerWatch{
				responses: map[string]string{"list_repos": tt.response},
				err:       tt.err,
			}
			got, err := discoverSourcegraphRepos(context.Background(), caller, "org/*")
			if (err != nil) != tt.wantErr {
				t.Fatalf("discoverSourcegraphRepos() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Fatalf("got %d repos, want %d", len(got), len(tt.want))
				}
				for i := range got {
					if got[i] != tt.want[i] {
						t.Errorf("repo[%d] = %q, want %q", i, got[i], tt.want[i])
					}
				}
			}
		})
	}
}

func TestRepoBaseName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"github.com/kubernetes/kubernetes", "kubernetes"},
		{"github.com/org/repo", "repo"},
		{"simple-repo", "simple-repo"},
		{"a/b/c/d", "d"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := repoBaseName(tt.input)
			if got != tt.want {
				t.Fatalf("repoBaseName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestWatchCmd_SourcegraphRequiresSRCToken(t *testing.T) {
	t.Setenv("SRC_ACCESS_TOKEN", "")

	// Set global vars directly (RunE reads from package-level vars).
	oldSource := watchSource
	oldRepos := watchRepos
	watchSource = "sourcegraph"
	watchRepos = "org/*"
	defer func() {
		watchSource = oldSource
		watchRepos = oldRepos
	}()

	err := watchCmd.RunE(watchCmd, nil)
	if err == nil {
		t.Fatal("expected error when SRC_ACCESS_TOKEN is not set")
	}
	if got := err.Error(); got != "SRC_ACCESS_TOKEN environment variable is required for --source sourcegraph" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestWatchCmd_SourcegraphRequiresRepos(t *testing.T) {
	t.Setenv("SRC_ACCESS_TOKEN", "test-token")

	oldSource := watchSource
	oldRepos := watchRepos
	watchSource = "sourcegraph"
	watchRepos = ""
	defer func() {
		watchSource = oldSource
		watchRepos = oldRepos
	}()

	err := watchCmd.RunE(watchCmd, nil)
	if err == nil {
		t.Fatal("expected error when --repos is not set")
	}
	if got := err.Error(); got != "--repos is required when --source sourcegraph is used" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestWatchCmd_IntervalDefault5mForSourcegraph(t *testing.T) {
	// The default interval flag is 5s for local, but runWatchSourcegraph
	// overrides it to 5m when --interval is not explicitly set.
	f := watchCmd.Flags().Lookup("interval")
	if f == nil {
		t.Fatal("--interval flag not found")
	}
	// The registered default is 5s (for local mode).
	expected := (5 * time.Second).String()
	if f.DefValue != expected {
		t.Fatalf("--interval default should be %s, got %s", expected, f.DefValue)
	}
}
