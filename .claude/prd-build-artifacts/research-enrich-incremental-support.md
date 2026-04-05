# Research: enrich-incremental-support

## Files in scope
- `sourcegraph/enrich.go` — core enrichment pipeline (EnrichOpts, Enricher, Run, selectSymbols, rankByReverseDeps, isCacheHit, storeClaimWithMeta)
- `sourcegraph/enrich_test.go` — comprehensive test suite with mockRouter, setupTestDB, insertSymbol helpers
- `cmd/livedocs/enrich.go` — cobra CLI command with flags (data-dir, budget, max-symbols, force, dry-run, verify)

## Key patterns
- EnrichOpts struct controls behavior; Run() is the main entrypoint
- selectSymbols queries DB for symbols matching defaultKinds + visibility
- rankByReverseDeps sorts by reverse-dep fan-in count
- isCacheHit checks for existing semantic claims with matching extractor version + content hash
- Content hash stored as `enrichExtractorVersion + "@" + contentHash` in ExtractorVersion field
- mockRouter in tests returns configurable results per "predicate:symbolName" key, or can return errors
- Tests use in-memory SQLite via db.OpenClaimsDB(":memory:")
- ClaimsDB.DB() exposes raw *sql.DB for custom queries
- No existing method to fetch symbols by ID list — will need raw SQL query

## Integration points
- Claims are inserted via cdb.InsertClaim(db.Claim{...})
- Claims fetched via cdb.GetClaimsBySubject(symbolID)
- Source file metadata via cdb.GetSourceFile(repo, path)
- Enricher.SelectSymbols() is the exported method used by CLI dry-run
