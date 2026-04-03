# PRD: Code Intelligence Extraction Layer

> Post-premortem (v2). Updated 2026-03-31 after 5-lens premortem. Key change: SCIP symbol format demoted from primary key to secondary index. Primary identity is composite `repo+import_path+name`. Schema redesigned for polyglot from day 1. See `premortem_live_docs_architecture.md` for rationale.

## Problem Statement

The live docs system needs a code extraction layer that produces structured claims from source code across polyglot codebases. SCIP (Source Code Intelligence Protocol) was initially proposed as the primary extractor, but research reveals a more nuanced picture: SCIP's symbol format is valuable as a canonical identifier, but the scip-go tool itself has significant limitations (no incremental indexing, Go version breakage, pre-1.0 maturity). Meanwhile, language-native tools (Go's `go/packages`, Python's Pyright, TypeScript's compiler API) provide the same underlying analysis that SCIP indexers wrap, without the external dependency.

The recommended approach (post-premortem revision): **use a composite key (`repo+import_path+name`) as canonical identity, store SCIP symbol strings as a secondary index for external tooling, build language-specific extractors behind a polyglot-ready interface, use WASM-based tree-sitter as the universal fast path with strict predicate boundaries, and optionally consume SCIP indexes for cross-repo enrichment.**

## Goals & Non-Goals

### Goals

- Extract structural claims (symbols, types, relationships, docstrings) from any language with best-available accuracy
- Support incremental updates: per-commit claims refresh in < 1 second via tree-sitter
- Enable cross-repo symbol resolution across polyglot codebases (79 kubernetes repos as test case)
- Define a language-agnostic extractor interface with pluggable backends per language
- First-class support for Go (primary), with clear extension path for TypeScript, Python, Java, Rust, and others

### Non-Goals

