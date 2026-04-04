# Premortem: Staleness Checker Wiring

## Risk Registry

| #   | Failure Mode                                         | Severity     | Likelihood | Score | Root Cause                                                            | Top Mitigation                                          |
| --- | ---------------------------------------------------- | ------------ | ---------- | ----- | --------------------------------------------------------------------- | ------------------------------------------------------- |
| 1   | RunInTransaction data corruption                     | Critical (4) | High (3)   | 12    | Shared mutable `c.exec` swapped without locking                       | sync.Mutex + concurrent race test                       |
| 2   | Scale: staleness cache OOM + DBPool thrashing        | Critical (4) | High (3)   | 12    | Unbounded cache map + 20-DB LRU cap at 800 repos                      | Bounded LRU cache + dynamic DBPool cap                  |
| 3   | Operational: MCP + watch writer contention           | Critical (4) | High (3)   | 12    | Two processes writing same .claims.db, thundering herd on SQLITE_BUSY | Best-effort writes + backoff on contention              |
| 4   | Integration: tree-sitter grammar silent breakage     | Critical (4) | Medium (2) | 8     | Node type strings hardcoded, grammar update zeros out claims          | Grammar contract tests + zero-claim write guard         |
| 5   | Deleted-file phantom claims                          | High (3)     | Medium (2) | 6     | CheckPackageStaleness skips os.ErrNotExist silently                   | os.IsNotExist check, emit deletion marker               |
| 6   | Unbounded re-extraction latency                      | High (3)     | Medium (2) | 6     | No context/timeout on CheckPackageStaleness                           | context.WithTimeout(5s) + recover()                     |
| 7   | Scope mismatch: staleness checks uncommitted edits   | High (3)     | Medium (2) | 6     | No debounce, re-extracts on every IDE save                            | 5s debounce or git HEAD comparison                      |
| 8   | Stale repo_root paths in extraction_meta             | Medium (2)   | Medium (2) | 4     | Repo moved/deleted after extraction                                   | Validate repo_root on MCP startup                       |
| 9   | Tree-sitter panic in request path                    | Critical (4) | Low (1)    | 4     | Malformed file crashes extractor                                      | recover() wrapper around RefreshStaleFiles              |
| 10  | Partial extraction on timeout leaves inconsistent DB | High (3)     | Medium (2) | 6     | 5s timeout fires mid-transaction with no rollback                     | Atomic transaction: all-or-nothing per extraction batch |

## Cross-Cutting Themes

### Theme 1: Concurrent Access Safety (Risks 1, 3)

The system was designed for single-writer access but gains a second write path via staleness. The RunInTransaction race (in-process) and MCP-vs-watch contention (cross-process) are the same fundamental problem at different levels. Both must be fixed before staleness writes are enabled. The fail-arch and fail-ops agents independently identified the same cascade: contention -> timeout -> retry -> thundering herd.

### Theme 2: Silent Data Degradation (Risks 4, 5, 8)

Three independent failure modes cause the system to silently serve wrong data: grammar breakage zeroing out claims, deleted files leaving phantom claims, and stale repo roots pointing nowhere. AI agents trust MCP responses — silent degradation is worse than loud failure.

### Theme 3: Scale Ceilings (Risks 2, 10)

The staleness cache (unbounded map), DBPool (20-DB cap), and per-file stat checks (O(files) per query) all hit ceilings at 200+ repos or monorepo scale. The fail-scale agent identified a concrete OOM path: 800 repos x 150 import paths = 120K cache entries, Go maps never release memory, 2GB RSS after 48 hours.

### Theme 4: Scope Miscalibration (Risk 7)

The fail-scope agent raised the most provocative question: agents need whole-repo consistency at task boundaries, not per-file incremental patching on every request. The staleness checker may be solving the wrong problem at the wrong granularity.

## Full Failure Narratives

### 1. Technical Architecture Failure (fail-arch)

