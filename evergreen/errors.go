package evergreen

import "errors"

// Sentinel errors returned by store, executor, and rate limiter implementations.
//
// These are part of the public contract: adapters and callers use errors.Is
// to distinguish them. New sentinels require a minor version bump.
var (
	// ErrNotFound is returned by DocumentStore.Get, Delete, and UpdateStatus
	// when the requested document does not exist.
	ErrNotFound = errors.New("evergreen: document not found")

	// ErrSymbolNotFound is returned by ClaimsReader when a queried symbol
	// or location is absent from the current claims DB.
	ErrSymbolNotFound = errors.New("evergreen: symbol not found")

	// ErrRateLimited is returned by RateLimiter.Allow when a refresh request
	// is denied. The MCP refresh tool surfaces this verbatim so clients can
	// distinguish quota from backend failures.
	ErrRateLimited = errors.New("evergreen: rate limit exceeded")

	// ErrOrphaned is returned by RefreshExecutor.Refresh or higher-level
	// refresh handlers when a document's status is OrphanedStatus and the
	// caller has not explicitly acknowledged the orphan. Unblocked via a
	// force/acknowledge path at the CLI or MCP layer.
	ErrOrphaned = errors.New("evergreen: document orphaned, refresh blocked pending acknowledgement")
)
