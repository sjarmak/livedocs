# Plan: enrich-incremental-support

## Step 1: Add SymbolIDs field to EnrichOpts
- Add `SymbolIDs []int64` to EnrichOpts struct
- Add `fetchSymbolsByIDs()` method to Enricher that queries symbols by ID list via raw SQL
- Modify Run() to check `len(opts.SymbolIDs) > 0` and if so, call fetchSymbolsByIDs instead of selectSymbols+rankByReverseDeps

## Step 2: Tombstone infrastructure
- Add constants for tombstone predicates: `enrichment_failed`, `enrichment_permanently_failed`
- Add `storeTombstone()` method: inserts claim with predicate='enrichment_failed', claim_tier='meta', extractor_version=content-hash
- Add `countConsecutiveFailures()` method: counts existing tombstones for a symbol
- After 3 consecutive failures, store predicate='enrichment_permanently_failed' instead
- Modify `isCacheHit()` to:
  - Return false for `enrichment_failed` tombstones (retry)
  - Return true for `enrichment_permanently_failed` tombstones ONLY if content hash matches (skip until source changes)
- On successful enrichment, delete any existing tombstones for the symbol
- On enrichment failure (all predicates fail), insert tombstone

## Step 3: Batch size cap
- Add `maxBatchSize = 50` constant
- After symbol selection/filtering, cap the slice to 50 symbols
- Apply before the enrichment loop, after MaxSymbols cap

## Step 4: --initial and --confirm CLI flags
- Add `enrichInitial` and `enrichConfirm` bool flags
- When --initial is set, override Budget=0 and MaxSymbols=0
- When --initial without --confirm: count symbols, estimate calls (symbols * len(defaultPredicates)), estimate cost ($0.003 per call assumption), print info, exit
- When --initial with --confirm: proceed with enrichment

## Step 5: Tests
- TestSymbolIDs_FilterExact: insert 3 symbols, pass 2 IDs, verify only those 2 are enriched
- TestTombstone_CreationAndRetry: mock router to fail, verify tombstone created, run again verify retry
- TestTombstone_ReplacementOnSuccess: create tombstone, then succeed, verify tombstone removed
- TestTombstone_PermanentAfterThreeFailures: fail 3 times, verify permanently_failed
- TestTombstone_PermanentSkippedUntilSourceChange: permanently_failed is cache hit; change hash, verify retry
- TestBatchSizeCap: insert 60 symbols, verify only 50 enriched
