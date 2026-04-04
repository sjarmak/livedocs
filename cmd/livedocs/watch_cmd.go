package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/cache"
	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
	"github.com/live-docs/live_docs/extractor/goextractor"
	"github.com/live-docs/live_docs/extractor/lang"
	"github.com/live-docs/live_docs/extractor/treesitter"
	"github.com/live-docs/live_docs/pipeline"
	"github.com/live-docs/live_docs/watch"
)

var (
	watchInterval  time.Duration
	watchRepo      string
	watchOutput    string
	watchStateFile string
	watchConfig    string
	watchReposDir  string
)

var watchCmd = &cobra.Command{
	Use:   "watch [path]",
	Short: "Watch a repository for changes and incrementally extract claims",
	Long: `Polls git rev-parse HEAD on an interval and triggers incremental
extraction when HEAD changes. Persists last-indexed commit SHA to a state
file so it resumes correctly after restart.

Supports three modes:
  1. Single repo:  livedocs watch <path>
  2. Config file:  livedocs watch --config repos.json
  3. Directory:    livedocs watch --repos-dir /path/to/repos

Handles force-push by falling back to full extraction when the stored SHA
is no longer an ancestor of the current HEAD.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Determine which mode we're in.
		hasPath := len(args) == 1
		hasConfig := watchConfig != ""
		hasReposDir := watchReposDir != ""

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
			entries, err = watch.LoadConfig(watchConfig)
			if err != nil {
				return err
			}
		case hasReposDir:
			entries, err = watch.ScanReposDir(watchReposDir)
			if err != nil {
				return err
			}
		default:
			// Single repo mode — build one entry from args and flags.
			absRepo, absErr := filepath.Abs(args[0])
			if absErr != nil {
				return fmt.Errorf("resolve repo path: %w", absErr)
			}
			name := watchRepo
			if name == "" {
				name = filepath.Base(absRepo)
			}
			output := watchOutput
			if output == "" {
				output = name + ".claims.db"
			}
			entries = []watch.RepoEntry{{
				Path:   absRepo,
				Name:   name,
				Output: output,
			}}
		}

		// Determine state file.
		stateFile := watchStateFile
		if stateFile == "" {
			// Place state file next to first output, or in current dir.
			if len(entries) > 0 {
				stateFile = filepath.Join(filepath.Dir(entries[0].Output), ".livedocs-watch-state.json")
			} else {
				stateFile = ".livedocs-watch-state.json"
			}
		}

		return runWatchMulti(cmd, entries, stateFile, watchInterval)
	},
}

func init() {
	watchCmd.Flags().DurationVar(&watchInterval, "interval", 5*time.Second, "polling interval (e.g. 5s, 1m)")
	watchCmd.Flags().StringVar(&watchRepo, "repo", "", "repository name (default: directory basename)")
	watchCmd.Flags().StringVarP(&watchOutput, "output", "o", "", "output SQLite file path (default: <repo>.claims.db)")
	watchCmd.Flags().StringVar(&watchStateFile, "state-file", "", "state file path (default: .livedocs-watch-state.json next to output)")
	watchCmd.Flags().StringVar(&watchConfig, "config", "", "JSON config file listing repos to watch")
	watchCmd.Flags().StringVar(&watchReposDir, "repos-dir", "", "directory of git repos to watch")
}

// buildRegistry creates an extractor registry for the given repo name.
func buildRegistry(repoName string) *extractor.Registry {
	registry := extractor.NewRegistry()

	goDeep := &goextractor.GoDeepExtractor{Repo: repoName}
	registry.Register(extractor.LanguageConfig{
		Language:      "go",
		Extensions:    []string{".go"},
		DeepExtractor: goDeep,
	})

	langRegistry := lang.NewRegistry()
	tsExtractor := treesitter.New(langRegistry)

	registry.Register(extractor.LanguageConfig{
		Language:          "typescript",
		Extensions:        []string{".ts", ".tsx"},
		TreeSitterGrammar: "tree-sitter-typescript",
		FastExtractor:     tsExtractor,
	})
	registry.Register(extractor.LanguageConfig{
		Language:          "python",
		Extensions:        []string{".py"},
		TreeSitterGrammar: "tree-sitter-python",
		FastExtractor:     tsExtractor,
	})
	registry.Register(extractor.LanguageConfig{
		Language:          "shell",
		Extensions:        []string{".sh"},
		TreeSitterGrammar: "tree-sitter-bash",
		FastExtractor:     tsExtractor,
	})

	return registry
}

// runWatchMulti launches one watcher per repo entry, all sharing the same
// state file and polling interval. Blocks until ctx is cancelled.
func runWatchMulti(cmd *cobra.Command, entries []watch.RepoEntry, stateFile string, interval time.Duration) error {
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

		// Open in-memory cache.
		cacheStore, err := cache.NewSQLiteStore(":memory:", 2*1024*1024*1024)
		if err != nil {
			claimsDB.Close()
			return fmt.Errorf("open cache for %s: %w", entry.Name, err)
		}

		resources = append(resources, repoResources{claimsDB: claimsDB, cacheStore: cacheStore})

		// Build registry and pipeline.
		registry := buildRegistry(entry.Name)
		p := pipeline.New(pipeline.Config{
			Repo:     entry.Name,
			RepoDir:  entry.Path,
			Cache:    cacheStore,
			ClaimsDB: claimsDB,
			Registry: registry,
		})

		w := watch.New(watch.Config{
			RepoDir:   entry.Path,
			RepoName:  entry.Name,
			Interval:  interval,
			StateFile: stateFile,
			Pipeline:  p,
			Out:       out,
			State:     sharedState,
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
