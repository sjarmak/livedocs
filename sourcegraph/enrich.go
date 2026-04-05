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

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
)

// enrichExtractorName is the extractor identifier stored in claims produced
// by the enrichment pipeline.
const enrichExtractorName = "sourcegraph-deepsearch"

// enrichExtractorVersion is the version of the enrichment pipeline.
const enrichExtractorVersion = "0.1.0"

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

// EnrichOpts controls the enrichment run.
type EnrichOpts struct {
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

	// 1. Select symbols.
	symbols, err := e.selectSymbols(opts.IncludeInternal)
	if err != nil {
		return summary, fmt.Errorf("select symbols: %w", err)
	}

	// 2. Rank by reverse-dep fan-in.
	symbols = e.rankByReverseDeps(symbols)

	// 3. Apply MaxSymbols cap.
	if opts.MaxSymbols > 0 && len(symbols) > opts.MaxSymbols {
		symbols = symbols[:opts.MaxSymbols]
	}

	// 4. Dry-run: return symbol count and estimated cost without calling router.
	if opts.DryRun {
		summary.SymbolsSkipped = len(symbols)
		summary.ElapsedTime = time.Since(start)
		return summary, nil
	}

	// 5. Enrich each symbol.
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
			summary.SymbolsEnriched++
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
func isCacheHit(claims []db.Claim, sourceFile, contentHash string) bool {
	if contentHash == "" {
		return false
	}
	expectedVersion := enrichExtractorVersion + "@" + contentHash
	for _, cl := range claims {
		if cl.ClaimTier == "semantic" && cl.Extractor == enrichExtractorName {
			if cl.ExtractorVersion == expectedVersion {
				return true
			}
		}
	}
	return false
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
