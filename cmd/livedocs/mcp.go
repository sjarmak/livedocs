package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sjarmak/livedocs/config"
	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor/defaults"
	"github.com/sjarmak/livedocs/mcpserver"
	"github.com/sjarmak/livedocs/sourcegraph"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start MCP server mode",
	Long: `Run livedocs as a Model Context Protocol (MCP) server.

Supports two transport modes:
  stdio (default)  Communicates over stdin/stdout (single client)
  http             Serves over HTTP with Server-Sent Events (multi-client)

Exposes tools to AI assistants:
  query_claims     Search documentation claims by symbol name
  check_drift      Detect stale references in README files
  verify_section   Check if claims for a code range are still valid
  check_ai_context Verify AI context files for broken references

Examples:
  livedocs mcp                              # stdio transport (default)
  livedocs mcp --transport http --port 8080  # HTTP/SSE on port 8080

Setup: claude mcp add livedocs -- livedocs mcp
See SETUP.md for Cursor and Windsurf configuration.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		defer resetCmdFlags(cmd)

		dbFlag := mustGetString(cmd, "db")
		dataDir := mustGetString(cmd, "data-dir")
		telemetry := mustGetBool(cmd, "telemetry")
		enableStaleness := mustGetBool(cmd, "enable-staleness")
		transport := mustGetString(cmd, "transport")
		portFlag := mustGetInt(cmd, "port")

		cfg := mcpserver.Config{}

		if dataDir != "" {
			cfg.DataDir = dataDir
		}
		if dbFlag != "" {
			cfg.DBPath = dbFlag
		}
		// If neither flag is set, fall back to the default single-DB path.
		if cfg.DBPath == "" && cfg.DataDir == "" {
			cfg.DBPath = filepath.Join(".livedocs", "claims.db")
		}

		// Auto-discover repo roots from claims DBs for staleness checking.
		if enableStaleness && dataDir != "" {
			roots, err := discoverRepoRoots(dataDir)
			if err != nil {
				log.Printf("warning: discover repo roots: %v", err)
			} else if len(roots) > 0 {
				cfg.RepoRoots = roots
				cfg.ExtractorRegistry = defaults.BuildDefaultRegistry("")
			}
		}

		// Wire up ExtractionRunner when data-dir is set and Sourcegraph is configured.
		if dataDir != "" && os.Getenv("SRC_ACCESS_TOKEN") != "" {
			sgClient, sgErr := sourcegraph.NewSourcegraphClient()
			if sgErr != nil {
				log.Printf("warning: create sourcegraph client for extraction runner: %v (extraction disabled)", sgErr)
			} else {
				cfg.ExtractionRunner = newExtractionRunner(sgClient, dataDir, 10)
				defer sgClient.Close()
			}
		}

		// Wire up MiningFactory for tribal_mine_on_demand. The factory is
		// registered only when (a) multi-repo mode is active, (b) tribal LLM
		// extraction is explicitly enabled, (c) DailyBudget > 0, and (d) an
		// LLM client (Claude CLI or Anthropic API key) is reachable. When any
		// precondition fails, the tool is silently omitted from the registry
		// — the safe default for a long-running server.
		if dataDir != "" {
			tribalCfg := loadTribalConfigForMCP()
			cfg.MiningFactory = buildMiningFactory(tribalCfg, defaultLLMClientFactory)
			logMiningFactoryWireup(cfg.MiningFactory, tribalCfg)
		}

		cfg.Telemetry = telemetry || os.Getenv("LIVEDOCS_TELEMETRY") == "1"
		srv, err := mcpserver.New(cfg)
		if err != nil {
			return fmt.Errorf("create mcp server: %w", err)
		}
		defer srv.Close()

		switch transport {
		case "stdio":
			return srv.Serve()
		case "http":
			port := portFlag
			if envPort := os.Getenv("PORT"); envPort != "" {
				if p, err := strconv.Atoi(envPort); err == nil {
					port = p
				}
			}
			addr := fmt.Sprintf(":%d", port)
			return srv.ServeHTTP(addr)
		default:
			return fmt.Errorf("unknown transport %q: supported values are \"stdio\" and \"http\"", transport)
		}
	},
}

func init() {
	mcpCmd.Flags().String("db", "", "path to claims database (default: .livedocs/claims.db)")
	mcpCmd.Flags().String("data-dir", "", "directory containing per-repo .claims.db files (multi-repo mode)")
	mcpCmd.Flags().Bool("telemetry", false, "enable anonymous usage telemetry (writes daily metrics to ~/.livedocs/telemetry/)")
	mcpCmd.Flags().Bool("enable-staleness", true, "auto-discover repo roots and enable lazy staleness checking")
	mcpCmd.Flags().String("transport", "stdio", "transport type: \"stdio\" (default) or \"http\" (HTTP/SSE for multi-client access)")
	mcpCmd.Flags().Int("port", 8080, "port for HTTP transport (only used with --transport http)")
}

// loadTribalConfigForMCP loads the repo-root .livedocs.yaml and returns the
// resolved TribalConfig (with defaults applied). If the config file cannot
// be read, it returns an empty TribalConfig so buildMiningFactory falls back
// to the no-op path. The MCP server has no concept of "the current repo"
// — it runs at workspace scope — so this mirrors extract_cmd's pattern of
// loading from CWD.
func loadTribalConfigForMCP() config.TribalConfig {
	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("warning: getwd for tribal config: %v (JIT mining disabled)", err)
		return config.TribalConfig{}
	}
	cfg, err := config.Load(config.ConfigPath(cwd))
	if err != nil {
		log.Printf("warning: load tribal config: %v (JIT mining disabled)", err)
		return config.TribalConfig{}
	}
	return cfg.Tribal
}

// discoverRepoRoots scans dataDir for *.claims.db files, opens each to read
// extraction metadata, and returns a map of repo name to repo root directory
// for entries whose RepoRoot exists on disk.
func discoverRepoRoots(dataDir string) (map[string]string, error) {
	pattern := filepath.Join(dataDir, "*.claims.db")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob claims DBs: %w", err)
	}

	roots := make(map[string]string)
	for _, match := range matches {
		base := filepath.Base(match)
		repoName := strings.TrimSuffix(base, ".claims.db")
		if repoName == "" {
			continue
		}

		cdb, err := db.OpenClaimsDB(match)
		if err != nil {
			log.Printf("warning: open %s: %v (skipping)", match, err)
			continue
		}
		meta, err := cdb.GetExtractionMeta()
		cdb.Close()
		if err != nil {
			log.Printf("warning: read extraction meta from %s: %v (skipping)", match, err)
			continue
		}

		if meta.RepoRoot == "" {
			continue
		}

		info, err := os.Stat(meta.RepoRoot)
		if err != nil || !info.IsDir() {
			log.Printf("warning: repo root %q from %s does not exist or is not a directory (skipping)", meta.RepoRoot, base)
			continue
		}

		roots[repoName] = meta.RepoRoot
	}

	return roots, nil
}