**What happened:** Six months after shipping, concurrent MCP requests for packages in the same repo interleaved their transactions via the shared `c.exec` field in `RunInTransaction`. One goroutine's transaction overwrote the other's executor mid-flight, causing claims to be inserted outside any transaction, or committed/rolled back by the wrong caller. In WAL mode with `busy_timeout=5000` and `maxOpenConnsPerDB=2`, this manifested as silent data corruption (duplicate claims, orphaned symbols) before escalating to SQLITE_BUSY errors under higher concurrency. The DBPool's mtime-based invalidation added a third dimension: when watch updated the DB, DBPool closed the old ClaimsDB underneath an in-flight RefreshStaleFiles transaction, causing panics.

**Root cause:** `RunInTransaction` mutates shared struct state (`c.exec`) without synchronization, and `DBPool` returns the same instance to multiple goroutines.

**Warning signs:** No concurrent RunInTransaction test; DBPool returns raw ClaimsDB with no per-caller isolation; reExtractFile ignores context.

**Mitigations:** Mutex on RunInTransaction; reference-counted DB leases in DBPool; context.WithTimeout on reExtractFile; singleflight.Group per repo for staleness checks.

**Severity:** Critical | **Likelihood:** High

### 2. Integration & Dependency Failure (fail-integration)

**What happened:** After a `go-tree-sitter` update tracking upstream tree-sitter 0.25, grammar node type names changed (`function_declaration` -> `func_declaration`). The extractor registry's hardcoded strings silently matched zero nodes, producing empty claim sets. The staleness checker faithfully re-extracted on every request, saw "no claims," and wrote empty results — destroying previously valid data. The 10s TTL cache and mtime invalidation masked the issue: fresh timestamps, zero claims. Three weeks of degraded data before anyone noticed.

**Root cause:** Extractor registry couples directly to tree-sitter grammar node type strings with no version-pinning or validation.

**Warning signs:** Tests use simple inline snippets that don't exercise renamed node types; pipeline treats "zero claims" as valid; grammar packages have independent version lifecycles.

**Mitigations:** Grammar contract tests per language with pinned reference files; write guard refusing to overwrite non-zero claims with zero claims unless --force; pin grammar versions exactly.

**Severity:** Critical | **Likelihood:** Medium

### 3. Operational Failure (fail-ops)

**What happened:** Users running `livedocs watch` alongside MCP experienced SQLITE_BUSY cascades. The staleness checker's 5s timeout was too short when watch held a write lock processing hundreds of files. Failed extractions weren't cached with backoff, so every subsequent MCP request re-triggered extraction — thundering herd. The MCP server runs over stdio as a subprocess; stderr errors were invisible. Users saw stale claims, assumed the tool was broken, stopped using it. Recovery was painful: orphaned -shm/-wal files, no health check, no startup WAL cleanup.

**Root cause:** Two independent processes performing concurrent write-heavy SQLite transactions with no coordination beyond SQLite's built-in locking.

**Warning signs:** Orphaned -shm/-wal files already visible in git status; no integration test for dual-process access; stderr is unmonitored.

**Mitigations:** Lock file or socket protocol for single-writer coordination; cache failed extractions with exponential backoff; surface failures in MCP response metadata; WAL checkpoint on startup.

**Severity:** Critical | **Likelihood:** High

### 4. Scope & Requirements Failure (fail-scope)

**What happened:** AI agents query in bursts at task start, then edit for minutes/hours before querying again. The 10s TTL cache expired long before the next query. Worse, agents typically query after sweeping multi-file edits — the 5s timeout was calibrated for single-file changes. Timeouts fired, extraction was incomplete, agents received partially-updated claims. The staleness checker solved for "freshness at query time" when agents needed "freshness at task start" — a complete, consistent snapshot even if it takes 30s. The mutex serialized concurrent tool calls, making the MCP server feel slow. Teams disabled staleness checking and fell back to manual `livedocs extract`.

**Root cause:** Assumed agents need continuous incremental freshness when they actually need consistent whole-repo snapshots at task boundaries.

