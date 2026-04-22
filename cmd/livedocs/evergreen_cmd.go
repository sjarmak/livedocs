package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/sjarmak/livedocs/evergreen"
	"github.com/sjarmak/livedocs/evergreen/executors"
	"github.com/sjarmak/livedocs/sourcegraph"
)

// evergreenExecutorFactory builds the RefreshExecutor used by save/refresh
// subcommands. Tests override this to inject a fake executor; the default
// constructs a deepsearch-MCP-backed executor that spawns a Sourcegraph
// MCP subprocess on demand.
//
// The returned close function releases any resources the factory allocated
// (currently the Sourcegraph MCP client). Callers must defer it.
var evergreenExecutorFactory = defaultEvergreenExecutor

func defaultEvergreenExecutor() (evergreen.RefreshExecutor, func(), error) {
	client, err := sourcegraph.NewSourcegraphClient()
	if err != nil {
		return nil, nil, fmt.Errorf("sourcegraph client: %w", err)
	}
	exec, err := executors.NewDeepSearchMCP(client)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("deepsearch executor: %w", err)
	}
	return exec, func() { _ = client.Close() }, nil
}

// evergreenCmd is the parent command for all evergreen-document subcommands.
var evergreenCmd = &cobra.Command{
	Use:   "evergreen",
	Short: "Manage evergreen deep-search documents",
	Long: `Save deep-search query answers as evergreen documents, detect drift
against the current codebase, and refresh on demand.

Documents are persisted in a local SQLite store. Running 'save' or
'refresh' calls the Sourcegraph MCP deepsearch tool to execute the
query; set SRC_ACCESS_TOKEN in your environment.`,
}

func init() {
	evergreenCmd.PersistentFlags().String("db", "evergreen.db",
		"Path to the evergreen SQLite store")

	evergreenCmd.AddCommand(evergreenListCmd)
	evergreenCmd.AddCommand(evergreenSaveCmd)
	evergreenCmd.AddCommand(evergreenCheckCmd)
	evergreenCmd.AddCommand(evergreenRefreshCmd)
	evergreenCmd.AddCommand(evergreenDeleteCmd)

	evergreenListCmd.Flags().Bool("json", false, "Emit JSON")

	evergreenSaveCmd.Flags().String("query", "",
		"Deep-search query to save (required)")
	evergreenSaveCmd.Flags().Int("max-age-days", 0,
		"Trigger age-based Cold drift after this many days; 0 disables")
	_ = evergreenSaveCmd.MarkFlagRequired("query")

	evergreenCheckCmd.Flags().Bool("json", false, "Emit JSON")

	evergreenRefreshCmd.Flags().Bool("acknowledge-orphan", false,
		"Proceed even if the document is in OrphanedStatus")
	evergreenRefreshCmd.Flags().Bool("dry-run", false,
		"Run the executor and print the result without persisting")
	evergreenRefreshCmd.Flags().Bool("json", false, "Emit JSON")
}

// --- list ----------------------------------------------------------------

var evergreenListCmd = &cobra.Command{
	Use:   "list",
	Short: "List saved evergreen documents",
	RunE: func(cmd *cobra.Command, args []string) error {
		defer resetCmdFlags(cmd)
		ctx := cmd.Context()
		dbPath := mustGetString(cmd, "db")
		jsonOut := mustGetBool(cmd, "json")

		store, err := evergreen.OpenSQLiteStore(ctx, dbPath)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		defer store.Close()

		docs, err := store.List(ctx)
		if err != nil {
			return fmt.Errorf("list: %w", err)
		}

		if jsonOut {
			return writeJSON(cmd.OutOrStdout(), docs)
		}
		return writeDocList(cmd.OutOrStdout(), docs)
	},
}

// --- save ----------------------------------------------------------------

