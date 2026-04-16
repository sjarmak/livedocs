package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/db"
)

// ValidateDBPath checks that the given path is a valid claims database path.
// It requires the .claims.db suffix and, if dataDir is non-empty, ensures the
// path is rooted under dataDir to prevent directory traversal.
//
// Both dataDir and dbPath are resolved via filepath.EvalSymlinks (falling back
// to filepath.Abs when the path does not yet exist) so that symlinks pointing
// outside the data directory are correctly rejected.
func ValidateDBPath(dbPath, dataDir string) error {
	if dbPath == "" {
		return fmt.Errorf("db path must not be empty")
	}

	if !strings.HasSuffix(dbPath, ".claims.db") {
		return fmt.Errorf("db path %q must end with .claims.db", dbPath)
	}

	if dataDir != "" {
		absData, err := resolveReal(dataDir)
		if err != nil {
			return fmt.Errorf("resolve data dir: %w", err)
		}
		absDB, err := resolveReal(dbPath)
		if err != nil {
			return fmt.Errorf("resolve db path: %w", err)
		}
		if !strings.HasPrefix(absDB, absData+string(filepath.Separator)) && absDB != absData {
			return fmt.Errorf("db path %q is outside data directory %q", dbPath, dataDir)
		}
	}

	return nil
}

// resolveReal returns the real, absolute path for p by calling
// filepath.EvalSymlinks. If p does not exist yet, it falls back to
// filepath.Abs so that ValidateDBPath works for paths that will be created.
func resolveReal(p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		if os.IsNotExist(err) {
			return filepath.Abs(p)
		}
		return "", err
	}
	return filepath.Abs(resolved)
}

// validateBodyLength returns an error if body exceeds db.MaxBodyBytes.
func validateBodyLength(body string) error {
	if len(body) > db.MaxBodyBytes {
		return fmt.Errorf("--body length (%d bytes) exceeds maximum allowed (%d bytes)", len(body), db.MaxBodyBytes)
	}
	return nil
}

var tribalCmd = &cobra.Command{
	Use:   "tribal",
	Short: "Tribal knowledge management commands",
	Long:  "Subcommands for inspecting and managing tribal knowledge facts in claims databases.",
}

var tribalStatusCmd = &cobra.Command{
	Use:   "status <db-path>",
	Short: "Show tribal knowledge fact counts by kind",
	Long:  "Opens a claims database and reports the number of active tribal facts grouped by kind.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath := args[0]

		if err := ValidateDBPath(dbPath, ""); err != nil {
			return err
		}

		claimsDB, err := db.OpenClaimsDB(dbPath)
		if err != nil {
			return fmt.Errorf("open claims db: %w", err)
		}
		defer claimsDB.Close()

		counts, err := claimsDB.CountTribalFactsByKind()
		if err != nil {
			return fmt.Errorf("count tribal facts: %w", err)
		}

		out := cmd.OutOrStdout()

		if len(counts) == 0 {
			fmt.Fprintf(out, "No tribal facts found in %s\n", dbPath)
			return nil
		}

		fmt.Fprintf(out, "## Tribal Knowledge Status\n\n")
		fmt.Fprintf(out, "Database: %s\n\n", dbPath)

		// Sort kinds for deterministic output.
		kinds := make([]string, 0, len(counts))
		for k := range counts {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)

		total := 0
		for _, kind := range kinds {
			count := counts[kind]
			fmt.Fprintf(out, "- **%s**: %d\n", kind, count)
			total += count
		}
		fmt.Fprintf(out, "- **total**: %d\n", total)

		return nil
	},
}

// createReplacementFact builds and upserts a replacement tribal fact based on
// the original, using the new body text. Returns the new fact ID. The extractor
// field records the action source (e.g., "cli_correct", "cli_supersede").
func createReplacementFact(cdb *db.ClaimsDB, original db.TribalFact, body, extractor string, factID int64) (int64, error) {
	now := db.Now()
	stalenessHash := fmt.Sprintf("%x", sha256.Sum256([]byte(body+original.SourceQuote)))

	newFact := db.TribalFact{
		SubjectID:        original.SubjectID,
		Kind:             original.Kind,
		Body:             body,
		SourceQuote:      original.SourceQuote,
		Confidence:       original.Confidence,
		Corroboration:    original.Corroboration,
		Extractor:        extractor,
		ExtractorVersion: "v1",
		StalenessHash:    stalenessHash,
		Status:           "active",
		CreatedAt:        now,
		LastVerified:     now,
	}

	evidence := []db.TribalEvidence{{
		SourceType:  "correction",
		SourceRef:   fmt.Sprintf("%s fact %d", extractor, factID),
		ContentHash: stalenessHash,
	}}

	newID, _, err := cdb.UpsertTribalFact(newFact, evidence)
	if err != nil {
		return 0, fmt.Errorf("insert replacement fact: %w", err)
	}
	return newID, nil
}

