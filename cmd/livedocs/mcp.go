package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/mcpserver"
)

var mcpDBPath string

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start MCP server mode",
	Long:  "Run livedocs as a Model Context Protocol server, exposing query_claims, check_drift, and verify_section tools over stdio.",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath := mcpDBPath
		if dbPath == "" {
			dbPath = filepath.Join(".livedocs", "claims.db")
		}

		srv, err := mcpserver.New(mcpserver.Config{DBPath: dbPath})
		if err != nil {
			return fmt.Errorf("create mcp server: %w", err)
		}
		defer srv.Close()

		return srv.Serve()
	},
}

func init() {
	mcpCmd.Flags().StringVar(&mcpDBPath, "db", "", "path to claims database (default: .livedocs/claims.db)")
}
