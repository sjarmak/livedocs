# PRD: Structural Search Enhancement for MCP Server

## Problem Statement

The live_docs MCP server extracts rich structural data from source code — interface implementations, function signatures, doc comments, import relationships, dependency graphs — but the query tools expose almost none of it for search. `search_symbols` does SQL LIKE matching on symbol names only. An AI agent asking "find all types that implement the Handler interface" or "what functions accept a context.Context" cannot get an answer, despite this data existing in the claims database.

Meanwhile, Sourcegraph's MCP server (already available in the tool environment) provides deep semantic code search, go-to-definition, and find-references. Building a competing text search engine inside live_docs would duplicate that work at lower quality. Instead, live_docs should deepen its unique value: fast, pre-computed structural queries that no other tool provides.

Three independent research perspectives converged on this conclusion: the highest-value improvement is not adding new search technology — it is exposing the structural relationships already extracted.

### Validation Spike Results (apimachinery.claims.db, 32K claims)

A validation spike against the Kubernetes apimachinery corpus confirmed:

- **59% of claims have empty `object_text`** — structural predicates (`defines`, `has_kind`, `exports`, `imports`, `is_test`, `is_generated`) carry no text. The predicate itself IS the information.
- **41% have searchable content** — `has_doc` (2,245 claims with real English doc comments), `has_signature` (4,590 with type signatures), `encloses` (4,059 with nesting info).
- **`has_doc` content is genuinely prose-searchable** — e.g., "DirectEqual is a MatchFunc that uses the == operator to compare two values."
- **`has_signature` is structurally searchable** — e.g., `func[T comparable](a T, b T) bool` matches LIKE for type patterns.
- **`implements` and `imports` have empty `object_text`** — filtering by predicate alone is the query, not text search.

**Conclusion**: `search_claims` is validated for **predicate/kind/visibility/import_path filtering** (the primary use case). `object_text` LIKE search is useful only for `has_doc` and `has_signature` predicates — it is a secondary filter, not a general search surface. This weakens the case for FTS5 further and confirms the premortem's Scope risk (#4) was partially correct.

## Goals & Non-Goals

### Goals

- Make the claims table's relational data (predicates, kinds, visibility, object_text) queryable through the MCP tool surface
- Enable AI agents to answer structural questions: "all implementations of X", "all exported interfaces", "functions with signature matching Y"
- Extend `search_symbols` with kind/visibility filters to reduce follow-up queries
- Document the division of labor between live_docs and Sourcegraph MCP

### Non-Goals

- Full-text search (FTS5) — deferred pending usage data showing keyword search over claim text is needed
- Embedding-based semantic search — deferred; `viant/sqlite-vec` keeps the door open if needed later
- Replicating Sourcegraph's code intelligence (go-to-definition, find-references, NLP search)
- Changes to the extraction pipeline or staleness system

## Requirements

### Must-Have

- Requirement: New `search_claims` multi-repo MCP tool that searches the claims table with optional filters on predicate, kind, visibility, import_path, and object_text pattern
  - Acceptance: `search_claims` tool is registered in multi-repo mode. Calling `search_claims(predicate="implements")` returns all symbols with `implements` claims across repos. Calling `search_claims(kind="interface", visibility="public", repo="kubernetes")` returns all public interfaces in the kubernetes repo. Calling `search_claims(predicate="has_doc", object_text="%compare%")` returns symbols whose doc comments mention "compare". Results include symbol name, import path, repo, kind, and matching claim details. `go test -race ./mcpserver/...` passes.

- Requirement: `search_claims` requires at least one indexed filter (predicate, kind, or name) — reject queries with only `object_text` to prevent full table scans
  - Acceptance: Calling `search_claims(object_text="%foo%")` without predicate, kind, or name returns an error message directing the caller to narrow the query. A test verifies the rejection. `go test -race ./mcpserver/...` passes.