// ---------------------------------------------------------------------------
// tribal correct
// ---------------------------------------------------------------------------

var tribalCorrectCmd = &cobra.Command{
	Use:   "correct",
	Short: "Correct an existing tribal fact",
	Long:  "Inserts a correction row with action='correct' and creates a new replacement fact with the updated body.",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")
		factID, _ := cmd.Flags().GetInt64("fact-id")
		body, _ := cmd.Flags().GetString("body")
		reason, _ := cmd.Flags().GetString("reason")

		if err := ValidateDBPath(dbPath, ""); err != nil {
			return err
		}
		if err := validateBodyLength(body); err != nil {
			return err
		}

		cdb, err := db.OpenClaimsDB(dbPath)
		if err != nil {
			return fmt.Errorf("open claims db: %w", err)
		}
		defer cdb.Close()

		original, err := cdb.GetTribalFactByID(factID)
		if err != nil {
			return fmt.Errorf("get fact: %w", err)
		}

		// Record the correction.
		_, err = cdb.InsertTribalCorrection(db.TribalCorrection{
			FactID:    factID,
			Action:    "correct",
			NewBody:   body,
			Reason:    reason,
			Actor:     "cli",
			CreatedAt: db.Now(),
		})
		if err != nil {
			return fmt.Errorf("insert correction: %w", err)
		}

		newID, err := createReplacementFact(cdb, original, body, "cli_correct", factID)
		if err != nil {
			return err
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Corrected fact %d -> new fact %d\n", factID, newID)
		return nil
	},
}

// ---------------------------------------------------------------------------
// tribal supersede
// ---------------------------------------------------------------------------

var tribalSupersedeCmd = &cobra.Command{
	Use:   "supersede",
	Short: "Supersede an existing tribal fact",
	Long:  "Sets the original fact to status='superseded', records a correction, and creates a replacement fact.",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")
		factID, _ := cmd.Flags().GetInt64("fact-id")
		body, _ := cmd.Flags().GetString("body")
		reason, _ := cmd.Flags().GetString("reason")

		if err := ValidateDBPath(dbPath, ""); err != nil {
			return err
		}
		if err := validateBodyLength(body); err != nil {
			return err
		}

		cdb, err := db.OpenClaimsDB(dbPath)
		if err != nil {
			return fmt.Errorf("open claims db: %w", err)
		}
		defer cdb.Close()

		original, err := cdb.GetTribalFactByID(factID)
		if err != nil {
			return fmt.Errorf("get fact: %w", err)
		}

		// Mark the original as superseded.
		if err := cdb.UpdateFactStatus(factID, "superseded"); err != nil {
			return fmt.Errorf("supersede old fact: %w", err)
		}

		// Record the correction.
		_, err = cdb.InsertTribalCorrection(db.TribalCorrection{
			FactID:    factID,
			Action:    "supersede",
			NewBody:   body,
			Reason:    reason,
			Actor:     "cli",
			CreatedAt: db.Now(),
		})
		if err != nil {
			return fmt.Errorf("insert correction: %w", err)
		}

		newID, err := createReplacementFact(cdb, original, body, "cli_supersede", factID)
		if err != nil {
			return err
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Superseded fact %d -> new fact %d\n", factID, newID)
		return nil
	},
}

// ---------------------------------------------------------------------------
// tribal delete
// ---------------------------------------------------------------------------

var tribalDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a tribal fact",
	Long:  "Sets the fact status to 'deleted' and records a correction row with action='delete'.",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")
		factID, _ := cmd.Flags().GetInt64("fact-id")
		reason, _ := cmd.Flags().GetString("reason")

		if err := ValidateDBPath(dbPath, ""); err != nil {
			return err
		}

		cdb, err := db.OpenClaimsDB(dbPath)
		if err != nil {
			return fmt.Errorf("open claims db: %w", err)
		}
		defer cdb.Close()

		// Set status to deleted (also validates fact exists).
		if err := cdb.UpdateFactStatus(factID, "deleted"); err != nil {
			return fmt.Errorf("delete fact: %w", err)
		}

		now := db.Now()

		// Record the correction.
		_, err = cdb.InsertTribalCorrection(db.TribalCorrection{
			FactID:    factID,
			Action:    "delete",
			Reason:    reason,
			Actor:     "cli",
			CreatedAt: now,
		})
		if err != nil {
			return fmt.Errorf("insert correction: %w", err)
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Deleted fact %d\n", factID)
		return nil
	},
}

