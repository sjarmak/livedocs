// Package mcpserver tribal_report.go defines the tribal_report_fact MCP tool.
// This handler uses ONLY adapter types — no mcp-go imports.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/live-docs/live_docs/db"
)

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

// tribalReportResponse is the JSON response for tribal_report_fact.
type tribalReportResponse struct {
	FactID     int64  `json:"fact_id"`
	FeedbackID int64  `json:"feedback_id"`
	Reason     string `json:"reason"`
	Status     string `json:"status"`
}

// ---------------------------------------------------------------------------
// Tool handler
// ---------------------------------------------------------------------------

// TribalReportFactHandler returns a ToolHandler that records user feedback
// about a tribal fact. Valid reasons are: wrong, stale, misleading, offensive.
func TribalReportFactHandler(pool *DBPool) ToolHandler {
	return func(_ context.Context, req ToolRequest) (ToolResult, error) {
		factID, err := req.RequireInt("fact_id")
		if err != nil {
			return NewErrorResult("missing required parameter 'fact_id'"), nil
		}
		reason, err := req.RequireString("reason")
		if err != nil {
			return NewErrorResult("missing required parameter 'reason'"), nil
		}
		details := req.GetString("details", "")
		reporter := req.GetString("reporter", "anonymous")

		// Validate reason.
		if !db.ValidFeedbackReason(reason) {
			return NewErrorResultf("invalid reason %q: must be one of wrong, stale, misleading, offensive", reason), nil
		}

		// Sanitize control characters from freeform fields.
		details = stripControlChars(details)
		reporter = strings.TrimSpace(stripControlChars(reporter))

		// Validate details length.
		if len(details) > maxDetailsLen {
			return NewErrorResultf("details too long: %d bytes (max %d)", len(details), maxDetailsLen), nil
		}

		// Validate reporter length and content.
		const maxReporterLen = 256
		if reporter == "" {
			reporter = "anonymous"
		}
		if len(reporter) > maxReporterLen {
			return NewErrorResultf("reporter too long: %d bytes (max %d)", len(reporter), maxReporterLen), nil
		}

		// Find the fact across all repos.
		manifest, err := pool.Manifest()
		if err != nil {
			return NewErrorResultf("tribal_report_fact: list repos: %v", err), nil
		}

		for _, repoName := range manifest {
			cdb, err := pool.Open(repoName)
			if err != nil {
				continue
			}

			// Try to find the fact in this repo.
			_, err = cdb.GetTribalFactByID(int64(factID))
			if err != nil {
				continue
			}

			// Found it. Insert feedback.
			fbID, err := cdb.InsertTribalFeedback(db.TribalFeedback{
				FactID:    int64(factID),
				Reason:    reason,
				Details:   details,
				Reporter:  reporter,
				CreatedAt: db.Now(),
			})
			if err != nil {
				return NewErrorResultf("tribal_report_fact: insert feedback: %v", err), nil
			}

			resp := tribalReportResponse{
				FactID:     int64(factID),
				FeedbackID: fbID,
				Reason:     reason,
				Status:     "recorded",
			}
			data, err := json.Marshal(resp)
			if err != nil {
				return NewErrorResultf("marshal result: %v", err), nil
			}
			return NewTextResult(string(data)), nil
		}

		return NewErrorResultf("tribal_report_fact: fact %d not found in any repository", factID), nil
	}
}

// ---------------------------------------------------------------------------
// Tool definition
// ---------------------------------------------------------------------------

// TribalReportFactToolDef returns the ToolDef for tribal_report_fact.
func TribalReportFactToolDef(pool *DBPool) ToolDef {
	return ToolDef{
		Name: "tribal_report_fact",
		Description: fmt.Sprintf(`Report a problem with a tribal knowledge fact.

Records user feedback about a fact that may be wrong, stale, misleading, or offensive.
Reports are aggregated to compute the S4 gate hallucination rate.

Valid reasons: wrong, stale, misleading, offensive.
Details field is optional (max %d bytes).`, maxDetailsLen),
		Params: []ParamDef{
			{Name: "fact_id", Type: ParamNumber, Required: true, Description: "ID of the tribal fact being reported."},
			{Name: "reason", Type: ParamString, Required: true, Description: "Reason for the report: wrong, stale, misleading, or offensive."},
			{Name: "details", Type: ParamString, Required: false, Description: "Optional details explaining the issue."},
			{Name: "reporter", Type: ParamString, Required: false, Description: "Identity of the reporter. Defaults to 'anonymous'."},
		},
		Handler: TribalReportFactHandler(pool),
	}
}

const maxDetailsLen = 4096

// stripControlChars removes ASCII control characters (0x00–0x1f) except tab
// from a string to prevent log injection.
func stripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
