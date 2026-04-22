package evergreen

import "context"

// DocumentStore persists Documents on behalf of the evergreen layer.
//
// The OSS default implementation is SQLite-backed. The sourcegraph adapter
// supplies an UpstreamStore that materializes Documents by reading
// evergreen_deepsearch and evergreen_deepsearch_versions rows directly;
// Save and UpdateStatus may be no-ops or push to a sidecar.
//
// Implementations must be safe for concurrent use.
type DocumentStore interface {
	// Save persists doc. Callers must set doc.ID before calling — an empty
	// ID is a caller bug and implementations may return an implementation-
	// defined error. Use NewDocumentID to mint a fresh ID. If a document
	// with the same ID already exists, Save overwrites it (append-only
	// revision history is an implementation concern, not part of this
	// contract).
	Save(ctx context.Context, doc *Document) error

	// Get returns the document with the given ID, or ErrNotFound.
	Get(ctx context.Context, id string) (*Document, error)

	// List returns all documents in the store.
	List(ctx context.Context) ([]*Document, error)

	// Delete removes a document by ID. Returns ErrNotFound if absent.
	// Implementations should not delete orphaned documents as a side effect
	// of drift detection; deletion must be an explicit user action.
	Delete(ctx context.Context, id string) error

	// UpdateStatus transitions a document's Status field without rewriting
	// the manifest or rendered answer. Returns ErrNotFound if absent.
	UpdateStatus(ctx context.Context, id string, status DocStatus) error
}

// RefreshExecutor re-executes a saved query against its backend and returns
// a new rendered answer plus dependency manifest.
//
// Implementations are expected to be idempotent with respect to cost controls:
// a RateLimiter at the MCP layer gates how often Refresh is called per
// document, but the executor itself may add further guardrails (e.g. defer
// to an upstream refresh worker already in progress).
type RefreshExecutor interface {
	// Refresh replays doc.Query against the backend and returns the new
	// answer and manifest. The caller persists the result; Refresh must not
	// mutate doc.
	//
	// Result ownership: the returned RefreshResult transfers ownership to
	// the caller. Executors must not retain references to the returned
	// Manifest slice or Metadata map — the caller is free to mutate either
	// after return (e.g. the store appending revision metadata). Executors
	// that cache results internally must return a deep copy.
	Refresh(ctx context.Context, doc *Document) (RefreshResult, error)

	// Name returns a stable executor identifier used in telemetry,
	// Document.Backend, and RefreshResult.Backend.
	Name() string
}

// ClaimsReader is the minimal view of the live_docs claims database that
// the detector consults.
//
// Implementations wrap the repo-scoped claims DB. In the OSS install there
// is one ClaimsReader per watched repo; the adapter may supply a composite
// reader that fans out across the repos cited in a manifest.
type ClaimsReader interface {
	// GetSymbol returns the current state of symbolID in the Repo identified
	// by the manifest entry. Returns ErrSymbolNotFound when the symbol is
	// absent — callers interpret this as DeletedChange.
	GetSymbol(ctx context.Context, repo string, symbolID int64) (*SymbolState, error)

	// ResolveSymbolByLocation returns the symbol_id currently overlapping the
	// file and 1-indexed inclusive line range [lineStart, lineEnd], or
	// ErrSymbolNotFound when no symbol is present at that range. Used to
	// distinguish rename (a different symbol now lives at the cited location)
	// from deletion (nothing lives there).
	//
	// When the manifest entry's line range is unknown (LineStart == 0 and
	// LineEnd == 0), callers should skip this lookup rather than pass zeros
	// — implementations may treat zero as "whole file" and return unrelated
	// symbols.
	ResolveSymbolByLocation(ctx context.Context, repo, filePath string, lineStart, lineEnd int) (int64, error)
}

// RateLimiter gates refresh operations at the MCP layer.
//
// The OSS default implementation wraps KeyedLimiter (used elsewhere in
// live_docs for tribal_mine_on_demand). Adapters that already have upstream
// rate limiting typically supply a pass-through or use the OSS limiter as
// a secondary cap.
type RateLimiter interface {
	// Allow returns nil when the request is permitted and ErrRateLimited
	// when denied. Key is typically a document ID; implementations may
	// treat a second argument (session ID, user ID) as a namespace — that
	// wiring is the RateLimiter's concern, not the interface's.
	Allow(ctx context.Context, key string) error
}
