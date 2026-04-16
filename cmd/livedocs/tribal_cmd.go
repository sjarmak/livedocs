package main

import (
	"crypto/sha256"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/live-docs/live_docs/db"
)

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
}