- Requirement: `search_claims` uses the existing `RoutingIndex` for repo fan-out in multi-repo mode, with a fallback to full fan-out when no symbol name filter is provided
  - Acceptance: When a `name` filter is provided, `search_claims` uses `RoutingIndex.Lookup` to narrow candidate repos. When no `name` filter is provided (e.g., predicate-only search), all repos are searched. Fan-out respects the existing `searchConcurrencyLimit` of 10. A test verifies both paths.

- Requirement: New `SearchClaimsByFilter` method on `ClaimsDB` that supports filtered queries on the claims+symbols join
  - Acceptance: `db.SearchClaimsByFilter(filter)` accepts a filter struct with optional fields: SymbolName (LIKE), Predicate (exact), Kind (exact), Visibility (exact), ImportPath (LIKE), ObjectText (LIKE), Limit (default 50). Returns `[]SymbolWithClaims` or similar. A test verifies each filter individually and in combination. `go test -race ./db/...` passes.

- Requirement: Extend `search_symbols` with optional `kind` and `visibility` parameters
  - Acceptance: `search_symbols` tool definition includes optional `kind` (string, e.g. "function", "interface", "type") and `visibility` (string, e.g. "public", "internal") parameters. When provided, the SQL query adds `AND kind = ?` and/or `AND visibility = ?` clauses. Existing behavior is unchanged when parameters are omitted. `go test -race ./mcpserver/...` passes.

- Requirement: Promote `query_claims` functionality to multi-repo mode or deprecate in favor of `search_claims`
  - Acceptance: Either `query_claims` is registered in multi-repo mode (with pool-based fan-out), or it is documented as deprecated in favor of `search_claims`. The chosen approach is consistent — no overlapping tools with confusing scope differences.

### Should-Have

- Requirement: `search_claims` results include a `context` field with the most relevant claim text (e.g., doc comment or signature) to reduce follow-up `describe_package` calls
  - Acceptance: Each result includes up to 3 claim texts (prioritized: has_doc > has_signature > purpose > other). A test verifies context is populated.

- Requirement: Document the division of labor between live_docs MCP and Sourcegraph MCP in tool descriptions
  - Acceptance: The `search_claims` and `search_symbols` tool descriptions include guidance like "For semantic code search, code navigation, and find-references, use Sourcegraph tools." `describe_package` description includes "For source-level definition lookup, use Sourcegraph go_to_definition."

- Requirement: Result deduplication — when the same symbol appears in multiple claims matching the filter, group by symbol and return one entry with all matching claims
  - Acceptance: `search_claims(predicate="exports")` for a repo does not return duplicate entries for the same symbol. A test verifies grouping.

### Nice-to-Have

- Requirement: `search_claims` accepts a `repo` filter to scope search to a single repo without fan-out
  - Acceptance: `search_claims(repo="kubernetes", kind="interface")` searches only the kubernetes claims DB. `go test -race ./mcpserver/...` passes.

- Requirement: Prepare FTS5 schema design document for future implementation
  - Acceptance: A `docs/design/fts5_design.md` file exists describing: the FTS5 virtual table schema, content-sync trigger design, tokenizer choice (trigram vs. standard), estimated index size overhead, and integration points with the staleness system. No code changes.

## Design Considerations

**Routing index interaction**: `search_claims` with a `name` filter can use the existing 3-character prefix routing index. Predicate-only searches (e.g., "all implements claims") require full fan-out across all repos. This is acceptable — these queries are inherently cross-cutting. The `searchConcurrencyLimit` of 10 bounds the blast radius.

**FTS5 deferral rationale**: Four factors drove the decision to defer FTS5: (1) validation spike confirmed 59% of claims have empty `object_text` — FTS5 would index mostly structural predicates that are better served by exact-match filters; (2) only `has_doc` (7%) and `has_signature` (14%) predicates have text worth searching, and LIKE with a predicate co-filter is sufficient for these; (3) FTS5 bypasses the routing index, requiring full fan-out for every query; (4) the freshness system would need to maintain FTS5 indexes, adding write amplification. Usage data from `search_claims` will inform whether FTS5 is warranted for the `has_doc`/`has_signature` subset.

