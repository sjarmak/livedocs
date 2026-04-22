package evergreen

import "time"

// Document is a saved deep-search-style prose answer plus the dependency
// manifest that lets the detector decide whether it has drifted.
//
// Document values cross the live_docs/adapter boundary and the MCP wire:
// JSON field names and semantics are part of the public contract.
type Document struct {
	// ID is a stable identifier assigned by the DocumentStore. For the
	// OSS SQLite store this is a UUID; adapters may use upstream IDs
	// (e.g. a sourcegraph evergreen_deepsearch.id).
	ID string `json:"id"`

	// Query is the original natural-language prompt that produced the answer.
	// Refresh executors are expected to replay this verbatim.
	Query string `json:"query"`

	// RenderedAnswer is the prose answer as of LastRefreshedAt.
	RenderedAnswer string `json:"rendered_answer"`

	// Manifest records the symbols, repos, and commits the rendered answer
	// was derived from. Entries may be precise (SymbolID resolved) or fuzzy
	// (repo+commit only); see ManifestEntry.Fuzzy.
	Manifest []ManifestEntry `json:"manifest"`

	// Status is the most recent detector verdict, or RefreshingStatus while
	// an executor run is in flight.
	Status DocStatus `json:"status"`

	// RefreshPolicy controls whether drift findings alert, wait for manual
	// trigger, or fire an executor automatically.
	RefreshPolicy RefreshPolicy `json:"refresh_policy"`

	// MaxAgeDays bounds how long a document may go without refresh before
	// being classified ColdSeverity regardless of symbol drift. Zero means
	// no age-based cold tier.
	MaxAgeDays int `json:"max_age_days,omitempty"`

	// CreatedAt is when Save first persisted this document.
	CreatedAt time.Time `json:"created_at"`

	// LastRefreshedAt is when the most recent successful executor run
	// completed. Equals CreatedAt if the document has never been refreshed.
	LastRefreshedAt time.Time `json:"last_refreshed_at"`

	// ExternalID is an optional adapter-side identifier (e.g. a sourcegraph
	// evergreen version ID) useful for cross-system audit and deduplication.
	ExternalID *string `json:"external_id,omitempty"`

	// Backend names the RefreshExecutor that most recently produced this
	// document (stable identifier, e.g. "deepsearch-mcp", "sourcegraph-evergreen").
	Backend string `json:"backend,omitempty"`
}

// ManifestEntry records one citation from a document's dependency manifest.
//
// A precise entry has SymbolID set and records hashes captured at render time;
// the detector can then compare against the current claims DB to classify
// drift per-symbol. A fuzzy entry has SymbolID == nil and only carries
// repo+commit context, which drives coarse "this repo advanced N commits"
// drift detection.
type ManifestEntry struct {
	// SymbolID is nil when the executor could not attribute this citation
	// to a specific symbol in the claims DB. See Fuzzy.
	SymbolID *int64 `json:"symbol_id,omitempty"`

	// Repo is the canonical repository identifier
	// (e.g. "github.com/kubernetes/kubernetes").
	Repo string `json:"repo"`

	// CommitSHA is the commit observed at render time.
	CommitSHA string `json:"commit_sha"`

	// FilePath is the path within Repo that was cited, empty for fuzzy entries.
	FilePath string `json:"file_path,omitempty"`

	// ContentHashAtRender is the content hash of the cited symbol's source
	// range at render time. Used by the detector to classify body-level drift.
	// Empty when SymbolID is nil.
	ContentHashAtRender string `json:"content_hash_at_render,omitempty"`

	// SignatureHashAtRender is a hash of just the exported-signature subset
	// (name, parameter types, return types) at render time. Used by the
	// detector's signature-hash early cutoff to distinguish hot (signature
	// moved) from warm (body moved but signature held) drift. Empty when
	// SymbolID is nil or the executor did not compute it.
	SignatureHashAtRender string `json:"signature_hash_at_render,omitempty"`

	// LineStart and LineEnd bound the cited range (1-indexed, inclusive).
	// Zero means unknown.
	LineStart int `json:"line_start,omitempty"`
	LineEnd   int `json:"line_end,omitempty"`

	// Fuzzy is true when the entry could not be resolved to a symbol and
	// drives only coarse repo/commit drift detection.
	Fuzzy bool `json:"fuzzy,omitempty"`
}

// Finding is a single drift observation emitted by the detector.
//
// A Finding may be per-entry (Entry points into Document.Manifest and
// describes drift for that specific citation) or document-scoped
// (Entry == nil, e.g. an age-based ColdSeverity finding).
type Finding struct {
	// DocumentID identifies the Document this finding pertains to.
	DocumentID string `json:"document_id"`

	// Severity is the drift tier.
	Severity Severity `json:"severity"`

	// ChangeKind identifies what moved between render and now.
	ChangeKind ChangeKind `json:"change_kind"`

	// Entry is the manifest citation that drifted, or nil for document-scoped
	// findings (e.g. age-based cold, orphan on a symbol not previously in the
	// manifest).
	Entry *ManifestEntry `json:"entry,omitempty"`

	// WasHash is the hash recorded in the manifest at render time (content or
	// signature, matching ChangeKind). Empty for document-scoped findings.
	WasHash string `json:"was_hash,omitempty"`

	// CurrentHash is the hash observed in the claims DB now. Empty when
	// ChangeKind is Deleted or for document-scoped findings.
	CurrentHash string `json:"current_hash,omitempty"`

	// Detail is a human-readable explanation suitable for CLI output and
	// MCP tool responses.
	Detail string `json:"detail,omitempty"`
}

