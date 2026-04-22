package evergreen

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Detect diffs doc's dependency manifest against the current state of the
// claims database and returns a list of drift findings.
//
// Detect is a pure function: its only I/O is reading through the provided
// ClaimsReader. It does not persist, mutate doc, log, or time-out.
//
// Severity rules (see also package doc):
//   - OrphanedSeverity: a cited symbol has been renamed or deleted. Refresh
//     should be blocked pending human review.
//   - HotSeverity: a cited symbol's signature hash has moved — the rendered
//     answer is likely factually wrong.
//   - WarmSeverity: a cited symbol's content hash moved but the signature
//     hash held; the answer may need revision.
//   - ColdSeverity: no per-symbol drift, but the document exceeds
//     MaxAgeDays. (Adjacent-repo churn detection is deferred — requires a
//     commit-graph source that is not yet plumbed through ClaimsReader.)
//
// Precision gracefully degrades when manifest entries lack hashes (as with
// the OSS deepsearch-MCP executor per live_docs-8yc.6 audit): Hot/Warm are
// unreachable for those entries, but Orphaned (via ResolveSymbolByLocation)
// and Cold (via age) still fire.
//
// ClaimsReader errors other than ErrSymbolNotFound are treated as backend
// failures and propagated. Findings are returned in severity-descending
// order (Orphaned, Hot, Warm, Cold) with per-severity stable ordering by
// manifest-entry index.
//
// Aliasing: returned Finding.Entry values point into doc.Manifest. Callers
// that continue to mutate doc after Detect returns will see those mutations
// reflected in findings. Per the Document contract in DocumentStore.Save,
// manifests are owned by the store and not mutated in place, so this
// aliasing is safe in the documented call path.
func Detect(ctx context.Context, doc *Document, claims ClaimsReader, opts ...DetectOption) ([]Finding, error) {
	if doc == nil {
		return nil, errors.New("evergreen: Detect requires a non-nil Document")
	}
	if claims == nil {
		return nil, errors.New("evergreen: Detect requires a non-nil ClaimsReader")
	}
	cfg := detectConfig{now: time.Now()}
	for _, opt := range opts {
		opt(&cfg)
	}

	var findings []Finding

	// Per-entry drift classification.
	for i := range doc.Manifest {
		entry := &doc.Manifest[i] // stable address into the slice for Finding.Entry
		f, err := classifyEntry(ctx, doc.ID, entry, claims)
		if err != nil {
			return nil, err
		}
		if f != nil {
			findings = append(findings, *f)
		}
	}

	// Doc-scoped: age-based cold.
	if ageFinding := classifyAge(doc, cfg.now); ageFinding != nil {
		findings = append(findings, *ageFinding)
	}

	sortFindings(findings)
	return findings, nil
}

// DetectOption configures Detect. Options do not perform I/O.
type DetectOption func(*detectConfig)

type detectConfig struct {
	now time.Time
}

// WithNow overrides the reference time used for age-based ColdSeverity
// findings. Useful for deterministic tests.
func WithNow(t time.Time) DetectOption {
	return func(c *detectConfig) { c.now = t }
}

// classifyEntry returns the finding (if any) for a single manifest entry.
// Returns nil when the entry shows no drift.
func classifyEntry(ctx context.Context, docID string, entry *ManifestEntry, claims ClaimsReader) (*Finding, error) {
	// Fuzzy entries carry no per-symbol signal. Skip; they contribute only
	// to future adjacent-repo-churn detection which is not yet wired.
	if entry.Fuzzy {
		return nil, nil
	}

	// Case A: entry has a symbol_id — we can do a direct per-symbol check.
	if entry.SymbolID != nil {
		return classifyBySymbolID(ctx, docID, entry, claims)
	}

	// Case B: entry has a location but no symbol_id — we can detect
	// deletion (nothing now lives at the cited range) but cannot detect
	// drift of an identified symbol.
	if entry.FilePath != "" && entry.LineStart > 0 {
		return classifyByLocation(ctx, docID, entry, claims)
	}

	// Case C: entry has neither symbol_id nor line range — nothing to check.
	return nil, nil
}

