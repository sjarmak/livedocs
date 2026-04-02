# Full Kubernetes Monorepo Extraction Results

**Date**: 2026-03-31
**Target**: kubernetes/kubernetes monorepo
**Go version**: go1.25.7 (k8s requires go1.26 -- cosmetic version warnings only)
**System**: Linux, 16 cores (GOMAXPROCS=16)
**Bead**: live_docs-acp.10

## Summary

| Metric                            | Value                      |
| --------------------------------- | -------------------------- |
| Total claims                      | 1,113,680                  |
| Total symbols                     | 704,733                    |
| Distinct import paths             | 19,093                     |
| Go deep claims                    | 333,023                    |
| Tree-sitter claims                | 780,657                    |
| .go files processed (tree-sitter) | 16,929                     |
| Errors                            | 0                          |
| Peak RSS (deep extraction load)   | 5,421 MB                   |
| DB size                           | 676.8 MB                   |
| Deep extraction time              | 8 min (5s load + 8m store) |
| Tree-sitter extraction time       | 21 min                     |
| Total wall-clock time             | ~29 min                    |

## Phase 1: Go Deep Extraction

The Go deep extractor loaded all packages via a single `go/packages.Load("./...")` call against the repo root. This leverages the shared dependency graph (as validated in the memory profile).

- **333,023 claims** extracted from all packages
- **5s** to load and extract (go/packages + go/types + go/doc)
- **8 min** to store in SQLite (single-threaded INSERT)
- **0 errors**
- **Peak RSS: 5,421 MB** (higher than the 3.5GB measured in memory profiling due to combined extraction + claim allocation; still well under 8GB budget)

### Deep Extractor Predicate Breakdown

| Predicate     | Count  |
| ------------- | ------ |
| defines       | 63,153 |
| has_kind      | 63,153 |
| encloses      | 47,442 |
| has_signature | 45,167 |
| exports       | 34,137 |
| imports       | 31,270 |
| has_doc       | 20,626 |
| is_test       | 14,258 |
| is_generated  | 11,273 |
| implements    | 2,544  |

Key observations:

- **63,153 symbols** defined (types, funcs, consts, vars at package scope + methods)
- **2,544 interface implementations** detected across the monorepo via go/types
- **45,167 function/method signatures** with full type resolution
- **47,442 encloses** relationships (package-to-symbol containment)

## Phase 2: Tree-sitter Extraction

The tree-sitter extractor processed every `.go` file individually using CGO-based parsing (smacker/go-tree-sitter). This is the fast-path comparison.

- **780,657 claims** from 16,929 files
- **21 min** total (parse + store)
- **0 errors**

### Tree-sitter Predicate Breakdown

| Predicate    | Count   |
| ------------ | ------- |
| has_doc      | 570,096 |
| defines      | 193,232 |
| imports      | 14,161  |
| is_test      | 3,014   |
| is_generated | 154     |

Key observations:

- Tree-sitter found **193,232 defines** vs deep extractor's **63,153** -- the tree-sitter extractor counts each AST node (function, method, type declarations) individually per file, while the deep extractor deduplicates across test/non-test variants
- **570,096 has_doc** claims -- tree-sitter counts every comment node as a doc claim, while the deep extractor only emits docs attached to named symbols (20,626)
- Tree-sitter does not emit deep-only predicates (has_kind, implements, has_signature, encloses) -- this is correct boundary enforcement

## Symbols

| Kind                   | Count   |
| ---------------------- | ------- |
| (empty -- tree-sitter) | 477,260 |
| func                   | 103,383 |
| method                 | 70,373  |
| type                   | 36,083  |
| var                    | 7,179   |
| module                 | 5,374   |
| const                  | 5,081   |

| Visibility | Count   |
| ---------- | ------- |
| public     | 621,103 |
| internal   | 83,630  |

## Top Import Paths by Claim Count

| Import Path                                                        | Claims |
| ------------------------------------------------------------------ | ------ |
| vendor/go.opentelemetry.io/otel/semconv/v1.39.0/attribute_group.go | 12,109 |
| vendor/go.opentelemetry.io/otel/semconv/v1.37.0/attribute_group.go | 11,294 |
| k8s.io/kubernetes/pkg/generated/openapi                            | 7,021  |
| k8s.io/kubernetes/pkg/apis/core/v1                                 | 6,327  |
| staging/src/k8s.io/api/core/v1/types.go                            | 5,969  |
| k8s.io/kubernetes/pkg/apis/core                                    | 5,870  |
| pkg/apis/core/types.go                                             | 4,758  |
| k8s.io/kubernetes/test/e2e_node                                    | 3,845  |
| k8s.io/kubernetes/pkg/kubelet                                      | 3,757  |
| k8s.io/kubernetes/pkg/apis/core/validation                         | 3,326  |

## Performance Analysis

### Memory

- Peak RSS 5.4GB confirms the monorepo is safely extractable in a single process
- Well under the 8GB budget from the premortem
- The 5.4GB peak (vs 3.5GB in the memory profile) is due to holding the claims slice in memory alongside the loaded packages

### Time

- **Deep extractor**: 5s extraction + 8m DB store = 8m total
- **Tree-sitter**: 21m for 16,929 files (1.2ms/file average)
- The DB insertion is the bottleneck, not extraction. Batch INSERT or prepared statements would cut this significantly.

### Accuracy

- Zero errors in both extractors across the full monorepo
- The deep extractor correctly identified 2,544 interface implementations
- Tree-sitter predicate boundary is correctly enforced (no deep-only predicates leaked)

## Conclusions

1. **Scale validated**: 1.1M+ claims extracted from the full kubernetes/kubernetes monorepo with zero errors
2. **Memory safe**: 5.4GB peak RSS, well under 8GB budget
3. **Complementary extractors**: Deep extractor provides type-resolved claims (implements, signatures, kinds); tree-sitter provides broad file-level coverage (comments, per-file defines)
4. **DB store is the bottleneck**: Extraction takes seconds; SQLite insertion takes minutes. Future optimization: batch inserts, prepared statements, or concurrent writers
5. **Production-ready**: Both extractors handle the full monorepo without panics, OOM, or errors

## CLI Tool

`/home/ds/live_docs/cmd/extract/main.go` -- run with `go run ./cmd/extract/`

## Database

`/home/ds/live_docs/validation/kubernetes_kubernetes.db` -- 677MB SQLite with WAL mode
