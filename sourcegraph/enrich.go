// Package sourcegraph — enrich.go implements the batch enrichment pipeline
// that selects symbols, prioritizes by reverse-dep fan-in, checks content-hash
// cache, calls the predicate router, feeds context into an LLM, and stores
// resulting semantic claims.
package sourcegraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
)

// enrichExtractorName is the extractor identifier stored in claims produced
// by the enrichment pipeline.
const enrichExtractorName = "sourcegraph-deepsearch"

// enrichExtractorVersion is the version of the enrichment pipeline.
const enrichExtractorVersion = "0.1.0"

// maxBatchSize caps the number of symbols enriched in a single Run call.
// Excess symbols are deferred to subsequent runs.
const maxBatchSize = 50

// maxConsecutiveFailures is the threshold after which a symbol's tombstone
// is escalated to permanently failed.
const maxConsecutiveFailures = 3

// Tombstone predicates for enrichment failures.
const (
	predicateEnrichmentFailed            = "enrichment_failed"
	predicateEnrichmentPermanentlyFailed = "enrichment_permanently_failed"
)

// Confidence levels based on whether the symbol appeared in the LLM prose.
const (
	confidenceSymbolInProse = 0.8
	confidenceSymbolAbsent  = 0.4
)

// defaultKinds are the symbol kinds selected for enrichment by default.
var defaultKinds = []extractor.SymbolKind{
	extractor.KindType,
	extractor.KindFunc,
	extractor.KindInterface,
	extractor.KindMethod,
}

// defaultPredicates are the semantic predicates enriched by default.
var defaultPredicates = []extractor.Predicate{
	extractor.PredicatePurpose,
	extractor.PredicateUsagePattern,
	extractor.PredicateComplexity,
	extractor.PredicateStability,
}

// DefaultPredicates returns the default set of semantic predicates used
// for enrichment. Exported for CLI cost estimation.
func DefaultPredicates() []extractor.Predicate {
	return defaultPredicates
}

// EnrichOpts controls the enrichment run.
type EnrichOpts struct {
	// SymbolIDs restricts enrichment to specific symbol IDs. When non-empty,
	// Run() enriches only these symbols, skipping selectSymbols and
	// rankByReverseDeps. When empty, behavior is unchanged.
	SymbolIDs []int64
	// IncludeInternal adds internal-visibility symbols to the selection.
	IncludeInternal bool
	// Force overrides the content-hash cache and re-enriches all symbols.
	Force bool
	// Budget is the maximum number of router calls. Zero means unlimited.
	Budget int
	// MaxSymbols caps how many symbols are selected for enrichment. Zero means unlimited.
	MaxSymbols int
	// DryRun skips router calls and returns only the symbol list and estimated cost.
	DryRun bool
	// Predicates restricts which semantic predicates to enrich.
	// Empty means all default predicates.
	Predicates []extractor.Predicate
}

// EnrichmentSummary holds telemetry from an enrichment run.
type EnrichmentSummary struct {
	SymbolsEnriched int           `json:"symbols_enriched"`
	SymbolsSkipped  int           `json:"symbols_skipped"`
	CallsMade       int           `json:"calls_made"`
	ElapsedTime     time.Duration `json:"elapsed_time"`
}

// Enricher is the core enrichment pipeline. It wires a ClaimsDB, a
// PredicateRouter, and an LLMClient together to produce semantic claims.
type Enricher struct {
	db     *db.ClaimsDB
	router PredicateRouter
}

// NewEnricher creates an Enricher with the given dependencies.
func NewEnricher(claimsDB *db.ClaimsDB, router PredicateRouter) (*Enricher, error) {
	if claimsDB == nil {
		return nil, fmt.Errorf("sourcegraph: claimsDB is required")
	}
	if router == nil {
		return nil, fmt.Errorf("sourcegraph: router is required")
	}
	return &Enricher{db: claimsDB, router: router}, nil
}

