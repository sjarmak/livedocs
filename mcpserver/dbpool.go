package mcpserver

import (
	"container/list"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/live-docs/live_docs/db"
)

const (
	// DefaultMaxOpenDBs is the default maximum number of concurrent open DB connections.
	DefaultMaxOpenDBs = 20

	// claimsDBSuffix is the file suffix for claims database files.
	claimsDBSuffix = ".claims.db"

	// maxOpenConnsPerDB is the maximum number of open connections per SQLite DB.
	maxOpenConnsPerDB = 2
)

// poolEntry tracks a single open claims DB and its position in the LRU list.
type poolEntry struct {
	repoName string
	claimsDB *db.ClaimsDB
}

// DBPool manages a pool of lazily-opened, LRU-evicted per-repo SQLite connections.
// It is safe for concurrent use.
type DBPool struct {
	dataDir string
	maxOpen int

	mu    sync.Mutex
	conns map[string]*list.Element // repoName -> LRU element containing *poolEntry
	lru   *list.List               // front = most recently used
}

// NewDBPool creates a new DBPool that looks for claims databases in dataDir.
// maxOpen controls the maximum number of concurrently open databases; if <= 0,
// DefaultMaxOpenDBs is used.
func NewDBPool(dataDir string, maxOpen int) *DBPool {
	if maxOpen <= 0 {
		maxOpen = DefaultMaxOpenDBs
	}
	return &DBPool{
		dataDir: dataDir,
		maxOpen: maxOpen,
		conns:   make(map[string]*list.Element),
		lru:     list.New(),
	}
}

// Open returns a *db.ClaimsDB for the given repo name, opening it lazily on
// first access. If the pool is at capacity, the least recently used connection
// is evicted (closed) before opening a new one. Subsequent calls for the same
// repo return the cached connection and promote it in the LRU order.
func (p *DBPool) Open(repoName string) (*db.ClaimsDB, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Cache hit: promote to front of LRU and return.
	if elem, ok := p.conns[repoName]; ok {
		p.lru.MoveToFront(elem)
		return elem.Value.(*poolEntry).claimsDB, nil
	}

	// Evict LRU if at capacity.
	if p.lru.Len() >= p.maxOpen {
		p.evictLRU()
	}

	// Open new connection.
	path := p.dbPath(repoName)
	cdb, err := db.OpenClaimsDB(path)
	if err != nil {
		return nil, fmt.Errorf("dbpool open %s: %w", repoName, err)
	}
	cdb.SetMaxOpenConns(maxOpenConnsPerDB)

	entry := &poolEntry{repoName: repoName, claimsDB: cdb}
	elem := p.lru.PushFront(entry)
	p.conns[repoName] = elem

	return cdb, nil
}

// evictLRU closes and removes the least recently used connection.
// Caller must hold p.mu.
func (p *DBPool) evictLRU() {
	back := p.lru.Back()
	if back == nil {
		return
	}
	entry := back.Value.(*poolEntry)
	_ = entry.claimsDB.Close()
	p.lru.Remove(back)
	delete(p.conns, entry.repoName)
}

// Manifest returns the list of repo names available in the data directory,
// derived from the filesystem without opening any databases. Names are sorted
// alphabetically.
func (p *DBPool) Manifest() ([]string, error) {
	pattern := filepath.Join(p.dataDir, "*"+claimsDBSuffix)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("dbpool manifest glob: %w", err)
	}

	repos := make([]string, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		name := strings.TrimSuffix(base, claimsDBSuffix)
		if name != "" {
			repos = append(repos, name)
		}
	}
	return repos, nil
}

// Close closes all open database connections in the pool.
func (p *DBPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var firstErr error
	for p.lru.Len() > 0 {
		back := p.lru.Back()
		entry := back.Value.(*poolEntry)
		if err := entry.claimsDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		p.lru.Remove(back)
		delete(p.conns, entry.repoName)
	}
	return firstErr
}

// Len returns the number of currently open connections. Useful for testing.
func (p *DBPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lru.Len()
}

// dbPath constructs the filesystem path for a repo's claims database.
func (p *DBPool) dbPath(repoName string) string {
	return filepath.Join(p.dataDir, repoName+claimsDBSuffix)
}
