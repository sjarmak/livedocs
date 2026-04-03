# Premortem: MCP Claims Server

> Generated 2026-04-03 via 5-lens prospective failure analysis. Annotates `prd_mcp_claims_server.md` (v2).

## Risk Registry

| #   | Failure Lens           | Severity | Likelihood | Score | Root Cause                                                                             | Top Mitigation                                          |
| --- | ---------------------- | -------- | ---------- | ----- | -------------------------------------------------------------------------------------- | ------------------------------------------------------- |
| 1   | Scope & Requirements   | Critical | High       | 12    | Validated what we can extract (structural), not what agents query for (semantic/usage) | Fast-track Tier 2 semantic claims into describe_package |
| 2   | Technical Architecture | Critical | High       | 12    | Per-repo files optimized for isolation; primary query pattern requires aggregation     | Consolidated search index for cross-repo queries        |
| 3   | Operational            | Critical | High       | 12    | Designed for developer's workstation, not user environments                            | Startup validation, absolute paths, signal handling     |
| 4   | Integration & Deps     | Critical | High       | 12    | Tight coupling to pre-1.0 mcp-go API with no handshake test                            | Adapter layer + E2E subprocess integration test         |
| 5   | Scale & Evolution      | Critical | High       | 12    | Bounded-corpus assumption; fan-out is O(repos) with no routing                         | Repo routing index; consolidated index for >200 repos   |

## Cross-Cutting Themes

### Theme 1: Fan-out search is architecturally broken (lenses 1, 2, 5)

Per-repo SQLite + errgroup fan-out was independently flagged by 3/5 lenses. At 80 repos the LRU cap (20) contradicts the fan-out width. At 800 repos, 76 sequential batches of 10 with constant file open/close makes search unusable (14s P95). The design scales linearly in I/O cost with repo count and provides no mechanism to prune the search space before touching disk.

**Combined severity:** If fan-out search is the headline feature and it doesn't work at scale, the product fails.

**Mitigation:** Build a symbol routing index (in-memory inverted index: symbol prefix -> repo IDs). search_symbols consults this first and only fans out to matching repos. Eliminates 90%+ of I/O. For >200 repos, offer a consolidated single-DB index mode.

### Theme 2: Structural claims are necessary but not sufficient (lens 4)

The scope failure narrative is the most devastating. Agents call describe_package once for orientation, then stop and revert to reading source files because structural facts don't answer "how do I use this." The project's own Tier 1 verdict noted 40% overlap with go doc and concentrated value in interface-heavy packages. For typical non-Kubernetes repos (Django, React, CLI tools), structural claims add near-zero value over built-in doc tools.

**Combined severity:** If agents don't use the tools after first try, the entire system is a net cost (wasted tokens, wasted latency).

**Mitigation:** Tier 2 semantic claims (purpose, usage patterns, complexity) in describe_package output is must-have, not nice-to-have. Add usage examples from test files. Validate against non-Kubernetes repos.

### Theme 3: Invisible failures kill adoption (lenses 3, 4)

stdio MCP servers have no visible terminal, no health endpoint, no crash logs. The default --db path resolves relative to cwd which is unpredictable when spawned by an IDE. Memory pressure from large SQLite files causes OOM kills with no diagnostic. mcp-go breaking changes cause silent handshake failures.

**Combined severity:** Every external user hits the path resolution issue on first use. Most abandon before filing a bug.

**Mitigation:** Startup validation (check DB exists before entering stdio loop), ready signal to stderr, absolute path resolution via --project-root or env var, signal handlers for clean shutdown, subprocess integration test.

### Theme 4: Pre-1.0 dependency risk (lens 2)

mcp-go has broken its API 3 times in 8 minor versions. The MCP spec revises quarterly. WAL sidecar files already present in data/claims/ indicate unclean shutdowns. modernc.org/sqlite has had WAL bugs on Linux 6.x.

**Combined severity:** A single mcp-go or spec update can break the server for all users simultaneously.

**Mitigation:** Adapter layer wrapping all mcp-go types. E2E integration test validating JSON-RPC handshake. Weekly CI against mcp-go@latest. SQLite integrity check on startup.

## Mitigation Priority List

| Priority | Mitigation                                                               | Failure Modes Addressed | Effort |
| -------- | ------------------------------------------------------------------------ | ----------------------- | ------ |
| 1        | Add semantic context to describe_package (Tier 2 claims, usage patterns) | Scope                   | Medium |
| 2        | Startup validation + subprocess integration test                         | Ops, Integration        | Low    |
| 3        | Symbol routing index for search                                          | Tech Arch, Scale        | Medium |
| 4        | mcp-go adapter layer + weekly CI                                         | Integration             | Low    |
| 5        | Staleness metadata in responses                                          | Ops                     | Low    |
| 6        | Absolute path resolution (--project-root / env var)                      | Ops                     | Low    |
| 7        | Signal handlers for clean shutdown                                       | Ops                     | Low    |
| 8        | Consolidated index mode for >200 repos                                   | Scale                   | Medium |
| 9        | Per-language extraction validators                                       | Scale                   | Medium |
| 10       | Frequency-weighted LRU eviction                                          | Tech Arch, Scale        | Low    |

