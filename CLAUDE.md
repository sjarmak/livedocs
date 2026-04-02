# Live Docs — Project Instructions

## Purpose

Experimental platform for **live documentation** — tools and workflows that keep repository documentation automatically up to date with every commit. The goal is self-documenting codebases.

## Test Subject

The cloned Kubernetes organization at `~/kubernetes/` serves as the primary test corpus:

- **79 repositories** cloned from the kubernetes GitHub org
- **Main repo**: `~/kubernetes/kubernetes/` — Go monorepo (Go 1.26.1), ~28 cmd entrypoints
- **Website repo**: `~/kubernetes/website/` — Hugo-based docs site
- Covers Go, documentation (Markdown/Hugo), YAML configs, shell scripts, and more

## Experiment Scope

- Generating and maintaining codemaps, READMEs, and architectural docs
- Detecting documentation drift after code changes
- Automating doc updates via git hooks or CI
- Measuring documentation freshness and coverage

## Conventions

- This project directory (`~/live_docs/`) holds experiment code, scripts, and results
- The kubernetes clone (`~/kubernetes/`) is read-only test data — do not commit changes there
- Document experiment results and findings as they emerge

## Build & Run

No build system yet — project is in bootstrapping phase.