**Warning signs:** No telemetry on inter-query intervals; 5s timeout never tested against multi-file edit patterns; watch already solved freshness at a coarser (correct) granularity.

**Mitigations:** Explicit `refresh` MCP tool for task-boundary re-extraction; background snapshot goroutine instead of per-request patching; per-DB read-write locks instead of global mutex; agent query pattern telemetry.

**Severity:** High | **Likelihood:** Medium

### 5. Scale & Evolution Failure (fail-scale)

**What happened:** At 800 repos with multi-agent concurrency, three failures compounded. (1) `describe_package` for `k8s.io/kubernetes/pkg/kubelet` stated 200+ files while holding the RunInTransaction mutex, blocking all other requests. (2) DBPool thrashed: 800 repos, 20-DB cap, constant eviction churn. (3) Staleness cache grew to 120K entries (800 repos x 150 import paths), Go maps never release memory, process hit 2GB RSS and was OOM-killed after 48 hours. The 5s timeout had no partial-rollback — if it fired mid-transaction, the DB contained a mix of fresh and stale claims.

**Root cause:** Per-file, synchronous, check-everything model assumed small repos and single-client access with no mechanism to amortize work across requests.

**Warning signs:** `describe_package` on large packages already takes 800ms without re-extraction; no DBPool eviction metrics; staleness cache has no size cap; 5s timeout has no rollback.

**Mitigations:** Batch staleness checks per directory (O(dirs) not O(files)); background work queue with deduplication; per-DB locks instead of global mutex; bounded LRU cache with size cap; dynamic DBPool sizing.

**Severity:** Critical | **Likelihood:** High

## Mitigation Priority List

| #   | Mitigation                                                      | Risks Addressed | Effort | Impact   |
| --- | --------------------------------------------------------------- | --------------- | ------ | -------- |
| 1   | Mutex on RunInTransaction + concurrent race test                | 1               | Low    | Critical |
| 2   | context.WithTimeout(5s) + recover() on RefreshStaleFiles        | 6, 9            | Low    | High     |
| 3   | os.IsNotExist detection + claim deletion                        | 5               | Low    | High     |
| 4   | Best-effort SQLITE_BUSY fallback + backoff cache for failures   | 3               | Medium | Critical |
| 5   | Bounded staleness cache (LRU, max 10K entries, 10s TTL)         | 2               | Low    | High     |
| 6   | Grammar contract tests per language                             | 4               | Medium | Critical |
| 7   | Zero-claim write guard (refuse to overwrite non-zero with zero) | 4               | Low    | High     |
| 8   | Validate repo_root on startup                                   | 8               | Low    | Medium   |
| 9   | Atomic transaction rollback on timeout (all-or-nothing)         | 10              | Medium | High     |
| 10  | Debounce re-extraction (5s cooldown after file modification)    | 7               | Medium | Medium   |

## Design Modification Recommendations

1. **Mutex + concurrent test for RunInTransaction** (Risk 1, score 12). The single highest-risk item. 3 lines of code + 1 test. Do this first.

2. **Best-effort SQLITE_BUSY handling with failure backoff** (Risk 3, score 12). When RefreshStaleFiles gets SQLITE_BUSY, return stale data with warning. Cache the failure with exponential backoff (10s, 30s, 60s) to prevent thundering herd. Prevents the most likely production incident.

3. **Bounded staleness cache** (Risk 2, score 12). Replace `map[string]cacheEntry` with a bounded LRU (max 10K entries). Prevents OOM at scale. Go's `container/list` + map is sufficient — no new deps.

4. **Grammar contract tests + zero-claim write guard** (Risk 4, score 8). A grammar update silently zeroing out claims is the sneakiest failure mode. One contract test per language (parse reference file, assert known symbols appear) + refusing to overwrite non-zero claims with zero prevents silent data destruction.

5. **Atomic timeout rollback** (Risk 10, score 6). If the 5s timeout fires mid-extraction, roll back the entire transaction rather than leaving a mix of fresh and stale claims. The `RunInTransaction` pattern already supports this — just ensure the context cancellation triggers rollback.
