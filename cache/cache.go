// Package cache provides content-hash caching for the extraction layer.
//
// Cache keys are composite: SHA-256(file_content) + extractor_version + grammar_version.
// When all three match, extraction is skipped. LRU eviction keeps total size under
// a configurable cap (default 2GB). Deletion-aware reconciliation marks removed files
// with a tombstone so stale entries are cleaned up.
package cache

import "time"

// Entry represents a cached extraction result for a single source file.
type Entry struct {
	ID               int64
	Repo             string
	RelativePath     string
	ContentHash      string // SHA-256 hex digest of file content
	ExtractorVersion string
	GrammarVersion   string // empty when deep extractor produced the entry
	LastIndexed      time.Time
	SizeBytes        int64
	Deleted          bool // tombstone flag
}

// Store defines the caching interface for extraction results.
type Store interface {
	// Hit checks whether a cache entry exists with the given composite key.
	// Returns true when repo+path has a non-deleted entry whose content hash,
	// extractor version, and grammar version all match.
	Hit(repo, path, contentHash, extractorVersion, grammarVersion string) (bool, error)

	// Put inserts or updates a cache entry. If an entry already exists for
	// repo+path, it is replaced. After insertion, Evict is called if the
	// total size exceeds the cap.
	Put(entry Entry) error

	// MarkDeleted sets the tombstone flag on a file entry.
	MarkDeleted(repo, path string) error

	// Reconcile synchronises the cache with the current state of a repo.
	// currentFiles maps relative paths to their SHA-256 content hashes.
	// Files present in the cache but absent from currentFiles are tombstoned.
	// Files in currentFiles whose hash differs from the cache are reported as
	// changed (returned in the slice).
	Reconcile(repo string, currentFiles map[string]string) (changed []string, err error)

	// Evict removes least-recently-used entries until total cached size is
	// at or below the configured cap. Tombstoned entries are evicted first.
	Evict() (evicted int, err error)

	// TotalSize returns the sum of SizeBytes for all non-deleted entries.
	TotalSize() (int64, error)

	// Close releases any resources held by the store.
	Close() error
}
