package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sjarmak/livedocs/audit"
	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/renderer"
)

var (
	exportFormat string
	exportOutput string
	exportRepo   string
	exportDB     string
)

var exportCmd = &cobra.Command{
	Use:   "export [path]",
	Short: "Export documentation audit report or rendered markdown",
	Long: `Generate a compliance audit report or Tier 1 markdown documentation.

Supported formats:
  audit-json   Machine-readable JSON (default)
  audit-md     Human-readable Markdown audit report
  markdown     Tier 1 enhanced go doc with cross-package sections

The audit formats trace documentation claims to commits and record freshness
status at a point in time. Suitable for SOC 2 / ISO 27001 evidence.

The markdown format renders claims-backed structural documentation including
Used By (reverse dependencies), Implements (interface satisfaction), and
Cross-Package References sections. Requires --repo flag.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}

		switch exportFormat {
		case "markdown":
			return runMarkdownExport(cmd, path)
		case "audit", "audit-json", "audit-md":
			return runAuditExport(cmd, path)
		default:
			return fmt.Errorf("unknown format %q: use \"audit-json\", \"audit-md\", or \"markdown\"", exportFormat)
		}
	},
}

func init() {
	exportCmd.Flags().StringVar(&exportFormat, "format", "audit-json",
		"output format: audit-json, audit-md, or markdown")
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "",
		"write output to file (default: stdout)")
	exportCmd.Flags().StringVar(&exportRepo, "repo", "",
		"repository name (required for markdown format)")
	exportCmd.Flags().StringVar(&exportDB, "db", "",
		"claims database path (default: <repo>.claims.db)")
}

func runAuditExport(cmd *cobra.Command, path string) error {
	report, err := audit.Generate(path, time.Now())
	if err != nil {
		return fmt.Errorf("generate audit report: %w", err)
	}

	w := cmd.OutOrStdout()
	if exportOutput != "" {
		f, err := os.Create(exportOutput)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer f.Close()
		w = f
	}

	switch exportFormat {
	case "audit", "audit-json":
		if err := audit.WriteJSON(w, report); err != nil {
			return fmt.Errorf("write JSON: %w", err)
		}
	case "audit-md":
		if err := audit.WriteMarkdown(w, report); err != nil {
			return fmt.Errorf("write markdown: %w", err)
		}
	}

	return nil
}

func runMarkdownExport(cmd *cobra.Command, path string) error {
	if exportRepo == "" {
		return fmt.Errorf("--repo is required for markdown format")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// Determine the claims DB path.
	dbPath := exportDB
	if dbPath == "" {
		dbPath = exportRepo + ".claims.db"
	}

	// Open the claims database.
	claimsDB, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		return fmt.Errorf("open claims db %s: %w", dbPath, err)
	}
	defer claimsDB.Close()

	// Derive the Go import path from the filesystem path.
	importPath, err := resolveImportPath(absPath)
	if err != nil {
		return fmt.Errorf("resolve import path: %w", err)
	}

	// Load package data from the claims DB.
	pd, err := renderer.LoadPackageData(claimsDB, importPath)
	if err != nil {
		return fmt.Errorf("load package data for %s: %w", importPath, err)
	}

	// Render markdown.
	md := renderer.RenderMarkdown(pd)

	// Write output.
	w := cmd.OutOrStdout()
	if exportOutput != "" {
		f, err := os.Create(exportOutput)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer f.Close()
		w = f
	}

	bw := bufio.NewWriter(w)
	if _, err := bw.WriteString(md); err != nil {
		return fmt.Errorf("write markdown: %w", err)
	}
	return bw.Flush()
}

// resolveImportPath determines the Go import path for a directory by reading
// the go.mod file in the module root and computing the relative subpackage path.
func resolveImportPath(absPath string) (string, error) {
	// Walk up from absPath to find go.mod.
	dir := absPath
	for {
		goModPath := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			modulePath, err := readModulePath(goModPath)
			if err != nil {
				return "", fmt.Errorf("read go.mod at %s: %w", goModPath, err)
			}

			rel, err := filepath.Rel(dir, absPath)
			if err != nil {
				return "", fmt.Errorf("compute relative path: %w", err)
			}

			if rel == "." {
				return modulePath, nil
			}
			// Use forward slashes for import paths.
			return modulePath + "/" + filepath.ToSlash(rel), nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("no go.mod found in %s or any parent directory", absPath)
}

// readModulePath extracts the module path from a go.mod file.
func readModulePath(goModPath string) (string, error) {
	f, err := os.Open(goModPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no module directive found in %s", goModPath)
}
