package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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
)

var watchCmd = &cobra.Command{
	Use:   "watch <path>",
	Short: "Watch a repository for changes and incrementally extract claims",
	Long: `Polls git rev-parse HEAD on an interval and triggers incremental
extraction when HEAD changes. Persists last-indexed commit SHA to a state
file so it resumes correctly after restart.

Handles force-push by falling back to full extraction when the stored SHA
is no longer an ancestor of the current HEAD.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoPath := args[0]
		absRepo, err := filepath.Abs(repoPath)
		if err != nil {
			return fmt.Errorf("resolve repo path: %w", err)
		}

		if watchRepo == "" {
			watchRepo = filepath.Base(absRepo)
		}

		if watchOutput == "" {
			watchOutput = watchRepo + ".claims.db"
		}

		if watchStateFile == "" {
			watchStateFile = filepath.Join(filepath.Dir(watchOutput), ".livedocs-watch-state.json")
		}

		return runWatch(cmd, absRepo, watchRepo, watchOutput, watchStateFile, watchInterval)
	},
}

func init() {
	watchCmd.Flags().DurationVar(&watchInterval, "interval", 5*time.Second, "polling interval (e.g. 5s, 1m)")
	watchCmd.Flags().StringVar(&watchRepo, "repo", "", "repository name (default: directory basename)")
	watchCmd.Flags().StringVarP(&watchOutput, "output", "o", "", "output SQLite file path (default: <repo>.claims.db)")
	watchCmd.Flags().StringVar(&watchStateFile, "state-file", "", "state file path (default: .livedocs-watch-state.json next to output)")
}

func runWatch(cmd *cobra.Command, repoDir, repoName, outputPath, stateFile string, interval time.Duration) error {
	out := cmd.OutOrStdout()

	// Open claims DB (create if needed).
	claimsDB, err := db.OpenClaimsDB(outputPath)
	if err != nil {
		return fmt.Errorf("open claims db: %w", err)
	}
	defer claimsDB.Close()

	if err := claimsDB.CreateSchema(); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// Open in-memory cache.
	cacheStore, err := cache.NewSQLiteStore(":memory:", 2*1024*1024*1024)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	defer cacheStore.Close()

	// Set up extractor registry (same as extract command).
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

	// Create pipeline.
	p := pipeline.New(pipeline.Config{
		Repo:     repoName,
		RepoDir:  repoDir,
		Cache:    cacheStore,
		ClaimsDB: claimsDB,
		Registry: registry,
	})

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

	// Create and run watcher.
	w := watch.New(watch.Config{
		RepoDir:   repoDir,
		RepoName:  repoName,
		Interval:  interval,
		StateFile: stateFile,
		Pipeline:  p,
		Out:       out,
	})

	return w.Run(ctx)
}
