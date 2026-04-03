package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/mcpserver"
)

var (
	mcpDBPath    string
	mcpDataDir   string
	mcpTelemetry bool
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start MCP server mode",
	Long: `Run livedocs as a Model Context Protocol (MCP) server over stdio.

Exposes four tools to AI assistants:
  query_claims     Search documentation claims by symbol name
  check_drift      Detect stale references in README files
  verify_section   Check if claims for a code range are still valid
  check_ai_context Verify AI context files for broken references

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

		cfg.Telemetry = mcpTelemetry || os.Getenv("LIVEDOCS_TELEMETRY") == "1"
		srv, err := mcpserver.New(cfg)
		if err != nil {
			return fmt.Errorf("create mcp server: %w", err)
		}
		defer srv.Close()

		return srv.Serve()
	},
}

func init() {
	mcpCmd.Flags().StringVar(&mcpDBPath, "db", "", "path to claims database (default: .livedocs/claims.db)")
	mcpCmd.Flags().StringVar(&mcpDataDir, "data-dir", "", "directory containing per-repo .claims.db files (multi-repo mode)")
	mcpCmd.Flags().BoolVar(&mcpTelemetry, "telemetry", false, "enable anonymous usage telemetry (writes daily metrics to ~/.livedocs/telemetry/)")
}
