# Wazero WASM Runtime Evaluation for Tree-Sitter

**Bead**: live_docs-acp.18
**Date**: 2026-03-31
**Status**: Complete

## Objective

Evaluate whether wazero-based WASM or pure-Go tree-sitter runtimes can replace CGO-based tree-sitter bindings, eliminating CGO build fragility and cross-platform issues (premortem theme 2).

## Candidates Evaluated

| Package                  | Approach         | Stars | Version         | Go Grammar          | Status    |
| ------------------------ | ---------------- | ----- | --------------- | ------------------- | --------- |
| `smacker/go-tree-sitter` | CGO (C bindings) | ~3k   | v0.0.0-20240827 | Yes                 | Baseline  |
| `malivvan/tree-sitter`   | wazero WASM      | 1     | v0.0.1          | **No** (C/C++ only) | Unusable  |
| `odvcencio/gotreesitter` | Pure Go runtime  | 436   | v0.12.2         | Yes (206 grammars)  | Evaluated |

### malivvan/tree-sitter (wazero)

The PRD specified wazero as the WASM runtime. `malivvan/tree-sitter` is the only Go library wrapping tree-sitter via wazero. However:

- Only ships C and C++ grammar WASM blobs (no Go, TypeScript, Python, etc.)
- v0.0.1 pre-release, 1 star, context-heavy API
- Would require building custom WASM blobs for each grammar and significant wrapper work
- The library is a proof of concept, not production-ready

### odvcencio/gotreesitter (pure Go)

A pure Go reimplementation of the tree-sitter runtime. No CGO, no C toolchain, no WASM. Loads the same parse-table format from upstream `parser.c` files, compressed into Go blobs.

- 206 grammars including Go, TypeScript, Python, Java, Rust, YAML, Markdown, Shell
- Query engine with full S-expression support
- Injection parsing (HTML+JS+CSS, Markdown+code)
- Incremental reparsing support
- Actively maintained (last update: 2026-03-31)

## Benchmark Results

### Test Corpus

Real Kubernetes Go files from `~/kubernetes/kubernetes/`:

| File         | Lines | Bytes   | Description          |
| ------------ | ----- | ------- | -------------------- |
| warnings.go  | 52    | 1,902   | Small utility        |
| main.go      | 102   | 4,549   | CLI entrypoint       |
| scheduler.go | 685   | 26,764  | Core scheduler       |
| kubelet.go   | 3,579 | 150,558 | Large component      |
| types.go     | 8,529 | 450,028 | API type definitions |

### Parse Time (go test -bench, AMD Ryzen 7 9800X3D)

| File                     | CGO (smacker) | Pure Go (odvcencio) | Slowdown   |
| ------------------------ | ------------- | ------------------- | ---------- |
| warnings.go (52 lines)   | 85 us         | 1.7 ms              | 19x        |
| main.go (102 lines)      | 156 us        | 2.3 ms              | 15x        |
| scheduler.go (685 lines) | 1.5 ms        | 41 ms               | 28x        |
| kubelet.go (3,579 lines) | 8.3 ms        | 524 ms              | 63x        |
| types.go (8,529 lines)   | 5.6 ms        | **40.7 s**          | **7,528x** |

### Memory Allocations (per parse)

| File         | CGO              | Pure Go                    |
| ------------ | ---------------- | -------------------------- |
| warnings.go  | 232 B / 6 allocs | 3.5 MB / 5,926 allocs      |
| main.go      | 232 B / 6 allocs | 7.7 MB / 5,964 allocs      |
| scheduler.go | 232 B / 6 allocs | 21 MB / 6,523 allocs       |
| kubelet.go   | 232 B / 6 allocs | 310 MB / 9,245 allocs      |
| types.go     | 232 B / 6 allocs | >282 MB / est. ~10k allocs |

### Correctness

Function extraction (the primary use case for live docs claims) matches 100% between CGO and pure Go across all test files. Node counts differ slightly (2-9%) due to different grammar versions and whitespace handling, but structural correctness is equivalent.

| File         | CGO Functions | Pure Go Functions | Match |
| ------------ | ------------- | ----------------- | ----- |
| warnings.go  | 1             | 1                 | 100%  |
| main.go      | 2             | 2                 | 100%  |
| scheduler.go | 18            | 18                | 100%  |
| kubelet.go   | 13            | 13                | 100%  |
| types.go     | 0             | 0                 | 100%  |

## Verdict

### PRD requirement: parse speed must be <500ms/file

- **FAIL** for pure Go on large files: kubelet.go (3,579 lines) is borderline at 524ms; types.go (8,529 lines) takes 40 seconds
- **PASS** for CGO: all files parse in under 9ms
- **N/A** for wazero: no Go grammar available

### Recommendation: Keep CGO, but with mitigation

**Do NOT replace CGO tree-sitter with wazero or pure Go at this time.**

1. **wazero approach is not viable**: The only wazero-based library (malivvan/tree-sitter) lacks Go grammar support and is pre-alpha. Building custom WASM blobs for 200+ grammars is a multi-month project with no proven upstream path.

2. **Pure Go (odvcencio/gotreesitter) is not viable for production**: The 12-7500x slowdown and 300MB+ memory allocations on large files make it unsuitable for the per-commit incremental parsing pipeline. The PRD requires <500ms/file; pure Go fails this on files >3000 lines, which are common in the kubernetes corpus.

3. **CGO tree-sitter remains the only viable option**: 85us-8ms parse times, 232 bytes memory per parse, proven correctness. The CGO build fragility concern from the premortem is real but manageable:
   - **Static linking**: Build with `CGO_ENABLED=1` and static C libs; produce a single binary
   - **Docker build**: Use a multi-stage Dockerfile with gcc for CGO compilation
   - **Pre-built binaries**: Distribute compiled binaries per platform via goreleaser
   - **Grammar vendoring**: Pin grammar C sources in the repo (smacker/go-tree-sitter already does this)

### Future re-evaluation triggers

- `odvcencio/gotreesitter` reaches v1.0 with GLR performance improvements on large files
- `malivvan/tree-sitter` adds Go grammar support and reaches v0.1+
- tree-sitter upstream ships official WASM builds with a Go consumer API
- A new wazero-based Go library emerges with multi-grammar support

## Benchmark Reproduction

```bash
cd /home/ds/live_docs/validation
go test -v -run TestCorrectnessComparison -count=1 -timeout 120s
go test -bench 'BenchmarkCGO' -benchmem -count=3 -run '^$' -timeout 120s
go test -bench 'BenchmarkPureGo/(kubelet|main|scheduler|warnings)' -benchmem -count=3 -run '^$' -timeout 120s
# NOTE: BenchmarkPureGo/types.go takes ~40s per iteration; use -timeout 600s
```
