package watch

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/live-docs/live_docs/pipeline"
)

// emptyTreeSHA is the hash of the empty tree in git, used as the "from"
// commit when doing a full extraction (no prior state or force-push).
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf899d69f82cf7118"

// PipelineRunner abstracts pipeline execution for testing.
type PipelineRunner interface {
	Run(ctx context.Context, fromCommit, toCommit string) (pipeline.Result, error)
}

// DeepExtractFn is a callback that runs the Go deep extractor on the repo
// and stores the resulting claims. It is called periodically by the watcher
// when DeepInterval is configured.
type DeepExtractFn func(ctx context.Context) error

// AccessTimeFunc returns the last time the given repo was queried.
// If the repo has never been queried, it returns the zero time and false.
type AccessTimeFunc func(repoName string) (time.Time, bool)

// OnExtractFn is a callback invoked after each successful pipeline extraction.
// It receives the pipeline result including ChangedPaths. Implementations must
// not block — the watch poll cycle continues immediately after the call.
type OnExtractFn func(result pipeline.Result)

// Config holds the configuration for a Watcher.
type Config struct {
	RepoDir      string         // absolute path to the git repo root
	RepoName     string         // repository identifier
	Interval     time.Duration  // polling interval (base interval when no freshness tiers)
	DeepInterval time.Duration  // deep extraction interval (0 = disabled)
	StateFile    string         // path to the state JSON file
	Pipeline     PipelineRunner // pipeline to run on changes
	DeepExtract  DeepExtractFn  // deep extraction callback (nil = disabled)
	OnExtract    OnExtractFn    // callback after each extraction (nil = disabled)
	Out          io.Writer      // output writer for log messages
	Git          GitOps         // git operations (nil = use real git)
	State        *State         // shared state (nil = load from StateFile)

	// Freshness tier fields — when set, polling interval is adjusted
	// dynamically based on how recently the repo was queried via MCP.
	FreshnessTiers []FreshnessTier  // tier boundaries (nil = use fixed Interval)
	ColdInterval   time.Duration    // interval for repos not matching any tier
	AccessTimeFn   AccessTimeFunc   // returns last query time for a repo (nil = disabled)
	NowFunc        func() time.Time // injectable clock (nil = time.Now)
}

// Watcher polls git for HEAD changes and triggers pipeline extraction.
type Watcher struct {
	repoDir      string
	repoName     string
	interval     time.Duration
	deepInterval time.Duration
	stateFile    string
	pipeline     PipelineRunner
	deepExtract  DeepExtractFn
	onExtract    OnExtractFn
	out          io.Writer
	git          GitOps
	state        *State // shared state, may be nil (loaded on Run)

	// Freshness tier support.
	freshnessTiers []FreshnessTier
	coldInterval   time.Duration
	accessTimeFn   AccessTimeFunc
	nowFunc        func() time.Time
}

// New creates a Watcher from the given Config.
func New(cfg Config) *Watcher {
	git := cfg.Git
	if git == nil {
		git = LocalGitOps{}
	}
	nowFunc := cfg.NowFunc
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &Watcher{
		repoDir:        cfg.RepoDir,
		repoName:       cfg.RepoName,
		interval:       cfg.Interval,
		deepInterval:   cfg.DeepInterval,
		stateFile:      cfg.StateFile,
		pipeline:       cfg.Pipeline,
		deepExtract:    cfg.DeepExtract,
		onExtract:      cfg.OnExtract,
		out:            cfg.Out,
		git:            git,
		state:          cfg.State,
		freshnessTiers: cfg.FreshnessTiers,
		coldInterval:   cfg.ColdInterval,
		accessTimeFn:   cfg.AccessTimeFn,
		nowFunc:        nowFunc,
	}
}

// effectiveInterval returns the current polling interval, taking freshness
// tiers into account. If no tiers are configured, it returns the base interval.
func (w *Watcher) effectiveInterval() time.Duration {
	if w.accessTimeFn == nil || len(w.freshnessTiers) == 0 {
		return w.interval
	}
	lastQuery, ok := w.accessTimeFn(w.repoName)
	if !ok {
		lastQuery = time.Time{} // zero time — treated as never queried
	}
	coldInterval := w.coldInterval
	if coldInterval == 0 {
		coldInterval = w.interval // fall back to base interval
	}
	return SelectInterval(w.freshnessTiers, lastQuery, w.nowFunc(), coldInterval)
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

	curInterval := w.effectiveInterval()
	fmt.Fprintf(w.out, "watch: polling %s every %s\n", w.repoDir, curInterval)

	ticker := time.NewTicker(curInterval)
	defer ticker.Stop()

	// Set up deep extraction ticker if configured.
	var deepC <-chan time.Time
	if w.deepInterval > 0 && w.deepExtract != nil {
		fmt.Fprintf(w.out, "watch: deep extraction every %s for %s\n", w.deepInterval, w.repoName)
		deepTicker := time.NewTicker(w.deepInterval)
		defer deepTicker.Stop()
		deepC = deepTicker.C
	}

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
			// Re-evaluate the polling interval based on current access recency.
			if w.accessTimeFn != nil && len(w.freshnessTiers) > 0 {
				newInterval := w.effectiveInterval()
				if newInterval != curInterval {
					fmt.Fprintf(w.out, "watch: adjusting poll interval for %s: %s -> %s\n", w.repoName, curInterval, newInterval)
					curInterval = newInterval
					ticker.Reset(curInterval)
				}
			}
		case <-deepC:
			w.runDeepExtract(ctx)
		}
	}
}

// runDeepExtract invokes the deep extraction callback and logs the result.
func (w *Watcher) runDeepExtract(ctx context.Context) {
	if w.deepExtract == nil {
		return
	}
	fmt.Fprintf(w.out, "watch: starting deep extraction for %s\n", w.repoName)
	start := time.Now()
	if err := w.deepExtract(ctx); err != nil {
		fmt.Fprintf(w.out, "watch: deep extraction error for %s: %v\n", w.repoName, err)
		return
	}
	fmt.Fprintf(w.out, "watch: deep extraction complete for %s (%s)\n", w.repoName, time.Since(start).Round(time.Millisecond))
}

// check performs a single poll cycle: get HEAD, compare, run pipeline if changed.
func (w *Watcher) check(ctx context.Context, state *State, lastSHA *string) error {
	headSHA, err := w.git.RevParseHEAD(ctx, w.repoDir)
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
		isAnc, err := w.git.IsAncestor(ctx, w.repoDir, fromSHA, headSHA)
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

	// Notify the OnExtract callback if configured.
	if w.onExtract != nil {
		w.onExtract(result)
	}

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
