// Package evergreen provides a provider-agnostic layer for maintaining saved
// deep-search-style prose documents as the code they cite evolves.
//
// A Document pairs a saved query with the rendered prose answer and a
// dependency manifest — the set of symbols, repos, and commits the answer
// was derived from. The detector diffs that manifest against current claims
// to emit drift findings classified by severity (hot/warm/cold/orphaned).
// A RefreshExecutor re-runs the underlying query to produce a new answer
// and manifest.
//
// The package is designed around interfaces so it can serve two deployments
// from one codebase:
//
//  1. live_docs OSS installs, where the default SQLite DocumentStore and
//     deepsearch-MCP RefreshExecutor are wired automatically.
//  2. An external adapter (e.g. the sourcegraph sj/egds-livedocs branch),
//     which supplies its own DocumentStore and RefreshExecutor backed by
//     existing upstream infrastructure and reuses the same detector,
//     interfaces, and MCP tool factories.
//
// The types and interfaces declared here are the public contract the
// adapter compiles against. Breaking changes must be semver-signaled.
package evergreen
