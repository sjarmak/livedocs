package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor/defaults"
	"github.com/live-docs/live_docs/mcpserver"
	"github.com/live-docs/live_docs/sourcegraph"
)

var (
	mcpDBPath          string
	mcpDataDir         string
	mcpTelemetry       bool
	mcpEnableStaleness bool
	mcpTransport       string
	mcpPort            int
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
		cfg := mcpserver.Config{}

		if mcpDataDir != "" {
			cfg.DataDir = mcpDataDir
		}
		if mcpDBPath != "" {
			cfg.DBPath = mcpDBPath
		}
		// If neither flag is set, fall back to the default single-DB path.
		if cfg.DBPath == "" && cfg.DataDir == "" {
			cfg.DBPath = filepath.Join(".livedocs", "claims.db")
		}

		// Auto-discover repo roots from claims DBs for staleness checking.
		if mcpEnableStaleness && mcpDataDir != "" {
			roots, err := discoverRepoRoots(mcpDataDir)
			if err != nil {
				log.Printf("warning: discover repo roots: %v", err)
			} else if len(roots) > 0 {
				cfg.RepoRoots = roots
				cfg.ExtractorRegistry = defaults.BuildDefaultRegistry("")
			}
		}

		// Wire up ExtractionRunner when data-dir is set and Sourcegraph is configured.
		if mcpDataDir != "" && os.Getenv("SRC_ACCESS_TOKEN") != "" {
			sgClient, sgErr := sourcegraph.NewSourcegraphClient()
			if sgErr != nil {
				log.Printf("warning: create sourcegraph client for extraction runner: %v (extraction disabled)", sgErr)
			} else {
				cfg.ExtractionRunner = newExtractionRunner(sgClient, mcpDataDir, 10)
				defer sgClient.Close()
			}
		}

		cfg.Telemetry = mcpTelemetry || os.Getenv("LIVEDOCS_TELEMETRY") == "1"
		srv, err := mcpserver.New(cfg)
		if err != nil {
			return fmt.Errorf("create mcp server: %w", err)
		}
		defer srv.Close()

		switch mcpTransport {
		case "stdio":
			return srv.Serve()
		case "http":
			port := mcpPort
			if envPort := os.Getenv("PORT"); envPort != "" {
				if p, err := strconv.Atoi(envPort); err == nil {
					port = p
				}
			}
			addr := fmt.Sprintf(":%d", port)
			return srv.ServeHTTP(addr)
		default:
			return fmt.Errorf("unknown transport %q: supported values are \"stdio\" and \"http\"", mcpTransport)
		}
	},
}

func init() {
	mcpCmd.Flags().StringVar(&mcpDBPath, "db", "", "path to claims database (default: .livedocs/claims.db)")
	mcpCmd.Flags().StringVar(&mcpDataDir, "data-dir", "", "directory containing per-repo .claims.db files (multi-repo mode)")
	mcpCmd.Flags().BoolVar(&mcpTelemetry, "telemetry", false, "enable anonymous usage telemetry (writes daily metrics to ~/.livedocs/telemetry/)")
	mcpCmd.Flags().BoolVar(&mcpEnableStaleness, "enable-staleness", true, "auto-discover repo roots and enable lazy staleness checking")
	mcpCmd.Flags().StringVar(&mcpTransport, "transport", "stdio", "transport type: \"stdio\" (default) or \"http\" (HTTP/SSE for multi-client access)")
	mcpCmd.Flags().IntVar(&mcpPort, "port", 8080, "port for HTTP transport (only used with --transport http)")
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
