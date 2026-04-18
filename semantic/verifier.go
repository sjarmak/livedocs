package semantic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
)

// VerificationVerdict is the outcome of reviewing a single claim.
type VerificationVerdict string

const (
	VerdictAccept    VerificationVerdict = "accept"
	VerdictReject    VerificationVerdict = "reject"
	VerdictDowngrade VerificationVerdict = "downgrade"
)

// VerificationResult records the outcome of verifying one claim.
type VerificationResult struct {
	SubjectName string              `json:"subject_name"`
	Predicate   string              `json:"predicate"`
	Verdict     VerificationVerdict `json:"verdict"`
	Reason      string              `json:"reason"`
}

// VerifySummary summarises the verification of a batch of claims.
type VerifySummary struct {
	Accepted   int
	Rejected   int
	Downgraded int
	Results    []VerificationResult
}

// confidenceDowngradeFactor is multiplied into the claim's confidence
// when the adversarial reviewer returns "downgrade".
const confidenceDowngradeFactor = 0.6

// Verifier checks semantic claims before they are stored. It applies
// two gates:
//  1. Structural check — the claim's subject must exist in the symbol map.
//  2. Adversarial LLM review — a second model call challenges the claims.
type Verifier struct {
	client   LLMClient
	claimsDB *db.ClaimsDB
}

// NewVerifier creates a Verifier with the given LLM client and claims DB.
func NewVerifier(client LLMClient, claimsDB *db.ClaimsDB) *Verifier {
	return &Verifier{client: client, claimsDB: claimsDB}
}

// Verify applies both structural and adversarial checks to the given claims.
// It returns only claims that pass both gates, with confidence adjusted for
// downgraded claims. The symbolMap maps symbol names to their DB records.
func (v *Verifier) Verify(
	ctx context.Context,
	claims []extractor.Claim,
	symbolMap map[string]db.Symbol,
	structuralContext string,
) (accepted []extractor.Claim, summary VerifySummary, err error) {
	// Gate 1: structural check — subject must exist in symbol map.
	var structurallyValid []extractor.Claim
	for _, c := range claims {
		if _, ok := symbolMap[c.SubjectName]; ok {
			structurallyValid = append(structurallyValid, c)
		} else {
			summary.Rejected++
			summary.Results = append(summary.Results, VerificationResult{
				SubjectName: c.SubjectName,
				Predicate:   string(c.Predicate),
				Verdict:     VerdictReject,
				Reason:      "symbol not found in structural claims",
			})
		}
	}

	if len(structurallyValid) == 0 {
		return nil, summary, nil
	}

	// Gate 2: adversarial LLM review.
	prompt := buildVerifyPrompt(structurallyValid, structuralContext)
	raw, err := v.client.Complete(ctx, verifySystemPrompt, prompt)
	if err != nil {
		return nil, summary, fmt.Errorf("adversarial review LLM call: %w", err)
	}

	verdicts, err := parseVerifyResponse(raw)
	if err != nil {
		return nil, summary, fmt.Errorf("parse adversarial review: %w", err)
	}

	// Build lookup: (subject_name, predicate) -> verdict.
	verdictMap := make(map[string]VerificationResult, len(verdicts))
	for _, v := range verdicts {
		key := v.SubjectName + "/" + v.Predicate
		verdictMap[key] = v
	}

	// Apply verdicts to structurally valid claims.
	for _, c := range structurallyValid {
		key := c.SubjectName + "/" + string(c.Predicate)
		vr, ok := verdictMap[key]
		if !ok {
			// No verdict from reviewer — treat as accepted (reviewer may have
			// omitted claims it found unambiguous).
			vr = VerificationResult{
				SubjectName: c.SubjectName,
				Predicate:   string(c.Predicate),
				Verdict:     VerdictAccept,
				Reason:      "no explicit verdict from reviewer; accepted by default",
			}
		}

		summary.Results = append(summary.Results, vr)

		switch vr.Verdict {
		case VerdictAccept:
			summary.Accepted++
			accepted = append(accepted, c)
		case VerdictDowngrade:
			summary.Downgraded++
			c.Confidence *= confidenceDowngradeFactor
			accepted = append(accepted, c)
		case VerdictReject:
			summary.Rejected++
		default:
			// Unknown verdict — reject to be safe.
			summary.Rejected++
		}
	}

	return accepted, summary, nil
}

// parseVerifyResponse parses the adversarial reviewer's JSON response.
func parseVerifyResponse(raw string) ([]VerificationResult, error) {
	raw = stripMarkdownFences(raw)
	raw = strings.TrimSpace(raw)

	var results []VerificationResult
	if err := json.Unmarshal([]byte(raw), &results); err != nil {
		return nil, fmt.Errorf("parse verify response: %w", err)
	}

	// Validate verdicts.
	for i, r := range results {
		switch r.Verdict {
		case VerdictAccept, VerdictReject, VerdictDowngrade:
			// valid
		default:
			results[i].Verdict = VerdictReject
			results[i].Reason = fmt.Sprintf("unknown verdict %q treated as reject", r.Verdict)
		}
	}

	return results, nil
}
