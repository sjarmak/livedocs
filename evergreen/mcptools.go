package evergreen

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// StatusInput is the request shape for the evergreen_status MCP tool.
// When DocID is empty the tool returns status for every document in
// the store; otherwise it returns status for just the named document.
type StatusInput struct {
	DocID string `json:"doc_id,omitempty"`
}

// StatusOutput is the response shape for evergreen_status.
type StatusOutput struct {
	Documents []DocumentWithFindings `json:"documents"`
}

// DocumentWithFindings pairs a persisted Document with the drift findings
// the detector emits for it at the time of the call.
type DocumentWithFindings struct {
	Document *Document `json:"document"`
	Findings []Finding `json:"findings"`
}

// RefreshInput is the request shape for the evergreen_refresh MCP tool.
// DocID is required.
//
// AcknowledgeOrphan is required to proceed when the target document's
// status is OrphanedStatus — it is the explicit human (or agent) ack
// that the orphaned citation has been reviewed. Callers without it
// receive ErrOrphaned.
type RefreshInput struct {
	DocID             string `json:"doc_id"`
	AcknowledgeOrphan bool   `json:"acknowledge_orphan,omitempty"`
}

// RefreshOutput is the response shape for evergreen_refresh. Findings
// reflects the post-refresh drift state — a well-formed refresh that
// uncovers new drift (or persistent orphans on the refreshed manifest)
// will surface those findings here so the caller doesn't have to make a
// separate status call.
type RefreshOutput struct {
	Document *Document `json:"document"`
	Findings []Finding `json:"findings,omitempty"`
}

// statusToolConfig holds constructor-injected optional dependencies.
type statusToolConfig struct {
	now func() time.Time
}

// StatusToolOption configures a StatusTool.
type StatusToolOption func(*statusToolConfig)

// StatusWithClock injects a clock function used for Detect's
// age-based findings. Useful for deterministic tests.
func StatusWithClock(fn func() time.Time) StatusToolOption {
	return func(c *statusToolConfig) { c.now = fn }
}

// StatusTool handles evergreen_status MCP invocations. Construct via
// NewStatusTool so dependencies are explicit and no globals leak.
type StatusTool struct {
	store  DocumentStore
	claims ClaimsReader // nil allowed; Detect degrades gracefully
	now    func() time.Time
}

// NewStatusTool constructs a StatusTool. store is required; claims is
// optional (pass nil when no claims DB is available — per-entry drift
// checks are skipped, doc-scoped findings still fire).
func NewStatusTool(store DocumentStore, claims ClaimsReader, opts ...StatusToolOption) (*StatusTool, error) {
	if store == nil {
		return nil, errors.New("evergreen: NewStatusTool requires a non-nil DocumentStore")
	}
	cfg := statusToolConfig{now: time.Now}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &StatusTool{
		store:  store,
		claims: claims,
		now:    cfg.now,
	}, nil
}

// Handle runs the evergreen_status flow. Missing doc_id expands to "all
// documents"; a non-empty doc_id returns ErrNotFound when absent.
//
// Detector errors are surfaced (wrapped) because an inconsistent claims
// backend is a real failure the caller should know about, not a partial
// result. If that becomes too strict in practice, switch to
// skip-and-log per-doc and add a warnings field to StatusOutput.
func (t *StatusTool) Handle(ctx context.Context, in StatusInput) (StatusOutput, error) {
	docs, err := t.loadDocs(ctx, in.DocID)
	if err != nil {
		return StatusOutput{}, err
	}
	out := StatusOutput{Documents: make([]DocumentWithFindings, 0, len(docs))}
	nowFn := t.now
	if nowFn == nil {
		nowFn = time.Now
	}
	for _, doc := range docs {
		findings, err := Detect(ctx, doc, t.claims, WithNow(nowFn()))
		if err != nil {
			return StatusOutput{}, fmt.Errorf("evergreen: detect %q: %w", doc.ID, err)
		}
		out.Documents = append(out.Documents, DocumentWithFindings{
			Document: doc,
			Findings: findings,
		})
	}
	return out, nil
}

func (t *StatusTool) loadDocs(ctx context.Context, id string) ([]*Document, error) {
	if id != "" {
		d, err := t.store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		return []*Document{d}, nil
	}
	return t.store.List(ctx)
}

// refreshToolConfig holds constructor-injected optional dependencies.
type refreshToolConfig struct {
	now func() time.Time
}

