package mcpserver

import (
	"container/list"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

// poolEntry tracks a single open claims DB, its position in the LRU list,
// and the file modification time when it was opened.
type poolEntry struct {
	repoName string
	claimsDB *db.ClaimsDB
	modTime  time.Time // mtime of the DB file when the connection was opened
}

// accessEntry records when a repo was last queried via Open().
type accessEntry struct {
	repoName   string
	lastAccess time.Time
}

// DBPool manages a pool of lazily-opened, LRU-evicted per-repo SQLite connections.
// It is safe for concurrent use.
type DBPool struct {
	dataDir string
	maxOpen int

	mu         sync.Mutex
	conns      map[string]*list.Element // repoName -> LRU element containing *poolEntry
	lru        *list.List               // front = most recently used
	lastAccess map[string]time.Time     // repoName -> last Open() call time
	nowFunc    func() time.Time         // injectable clock for testing
}

// NewDBPool creates a new DBPool that looks for claims databases in dataDir.
// maxOpen controls the maximum number of concurrently open databases; if <= 0,
// DefaultMaxOpenDBs is used.
func NewDBPool(dataDir string, maxOpen int) *DBPool {
	if maxOpen <= 0 {
		maxOpen = DefaultMaxOpenDBs
	}
	return &DBPool{
		dataDir:    dataDir,
		maxOpen:    maxOpen,
		conns:      make(map[string]*list.Element),
		lru:        list.New(),
		lastAccess: make(map[string]time.Time),
		nowFunc:    time.Now,
	}
}

// validateRepoName rejects repo names that could cause path traversal.
// Repo names must not contain path separators, "..", or the OS-specific
// path separator character.
func validateRepoName(name string) error {
	if name == "" {
		return fmt.Errorf("repo name must not be empty")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("repo name %q contains path traversal sequence", name)
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("repo name %q contains path separator", name)
	}
	if os.PathSeparator != '/' && strings.ContainsRune(name, os.PathSeparator) {
		return fmt.Errorf("repo name %q contains path separator", name)
	}
	return nil
}

// Open returns a *db.ClaimsDB for the given repo name, opening it lazily on
// first access. If the pool is at capacity, the least recently used connection
// is evicted (closed) before opening a new one. Subsequent calls for the same
// repo return the cached connection and promote it in the LRU order.
func (p *DBPool) Open(repoName string) (*db.ClaimsDB, error) {
	if err := validateRepoName(repoName); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Record the access time for freshness tier calculation.
	p.lastAccess[repoName] = p.nowFunc()

	// Cache hit: check if the underlying file has been modified since we opened it.
	if elem, ok := p.conns[repoName]; ok {
		entry := elem.Value.(*poolEntry)
		stale, err := p.isStale(repoName, entry.modTime)
		if err != nil {
			// If we can't stat the file, treat the cached connection as valid
			// rather than breaking callers (file may have been temporarily unavailable).
			p.lru.MoveToFront(elem)
			return entry.claimsDB, nil
		}
		if !stale {
			p.lru.MoveToFront(elem)
			return entry.claimsDB, nil
		}
		// File is newer — close old connection and remove from pool.
		_ = entry.claimsDB.Close()
		p.lru.Remove(elem)
		delete(p.conns, repoName)
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

	// Record the file's current mtime for future staleness checks.
	var modTime time.Time
	if info, err := os.Stat(path); err == nil {
		modTime = info.ModTime()
	}

	entry := &poolEntry{repoName: repoName, claimsDB: cdb, modTime: modTime}
	elem := p.lru.PushFront(entry)
	p.conns[repoName] = elem

	return cdb, nil
}

// isStale reports whether the DB file for repoName has been modified since cachedMtime.
// Caller must hold p.mu.
func (p *DBPool) isStale(repoName string, cachedMtime time.Time) (bool, error) {
	info, err := os.Stat(p.dbPath(repoName))
	if err != nil {
		return false, err
	}
	return info.ModTime().After(cachedMtime), nil
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

// RepoExists reports whether a claims database file exists for the given repo
// name. It validates the name to prevent path traversal, then checks the
// filesystem without opening the database.
func (p *DBPool) RepoExists(repoName string) (bool, error) {
	if err := validateRepoName(repoName); err != nil {
		return false, err
	}
	_, err := os.Stat(p.dbPath(repoName))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("check repo %s: %w", repoName, err)
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

// LastAccess returns the time the given repo was last queried via Open().
// If the repo has never been accessed, the zero time is returned along with false.
func (p *DBPool) LastAccess(repoName string) (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.lastAccess[repoName]
	return t, ok
}

// Len returns the number of currently open connections. Useful for testing.
func (p *DBPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lru.Len()
}

// DataDir returns the base directory containing claims database files.
func (p *DBPool) DataDir() string {
	return p.dataDir
}

// dbPath constructs the filesystem path for a repo's claims database.
func (p *DBPool) dbPath(repoName string) string {
	return filepath.Join(p.dataDir, repoName+claimsDBSuffix)
}