func init() {
	tribalCmd.AddCommand(tribalStatusCmd)

	// correct flags
	tribalCorrectCmd.Flags().String("db", "", "Path to claims database")
	tribalCorrectCmd.Flags().Int64("fact-id", 0, "ID of the fact to correct")
	tribalCorrectCmd.Flags().String("body", "", "New corrected body text")
	tribalCorrectCmd.Flags().String("reason", "", "Reason for the correction")
	_ = tribalCorrectCmd.MarkFlagRequired("db")
	_ = tribalCorrectCmd.MarkFlagRequired("fact-id")
	_ = tribalCorrectCmd.MarkFlagRequired("body")
	_ = tribalCorrectCmd.MarkFlagRequired("reason")
	tribalCmd.AddCommand(tribalCorrectCmd)

	// supersede flags
	tribalSupersedeCmd.Flags().String("db", "", "Path to claims database")
	tribalSupersedeCmd.Flags().Int64("fact-id", 0, "ID of the fact to supersede")
	tribalSupersedeCmd.Flags().String("body", "", "New replacement body text")
	tribalSupersedeCmd.Flags().String("reason", "", "Reason for superseding")
	_ = tribalSupersedeCmd.MarkFlagRequired("db")
	_ = tribalSupersedeCmd.MarkFlagRequired("fact-id")
	_ = tribalSupersedeCmd.MarkFlagRequired("body")
	_ = tribalSupersedeCmd.MarkFlagRequired("reason")
	tribalCmd.AddCommand(tribalSupersedeCmd)

	// delete flags
	tribalDeleteCmd.Flags().String("db", "", "Path to claims database")
	tribalDeleteCmd.Flags().Int64("fact-id", 0, "ID of the fact to delete")
	tribalDeleteCmd.Flags().String("reason", "", "Reason for deletion")
	_ = tribalDeleteCmd.MarkFlagRequired("db")
	_ = tribalDeleteCmd.MarkFlagRequired("fact-id")
	_ = tribalDeleteCmd.MarkFlagRequired("reason")
	tribalCmd.AddCommand(tribalDeleteCmd)

	// reverify flags
	tribalReverifyCmd.Flags().Int("sample", 20, "maximum number of facts to reverify")
	tribalReverifyCmd.Flags().String("max-age", "30d", "minimum age for a fact to be eligible (e.g. 30d, 720h)")
	tribalReverifyCmd.Flags().Int("budget", 100, "maximum LLM calls allowed")
	tribalReverifyCmd.Flags().Bool("dry-run", false, "show eligible facts without reverifying")
	tribalCmd.AddCommand(tribalReverifyCmd)
}

var tribalReverifyCmd = &cobra.Command{
	Use:   "reverify <db-path>",
	Short: "Reverify aged LLM-extracted tribal facts via semantic check",
	Long: `Samples active LLM-extracted facts older than --max-age, runs one semantic
verification call per fact, and applies the verdict:
  accept    → update last_verified to now
  downgrade → multiply confidence by 0.6
  reject    → set status to 'stale'

Budget-tracked: stops after --budget LLM calls even if more facts remain.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath := args[0]

		if err := ValidateDBPath(dbPath, ""); err != nil {
			return err
		}

		sampleSize, _ := cmd.Flags().GetInt("sample")
		maxAgeStr, _ := cmd.Flags().GetString("max-age")
		budget, _ := cmd.Flags().GetInt("budget")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		if sampleSize <= 0 {
			return fmt.Errorf("--sample must be > 0, got %d", sampleSize)
		}

		maxAge, err := parseDuration(maxAgeStr)
		if err != nil {
			return fmt.Errorf("invalid --max-age %q: %w", maxAgeStr, err)
		}

		claimsDB, err := db.OpenClaimsDB(dbPath)
		if err != nil {
			return fmt.Errorf("open claims db: %w", err)
		}
		defer claimsDB.Close()

		out := cmd.OutOrStdout()

		if dryRun {
			cutoff := time.Now().Add(-maxAge).UTC().Format(time.RFC3339)
			facts, err := claimsDB.GetActiveLLMFactsOlderThan(cutoff)
			if err != nil {
				return fmt.Errorf("query facts: %w", err)
			}
			if sampleSize < len(facts) {
				facts = facts[:sampleSize]
			}
			fmt.Fprintf(out, "Dry-run: would reverify %d facts (sample=%d, max-age=%s, budget=%d)\n",
				len(facts), sampleSize, maxAgeStr, budget)
			return nil
		}

		return fmt.Errorf("semantic verifier not yet wired; use --dry-run to preview eligible facts")
	},
}

// parseDuration parses a duration string that supports "d" suffix for days
// in addition to Go's standard time.ParseDuration suffixes.
func parseDuration(s string) (time.Duration, error) {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err != nil {
			return 0, fmt.Errorf("parse days: %w", err)
		}
		if days <= 0 {
			return 0, fmt.Errorf("days must be > 0, got %d", days)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