// RefreshToolOption configures a RefreshTool.
type RefreshToolOption func(*refreshToolConfig)

// RefreshWithClock injects a clock function for LastRefreshedAt and
// post-refresh Detect's age-based findings. Useful for deterministic tests.
func RefreshWithClock(fn func() time.Time) RefreshToolOption {
	return func(c *refreshToolConfig) { c.now = fn }
}

// RefreshTool handles evergreen_refresh MCP invocations. Construct via
// NewRefreshTool so dependencies are explicit and no globals leak.
type RefreshTool struct {
	store    DocumentStore
	executor RefreshExecutor
	limiter  RateLimiter
	claims   ClaimsReader // nil allowed for post-refresh detection
	now      func() time.Time
}

// NewRefreshTool constructs a RefreshTool. store, executor, and limiter
// are required; claims is optional and only used for post-refresh
// detection.
func NewRefreshTool(
	store DocumentStore,
	executor RefreshExecutor,
	limiter RateLimiter,
	claims ClaimsReader,
	opts ...RefreshToolOption,
) (*RefreshTool, error) {
	if store == nil {
		return nil, errors.New("evergreen: NewRefreshTool requires a non-nil DocumentStore")
	}
	if executor == nil {
		return nil, errors.New("evergreen: NewRefreshTool requires a non-nil RefreshExecutor")
	}
	if limiter == nil {
		return nil, errors.New("evergreen: NewRefreshTool requires a non-nil RateLimiter")
	}
	cfg := refreshToolConfig{now: time.Now}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &RefreshTool{
		store:    store,
		executor: executor,
		limiter:  limiter,
		claims:   claims,
		now:      cfg.now,
	}, nil
}

// Handle runs the evergreen_refresh flow. Order of operations:
//
//  1. Load current Document (ErrNotFound when absent)
//  2. Orphan guard: OrphanedStatus + !AcknowledgeOrphan → ErrOrphaned
//  3. Rate-limit via RateLimiter (ErrRateLimited on denial)
//  4. Executor.Refresh (errors propagate wrapped; no store mutation)
//  5. Build new Document state and Save
//  6. Post-refresh Detect (best-effort; refresh succeeds even on error)
//
// The rate-limit check sits AFTER the orphan guard so repeated
// "acknowledge-orphan-only-then-we'll-refresh" prompts don't drain tokens
// against a blocked document. Conversely, the orphan guard sits AFTER the
// store load so the tool still returns ErrNotFound rather than leaking
// the existence of an orphaned doc via ErrOrphaned.
func (t *RefreshTool) Handle(ctx context.Context, in RefreshInput) (RefreshOutput, error) {
	if in.DocID == "" {
		return RefreshOutput{}, errors.New("evergreen: doc_id is required")
	}

	doc, err := t.store.Get(ctx, in.DocID)
	if err != nil {
		return RefreshOutput{}, err
	}

	if doc.Status == OrphanedStatus && !in.AcknowledgeOrphan {
		return RefreshOutput{}, ErrOrphaned
	}

	if err := t.limiter.Allow(ctx, in.DocID); err != nil {
		return RefreshOutput{}, err
	}

	res, err := t.executor.Refresh(ctx, doc)
	if err != nil {
		return RefreshOutput{}, fmt.Errorf("evergreen: executor refresh: %w", err)
	}

	nowFn := t.now
	if nowFn == nil {
		nowFn = time.Now
	}

	// Build the new document state. We start from a copy of the existing
	// doc so fields the executor does not return (Query, MaxAgeDays,
	// RefreshPolicy, CreatedAt) are preserved.
	newDoc := *doc
	newDoc.RenderedAnswer = res.RenderedAnswer
	newDoc.Manifest = res.Manifest
	if res.Backend != "" {
		newDoc.Backend = res.Backend
	}
	newDoc.ExternalID = res.ExternalID
	newDoc.LastRefreshedAt = nowFn()
	newDoc.Status = FreshStatus

	if err := t.store.Save(ctx, &newDoc); err != nil {
		return RefreshOutput{}, fmt.Errorf("evergreen: save refreshed document: %w", err)
	}

	// Post-refresh detector pass. Tolerate failures: the refresh succeeded,
	// findings are supplementary. Callers can always issue a status call
	// to retry detection.
	findings, _ := Detect(ctx, &newDoc, t.claims, WithNow(nowFn()))
	return RefreshOutput{Document: &newDoc, Findings: findings}, nil
}
