package drift

import (
	"context"
	"fmt"
	"time"

	"github.com/sjarmak/livedocs/db"
)

// ReverifyVerdict is the outcome of a semantic reverification check.
type ReverifyVerdict int

const (
	// VerdictAccept means the fact still accurately describes the code.
	VerdictAccept ReverifyVerdict = iota
	// VerdictDowngrade means the fact is partially outdated.
	VerdictDowngrade
	// VerdictReject means the fact no longer describes the code.
	VerdictReject
)

// SemanticVerifier checks whether a tribal fact still accurately describes
// the code it refers to. Implementations typically make one LLM call per
// invocation.
type SemanticVerifier interface {
	VerifyFact(ctx context.Context, fact db.TribalFact) (ReverifyVerdict, error)
}

// ReverifyOptions configures a reverification pass.
type ReverifyOptions struct {
	// SampleSize is the maximum number of facts to reverify.
	SampleSize int
	// MaxAge is the minimum age of a fact's last_verified timestamp
	// for it to be eligible for reverification.
	MaxAge time.Duration
	// NowFn returns the current time. Injectable for testing.
	NowFn func() time.Time
	// Verifier is the semantic verifier (LLM call).
	Verifier SemanticVerifier
	// Budget is the maximum number of LLM calls allowed. Zero means unlimited.
	Budget int
}

// ReverifyResult summarizes the outcome of a reverification pass.
type ReverifyResult struct {
	Accepted        int
	Downgraded      int
	Rejected        int
	BudgetExhausted bool
}

// ReverifyTribal samples active LLM-extracted facts older than MaxAge and
// runs a semantic verification check on each. Returns a summary of actions taken.
func ReverifyTribal(cdb *db.ClaimsDB, opts ReverifyOptions) (ReverifyResult, error) {
	nowFn := opts.NowFn
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	cutoff := now.Add(-opts.MaxAge).UTC().Format(time.RFC3339)

	facts, err := cdb.GetActiveLLMFactsOlderThan(cutoff)
	if err != nil {
		return ReverifyResult{}, fmt.Errorf("reverify tribal: %w", err)
	}

	// Apply sample size limit.
	if opts.SampleSize > 0 && len(facts) > opts.SampleSize {
		facts = facts[:opts.SampleSize]
	}

	ctx := context.Background()
	var result ReverifyResult
	callCount := 0

	for _, fact := range facts {
		// Budget check.
		if opts.Budget > 0 && callCount >= opts.Budget {
			result.BudgetExhausted = true
			break
		}

		verdict, verr := opts.Verifier.VerifyFact(ctx, fact)
		if verr != nil {
			return result, fmt.Errorf("reverify tribal: verify fact %d: %w", fact.ID, verr)
		}
		callCount++

		switch verdict {
		case VerdictAccept:
			if err := cdb.UpdateFactLastVerified(fact.ID, now.UTC().Format(time.RFC3339)); err != nil {
				return result, fmt.Errorf("reverify tribal: accept fact %d: %w", fact.ID, err)
			}
			result.Accepted++

		case VerdictDowngrade:
			newConf := fact.Confidence * 0.6
			if err := cdb.UpdateFactConfidence(fact.ID, newConf); err != nil {
				return result, fmt.Errorf("reverify tribal: downgrade fact %d: %w", fact.ID, err)
			}
			result.Downgraded++

		case VerdictReject:
			if err := cdb.UpdateFactStatus(fact.ID, "stale"); err != nil {
				return result, fmt.Errorf("reverify tribal: reject fact %d: %w", fact.ID, err)
			}
			result.Rejected++
		}
	}

	return result, nil
}
