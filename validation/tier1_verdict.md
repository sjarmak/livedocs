# Tier 1 Validation Verdict

> Bead: live_docs-acp.17
> Date: 2026-03-31
> Method: Hand-wrote target Tier 1 docs for 5 k8s packages, compared against `go doc` output

## Packages Evaluated

1. **client-go/tools/cache** — complex, interface-heavy, widely imported (431 reverse deps)
2. **api/core/v1** — massive type-definition package (8529-line types.go), most-imported (576 reverse deps)
3. **apimachinery/pkg/runtime** — foundational infrastructure (1224 reverse deps), many interfaces
4. **kubelet/config** — small internal package (10 files), moderate deps (20 reverse deps)
5. **klog/v2** — leaf logging library, near-universal dep (413+ importers)

## What `go doc` Already Provides

For each package, `go doc` gives:

- Package-level doc comment (when present)
- Full list of exported constants, variables, functions, types
- Type method lists with `{ ... }` abbreviated struct bodies
- Constructor functions grouped with their types
- Full field-by-field struct details with `go doc -all` or `go doc Type`

This is substantial. For klog and api/core/v1, `go doc` output is already quite comprehensive for day-to-day use.

## What Our Tier 1 Docs Add Beyond `go doc`

### High Value (things `go doc` cannot provide)

| Claim Category                       | Example                                                                            | `go doc` Has It?                                   | Value       |
| ------------------------------------ | ---------------------------------------------------------------------------------- | -------------------------------------------------- | ----------- |
| **Interface satisfaction**           | "DeltaFIFO implements TransformingStore, DoneChecker"                              | NO — requires type checker                         | HIGH        |
| **Dependency graph**                 | "This package imports k8s.io/apimachinery/pkg/runtime, k8s.io/client-go/rest, ..." | NO                                                 | HIGH        |
| **Reverse dependencies**             | "431 packages import client-go/tools/cache"                                        | NO                                                 | HIGH        |
| **Cross-package type relationships** | "Store <- Indexer <- SharedInformer <- SharedIndexInformer"                        | PARTIAL (embedding visible per-type, not as graph) | HIGH        |
| **Architecture diagrams**            | PodConfig merge-point diagram for kubelet/config                                   | NO                                                 | HIGH        |
| **Function categorization**          | "Informer constructors (7): ..., Key functions (4): ..."                           | NO — flat alphabetical list only                   | MEDIUM-HIGH |
| **Test coverage metadata**           | "28 test files covering: controller, delta_fifo, ..."                              | NO                                                 | MEDIUM      |

### Low/Zero Value (duplicates `go doc`)

| Claim Category          | Example                                                     | Value                                             |
| ----------------------- | ----------------------------------------------------------- | ------------------------------------------------- |
| **Export lists**        | "Exported types: Pod, Service, Node, ..."                   | ZERO — `go doc` lists these identically           |
| **Function signatures** | "func NewStore(keyFunc KeyFunc, opts ...StoreOption) Store" | ZERO — `go doc` shows these verbatim              |
| **Constant lists**      | "ResourcePods, ResourceCPU, ..."                            | ZERO — `go doc` lists all constants               |
| **Package doc comment** | "Package cache is a client-side caching mechanism..."       | ZERO — `go doc` shows this first                  |
| **Struct field lists**  | Fields of Container, PodSpec, etc.                          | ZERO — `go doc -all` or `go doc Type` shows these |

## Package-by-Package Assessment

### client-go/tools/cache: HIGH VALUE

Tier 1 docs add significant value. This package has 11 exported interfaces with complex embedding relationships (Store -> Indexer -> SharedInformer) and many implementations. `go doc` shows each type in isolation; our docs show the type hierarchy, which implementations satisfy which interfaces, and the 15 k8s.io dependencies. The function categorization (7 informer constructors, 7 store constructors, 4 queue constructors, 4 key functions, 4 sync utilities) is genuinely useful versus `go doc`'s flat alphabetical list of 30+ functions.

### api/core/v1: LOW VALUE

This is a pure type-definition package. `go doc` already provides an excellent view — it lists all types, constants, and the few helper functions. Our Tier 1 docs add: dependency graph (7 imports), reverse dep count (576), categorization of types by resource kind, and noting which code is generated. The categorization has moderate value but most developers already know the core API type taxonomy. The 8529-line types.go is hard to navigate regardless of documentation format.

### apimachinery/pkg/runtime: MEDIUM-HIGH VALUE

