package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sjarmak/livedocs/cache"
	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor/defaults"
	"github.com/sjarmak/livedocs/extractor/goextractor"
	"github.com/sjarmak/livedocs/pipeline"
	"github.com/sjarmak/livedocs/sourcegraph"
	"github.com/sjarmak/livedocs/watch"
)

var watchCmd = &cobra.Command{
	Use:   "watch [path]",
	Short: "Watch a repository for changes and incrementally extract claims",
	Long: `Polls git rev-parse HEAD on an interval and triggers incremental
extraction when HEAD changes. Persists last-indexed commit SHA to a state
file so it resumes correctly after restart.

Supports four modes:
  1. Single repo:  livedocs watch <path>
  2. Config file:  livedocs watch --config repos.json
  3. Directory:    livedocs watch --repos-dir /path/to/repos
  4. Sourcegraph:  livedocs watch --source sourcegraph --repos 'org/*'

Mode 4 polls Sourcegraph for new commits on remote repos without cloning.

Handles force-push by falling back to full extraction when the stored SHA
is no longer an ancestor of the current HEAD.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		defer resetCmdFlags(cmd)

		source := mustGetString(cmd, "source")

		// Sourcegraph remote mode.
		if source == "sourcegraph" {
			return runWatchSourcegraph(cmd)
		}

		configPath := mustGetString(cmd, "config")
		reposDir := mustGetString(cmd, "repos-dir")
		repoName := mustGetString(cmd, "repo")
		output := mustGetString(cmd, "output")
		stateFile := mustGetString(cmd, "state-file")
		interval := mustGetDuration(cmd, "interval")
		deepInterval := mustGetDuration(cmd, "deep-interval")
		enrich := mustGetBool(cmd, "enrich")
		enrichDebounce := mustGetDuration(cmd, "enrich-debounce")

		// Determine which mode we're in.
		hasPath := len(args) == 1
		hasConfig := configPath != ""
		hasReposDir := reposDir != ""

		// Exactly one source must be specified.
		sources := 0
		if hasPath {
			sources++
		}
		if hasConfig {
			sources++
		}
		if hasReposDir {
			sources++
		}
		if sources == 0 {
			return fmt.Errorf("specify a repo path, --config, or --repos-dir")
		}
		if sources > 1 {
			return fmt.Errorf("specify only one of: repo path, --config, --repos-dir")
		}

		// Build repo entries.
		var entries []watch.RepoEntry
		var err error

		switch {
		case hasConfig:
			entries, err = watch.LoadConfig(configPath)
			if err != nil {
				return err
			}
		case hasReposDir:
			entries, err = watch.ScanReposDir(reposDir)
			if err != nil {
				return err
			}
		default:
			// Single repo mode — build one entry from args and flags.
			absRepo, absErr := filepath.Abs(args[0])
			if absErr != nil {
				return fmt.Errorf("resolve repo path: %w", absErr)
			}
			name := repoName
			if name == "" {
				name = filepath.Base(absRepo)
			}
			outputPath := output
			if outputPath == "" {
				outputPath = name + ".claims.db"
			}
			entries = []watch.RepoEntry{{
				Path:   absRepo,
				Name:   name,
				Output: outputPath,
			}}
		}

		// Determine state file.
		if stateFile == "" {
			// Place state file next to first output, or in current dir.
			if len(entries) > 0 {
				stateFile = filepath.Join(filepath.Dir(entries[0].Output), ".livedocs-watch-state.json")
			} else {
				stateFile = ".livedocs-watch-state.json"
			}
		}

		return runWatchMulti(cmd, entries, stateFile, interval, deepInterval, enrich, enrichDebounce)
	},
}

func init() {
	watchCmd.Flags().Duration("interval", 5*time.Second, "polling interval (e.g. 5s, 1m)")
	watchCmd.Flags().Duration("deep-interval", 10*time.Minute, "Go deep extractor interval (e.g. 10m, 1h; 0 to disable)")
	watchCmd.Flags().String("repo", "", "repository name (default: directory basename)")
	watchCmd.Flags().StringP("output", "o", "", "output SQLite file path (default: <repo>.claims.db)")
	watchCmd.Flags().String("state-file", "", "state file path (default: .livedocs-watch-state.json next to output)")
	watchCmd.Flags().String("config", "", "JSON config file listing repos to watch")
	watchCmd.Flags().String("repos-dir", "", "directory of git repos to watch")
	watchCmd.Flags().Bool("enrich", false, "enable semantic enrichment via Sourcegraph after each extraction")
	watchCmd.Flags().Duration("enrich-debounce", 5*time.Second, "debounce interval for enrichment queue (e.g. 5s, 10s)")
	watchCmd.Flags().String("source", "local", "extraction source: 'local' or 'sourcegraph'")
	watchCmd.Flags().String("repos", "", "repo pattern for Sourcegraph discovery (e.g. 'kubernetes/*')")
	watchCmd.Flags().Int("concurrency", 10, "max concurrent MCP calls per repo (sourcegraph mode)")
	watchCmd.Flags().String("data-dir", "", "output directory for .claims.db files (sourcegraph mode)")
}

// runWatchMulti launches one watcher per repo entry, all sharing the same
// state file and polling interval. Blocks until ctx is cancelled.
func runWatchMulti(cmd *cobra.Command, entries []watch.RepoEntry, stateFile string, interval, deepInterval time.Duration, enrich bool, enrichDebounce time.Duration) error {
	out := cmd.OutOrStdout()

	// Set up signal handling for clean shutdown.
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigCh:
			fmt.Fprintf(out, "\nwatch: received %s, shutting down...\n", sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	if len(entries) > 1 {
		fmt.Fprintf(out, "watch: monitoring %d repositories\n", len(entries))
	}

	// Load shared state so all watchers use the same in-memory state.
	sharedState := watch.LoadState(stateFile)

	// Track resources for cleanup.
	type repoResources struct {
		claimsDB   *db.ClaimsDB
		cacheStore *cache.SQLiteStore
	}
	resources := make([]repoResources, 0, len(entries))
	defer func() {
		for _, r := range resources {
			r.cacheStore.Close()
			r.claimsDB.Close()
		}
	}()

	// Set up Sourcegraph enrichment client if --enrich is enabled.
	var sgClient *sourcegraph.SourcegraphClient
	if enrich {
		var err error
		sgClient, err = sourcegraph.NewSourcegraphClient()
		if err != nil {
			return fmt.Errorf("create sourcegraph client: %w", err)
		}
		defer sgClient.Close()
		fmt.Fprintf(out, "watch: enrichment enabled (debounce=%s)\n", enrichDebounce)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(entries))

	for _, entry := range entries {
		// Open claims DB.
		claimsDB, err := db.OpenClaimsDB(entry.Output)
		if err != nil {
			return fmt.Errorf("open claims db for %s: %w", entry.Name, err)
		}
		if err := claimsDB.CreateSchema(); err != nil {
			claimsDB.Close()
			return fmt.Errorf("create schema for %s: %w", entry.Name, err)
		}

		// Store extraction metadata with repo root path.
		if err := claimsDB.SetExtractionMeta(db.ExtractionMeta{
			ExtractedAt: db.Now(),
			RepoRoot:    entry.Path,
		}); err != nil {
			claimsDB.Close()
			return fmt.Errorf("set extraction meta for %s: %w", entry.Name, err)
		}

		// Open in-memory cache.
		cacheStore, err := cache.NewSQLiteStore(":memory:", 2*1024*1024*1024)
		if err != nil {
			claimsDB.Close()
			return fmt.Errorf("open cache for %s: %w", entry.Name, err)
		}

		resources = append(resources, repoResources{claimsDB: claimsDB, cacheStore: cacheStore})

		// Build registry and pipeline.
		registry := defaults.BuildDefaultRegistry(entry.Name)
		p := pipeline.New(pipeline.Config{
			Repo:     entry.Name,
			RepoDir:  entry.Path,
			Cache:    cacheStore,
			ClaimsDB: claimsDB,
			Registry: registry,
		})

		// Build deep extraction callback.
		var deepFn watch.DeepExtractFn
		if deepInterval > 0 {
			goDeep := &goextractor.GoDeepExtractor{Repo: entry.Name}
			repoDir := entry.Path
			deepFn = func(ctx context.Context) error {
				claims, err := goDeep.Extract(ctx, repoDir, "go")
				if err != nil {
					return fmt.Errorf("go deep extract: %w", err)
				}
				_, err = storeClaims(claimsDB, entry.Name, claims)
				if err != nil {
					return fmt.Errorf("store deep claims: %w", err)
				}
				return nil
			}
		}

		// Build enrichment callback if --enrich is enabled.
		var onExtractFn watch.OnExtractFn
		if enrich && sgClient != nil {
			router := sourcegraph.NewDefaultRouter(sgClient)
			enricher, enrichErr := sourcegraph.NewEnricher(claimsDB, router)
			if enrichErr != nil {
				return fmt.Errorf("create enricher for %s: %w", entry.Name, enrichErr)
			}
			queue := sourcegraph.NewEnrichmentQueue(sourcegraph.QueueConfig{
				DebounceDuration: enrichDebounce,
				Repo:             entry.Name,
			}, enricher, claimsDB)
			queue.Start(ctx)
			onExtractFn = func(result pipeline.Result) {
				if len(result.ChangedPaths) > 0 {
					queue.Send(result.ChangedPaths)
				}
			}
		}

		w := watch.New(watch.Config{
			RepoDir:      entry.Path,
			RepoName:     entry.Name,
			Interval:     interval,
			DeepInterval: deepInterval,
			StateFile:    stateFile,
			Pipeline:     p,
			DeepExtract:  deepFn,
			OnExtract:    onExtractFn,
			Out:          out,
			State:        sharedState,
		})

		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if err := w.Run(ctx); err != nil {
				errCh <- fmt.Errorf("watcher %s: %w", name, err)
			}
		}(entry.Name)
	}

	// Wait for all watchers to finish.
	wg.Wait()
	close(errCh)

	// Return first error if any.
	for err := range errCh {
		return err
	}
	return nil
}

// runWatchSourcegraph launches remote watchers that poll Sourcegraph for new
// commits on repos matching the --repos pattern. Each repo gets its own Watcher
// with RemoteGitOps and a SourcegraphFileSource-backed pipeline.
func runWatchSourcegraph(cmd *cobra.Command) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	repos := mustGetString(cmd, "repos")
	dataDir := mustGetString(cmd, "data-dir")
	stateFile := mustGetString(cmd, "state-file")
	intervalFlag := mustGetDuration(cmd, "interval")
	concurrency := mustGetInt(cmd, "concurrency")

	// Validate required inputs.
	if os.Getenv("SRC_ACCESS_TOKEN") == "" {
		return fmt.Errorf("SRC_ACCESS_TOKEN environment variable is required for --source sourcegraph")
	}
	if repos == "" {
		return fmt.Errorf("--repos is required when --source sourcegraph is used")
	}
	// Default interval for remote mode is 5m (local default is 5s).
	interval := intervalFlag
	if !cmd.Flags().Changed("interval") {
		interval = 5 * time.Minute
	}

	if dataDir == "" {
		dataDir = "."
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create data-dir: %w", err)
	}

	// Create Sourcegraph MCP client.
	sgClient, err := sourcegraph.NewSourcegraphClient()
	if err != nil {
		return fmt.Errorf("create sourcegraph client: %w", err)
	}
	defer sgClient.Close()

	// Discover repos matching the pattern.
	discoveredRepos, err := discoverSourcegraphRepos(ctx, sgClient, repos)
	if err != nil {
		return fmt.Errorf("discover repos: %w", err)
	}
	if len(discoveredRepos) == 0 {
		return fmt.Errorf("no repos found matching pattern %q", repos)
	}

	fmt.Fprintf(out, "watch: discovered %d repos matching %q\n", len(discoveredRepos), repos)

	// Set up signal handling for clean shutdown.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigCh:
			fmt.Fprintf(out, "\nwatch: received %s, shutting down...\n", sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	// Determine state file.
	if stateFile == "" {
		stateFile = filepath.Join(dataDir, ".livedocs-watch-state.json")
	}

	// Load shared state.
	sharedState := watch.LoadState(stateFile)

	// Track resources for cleanup.
	type remoteResources struct {
		claimsDB   *db.ClaimsDB
		cacheStore *cache.SQLiteStore
	}
	resources := make([]remoteResources, 0, len(discoveredRepos))
	defer func() {
		for _, r := range resources {
			r.cacheStore.Close()
			r.claimsDB.Close()
		}
	}()

	// Create a SourcegraphFileSource shared across repos (one MCP client).
	fileSource, err := pipeline.NewSourcegraphFileSource(sgClient, sgToolLister{}, pipeline.WithConcurrency(concurrency))
	if err != nil {
		return fmt.Errorf("create sourcegraph file source: %w", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(discoveredRepos))

	for _, repo := range discoveredRepos {
		repoName := repoBaseName(repo)
		dbPath := filepath.Join(dataDir, repoName+".claims.db")

		// Open claims DB.
		claimsDB, err := db.OpenClaimsDB(dbPath)
		if err != nil {
			return fmt.Errorf("open claims db for %s: %w", repoName, err)
		}
		if err := claimsDB.CreateSchema(); err != nil {
			claimsDB.Close()
			return fmt.Errorf("create schema for %s: %w", repoName, err)
		}

		// Store extraction metadata.
		if err := claimsDB.SetExtractionMeta(db.ExtractionMeta{
			ExtractedAt: db.Now(),
			RepoRoot:    repo,
		}); err != nil {
			claimsDB.Close()
			return fmt.Errorf("set extraction meta for %s: %w", repoName, err)
		}

		// Open in-memory cache.
		cacheStore, err := cache.NewSQLiteStore(":memory:", 2*1024*1024*1024)
		if err != nil {
			claimsDB.Close()
			return fmt.Errorf("open cache for %s: %w", repoName, err)
		}

		resources = append(resources, remoteResources{claimsDB: claimsDB, cacheStore: cacheStore})

		// Build registry (tree-sitter only for remote).
		registry := buildRemoteRegistry()

		// Build pipeline with SourcegraphFileSource.
		p := pipeline.New(pipeline.Config{
			Repo:       repo,
			RepoDir:    "", // no local dir for remote repos
			Cache:      cacheStore,
			ClaimsDB:   claimsDB,
			Registry:   registry,
			FileSource: fileSource,
		})

		// Create watcher with RemoteGitOps.
		w := watch.New(watch.Config{
			RepoDir:   repo, // repo identifier used by RemoteGitOps
			RepoName:  repoName,
			Interval:  interval,
			StateFile: stateFile,
			Pipeline:  p,
			Out:       out,
			Git:       &watch.RemoteGitOps{Caller: sgClient},
			State:     sharedState,
		})

		fmt.Fprintf(out, "watch: monitoring %s (remote)\n", repo)

		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if err := w.Run(ctx); err != nil {
				errCh <- fmt.Errorf("watcher %s: %w", name, err)
			}
		}(repoName)
	}

	// Wait for all watchers to finish.
	wg.Wait()
	close(errCh)

	// Return first error if any.
	for err := range errCh {
		return err
	}
	return nil
}

// discoverSourcegraphRepos calls the Sourcegraph list_repos MCP tool to find
// repos matching the given pattern. Returns a slice of fully-qualified repo
// identifiers (e.g., "github.com/kubernetes/kubernetes").
func discoverSourcegraphRepos(ctx context.Context, caller pipeline.MCPCaller, pattern string) ([]string, error) {
	result, err := caller.CallTool(ctx, "list_repos", map[string]any{
		"query": pattern,
	})
	if err != nil {
		return nil, fmt.Errorf("list_repos for %q: %w", pattern, err)
	}

	if strings.TrimSpace(result) == "" {
		return nil, nil
	}

	var repos []string
	for _, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			repos = append(repos, trimmed)
		}
	}
	return repos, nil
}

// repoBaseName extracts the last path component from a repo identifier.
// e.g., "github.com/kubernetes/kubernetes" -> "kubernetes"
func repoBaseName(repo string) string {
	if idx := strings.LastIndex(repo, "/"); idx >= 0 {
		return repo[idx+1:]
	}
	return repo
}
