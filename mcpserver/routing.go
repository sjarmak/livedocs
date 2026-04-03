package mcpserver

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// minPrefixLen is the minimum symbol name prefix length used for routing.
const minPrefixLen = 3

// RoutingIndex maps lowercase symbol name prefixes to sets of repo names.
// It enables fast narrowing of candidate repos before a cross-repo fan-out
// search, avoiding the need to query every database.
//
// Thread-safe for concurrent reads via sync.RWMutex.
type RoutingIndex struct {
	mu       sync.RWMutex
	prefixes map[string]map[string]bool // lowercase prefix -> set of repo names
	allRepos []string                   // all known repos for fallback
}

// NewRoutingIndex creates an empty RoutingIndex.
func NewRoutingIndex() *RoutingIndex {
	return &RoutingIndex{
		prefixes: make(map[string]map[string]bool),
	}
}

// Build scans all repo databases in the pool to populate the prefix-to-repo
// mapping. Each database is queried for distinct lowercase 3-char prefixes of
// symbol names. Errors opening individual repos are logged but do not abort
// the build — partial indexes are still useful.
func (ri *RoutingIndex) Build(pool *DBPool) error {
	manifest, err := pool.Manifest()
	if err != nil {
		return fmt.Errorf("routing index build: %w", err)
	}

	newPrefixes := make(map[string]map[string]bool)

	for _, repoName := range manifest {
		cdb, err := pool.Open(repoName)
		if err != nil {
			// Skip repos that fail to open; partial index is acceptable.
			continue
		}

		prefixList, err := cdb.DistinctSymbolPrefixes(minPrefixLen)
		if err != nil {
			// Skip repos with query errors (e.g., no symbols table yet).
			continue
		}

		for _, prefix := range prefixList {
			if _, ok := newPrefixes[prefix]; !ok {
				newPrefixes[prefix] = make(map[string]bool)
			}
			newPrefixes[prefix][repoName] = true
		}
	}

	ri.mu.Lock()
	ri.prefixes = newPrefixes
	ri.allRepos = make([]string, len(manifest))
	copy(ri.allRepos, manifest)
	sort.Strings(ri.allRepos)
	ri.mu.Unlock()

	return nil
}

// Lookup returns the candidate repo names that may contain symbols matching
// the query. If the query is shorter than minPrefixLen or contains SQL
// wildcards (%), all repos are returned as a fallback.
func (ri *RoutingIndex) Lookup(query string) []string {
	ri.mu.RLock()
	defer ri.mu.RUnlock()

	// Fallback: return all repos for short queries or wildcard patterns.
	if len(query) < minPrefixLen || strings.Contains(query, "%") {
		result := make([]string, len(ri.allRepos))
		copy(result, ri.allRepos)
		return result
	}

	prefix := strings.ToLower(query[:minPrefixLen])
	repoSet, ok := ri.prefixes[prefix]
	if !ok {
		return nil
	}

	repos := make([]string, 0, len(repoSet))
	for repo := range repoSet {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	return repos
}

// RepoCount returns the total number of repos known to the index.
func (ri *RoutingIndex) RepoCount() int {
	ri.mu.RLock()
	defer ri.mu.RUnlock()
	return len(ri.allRepos)
}