**Embedding deferral rationale**: `viant/sqlite-vec` (pure-Go, no CGO) makes local embeddings technically feasible, but the ~90MB model size, embedding generation pipeline, and questionable marginal value for AI agents (who already reason semantically) argue against building it now. The door remains open.

**query_claims overlap**: The existing `query_claims` tool (single-DB mode only) searches by symbol name + optional predicate filter. `search_claims` subsumes this functionality in multi-repo mode. Options: (a) promote `query_claims` to multi-repo as an alias for `search_claims`, (b) deprecate `query_claims` and direct to `search_claims`, (c) keep both with clear scope documentation. Option (b) is cleanest.

## Convergence Decisions

Three positions were debated: (A) extend existing tools only, (B) new `search_claims` tool, (C) `search_claims` + FTS5 now. After two rounds of structured debate:

- **Resolved: Build `search_claims` as a new tool (Position B adopted).** Position A's tool-proliferation concern was neutralized by deprecating `query_claims`, keeping net tool count at 8. The different query shapes (name search vs. structural relationship search) justify separate tools for agent clarity.
- **Resolved: Extend `search_symbols` with kind/visibility filters (unanimous).** All three positions agreed this is low-risk, high-value.
- **Resolved: Deprecate `query_claims` in favor of `search_claims` (B+C consensus).** `search_claims` subsumes it with multi-repo support.
- **Deferred with design-forward: FTS5 (Position C partially adopted).** Design `SearchClaimsByFilter` to be FTS5-ready: when `object_text` filter is provided, use FTS5 `MATCH` if the virtual table exists, fall back to LIKE otherwise. This makes FTS5 a future schema migration, not a code change. Add FTS5 when usage data shows LIKE is insufficient.
- **Preserved principle from Position A**: "Simpler tool surfaces lead to better agent tool selection." Apply this to tool description writing — each tool's description must make its purpose immediately clear vs. other tools.
- **Preserved insight from Position C**: FTS5 content-sync triggers fire inside the same transaction as claim writes, so freshness is automatic. This removes the staleness concern when FTS5 is eventually added.

## Open Questions

- How often do agents currently use `search_symbols`? Telemetry data would validate demand.
- Should `search_claims` support `object_id`-based lookups (e.g., "find all claims referencing symbol ID 42")? This enables graph traversal but exposes internal IDs.
- What is the maximum acceptable result count before pagination is needed? The current `maxSearchResults = 50` may be too low for broad predicate queries.
- Would exposing `source_file` and `source_line` in search results enable agents to jump directly to code without an intermediate `describe_package` call?

### Resolved Open Questions

- **New tool vs. extend existing?** New tool (`search_claims`), deprecating `query_claims`. Net tool count unchanged.
- **FTS5 now or later?** Later, but design the DB method to be FTS5-ready so it's a schema migration when needed.

## Risk Annotations (from Premortem — 5 independent failure agents)

### Top Risks (sorted by risk score)

| #   | Failure Lens             | Severity | Likelihood | Score | Root Cause                                                               | Top Mitigation                                                                                     |
| --- | ------------------------ | -------- | ---------- | ----- | ------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------- |
| 1   | Technical Architecture   | Critical | High       | 12    | No selectivity gate — unindexed LIKE + full fan-out = unbounded work     | Require at least one indexed filter before allowing fan-out                                        |
| 2   | Scale & Evolution        | Critical | High       | 12    | LIKE O(n) + full fan-out is quadratic as repos/claims grow               | Add covering indexes on (kind, predicate); metadata sidecar for fan-out pruning                    |
| 3   | Operational              | High     | High       | 9     | DBPool 20-connection cap thrashes under fan-out + staleness contention   | Per-query timeout, separate pools for read fan-out vs. staleness writes                            |
| 4   | Scope & Requirements     | High     | High       | 9     | Demand hypothesized from data model, not validated by agent behavior     | Validate with agent session transcripts before building; usage gate (remove if 0 calls in 30 days) |
| 5   | Integration & Dependency | High     | Medium     | 6     | mcp-go optional-parameter semantics can change silently between versions | Pin exact version; integration tests for absent-parameter paths                                    |