// Run executes the enrichment pipeline and returns a summary.
func (e *Enricher) Run(ctx context.Context, opts EnrichOpts) (EnrichmentSummary, error) {
	start := time.Now()
	var summary EnrichmentSummary

	predicates := opts.Predicates
	if len(predicates) == 0 {
		predicates = defaultPredicates
	}

	var symbols []db.Symbol
	var err error

	if len(opts.SymbolIDs) > 0 {
		// Direct symbol ID lookup — skip selectSymbols and rankByReverseDeps.
		symbols, err = e.fetchSymbolsByIDs(opts.SymbolIDs)
		if err != nil {
			return summary, fmt.Errorf("fetch symbols by IDs: %w", err)
		}
	} else {
		// 1. Select symbols.
		symbols, err = e.selectSymbols(opts.IncludeInternal)
		if err != nil {
			return summary, fmt.Errorf("select symbols: %w", err)
		}

		// 2. Rank by reverse-dep fan-in.
		symbols = e.rankByReverseDeps(symbols)

		// 3. Apply MaxSymbols cap.
		if opts.MaxSymbols > 0 && len(symbols) > opts.MaxSymbols {
			symbols = symbols[:opts.MaxSymbols]
		}
	}

	// 4. Apply batch size cap.
	if len(symbols) > maxBatchSize {
		symbols = symbols[:maxBatchSize]
	}

	// 5. Dry-run: return symbol count and estimated cost without calling router.
	if opts.DryRun {
		summary.SymbolsSkipped = len(symbols)
		summary.ElapsedTime = time.Since(start)
		return summary, nil
	}

	// 6. Enrich each symbol.
	for _, sym := range symbols {
		select {
		case <-ctx.Done():
			summary.ElapsedTime = time.Since(start)
			return summary, ctx.Err()
		default:
		}

		// Budget check.
		if opts.Budget > 0 && summary.CallsMade >= opts.Budget {
			break
		}

		// Pre-fetch claims and source file metadata once per symbol.
		existingClaims, _ := e.db.GetClaimsBySubject(sym.ID)
		sourceFile, contentHash := resolveSourceMeta(e.db, sym.Repo, existingClaims)

		// Cache check.
		if !opts.Force && isCacheHit(existingClaims, sourceFile, contentHash) {
			summary.SymbolsSkipped++
			continue
		}

		enriched := false
		allFailed := true
		for _, pred := range predicates {
			// Budget check before each call.
			if opts.Budget > 0 && summary.CallsMade >= opts.Budget {
				break
			}

			symCtx := SymbolContext{
				Name:       sym.SymbolName,
				Repo:       sym.Repo,
				ImportPath: sym.ImportPath,
			}
			contextText, err := e.router.Route(ctx, pred, symCtx)
			summary.CallsMade++
			if err != nil {
				continue
			}

			// At least one call succeeded (no error), so not all failed.
			allFailed = false

			confidence := confidenceSymbolInProse
			if contextText == LowConfidenceSentinel ||
				!strings.Contains(strings.ToLower(contextText), strings.ToLower(sym.SymbolName)) {
				confidence = confidenceSymbolAbsent
			}

			if contextText == LowConfidenceSentinel {
				continue
			}

			claimText := extractClaimFromContext(contextText)
			if claimText == "" {
				continue
			}

			if err := e.storeClaimWithMeta(sym, pred, claimText, confidence, sourceFile, contentHash); err != nil {
				continue
			}
			enriched = true
		}

		if enriched {
			// Success: remove any existing tombstones for this symbol.
			e.removeTombstones(sym.ID)
			summary.SymbolsEnriched++
		} else if allFailed {
			// All router calls failed: insert or escalate tombstone.
			e.insertOrEscalateTombstone(sym, sourceFile, contentHash)
		}
	}

	summary.ElapsedTime = time.Since(start)
	return summary, nil
}

// SelectSymbols returns the candidate symbols for enrichment, ranked by
// reverse-dep fan-in and capped at maxSymbols (0 means unlimited). This is
// exported so the CLI dry-run can list symbols without calling the router.
func (e *Enricher) SelectSymbols(includeInternal bool, maxSymbols int) ([]db.Symbol, error) {
	symbols, err := e.selectSymbols(includeInternal)
	if err != nil {
		return nil, err
	}
	symbols = e.rankByReverseDeps(symbols)
	if maxSymbols > 0 && len(symbols) > maxSymbols {
		symbols = symbols[:maxSymbols]
	}
	return symbols, nil
}

