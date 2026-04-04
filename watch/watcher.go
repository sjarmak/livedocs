package watch

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/live-docs/live_docs/pipeline"
)

// emptyTreeSHA is the hash of the empty tree in git, used as the "from"
// commit when doing a full extraction (no prior state or force-push).
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf899d69f82cf7118"

// GitOps defines the git operations the watcher needs. This interface
// enables testing without real git repos.
type GitOps interface {
	// RevParseHEAD returns the current HEAD SHA for the repo.
	RevParseHEAD(repoDir string) (string, error)
	// IsAncestor returns true if ancestor is an ancestor of HEAD in the repo.
	IsAncestor(repoDir, ancestor string) (bool, error)
}

// realGitOps implements GitOps using actual git commands.
type realGitOps struct{}

func (realGitOps) RevParseHEAD(repoDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("watch: git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (realGitOps) IsAncestor(repoDir, ancestor string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, "HEAD")
	cmd.Dir = repoDir
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	// Exit code 1 means "not an ancestor" — not a real error.
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("watch: git merge-base: %w", err)
}

// PipelineRunner abstracts pipeline execution for testing.
type PipelineRunner interface {
	Run(ctx context.Context, fromCommit, toCommit string) (pipeline.Result, error)
}

// Config holds the configuration for a Watcher.
type Config struct {
	RepoDir   string         // absolute path to the git repo root
	RepoName  string         // repository identifier
	Interval  time.Duration  // polling interval
	StateFile string         // path to the state JSON file
	Pipeline  PipelineRunner // pipeline to run on changes
	Out       io.Writer      // output writer for log messages
	Git       GitOps         // git operations (nil = use real git)
	State     *State         // shared state (nil = load from StateFile)
}

// Watcher polls git for HEAD changes and triggers pipeline extraction.
type Watcher struct {
	repoDir   string
	repoName  string
	interval  time.Duration
	stateFile string
	pipeline  PipelineRunner
	out       io.Writer
	git       GitOps
	state     *State // shared state, may be nil (loaded on Run)
}

// New creates a Watcher from the given Config.
func New(cfg Config) *Watcher {
	git := cfg.Git
	if git == nil {
		git = realGitOps{}
	}
	return &Watcher{
		repoDir:   cfg.RepoDir,
		repoName:  cfg.RepoName,
		interval:  cfg.Interval,
		stateFile: cfg.StateFile,
		pipeline:  cfg.Pipeline,
		out:       cfg.Out,
		git:       git,
		state:     cfg.State,
	}
}

// Run starts the watch loop. It blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	state := w.state
	if state == nil {
		state = LoadState(w.stateFile)
	}
	lastSHA := state.GetSHA(w.repoName)

	if lastSHA != "" {
		fmt.Fprintf(w.out, "watch: resuming from SHA %s for %s\n", lastSHA[:minLen(len(lastSHA), 12)], w.repoName)
	} else {
		fmt.Fprintf(w.out, "watch: no prior state for %s, will do full extraction on first change\n", w.repoName)
	}

	fmt.Fprintf(w.out, "watch: polling %s every %s\n", w.repoDir, w.interval)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// Check immediately on start, then on each tick.
	if err := w.check(ctx, state, &lastSHA); err != nil {
		fmt.Fprintf(w.out, "watch: initial check error: %v\n", err)
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(w.out, "watch: stopping\n")
			return nil
		case <-ticker.C:
			if err := w.check(ctx, state, &lastSHA); err != nil {
				fmt.Fprintf(w.out, "watch: check error: %v\n", err)
			}
		}
	}
}

// check performs a single poll cycle: get HEAD, compare, run pipeline if changed.
func (w *Watcher) check(ctx context.Context, state *State, lastSHA *string) error {
	headSHA, err := w.git.RevParseHEAD(w.repoDir)
	if err != nil {
		return fmt.Errorf("get HEAD: %w", err)
	}

	if headSHA == *lastSHA {
		return nil // no change
	}

	fromSHA := *lastSHA
	if fromSHA == "" {
		// No prior state — full extraction.
		fmt.Fprintf(w.out, "watch: full extraction (no prior state), HEAD=%s\n", headSHA[:minLen(len(headSHA), 12)])
		fromSHA = emptyTreeSHA
	} else {
		// Check if the stored SHA is still an ancestor of HEAD (handles force-push).
		isAnc, err := w.git.IsAncestor(w.repoDir, fromSHA)
		if err != nil {
			return fmt.Errorf("check ancestor: %w", err)
		}
		if !isAnc {
			fmt.Fprintf(w.out, "watch: force-push detected (stored SHA %s not ancestor of HEAD %s), falling back to full extraction\n",
				fromSHA[:minLen(len(fromSHA), 12)], headSHA[:minLen(len(headSHA), 12)])
			fromSHA = emptyTreeSHA
		} else {
			fmt.Fprintf(w.out, "watch: HEAD changed %s -> %s\n",
				(*lastSHA)[:minLen(len(*lastSHA), 12)], headSHA[:minLen(len(headSHA), 12)])
		}
	}

	result, err := w.pipeline.Run(ctx, fromSHA, headSHA)
	if err != nil {
		return fmt.Errorf("pipeline run: %w", err)
	}

	fmt.Fprintf(w.out, "watch: extraction complete — %d files changed, %d extracted, %d claims, %s\n",
		result.FilesChanged, result.FilesExtracted, result.ClaimsStored, result.Duration.Round(time.Millisecond))

	// Update state and persist.
	*lastSHA = headSHA
	state.SetSHA(w.repoName, headSHA)
	if err := SaveState(w.stateFile, state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	return nil
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}