Interface-heavy package where `go doc` shows each interface independently but misses the critical insight that `Scheme` satisfies 4 different interfaces simultaneously (ObjectTyper, ObjectCreater, ObjectConvertor, ObjectDefaulter). The architectural pattern (types -> Scheme -> serialization pipeline) is invisible in `go doc` output. Reverse dep count (1224) and dependency graph add context. Function categorization helps navigate the 30+ exported functions.

### kubelet/config: MEDIUM VALUE

Small package where the architecture diagram (3 sources merging into PodConfig) adds genuine value over `go doc`'s flat listing. The reverse dependency list (20 specific kubelet subsystems) tells a developer who should care about changes. But with only 5 exported functions and 3 types, `go doc` output is already easy to digest. The dependency graph (16 k8s.io imports) reveals this small package has a wide dependency fan-out.

### klog/v2: LOW VALUE

`go doc` output is already excellent for klog. The package doc comment is comprehensive (explains flags, usage patterns, sub-packages). Our function categorization (unstructured logging, structured logging, V-leveled, logger management, object references, output control) adds moderate organizational value but klog's API surface is well-known. The reverse dep count (413+) is interesting but not actionable.

## Quantitative Summary

| Value Category          | Claim Predicates                                                                 | Notes                                             |
| ----------------------- | -------------------------------------------------------------------------------- | ------------------------------------------------- |
| **Unique to us (HIGH)** | `implements`, dependency graph (forward + reverse), type hierarchy, architecture | Requires type checker + cross-package analysis    |
| **Additive (MEDIUM)**   | Function categorization, test metadata, file counts, generated code detection    | Could be derived from AST without type resolution |
| **Duplicate (ZERO)**    | Export lists, signatures, constants, package doc, struct fields                  | Identical to `go doc` output                      |

Estimated breakdown of a typical Tier 1 doc:

- ~40% duplicates `go doc` (export lists, signatures, constants)
- ~35% adds genuine new value (interface satisfaction, dependencies, reverse deps, architecture)
- ~25% adds marginal organizational value (categorization, metadata)

## Verdict

**PROCEED WITH TIER 1, BUT REFRAME AS "ENHANCED GO DOC" (not standalone docs)**

The premortem was right: a naive Tier 1 that just lists exports, signatures, and constants would be worthless — `go doc` already does this. But the high-value claims (interface satisfaction, dependency graph, reverse dependencies, type hierarchy) are genuinely unavailable from any existing tool and require exactly the cross-package type analysis our system would provide.

### Specific Recommendations

1. **Strip all `go doc` duplicates from Tier 1 output.** Do not render export lists, function signatures, or constant enumerations. Link to `go doc` for those. Our rendered output should contain ONLY what `go doc` cannot provide.

2. **Prioritize these Tier 1 claim categories:**
   - Interface satisfaction map (which types implement which interfaces)
   - Forward dependency graph (what this package imports from k8s.io)
   - Reverse dependency count and list (who imports this package)
   - Interface embedding hierarchy visualization
   - Function categorization by purpose (not alphabetical)
   - Architecture diagrams for packages with clear structural patterns

3. **Package-type targeting matters.** Tier 1 docs are HIGH value for interface-heavy packages (cache, runtime) and LOW value for type-definition packages (api/core/v1) and leaf libraries (klog). Consider generating different output formats per package profile.

4. **Fast-track a subset of Tier 2.** The package summary lines in our target docs ("Client-side caching mechanism for reducing Kubernetes API server calls...") are Tier 2 semantic claims, not Tier 1 structural claims. Without them, the Tier 1 docs feel like reference tables with no narrative. Consider adding a single LLM-generated purpose sentence as an early Tier 2 feature.

5. **The "enhanced go doc" framing is correct.** The PRD's should-have requirement ("Tier 1 README rendering: structural claims rendered as 'enhanced go doc' — adds cross-package relationships, interface satisfaction, 'used by' lists on top of what go doc already provides") is exactly the right scope. This validation confirms that framing.

### Decision

**GO: Proceed with Tier 1 extraction, scoped to cross-package structural claims only.** Do not build a `go doc` clone. Build the dependency graph, interface satisfaction map, and reverse dependency index. These require `go/packages` + `go/types` (the deep extractor) and cannot be approximated by tree-sitter alone.

This unblocks `live_docs-acp.1` (Claims DB schema) with confidence that the schema will store genuinely useful data.