// fetchSymbolsByIDs queries the DB for symbols matching the given IDs.
func (e *Enricher) fetchSymbolsByIDs(ids []int64) ([]db.Symbol, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT id, repo, import_path, symbol_name, language, kind, visibility,
		       COALESCE(display_name, ''), COALESCE(scip_symbol, '')
		FROM symbols
		WHERE id IN (%s)
	`, strings.Join(placeholders, ","))

	rows, err := e.db.DB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query symbols by IDs: %w", err)
	}
	defer rows.Close()

	var symbols []db.Symbol
	for rows.Next() {
		var s db.Symbol
		if err := rows.Scan(&s.ID, &s.Repo, &s.ImportPath, &s.SymbolName,
			&s.Language, &s.Kind, &s.Visibility, &s.DisplayName, &s.SCIPSymbol); err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		symbols = append(symbols, s)
	}
	return symbols, rows.Err()
}

// selectSymbols queries the DB for symbols matching the default kinds and
// visibility filters.
func (e *Enricher) selectSymbols(includeInternal bool) ([]db.Symbol, error) {
	// Build kind placeholders.
	kindStrings := make([]interface{}, len(defaultKinds))
	kindPlaceholders := make([]string, len(defaultKinds))
	for i, k := range defaultKinds {
		kindStrings[i] = string(k)
		kindPlaceholders[i] = "?"
	}

	// Build visibility filter.
	visStrings := []interface{}{string(extractor.VisibilityPublic)}
	visPlaceholders := []string{"?"}
	if includeInternal {
		visStrings = append(visStrings, string(extractor.VisibilityInternal))
		visPlaceholders = append(visPlaceholders, "?")
	}

	query := fmt.Sprintf(`
		SELECT id, repo, import_path, symbol_name, language, kind, visibility,
		       COALESCE(display_name, ''), COALESCE(scip_symbol, '')
		FROM symbols
		WHERE kind IN (%s) AND visibility IN (%s)
		ORDER BY symbol_name
	`, strings.Join(kindPlaceholders, ","), strings.Join(visPlaceholders, ","))

	args := append(kindStrings, visStrings...)
	rows, err := e.db.DB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query symbols: %w", err)
	}
	defer rows.Close()

	var symbols []db.Symbol
	for rows.Next() {
		var s db.Symbol
		if err := rows.Scan(&s.ID, &s.Repo, &s.ImportPath, &s.SymbolName,
			&s.Language, &s.Kind, &s.Visibility, &s.DisplayName, &s.SCIPSymbol); err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		symbols = append(symbols, s)
	}
	return symbols, rows.Err()
}

// packageRank holds a package's import path and its reverse-dep count.
type packageRank struct {
	importPath    string
	reverseDepCnt int
}

// rankByReverseDeps sorts symbols so that symbols belonging to packages with
// the most reverse dependencies come first. Reverse-dep count is approximated
// by counting "imports" claims whose object_text matches the package import path.
func (e *Enricher) rankByReverseDeps(symbols []db.Symbol) []db.Symbol {
	if len(symbols) == 0 {
		return symbols
	}

	// Collect distinct import paths.
	pathSet := make(map[string]bool)
	for _, s := range symbols {
		pathSet[s.ImportPath] = true
	}

	// Count reverse deps for each import path.
	pathRank := make(map[string]int)
	importClaims, err := e.db.GetClaimsByPredicate(string(extractor.PredicateImports))
	if err == nil {
		for _, cl := range importClaims {
			if pathSet[cl.ObjectText] {
				pathRank[cl.ObjectText]++
			}
		}
	}

	// Sort symbols: highest reverse-dep count first, then alphabetical.
	sort.SliceStable(symbols, func(i, j int) bool {
		ri := pathRank[symbols[i].ImportPath]
		rj := pathRank[symbols[j].ImportPath]
		if ri != rj {
			return ri > rj
		}
		return symbols[i].ImportPath < symbols[j].ImportPath
	})

	return symbols
}

// resolveSourceMeta finds the source file path and content hash for a symbol
// by scanning its existing claims. Called once per symbol to avoid repeated
// DB lookups.
func resolveSourceMeta(cdb *db.ClaimsDB, repo string, claims []db.Claim) (sourceFile, contentHash string) {
	for _, cl := range claims {
		if cl.SourceFile != "" {
			sourceFile = cl.SourceFile
			break
		}
	}
	if sourceFile == "" {
		return "", ""
	}
	sf, err := cdb.GetSourceFile(repo, sourceFile)
	if err != nil {
		return sourceFile, ""
	}
	return sourceFile, sf.ContentHash
}

// isCacheHit checks whether a symbol already has semantic claims from our
// extractor whose source file content hash has not changed since enrichment.
// Tombstones with predicate 'enrichment_failed' are treated as cache misses
// (retry on next run). Tombstones with predicate 'enrichment_permanently_failed'
// are treated as cache hits only when the content hash matches (skip until
// source changes).
func isCacheHit(claims []db.Claim, sourceFile, contentHash string) bool {
	if contentHash == "" {
		return false
	}
	expectedVersion := enrichExtractorVersion + "@" + contentHash
	hasPermanentTombstone := false
	for _, cl := range claims {
		if cl.Extractor == enrichExtractorName {
			// Enrichment-failed tombstones are always cache misses (retry).
			if cl.Predicate == predicateEnrichmentFailed {
				continue
			}
			// Permanently-failed tombstones are cache hits only if hash matches.
			if cl.Predicate == predicateEnrichmentPermanentlyFailed {
				if cl.ExtractorVersion == expectedVersion {
					hasPermanentTombstone = true
				}
				continue
			}
			// Normal semantic claims with matching version are cache hits.
			if cl.ClaimTier == "semantic" && cl.ExtractorVersion == expectedVersion {
				return true
			}
		}
	}
	return hasPermanentTombstone
}

// insertOrEscalateTombstone inserts a failure tombstone for the symbol.
// If the symbol already has maxConsecutiveFailures-1 existing failed tombstones,
// the tombstone is escalated to permanently failed.
func (e *Enricher) insertOrEscalateTombstone(sym db.Symbol, sourceFile, contentHash string) {
	failCount := e.countConsecutiveFailures(sym.ID)

	predicate := predicateEnrichmentFailed
	if failCount+1 >= maxConsecutiveFailures {
		predicate = predicateEnrichmentPermanentlyFailed
		// Remove prior non-permanent tombstones since we're escalating.
		e.removeTombstonesByPredicate(sym.ID, predicateEnrichmentFailed)
	}

	extractorVersion := enrichExtractorVersion
	if contentHash != "" {
		extractorVersion = enrichExtractorVersion + "@" + contentHash
	}

	_, _ = e.db.InsertClaim(db.Claim{
		SubjectID:        sym.ID,
		Predicate:        predicate,
		ObjectText:       fmt.Sprintf("enrichment failed (attempt %d)", failCount+1),
		SourceFile:       sourceFile,
		Confidence:       0,
		ClaimTier:        "meta",
		Extractor:        enrichExtractorName,
		ExtractorVersion: extractorVersion,
		LastVerified:     db.Now(),
	})
}

// countConsecutiveFailures counts existing enrichment_failed tombstones for a symbol.
func (e *Enricher) countConsecutiveFailures(symbolID int64) int {
	claims, err := e.db.GetClaimsBySubject(symbolID)
	if err != nil {
		return 0
	}
	count := 0
	for _, cl := range claims {
		if cl.Extractor == enrichExtractorName && cl.Predicate == predicateEnrichmentFailed {
			count++
		}
	}
	return count
}

// removeTombstones removes all enrichment tombstone claims for a symbol.
func (e *Enricher) removeTombstones(symbolID int64) {
	e.removeTombstonesByPredicate(symbolID, predicateEnrichmentFailed)
	e.removeTombstonesByPredicate(symbolID, predicateEnrichmentPermanentlyFailed)
}

// removeTombstonesByPredicate removes tombstone claims for a symbol with a specific predicate.
func (e *Enricher) removeTombstonesByPredicate(symbolID int64, predicate string) {
	_, _ = e.db.DB().Exec(
		"DELETE FROM claims WHERE subject_id = ? AND extractor = ? AND predicate = ?",
		symbolID, enrichExtractorName, predicate,
	)
}

// storeClaimWithMeta persists a single semantic claim, using pre-resolved
// source file metadata to avoid redundant DB lookups.
func (e *Enricher) storeClaimWithMeta(sym db.Symbol, pred extractor.Predicate, text string, confidence float64, sourceFile, contentHash string) error {
	extractorVersion := enrichExtractorVersion
	if contentHash != "" {
		extractorVersion = enrichExtractorVersion + "@" + contentHash
	}

	_, err := e.db.InsertClaim(db.Claim{
		SubjectID:        sym.ID,
		Predicate:        string(pred),
		ObjectText:       text,
		SourceFile:       sourceFile,
		Confidence:       confidence,
		ClaimTier:        string(extractor.TierSemantic),
		Extractor:        enrichExtractorName,
		ExtractorVersion: extractorVersion,
		LastVerified:     db.Now(),
	})
	if err != nil {
		return fmt.Errorf("insert enrichment claim for %s/%s: %w", sym.SymbolName, pred, err)
	}
	return nil
}

// extractClaimFromContext extracts a claim from the router's context text.
// Returns empty string if the context is empty or "null".
func extractClaimFromContext(contextText string) string {
	text := strings.TrimSpace(contextText)
	if text == "" || strings.EqualFold(text, "null") {
		return ""
	}
	return text
}