// RefreshResult is what a RefreshExecutor returns on success.
type RefreshResult struct {
	// RenderedAnswer is the new prose to store as Document.RenderedAnswer.
	RenderedAnswer string `json:"rendered_answer"`

	// Manifest is the new dependency manifest captured during the refresh.
	Manifest []ManifestEntry `json:"manifest"`

	// Backend is a stable executor identifier stored on the Document.
	Backend string `json:"backend"`

	// ExternalID is an optional upstream identifier for cross-system audit.
	ExternalID *string `json:"external_id,omitempty"`

	// Metadata is backend-specific telemetry (token counts, upstream version
	// IDs, etc.) preserved verbatim by the caller for logging.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// SymbolState is the minimal view of a symbol's current state that the
// detector needs from a ClaimsReader.
type SymbolState struct {
	// SymbolID matches the claims DB primary key and should round-trip
	// with ManifestEntry.SymbolID.
	SymbolID int64 `json:"symbol_id"`

	// ContentHash is the current content hash of the symbol's source range.
	ContentHash string `json:"content_hash"`

	// SignatureHash is the current signature hash (may be empty when the
	// symbol kind has no meaningful signature, e.g. constants).
	SignatureHash string `json:"signature_hash,omitempty"`

	// Repo and FilePath locate the symbol in the current tree.
	Repo     string `json:"repo"`
	FilePath string `json:"file_path"`
}

// DocStatus is the Document lifecycle state.
type DocStatus string

// Document lifecycle states.
const (
	// FreshStatus means no drift findings above the alert threshold.
	FreshStatus DocStatus = "fresh"
	// StaleStatus means the detector has emitted at least one hot/warm finding.
	StaleStatus DocStatus = "stale"
	// OrphanedStatus means at least one cited symbol has been deleted or
	// renamed and the document is blocked from auto-refresh pending review.
	//
	// The wire value "orphaned" is intentionally shared with OrphanedSeverity;
	// the two types are always resolved from context (Document.Status vs
	// Finding.Severity). See TestEnumWireValuesUnique for the pinned policy.
	OrphanedStatus DocStatus = "orphaned"
	// RefreshingStatus means a RefreshExecutor call is in flight.
	RefreshingStatus DocStatus = "refreshing"
)

// RefreshPolicy controls how the system reacts to drift findings.
type RefreshPolicy string

// Refresh policies.
const (
	// AlertPolicy surfaces findings to the user and waits for manual refresh.
	AlertPolicy RefreshPolicy = "alert"
	// ManualPolicy suppresses auto-alerts entirely; users query status on demand.
	ManualPolicy RefreshPolicy = "manual"
	// AutoPolicy triggers a RefreshExecutor run on qualifying findings.
	// Applies only to OSS-path executors; adapter backends typically defer
	// to their own upstream refresh loop.
	AutoPolicy RefreshPolicy = "auto"
)

// Severity is the drift tier assigned by the detector.
type Severity string

// Drift severities, ordered by urgency: Hot > Warm > Cold > Orphaned requires
// human review and is not ordered against the drift tiers.
const (
	// HotSeverity means at least one cited symbol's signature has moved —
	// the answer is likely wrong.
	HotSeverity Severity = "hot"
	// WarmSeverity means body-level drift without signature change — the
	// answer may need revision but is not clearly wrong.
	WarmSeverity Severity = "warm"
	// ColdSeverity means no per-symbol drift but the document exceeds
	// MaxAgeDays or the repo has advanced significantly.
	ColdSeverity Severity = "cold"
	// OrphanedSeverity means a cited symbol is gone (deleted or renamed).
	// The document status should transition to OrphanedStatus and refresh
	// is blocked pending human acknowledgement.
	//
	// The wire value "orphaned" is intentionally shared with OrphanedStatus.
	OrphanedSeverity Severity = "orphaned"
)

// ChangeKind names what kind of change the detector observed for a Finding.
type ChangeKind string

// Change kinds.
const (
	// NoChange is used for document-scoped findings (e.g. age-based cold).
	NoChange ChangeKind = "none"
	// SignatureChange means the cited symbol's signature hash moved.
	SignatureChange ChangeKind = "signature"
	// BodyChange means the content hash moved but the signature hash held.
	BodyChange ChangeKind = "body"
	// DeletedChange means the cited symbol is no longer present in the claims DB.
	DeletedChange ChangeKind = "deleted"
	// RenamedChange means a symbol still occupies the cited location but its
	// identity (symbol_id) changed — typically a rename.
	RenamedChange ChangeKind = "renamed"
	// AgeChange means the document exceeded MaxAgeDays with no per-symbol drift.
	AgeChange ChangeKind = "age"
)