var evergreenSaveCmd = &cobra.Command{
	Use:   "save",
	Short: "Run a deep-search query and save the result as an evergreen document",
	RunE: func(cmd *cobra.Command, args []string) error {
		defer resetCmdFlags(cmd)
		ctx := cmd.Context()
		dbPath := mustGetString(cmd, "db")
		query := mustGetString(cmd, "query")
		maxAgeDays := mustGetInt(cmd, "max-age-days")

		store, err := evergreen.OpenSQLiteStore(ctx, dbPath)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		defer store.Close()

		exec, closeExec, err := evergreenExecutorFactory()
		if err != nil {
			return fmt.Errorf("executor: %w", err)
		}
		defer closeExec()

		// Build a skeleton document, run the executor against it, then save.
		now := time.Now().UTC()
		seed := &evergreen.Document{
			ID:              evergreen.NewDocumentID(),
			Query:           query,
			Status:          evergreen.FreshStatus,
			RefreshPolicy:   evergreen.AlertPolicy,
			MaxAgeDays:      maxAgeDays,
			CreatedAt:       now,
			LastRefreshedAt: now,
			Backend:         exec.Name(),
		}
		res, err := exec.Refresh(ctx, seed)
		if err != nil {
			return fmt.Errorf("executor refresh: %w", err)
		}
		seed.RenderedAnswer = res.RenderedAnswer
		seed.Manifest = res.Manifest
		if res.Backend != "" {
			seed.Backend = res.Backend
		}
		seed.ExternalID = res.ExternalID

		if err := store.Save(ctx, seed); err != nil {
			return fmt.Errorf("save: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(),
			"saved %s (manifest entries: %d, backend: %s)\n",
			seed.ID, len(seed.Manifest), seed.Backend)
		return nil
	},
}

// --- check ---------------------------------------------------------------

var evergreenCheckCmd = &cobra.Command{
	Use:   "check [doc-id]",
	Short: "Detect drift on one or all evergreen documents",
	Long: `Runs the drift detector and reports findings grouped by severity
(Orphaned, Hot, Warm, Cold).

When a ClaimsReader is not wired (the default in the OSS CLI) only
doc-scoped findings (age-based Cold) are emitted — per-symbol drift
requires a claims-DB-backed reader, which is not yet available in the
OSS build.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		defer resetCmdFlags(cmd)
		ctx := cmd.Context()
		dbPath := mustGetString(cmd, "db")
		jsonOut := mustGetBool(cmd, "json")

		store, err := evergreen.OpenSQLiteStore(ctx, dbPath)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		defer store.Close()

		tool, err := evergreen.NewStatusTool(store, nil)
		if err != nil {
			return fmt.Errorf("status tool: %w", err)
		}

		in := evergreen.StatusInput{}
		if len(args) == 1 {
			in.DocID = args[0]
		}
		out, err := tool.Handle(ctx, in)
		if err != nil {
			if errors.Is(err, evergreen.ErrNotFound) {
				return fmt.Errorf("document not found")
			}
			return err
		}

		if jsonOut {
			return writeJSON(cmd.OutOrStdout(), out)
		}
		return writeCheckReport(cmd.OutOrStdout(), out)
	},
}

// --- refresh -------------------------------------------------------------

var evergreenRefreshCmd = &cobra.Command{
	Use:   "refresh <doc-id>",
	Short: "Re-run the saved query and update the document",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		defer resetCmdFlags(cmd)
		ctx := cmd.Context()
		docID := args[0]
		dbPath := mustGetString(cmd, "db")
		ack := mustGetBool(cmd, "acknowledge-orphan")
		dryRun := mustGetBool(cmd, "dry-run")
		jsonOut, _ := cmd.Flags().GetBool("json")

		store, err := evergreen.OpenSQLiteStore(ctx, dbPath)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		defer store.Close()

		exec, closeExec, err := evergreenExecutorFactory()
		if err != nil {
			return fmt.Errorf("executor: %w", err)
		}
		defer closeExec()

		if dryRun {
			return runEvergreenDryRunRefresh(ctx, cmd, store, exec, docID, ack, jsonOut)
		}

		limiter := evergreen.NewKeyedRateLimiter(evergreen.RateLimiterConfig{})
		tool, err := evergreen.NewRefreshTool(store, exec, limiter, nil)
		if err != nil {
			return fmt.Errorf("refresh tool: %w", err)
		}
		out, err := tool.Handle(ctx, evergreen.RefreshInput{
			DocID:             docID,
			AcknowledgeOrphan: ack,
		})
		if err != nil {
			switch {
			case errors.Is(err, evergreen.ErrNotFound):
				return fmt.Errorf("document %q not found", docID)
			case errors.Is(err, evergreen.ErrOrphaned):
				return fmt.Errorf("document is orphaned; pass --acknowledge-orphan to proceed")
			case errors.Is(err, evergreen.ErrRateLimited):
				return fmt.Errorf("rate-limited; try again later")
			default:
				return err
			}
		}

		if jsonOut {
			return writeJSON(cmd.OutOrStdout(), out)
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"refreshed %s (manifest: %d entries, findings: %d)\n",
			out.Document.ID, len(out.Document.Manifest), len(out.Findings))
		return nil
	},
}

// runEvergreenDryRunRefresh runs the executor but does not persist. Useful
// for previewing a refresh without consuming the rate-limit budget or
// disturbing the stored revision history.
//
// The orphan guard applies here as well: a dry-run against an orphaned
// document still requires explicit acknowledgement so users don't
// accidentally spend executor resources on a doc flagged for human review.
func runEvergreenDryRunRefresh(
	ctx context.Context,
	cmd *cobra.Command,
	store evergreen.DocumentStore,
	exec evergreen.RefreshExecutor,
	docID string,
	ack bool,
	jsonOut bool,
) error {
	doc, err := store.Get(ctx, docID)
	if err != nil {
		if errors.Is(err, evergreen.ErrNotFound) {
			return fmt.Errorf("document %q not found", docID)
		}
		return err
	}
	if doc.Status == evergreen.OrphanedStatus && !ack {
		return fmt.Errorf("document is orphaned; pass --acknowledge-orphan to proceed")
	}
	res, err := exec.Refresh(ctx, doc)
	if err != nil {
		return fmt.Errorf("executor refresh: %w", err)
	}
	if jsonOut {
		return writeJSON(cmd.OutOrStdout(), res)
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"dry-run: would refresh %s (new manifest: %d entries, backend: %s)\n",
		doc.ID, len(res.Manifest), res.Backend)
	return nil
}

// --- delete --------------------------------------------------------------

var evergreenDeleteCmd = &cobra.Command{
	Use:   "delete <doc-id>",
	Short: "Delete an evergreen document and its revision history",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		defer resetCmdFlags(cmd)
		ctx := cmd.Context()
		docID := args[0]
		dbPath := mustGetString(cmd, "db")

		store, err := evergreen.OpenSQLiteStore(ctx, dbPath)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		defer store.Close()

		if err := store.Delete(ctx, docID); err != nil {
			if errors.Is(err, evergreen.ErrNotFound) {
				return fmt.Errorf("document %q not found", docID)
			}
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", docID)
		return nil
	},
}

// --- rendering helpers --------------------------------------------------

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeDocList(w io.Writer, docs []*evergreen.Document) error {
	if len(docs) == 0 {
		fmt.Fprintln(w, "(no evergreen documents)")
		return nil
	}
	fmt.Fprintf(w, "%-40s  %-10s  %-20s  %s\n", "ID", "STATUS", "LAST REFRESH", "QUERY")
	for _, d := range docs {
		q := d.Query
		if len(q) > 60 {
			q = q[:57] + "..."
		}
		fmt.Fprintf(w, "%-40s  %-10s  %-20s  %s\n",
			d.ID, d.Status,
			d.LastRefreshedAt.UTC().Format(time.RFC3339),
			q,
		)
	}
	return nil
}

func writeCheckReport(w io.Writer, out evergreen.StatusOutput) error {
	if len(out.Documents) == 0 {
		fmt.Fprintln(w, "(no evergreen documents)")
		return nil
	}
	for i, df := range out.Documents {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "## %s [%s]\n", df.Document.ID, df.Document.Status)
		fmt.Fprintf(w, "query: %s\n", df.Document.Query)
		fmt.Fprintf(w, "last refresh: %s\n", df.Document.LastRefreshedAt.UTC().Format(time.RFC3339))
		if len(df.Findings) == 0 {
			fmt.Fprintln(w, "findings: (none)")
			continue
		}
		bySev := map[evergreen.Severity][]evergreen.Finding{}
		for _, f := range df.Findings {
			bySev[f.Severity] = append(bySev[f.Severity], f)
		}
		for _, sev := range []evergreen.Severity{
			evergreen.OrphanedSeverity,
			evergreen.HotSeverity,
			evergreen.WarmSeverity,
			evergreen.ColdSeverity,
		} {
			fs := bySev[sev]
			if len(fs) == 0 {
				continue
			}
			fmt.Fprintf(w, "%s (%d):\n", sev, len(fs))
			for _, f := range fs {
				fmt.Fprintf(w, "  - %s: %s\n", f.ChangeKind, f.Detail)
			}
		}
	}
	return nil
}
