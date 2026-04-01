# Contributing to Live Docs

Thanks for your interest in contributing! Live Docs keeps repository documentation automatically in sync with code changes. We welcome contributions of all kinds.

## Getting Started

### Prerequisites

- Go 1.25+ installed
- Git

### Build and Test

```bash
# Build
go build ./...

# Run tests
go test ./...

# Run tests with race detection
go test -race ./...

# Check coverage
go test -cover ./...
```

### Code Style

- Run `go fmt ./...` before committing
- Run `go vet ./...` to catch common issues
- Keep functions under 50 lines where practical
- Handle all errors explicitly with context: `fmt.Errorf("doing X: %w", err)`

## How to Contribute

1. Fork the repository
2. Create a feature branch: `git checkout -b feat/your-feature`
3. Write tests first, then implement (TDD)
4. Ensure all tests pass: `go test ./...`
5. Commit with a descriptive message: `feat: add support for X`
6. Open a pull request against `main`

### Commit Message Format

```
<type>: <description>
```

Types: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `perf`, `ci`

### Pull Request Process

- Describe what changed and why
- Include a test plan
- Link related issues
- Keep PRs focused — one logical change per PR

## Adding a New Language Extractor

Live Docs uses [tree-sitter](https://tree-sitter.github.io/) grammars to extract symbols (functions, types, interfaces) from source code. Adding a new language is one of the best ways to contribute.

### Steps

1. Create a new directory: `extractor/<language>/`
2. Add the tree-sitter grammar as a dependency
3. Implement the `extractor.Extractor` interface:
   - `Extract(path string, src []byte) ([]Symbol, error)` — parse a file and return symbols
   - `Languages() []string` — return supported file extensions
4. Register the extractor in `extractor/registry.go`
5. Add tests with real-world sample files in `extractor/<language>/testdata/`
6. Update the README to list the new language

### What Makes a Good Extractor

- Extracts functions, methods, types/classes, interfaces, and constants
- Captures doc comments and signatures
- Handles edge cases (nested types, generics, etc.)
- Includes tests with representative code samples

## Good First Issues

These language extractors are planned and well-scoped for new contributors:

- **Rust extractor** — extract `fn`, `struct`, `enum`, `trait`, `impl` blocks using `tree-sitter-rust`
- **Java extractor** — extract classes, interfaces, methods, annotations using `tree-sitter-java`
- **C++ extractor** — extract classes, functions, templates, namespaces using `tree-sitter-cpp`

Each requires implementing the `Extractor` interface and adding test coverage. Look for issues labeled `good first issue` and the relevant language label.

## Reporting Bugs

Use the [bug report template](https://github.com/live-docs/live_docs/issues/new?template=bug_report.md) and include:

- Steps to reproduce
- Expected vs actual behavior
- Go version and OS

## Requesting Features

Use the [feature request template](https://github.com/live-docs/live_docs/issues/new?template=feature_request.md).

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).