### Cross-Cutting Themes

**Theme 1: Unbounded fan-out is the #1 systemic risk** (surfaced by Technical, Operational, Scale)
All three lenses independently identified that predicate-only queries triggering full fan-out across all repos is the core architectural vulnerability. At 80 repos it's slow; at 400 repos it's broken. The routing index only helps when a name filter is provided.

**Theme 2: LIKE on object_text is a trap** (surfaced by Technical, Scale, Scope)
LIKE with wildcards does full table scans on unindexed text. But more fundamentally, the Scope agent found that `object_text` content is machine-extracted and structurally compact — agents querying it with natural language fragments get zero results. The search surface may not exist.

**Theme 3: Unvalidated demand** (surfaced by Scope)
The entire project is justified by hypothesized demand from the data model, not observed agent behavior. The Scope agent's narrative — "zero confirmed agent-initiated calls in 6 months" — is the most damaging failure mode because it means all other engineering was wasted.

### Mitigations Promoted to Requirements

**Must-Have additions:**

- **Mandatory selectivity gate**: `search_claims` MUST require at least one indexed filter (name, kind, predicate, or import_path) before permitting fan-out. Reject queries with only `object_text` filter. (Addresses risks #1, #2, #3)
- **Per-query timeout**: `search_claims` enforces `context.WithTimeout(5s)` before fan-out. Return partial results with truncation indicator on timeout. (Addresses risks #1, #3)
- **Per-DB result limit**: SQL-level `LIMIT 100` per repo before Go-side deduplication. Prevents unbounded memory allocation. (Addresses risk #1)
- **Demand validation spike** (**COMPLETED**): Validation spike against apimachinery.claims.db confirmed structural predicate queries (implements, has_signature, kind filters) are viable. `object_text` search is limited to `has_doc` and `has_signature` predicates only. Agent session transcripts not available (no telemetry data exists), but the data model supports the use case. Risk #4 partially mitigated — add usage telemetry post-launch to confirm actual agent adoption.

**Should-Have additions:**

- **Integration tests for absent parameters**: Test every optional parameter combination through the full adapter→SQL stack, asserting absent params produce unfiltered queries. (Addresses risk #5)
- **Pin mcp-go exact version**: Treat upgrades as explicit change events. (Addresses risk #5)
- **Keep `query_claims` during deprecation period**: Register behind a warning for one release cycle before removal. (Addresses risk #5)
- **DBPool sizing**: Increase max from 20 to `min(repo_count, 80)` or make configurable. Separate staleness checker into its own pool budget. (Addresses risk #3)
- **Zero-result telemetry**: Counter for zero-result responses per tool, alert on sustained elevation. (Addresses risks #4, #5)
- **Covering indexes**: Add `(kind, predicate)` composite index to claims table. Add `(kind, visibility)` to symbols table. (Addresses risks #1, #2)

## Research Provenance

Three independent research agents contributed:

- **Prior Art & Gap Analysis**: Catalogued all 8 MCP tools + Sourcegraph's 14 tools. Identified the core gap: live_docs has rich relational data in claims but only exposes symbol name search. Recommended `search_claims` + extending `search_symbols`.
- **Technical Architecture**: Evaluated FTS5 (feasible, zero new deps), embeddings (feasible via sqlite-vec, deferred), and Sourcegraph federation. Recommended FTS5 as Phase 1, but synthesis deferred this pending usage data. Confirmed the routing index supports the fan-out pattern.
- **Failure Modes & Devil's Advocate**: Challenged the premise — agents already have grep/glob/read. Identified routing index bypass risk with FTS5, staleness system complexity compounding, and scope creep risk. Recommended focusing on structural query depth over text search.

Convergence: all three agents agreed that exposing existing structural data is higher value than adding new search technology. The `search_claims` tool is the consensus recommendation.