func classifyBySymbolID(ctx context.Context, docID string, entry *ManifestEntry, claims ClaimsReader) (*Finding, error) {
	state, err := claims.GetSymbol(ctx, entry.Repo, *entry.SymbolID)
	switch {
	case errors.Is(err, ErrSymbolNotFound):
		return classifyMissingSymbol(ctx, docID, entry, claims), nil
	case err != nil:
		return nil, fmt.Errorf("evergreen: GetSymbol(%s, %d) failed: %w", entry.Repo, *entry.SymbolID, err)
	}

	// Signature-hash early cutoff: any signature drift is Hot regardless of
	// content hash state.
	if entry.SignatureHashAtRender != "" && state.SignatureHash != "" &&
		entry.SignatureHashAtRender != state.SignatureHash {
		return &Finding{
			DocumentID:  docID,
			Severity:    HotSeverity,
			ChangeKind:  SignatureChange,
			Entry:       entry,
			WasHash:     entry.SignatureHashAtRender,
			CurrentHash: state.SignatureHash,
			Detail:      "symbol signature has changed since render",
		}, nil
	}

	if entry.ContentHashAtRender != "" && state.ContentHash != "" &&
		entry.ContentHashAtRender != state.ContentHash {
		return &Finding{
			DocumentID:  docID,
			Severity:    WarmSeverity,
			ChangeKind:  BodyChange,
			Entry:       entry,
			WasHash:     entry.ContentHashAtRender,
			CurrentHash: state.ContentHash,
			Detail:      "symbol body has changed since render (signature held)",
		}, nil
	}

	return nil, nil
}

// classifyMissingSymbol distinguishes rename (a different symbol now lives
// at the cited range) from deletion (nothing lives there).
func classifyMissingSymbol(ctx context.Context, docID string, entry *ManifestEntry, claims ClaimsReader) *Finding {
	f := &Finding{
		DocumentID: docID,
		Severity:   OrphanedSeverity,
		ChangeKind: DeletedChange,
		Entry:      entry,
		Detail:     "cited symbol no longer present in claims",
	}
	// Rename detection is best-effort; a missing location or reader error
	// falls through to DeletedChange.
	if entry.FilePath == "" || entry.LineStart == 0 {
		return f
	}
	otherID, err := claims.ResolveSymbolByLocation(ctx, entry.Repo, entry.FilePath, entry.LineStart, entry.LineEnd)
	if err != nil {
		return f
	}
	if entry.SymbolID != nil && otherID == *entry.SymbolID {
		// Same symbol_id now occupies the range — upstream inconsistency.
		// Treat as deleted because GetSymbol said it's gone; the mismatch
		// is not our problem to adjudicate.
		return f
	}
	f.ChangeKind = RenamedChange
	f.Detail = "cited symbol renamed or replaced at the same location"
	return f
}

func classifyByLocation(ctx context.Context, docID string, entry *ManifestEntry, claims ClaimsReader) (*Finding, error) {
	_, err := claims.ResolveSymbolByLocation(ctx, entry.Repo, entry.FilePath, entry.LineStart, entry.LineEnd)
	switch {
	case errors.Is(err, ErrSymbolNotFound):
		return &Finding{
			DocumentID: docID,
			Severity:   OrphanedSeverity,
			ChangeKind: DeletedChange,
			Entry:      entry,
			Detail:     "cited location no longer contains a known symbol",
		}, nil
	case err != nil:
		return nil, fmt.Errorf("evergreen: ResolveSymbolByLocation(%s, %s, %d-%d) failed: %w",
			entry.Repo, entry.FilePath, entry.LineStart, entry.LineEnd, err)
	}
	// Some symbol currently lives at the cited range. Without an original
	// symbol_id we cannot say whether it's the same logical symbol. Skip.
	return nil, nil
}

func classifyAge(doc *Document, now time.Time) *Finding {
	if doc.MaxAgeDays <= 0 {
		return nil
	}
	if doc.LastRefreshedAt.IsZero() {
		return nil
	}
	maxAge := time.Duration(doc.MaxAgeDays) * 24 * time.Hour
	if now.Sub(doc.LastRefreshedAt) <= maxAge {
		return nil
	}
	return &Finding{
		DocumentID: doc.ID,
		Severity:   ColdSeverity,
		ChangeKind: AgeChange,
		Detail: fmt.Sprintf("document age %s exceeds max_age_days=%d",
			now.Sub(doc.LastRefreshedAt).Round(time.Hour), doc.MaxAgeDays),
	}
}

// sortFindings orders findings Orphaned > Hot > Warm > Cold, stable within
// each severity to preserve the manifest-entry traversal order.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		return severityRank(fs[i].Severity) < severityRank(fs[j].Severity)
	})
}

func severityRank(s Severity) int {
	switch s {
	case OrphanedSeverity:
		return 0
	case HotSeverity:
		return 1
	case WarmSeverity:
		return 2
	case ColdSeverity:
		return 3
	default:
		return 4
	}
}