## Design Modification Recommendations

### 1. Promote Tier 2 semantic claims to Phase 1 must-have

**What:** Include LLM-generated purpose sentence and top usage patterns in describe_package output. Add a `how_to_use` section derived from test files and example code.

**Why:** 4/5 failure lenses (scope directly, others indirectly) depend on the tool output being actionable, not just structurally correct. Without semantic context, agents use the tool once and abandon it.

**Effort:** Medium. Requires wiring existing semantic/ package into the renderer pipeline. The semantic Generator, AnthropicClient, and batch infrastructure already exist.

### 2. Build symbol routing index before shipping search_symbols

**What:** At startup, scan all DBs to build an in-memory map of symbol names -> repo IDs (~65 MB for 665k entries). search_symbols consults this first, then opens only matching repos.

**Why:** Fan-out across all repos is the single riskiest architectural pattern, flagged by 3/5 lenses. The routing index makes search O(matching_repos) instead of O(all_repos).

**Effort:** Medium. ~200 lines of Go. Can reuse the XRefDB schema or build a lighter in-memory structure.

### 3. Add operational hardening to Phase 1

**What:** Startup DB validation, stderr ready signal, SIGTERM/SIGINT handlers, absolute path resolution, staleness warnings.

**Why:** Every external user will hit the invisible-failure modes on first use. These are all <50 LOC each and prevent the most common adoption-killing issues.

**Effort:** Low. ~200 lines total across 5 small changes.

### 4. Wrap mcp-go behind adapter interface

**What:** Define a `ToolRegistry` interface and `RequestParser` interface. Implement them with mcp-go. All tool handlers use the interfaces, not mcp-go types directly.

**Why:** mcp-go is pre-1.0 with a history of breaking changes. The adapter confines breakage to one file.

**Effort:** Low. ~150 lines.

### 5. Validate against non-Kubernetes repos before claiming general applicability

**What:** Run the full tool suite against 5 diverse repos (Django app, React frontend, CLI tool, microservice, data pipeline). Measure whether describe_package output is meaningfully better than built-in doc tools.

**Why:** The Kubernetes corpus is unusually interface-heavy and well-structured. Generalizing from it to "all codebases" is the scope failure's root cause.

**Effort:** Low (testing only, no code changes).

## Full Failure Narratives

### 1. Technical Architecture Failure (Severity: Critical, Likelihood: High)

Per-repo SQLite + errgroup fan-out collapsed under concurrent agent workloads. LRU thrashing (evicting DBs needed by in-flight goroutines), duplicated memory-mapped pages across 80 independent connections, and SetMaxOpenConns(2) bottleneck combined to produce 15-30s search latencies. The storage topology (per-repo isolation) was orthogonal to the access pattern (cross-repo aggregation).

### 2. Integration & Dependency Failure (Severity: Critical, Likelihood: High)

mcp-go v0.52 broke the ToolHandlerFunc signature and tool registration API. Claude Code's updated MCP client required capabilities from spec 2025-09-15 that our pinned v0.46 couldn't negotiate. Simultaneously, modernc.org/sqlite WAL handling bug caused phantom duplicate rows. Combined: server couldn't connect to updated clients, and returned corrupt data to older clients.

### 3. Operational Failure (Severity: Critical, Likelihood: High)

Default DB path resolved relative to unpredictable cwd when spawned by IDE. No startup validation, no ready signal, no crash logs. Memory pressure from large SQLite mmap on 16GB MacBooks caused OOM kills with no diagnostic. Stale claims (never re-extracted) produced incorrect output worse than no tool at all.

### 4. Scope & Requirements Failure (Severity: Critical, Likelihood: High)

Agents called describe_package once for orientation then stopped — structural claims answer "what exists" but agents need "how to use this." Usage collapsed after initial session. Static CONTEXT.md files consumed context window budget without providing actionable information. Kubernetes test corpus masked this because its interface-heavy structure is atypical.

### 5. Scale & Evolution Failure (Severity: Critical, Likelihood: High)

Enterprise adoption (752 repos, 2.1 GB claims) caused LRU thrashing, 14s P95 search latency, OOM kills. Fan-out across 752 repos required 76 sequential batches. Polyglot extraction (Java, Python, TypeScript) produced degraded results — Python missed 40% of class hierarchies, TypeScript re-exports created duplicate claims. Team abandoned after 6 weeks.
