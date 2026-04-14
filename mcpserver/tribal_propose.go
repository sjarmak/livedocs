// Package mcpserver tribal_propose.go implements the tribal_propose_fact MCP
// tool that allows agents and humans to propose new tribal knowledge facts.
// Uses ONLY adapter types — no mcp-go imports.
package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/live-docs/live_docs/db"
)

// ---------------------------------------------------------------------------
// Evidence input type (parsed from the JSON array parameter)
// ---------------------------------------------------------------------------

// evidenceInput is one element of the evidence JSON array submitted by callers.
type evidenceInput struct {
	SourceType  string `json:"source_type"`
	SourceRef   string `json:"source_ref"`
	ContentHash string `json:"content_hash"`
}

// ---------------------------------------------------------------------------
// Response type
// ---------------------------------------------------------------------------

// proposeFactResponse is the JSON output for tribal_propose_fact.
type proposeFactResponse struct {
	FactID int64  `json:"fact_id"`
	Status string `json:"status"`
	Action string `json:"action"`
}

// ---------------------------------------------------------------------------
// Tool definition
// ---------------------------------------------------------------------------

// TribalProposeFactToolDef returns the ToolDef for tribal_propose_fact.
func TribalProposeFactToolDef(pool *DBPool) ToolDef {
	return ToolDef{
		Name: "tribal_propose_fact",
		Description: `Propose a new tribal knowledge fact for a symbol.

Creates a tribal fact with provenance evidence. Unsigned proposals (no writer_identity)
are quarantined for review. Signed proposals (non-empty writer_identity) are activated
immediately.

Supports three actions:
- create (default): insert a new fact
- correct: mark an existing fact as corrected, then insert a replacement
- supersede: mark an existing fact as superseded, record a correction, then insert a replacement

Evidence must be a JSON array with at least one entry. Each entry requires source_type,
source_ref, and content_hash fields.`,
		Params: []ParamDef{
			{Name: "symbol", Type: ParamString, Required: true, Description: "Symbol name the fact is about."},
			{Name: "repo", Type: ParamString, Required: true, Description: "Repository name (must match a .claims.db file)."},
			{Name: "kind", Type: ParamString, Required: true, Description: "Fact kind: ownership, rationale, invariant, quirk, todo, or deprecation."},
			{Name: "body", Type: ParamString, Required: true, Description: "The fact body text."},
			{Name: "source_quote", Type: ParamString, Required: true, Description: "Direct quote from the source material supporting this fact."},
			{Name: "evidence", Type: ParamString, Required: true, Description: "JSON array of evidence objects. Each must have source_type, source_ref, and content_hash."},
			{Name: "writer_identity", Type: ParamString, Required: false, Description: "Identity of the proposer. If empty, fact is quarantined. If non-empty, fact is activated."},
			{Name: "action", Type: ParamString, Required: false, Description: "Action: create (default), correct, or supersede. For correct/supersede, fact_id is required."},
			{Name: "fact_id", Type: ParamNumber, Required: false, Description: "ID of the existing fact to correct or supersede. Required when action is correct or supersede."},
		},
		Handler: tribalProposeFactHandler(pool),
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func tribalProposeFactHandler(pool *DBPool) ToolHandler {
	return func(_ context.Context, req ToolRequest) (ToolResult, error) {
		// --- Extract required parameters ---
		symbol, err := req.RequireString("symbol")
		if err != nil {
			return NewErrorResult("missing required parameter 'symbol'"), nil
		}
		repo, err := req.RequireString("repo")
		if err != nil {
			return NewErrorResult("missing required parameter 'repo'"), nil
		}
		kind, err := req.RequireString("kind")
		if err != nil {
			return NewErrorResult("missing required parameter 'kind'"), nil
		}
		body, err := req.RequireString("body")
		if err != nil {
			return NewErrorResult("missing required parameter 'body'"), nil
		}
		sourceQuote, err := req.RequireString("source_quote")
		if err != nil {
			return NewErrorResult("missing required parameter 'source_quote'"), nil
		}
		evidenceRaw, err := req.RequireString("evidence")
		if err != nil {
			return NewErrorResult("missing required parameter 'evidence'"), nil
		}

		// --- Extract optional parameters ---
		writerIdentity := req.GetString("writer_identity", "")
		action := req.GetString("action", "create")
		factID := int64(req.GetFloat("fact_id", 0))

		// --- Validate kind ---
		if !db.ValidFactKind(kind) {
			return NewErrorResultf("invalid kind %q: must be one of ownership, rationale, invariant, quirk, todo, deprecation", kind), nil
		}

		// --- Validate action ---
		validActions := map[string]bool{"create": true, "correct": true, "supersede": true}
		if !validActions[action] {
			return NewErrorResultf("invalid action %q: must be create, correct, or supersede", action), nil
		}

		// --- Parse and validate evidence ---
		var evidenceItems []evidenceInput
		if err := json.Unmarshal([]byte(evidenceRaw), &evidenceItems); err != nil {
			return NewErrorResultf("invalid evidence JSON: %v", err), nil
		}
		if len(evidenceItems) == 0 {
			return NewErrorResult("evidence array must contain at least one entry"), nil
		}
		for i, ev := range evidenceItems {
			if ev.SourceType == "" {
				return NewErrorResultf("evidence[%d]: source_type is required", i), nil
			}
			if ev.SourceRef == "" {
				return NewErrorResultf("evidence[%d]: source_ref is required", i), nil
			}
			if ev.ContentHash == "" {
				return NewErrorResultf("evidence[%d]: content_hash is required", i), nil
			}
		}

		// --- Validate fact_id for correct/supersede ---
		if (action == "correct" || action == "supersede") && factID == 0 {
			return NewErrorResultf("fact_id is required for action %q", action), nil
		}

		// --- Determine status ---
		status := "quarantined"
		if writerIdentity != "" {
			status = "active"
		}

		// --- Open repo DB ---
		cdb, err := pool.Open(repo)
		if err != nil {
			return NewErrorResultf("open repo %q: %v", repo, err), nil
		}

		// Ensure tribal schema exists.
		if err := cdb.CreateTribalSchema(); err != nil {
			return NewErrorResultf("create tribal schema: %v", err), nil
		}

		// --- Resolve symbol ---
		// Try to find the symbol by name in this repo. If not found, create a
		// file-level symbol so the fact has a valid subject_id.
		symbols, err := cdb.SearchSymbolsByName(symbol)
		if err != nil {
			return NewErrorResultf("search symbol %q: %v", symbol, err), nil
		}

		var subjectID int64
		if len(symbols) > 0 {
			// Use the first match in this repo; prefer exact repo match.
			for _, s := range symbols {
				if s.Repo == repo {
					subjectID = s.ID
					break
				}
			}
			if subjectID == 0 {
				subjectID = symbols[0].ID
			}
		} else {
			// Create a file-level symbol as a placeholder.
			subjectID, err = cdb.UpsertSymbol(db.Symbol{
				Repo:       repo,
				ImportPath: "",
				SymbolName: symbol,
				Language:   "unknown",
				Kind:       "file",
				Visibility: "public",
			})
			if err != nil {
				return NewErrorResultf("create symbol %q: %v", symbol, err), nil
			}
		}

		// --- Build evidence rows ---
		dbEvidence := make([]db.TribalEvidence, len(evidenceItems))
		for i, ev := range evidenceItems {
			dbEvidence[i] = db.TribalEvidence{
				SourceType:  ev.SourceType,
				SourceRef:   ev.SourceRef,
				ContentHash: ev.ContentHash,
			}
		}

		// --- Build the fact ---
		now := db.Now()
		stalenessHash := fmt.Sprintf("%x", sha256.Sum256([]byte(body+sourceQuote)))
		extractor := "mcp_propose"
		if writerIdentity != "" {
			extractor = "mcp_propose:" + writerIdentity
		}

		newFact := db.TribalFact{
			SubjectID:        subjectID,
			Kind:             kind,
			Body:             body,
			SourceQuote:      sourceQuote,
			Confidence:       1.0,
			Corroboration:    1,
			Extractor:        extractor,
			ExtractorVersion: "v1",
			StalenessHash:    stalenessHash,
			Status:           status,
			CreatedAt:        now,
			LastVerified:     now,
		}

		// --- Execute the action ---
		var newFactID int64

		switch action {
		case "create":
			newFactID, err = cdb.InsertTribalFact(newFact, dbEvidence)
			if err != nil {
				return NewErrorResultf("insert fact: %v", err), nil
			}

		case "correct":
			err = cdb.RunInTransaction(func() error {
				// Record the correction.
				_, cErr := cdb.InsertTribalCorrection(db.TribalCorrection{
					FactID:    factID,
					Action:    "correct",
					NewBody:   body,
					Reason:    "corrected via tribal_propose_fact",
					Actor:     writerIdentity,
					CreatedAt: now,
				})
				if cErr != nil {
					return fmt.Errorf("insert correction: %w", cErr)
				}

				// Insert the replacement fact.
				newFactID, cErr = cdb.InsertTribalFact(newFact, dbEvidence)
				if cErr != nil {
					return fmt.Errorf("insert replacement fact: %w", cErr)
				}
				return nil
			})
			if err != nil {
				return NewErrorResultf("correct fact: %v", err), nil
			}

		case "supersede":
			err = cdb.RunInTransaction(func() error {
				// Mark old fact as superseded.
				if sErr := cdb.UpdateFactStatus(factID, "superseded"); sErr != nil {
					return fmt.Errorf("supersede old fact: %w", sErr)
				}

				// Record the correction.
				_, cErr := cdb.InsertTribalCorrection(db.TribalCorrection{
					FactID:    factID,
					Action:    "supersede",
					NewBody:   body,
					Reason:    "superseded via tribal_propose_fact",
					Actor:     writerIdentity,
					CreatedAt: now,
				})
				if cErr != nil {
					return fmt.Errorf("insert correction: %w", cErr)
				}

				// Insert the replacement fact.
				newFactID, cErr = cdb.InsertTribalFact(newFact, dbEvidence)
				if cErr != nil {
					return fmt.Errorf("insert replacement fact: %w", cErr)
				}
				return nil
			})
			if err != nil {
				return NewErrorResultf("supersede fact: %v", err), nil
			}
		}

		// --- Return result ---
		resp := proposeFactResponse{
			FactID: newFactID,
			Status: status,
			Action: action,
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return NewErrorResultf("marshal response: %v", err), nil
		}
		return NewTextResult(string(data)), nil
	}
}