- Full SCIP/Sourcegraph integration (we use the format, not the platform)
- Call graph extraction (SCIP doesn't provide this; defer to LLM)
- Real-time LSP-style code intelligence
- Supporting every language at compiler-grade accuracy simultaneously (tree-sitter provides universal baseline; deep extractors are added per-language as needed)

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                      EXTRACTION LAYER                            │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │         Language-Specific Deep Extractors (optional)        │ │
│  │                                                             │ │
│  │  Go: go/packages    TS: compiler API   Py: ast + Pyright   │ │
│  │      go/types            tsc                                │ │
│  │      go/doc                                                 │ │
│  │  Rust/Java/etc: SCIP indexer as deep extractor fallback     │ │
│  └────────────────────────────┬────────────────────────────────┘ │
│                               │                                  │
│  ┌────────────────────────────┼────────────────────────────────┐ │
│  │  Universal Fast Path: tree-sitter (200+ languages)          │ │
│  │  Per-commit incremental — 50-200ms — any language           │ │
│  └────────────────────────────┼────────────────────────────────┘ │
│                               │                                  │
│  ┌────────────────────────────┼────────────────────────────────┐ │
│  │  SCIP Importer (optional — cross-repo symbol resolution)    │ │
│  │  Parses index.scip protobuf from any of 12+ SCIP indexers  │ │
│  └────────────────────────────┼────────────────────────────────┘ │
│                               ▼                                  │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │              Extractor Interface                             │ │
│  │  Extract(path, lang) → []Claim                               │ │
│  │  Primary ID: repo + import_path + symbol_name                │ │
│  │  SCIP symbol: secondary index for external tooling           │ │
│  │  Polyglot-aware: language, visibility, scope fields          │ │
│  └──────────────────────────┬──────────────────────────────────┘ │
│                             ▼                                    │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │              Claims DB (SQLite)                              │ │
│  │  symbols | claims | source_files | cross_repo_refs          │ │
│  │  A `defines` claim from Go and Python look identical        │ │
│  └─────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

### Three Extraction Tiers (per language)

| Tier                   | Tool                         | Speed         | Accuracy                                           | When                              |
| ---------------------- | ---------------------------- | ------------- | -------------------------------------------------- | --------------------------------- |
| **Universal baseline** | tree-sitter (any language)   | 50-200 ms     | Syntactic — signatures, imports, exports, comments | Per-commit hot path               |
| **Deep extractor**     | Language-native compiler API | 10-30 min     | Compiler-grade — type resolution, interface impls  | Initial index, nightly validation |
| **Cross-repo**         | SCIP protobuf import         | N/A (offline) | Compiler-grade                                     | Cross-repo symbol resolution      |

### Per-Language Extractor Status

| Language          | Deep Extractor                        | Tree-sitter                                 | SCIP Indexer           | Status                     |
| ----------------- | ------------------------------------- | ------------------------------------------- | ---------------------- | -------------------------- |
| **Go**            | `go/packages` + `go/types` + `go/doc` | `tree-sitter-go`                            | `scip-go`              | MVP — Phase 1              |
| **TypeScript**    | TypeScript compiler API               | `tree-sitter-typescript`                    | `scip-typescript`      | Phase 3+                   |
| **Python**        | `ast` module + Pyright                | `tree-sitter-python`                        | `scip-python`          | Phase 3+                   |
| **Java**          | —                                     | `tree-sitter-java`                          | `scip-java`            | Phase 3+                   |
| **Rust**          | —                                     | `tree-sitter-rust`                          | `rust-analyzer` (SCIP) | Phase 3+                   |
| **YAML/Markdown** | —                                     | `tree-sitter-yaml` / `tree-sitter-markdown` | N/A                    | Phase 2 (config/doc files) |
| **Shell**         | —                                     | `tree-sitter-bash`                          | N/A                    | Phase 3+                   |
| **Any other**     | —                                     | tree-sitter grammar (if exists)             | —                      | On demand                  |

### Adding a New Language

1. **Tree-sitter grammar** (usually pre-existing) — gives universal baseline immediately
2. **AST node → claim predicate mapping** — e.g., Python `def` → `defines`, `import` → `imports`
3. **Optionally, a deep extractor** — language-native compiler API or SCIP indexer for compiler-grade accuracy

## Requirements

### Must-Have

- **Extractor interface**: `Extract(path, lang) → []Claim` with pluggable backends per language. Claim struct must be polyglot-ready: `Language` field, `Visibility` enum (not boolean), scope-based paths (not Go module paths).
- **Go deep extractor**: `go/packages` + `go/types` + `go/doc` with 8GB memory cap and topological-layer extraction fallback
- **Tree-sitter universal extractor (WASM)**: Via wazero — no CGO dependency. Vendored grammar `.wasm` blobs with pinned versions. Strict predicate boundary: only `defines`, `imports`, `exports`, `has_doc`, `is_test`, `is_generated`. No type-resolution predicates.
- **Language registry**: Maps file extensions → tree-sitter grammar + optional deep extractor + AST node → claim predicate mapping. New languages gated on <2% parse error rate against corpus.
- **Composite primary key**: `repo + import_path + symbol_name` as canonical identity. SCIP symbol strings stored as secondary indexed column for optional external tooling interoperability.
- **Claims DB schema**: Per-repo SQLite files (not one monolithic DB). Lightweight cross-repo symbol index for resolution. See updated schema below.
- **Content-hash caching**: SHA-256 of source file + extractor version + grammar version. LRU eviction at 2GB cap. Deletion-aware reconciliation on every git diff.

### Should-Have

- **SCIP protobuf importer**: Parse `index.scip` files to enrich claims DB (for consuming external indexes)
- **Automated version normalization**: Parse `go.mod` replace directives via `go mod edit -json`, map staging `v0.0.0` to release tags via `git describe --tags`. Zero manual curation.
- **Docstring claim ingestion**: `documentation[]` / godoc comments → `has_doc` claims at confidence 0.85
- **Claim predicate vocabulary**: 10 structural predicates (tree-sitter-safe subset) + 4 deep-extractor predicates + 4 semantic predicates
- **Cross-extractor consistency check**: Before rendering, flag any symbol with conflicting claims from tree-sitter vs deep extractor. Deep extractor wins; tree-sitter claim marked stale.
- **Schema conformance smoke test**: Trivial TypeScript extractor (exports + function names) built in Phase 1 to validate polyglot schema before data volume grows.

### Nice-to-Have

- Full SCIP index generation (if we need to publish indexes for external consumers)
- Cross-repo drift detection queries
- Deep extractors for TypeScript (compiler API), Python (ast + Pyright), and other languages
- Auto-detection of project languages from file extensions and build configs

## Claims Schema (Post-Premortem v2)

> Key changes from v1: composite primary key replaces SCIP, polyglot-ready fields,
> extractor version tracking, per-repo DB design. See `premortem_live_docs_architecture.md`
> themes 1 (SCIP liability), 2 (cache keys), 4 (two-extractor consistency), 5 (polyglot).

```sql
-- Per-repo SQLite file: {repo_slug}.db
-- Cross-repo index: _xref.db (symbol_key → repo → symbol_id)

CREATE TABLE symbols (
    id              INTEGER PRIMARY KEY,
    repo            TEXT NOT NULL,              -- 'kubernetes/kubernetes'
    import_path     TEXT NOT NULL,              -- 'k8s.io/api/core/v1' (Go), 'src/components' (TS)
    symbol_name     TEXT NOT NULL,              -- 'Pod', 'NewInformer'
    language        TEXT NOT NULL,              -- 'go', 'typescript', 'python', 'shell'
    kind            TEXT NOT NULL,              -- 'type', 'func', 'const', 'var', 'class', 'module'
    visibility      TEXT NOT NULL DEFAULT 'public'
                    CHECK(visibility IN ('public', 'internal', 'private', 're-exported', 'conditional')),
    display_name    TEXT,
    scip_symbol     TEXT,                       -- secondary index, nullable, for external tooling
    UNIQUE(repo, import_path, symbol_name)
);
CREATE INDEX idx_symbols_scip ON symbols(scip_symbol) WHERE scip_symbol IS NOT NULL;
CREATE INDEX idx_symbols_import_path ON symbols(import_path);

CREATE TABLE claims (
    id              INTEGER PRIMARY KEY,
    subject_id      INTEGER NOT NULL REFERENCES symbols(id),
    predicate       TEXT NOT NULL,
    object_text     TEXT,
    object_id       INTEGER REFERENCES symbols(id),
    source_file     TEXT NOT NULL,
    source_line     INTEGER,
    confidence      REAL NOT NULL DEFAULT 1.0,
    claim_tier      TEXT NOT NULL CHECK(claim_tier IN ('structural', 'semantic')),
    extractor       TEXT NOT NULL,              -- 'go-deep:1.2', 'tree-sitter-go:0.21.0'
    extractor_version TEXT NOT NULL,            -- version of extractor that produced this claim
    last_verified   TEXT NOT NULL
);
CREATE INDEX idx_claims_subject ON claims(subject_id);
CREATE INDEX idx_claims_source_file ON claims(source_file);

CREATE TABLE source_files (
    id              INTEGER PRIMARY KEY,
    repo            TEXT NOT NULL,
    relative_path   TEXT NOT NULL,
    content_hash    TEXT NOT NULL,              -- SHA-256 of file content
    extractor_version TEXT NOT NULL,            -- version of extractor used
    grammar_version TEXT,                       -- tree-sitter grammar version (nullable for deep extractor)
    last_indexed    TEXT NOT NULL,
    deleted         INTEGER NOT NULL DEFAULT 0, -- tombstone flag for deletion-aware reconciliation
    UNIQUE(repo, relative_path)
);
```

### Per-Repo Partitioning

Each repo gets its own SQLite file (`kubernetes_kubernetes.db`, `kubernetes_client-go.db`, etc.).
Cross-repo resolution uses a lightweight index DB:

```sql
-- _xref.db: cross-repo symbol resolution index
CREATE TABLE xref (
    symbol_key      TEXT NOT NULL,             -- 'k8s.io/api/core/v1.Pod' (import_path.name)
    repo            TEXT NOT NULL,
    symbol_id       INTEGER NOT NULL,
    PRIMARY KEY(symbol_key, repo)
);
```

Cross-repo queries become two-step: look up repos in `_xref.db`, then fan out to per-repo DBs.
This avoids multi-million-row self-JOINs on a single file.

## Structural Claim Predicates (language-agnostic)

These predicates apply across all languages. The _tree-sitter boundary_ column indicates whether tree-sitter may emit this predicate (premortem constraint #6).

### Tree-sitter-safe predicates (fast path may emit)

| Predicate      | Go Source                   | Tree-sitter (any lang)                                 | Confidence | Fast Path |
| -------------- | --------------------------- | ------------------------------------------------------ | ---------- | --------- |
| `defines`      | `go/packages`               | Function/class/type declaration nodes                  | 1.0        | YES       |
| `imports`      | `go/packages` import graph  | import/require/use statement nodes                     | 1.0        | YES       |
| `exports`      | Exported symbol detection   | `export` keyword / visibility modifier nodes           | 1.0        | YES       |
| `has_doc`      | `go/doc` comment extraction | Adjacent comment node heuristic                        | 0.85       | YES       |
| `is_generated` | `// Code generated` header  | Comment content matching                               | 1.0        | YES       |
| `is_test`      | `_test.go` file detection   | File path pattern (`*_test.*`, `test_*`, `__tests__/`) | 1.0        | YES       |

### Deep-extractor-only predicates (require type resolution)

| Predicate       | Go Source                     | Tree-sitter                                           | Confidence | Fast Path |
| --------------- | ----------------------------- | ----------------------------------------------------- | ---------- | --------- |
| `has_kind`      | `go/types`                    | NOT ALLOWED (needs resolved types)                    | 1.0        | NO        |
| `implements`    | `go/types.Implements()`       | NOT ALLOWED (needs type resolution)                   | 1.0        | NO        |
| `has_signature` | `go/types` function signature | NOT ALLOWED (needs resolved types for cross-pkg refs) | 1.0        | NO        |
| `encloses`      | Package → symbol containment  | NOT ALLOWED (needs package-level context)             | 1.0        | NO        |

> **Consistency rule**: When both tree-sitter and deep extractor have produced claims for the same symbol, deep extractor claims always win. Tree-sitter claims are marked stale and excluded from rendering. This prevents the two-source-of-truth problem identified in premortem themes 1 and 4.

## Design Considerations

**Why not scip-go as the primary extractor?**

- No incremental indexing (full repo every time, 10-30 min)
- Breaks on every major Go release (Go 1.24, 1.25 confirmed; 1.26 untested)
- Pre-1.0 maturity (v0.1.26, 120 commits)
- 60% of SCIP data irrelevant to our use case (syntax highlighting, diagnostics)
- `go/doc` is literally designed for documentation extraction — closer fit than an IDE protocol

**Why SCIP symbol format is secondary, not primary (premortem revision):**

The original design used SCIP symbol strings as the primary key. Premortem analysis (3 independent lenses) identified this as the highest-confidence risk:

- Kubernetes staging `replace` directives produce 47+ symbol variants per logical entity (e.g., `k8s.io/client-go` resolves differently from monorepo vs standalone)
- SCIP spec is pre-1.0 (v0.7.0→v0.8.0) with field additions in minor versions
- Go-specific module path encoding makes format non-portable to other languages
- Version strings in symbols make normalization a mandatory, error-prone layer

**New approach**: Composite key `repo + import_path + symbol_name` as primary identity. SCIP symbol strings stored as nullable secondary index for optional external tooling compatibility. Cross-repo JOINs use `import_path + symbol_name` which is version-agnostic and staging-agnostic.

**Staging directory version mismatch:**
The main kubernetes repo uses `replace` directives mapping `k8s.io/client-go => ./staging/src/k8s.io/client-go`. Solution: automated normalization via `go mod edit -json` + `git describe --tags`, not a manually-maintained table. Normalization is only needed for the SCIP secondary index, not for primary identity resolution.

## Phased Implementation (Post-Premortem v2)

**Phase 1 (Week 1-2): Go extractor + polyglot-ready Claims DB**

- Profile `go/packages` on 500-package subset with 8GB memory cap — validate feasibility
- Build Go deep extractor with topological-layer extraction fallback if memory cap exceeded
- Create per-repo SQLite claims DB with composite primary key schema (v2)
- Build trivial TypeScript extractor (exports + function names) as schema conformance smoke test
- Generate SCIP symbol strings as secondary index (nullable, not primary key)
- Test on 5 representative repos (not full 79-repo corpus)
- Deliverable: per-repo claims DBs for 5 repos, polyglot schema validated

**Phase 2 (Week 3-4): WASM tree-sitter + incremental pipeline**

- Build tree-sitter extractor via wazero WASM runtime — no CGO dependency
- Vendor grammar `.wasm` blobs with pinned versions
- Enforce strict predicate boundary (6 fast-path predicates only)
- Build content-hash caching with extractor+grammar version in keys, 2GB LRU cap
- Build deletion-aware reconciliation (tombstone on file delete/rename)
- Build cross-extractor consistency check (deep wins on conflict)
- Deliverable: incremental pipeline with WASM tree-sitter, no CGO, consistency checks

**Phase 3 (Month 2): Full corpus + cross-repo + polyglot baseline**

- Expand to full 79-repo corpus with automated sync and health checks
- Build cross-repo index (`_xref.db`) for symbol resolution via composite keys
- Automated version normalization for SCIP secondary index (`go mod edit -json` + `git describe`)
- Add tree-sitter claim mappings for TypeScript, Python, Shell (gated on <2% parse error rate)
- Deliverable: per-repo DBs for 79 repos, cross-repo resolution working, polyglot baseline

**Phase 4 (Month 3+): Semantic claims + deep extractors**

- LLM-generated semantic claims (purpose, usage patterns)
- Contract anchor verification
- Drift detection pipeline
- SCIP protobuf importer for optional cross-repo enrichment
- Deep extractors for TypeScript/Python only if tree-sitter baseline proves insufficient
- Deliverable: full live docs system, polyglot-capable

## Research Provenance

| Lens         | Key Contribution                                                                       |
| ------------ | -------------------------------------------------------------------------------------- |
| Feasibility  | 8-16GB RAM, 10-30min estimates; Go version breakage pattern; start with staging module |
| Data Model   | Complete SQL schema, 14+4 claim predicates, documentation[] as claim source            |
| Incremental  | scip-go has no incremental; tree-sitter 1000x faster; tiered extraction strategy       |
| Multi-Repo   | Symbol strings as cross-repo join key; staging version mismatch; 3 merge strategies    |
| Alternatives | go/doc is purpose-built for docs; SCIP wraps same Go tools; build direct, adopt format |

**Key decision (revised post-premortem): Use SCIP's symbol format as secondary index, not primary identity.** Primary key is `repo+import_path+name`. Build on Go stdlib for extraction, WASM tree-sitter for speed with strict predicate boundary, per-repo SQLite for scale. SCIP protobuf import optional for cross-repo enrichment.
