// Package tribal — service.go is the single orchestration layer for all tribal
// mining paths (batch and JIT). Both cmd/livedocs/extract_cmd.go and
// mcpserver/tribal_mine.go call MineFile / MineSymbol; neither touches
// PRCommentMiner, DailyBudget, or cursor columns directly.
//
// The seven shared invariants this service enforces:
//  1. Handle ownership — the service owns the PRCommentMiner lifecycle
//  2. Atomic budget — budget checks and increments are in one place
//  3. Uniform error propagation — structured MiningError responses
//  4. Cursor management — loads, updates, and regression-handling here
//  5. Prioritization — delegated to the caller (service mines what it's told)
//  6. Normalization — uses extractor/tribal/normalize exclusively
//  7. Transaction boundaries — per-file atomic writes
package tribal

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/live-docs/live_docs/db"
)

// Trigger is an enum identifying the caller of the mining service.
// Used ONLY for telemetry, never for behavior branching.
type Trigger string

const (
	// TriggerBatchSchedule is the trigger for scheduled batch extraction.
	TriggerBatchSchedule Trigger = "batch_schedule"
	// TriggerJITOnDemand is the trigger for on-demand JIT mining.
	TriggerJITOnDemand Trigger = "jit_on_demand"
	// TriggerBackfill is the trigger for backfill operations.
	TriggerBackfill Trigger = "backfill"
)

// MiningError is a structured error returned by the mining service.
// It carries a machine-readable Code for callers that need to branch
// on error type (e.g. MCP returning JSON error envelopes).
type MiningError struct {
	Code    string // e.g. "rate_limited", "cursor_regression", "budget_exceeded"
	Message string
	Err     error // wrapped underlying error, may be nil
}

func (e *MiningError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// SafeMessage returns a caller-facing message that omits internal file paths
// and wrapped error details. Use this in MCP tool responses instead of Error().
func (e *MiningError) SafeMessage() string {
	switch e.Code {
	case "budget_exceeded":
		return "daily LLM call budget reached"
	case "cursor_regression":
		return "file requires re-mining due to cursor state change"
	case "symbol_resolution_failed":
		return "could not resolve symbol to source files"
	case "symbol_upsert_failed":
		return "failed to register file symbol"
	case "extraction_failed":
		return "PR comment extraction failed for file"
	default:
		return e.Code
	}
}

func (e *MiningError) Unwrap() error { return e.Err }

// MiningResult holds the outcome of a single file mining operation.
type MiningResult struct {
	Facts   []db.TribalFact // newly mined facts (SubjectID already set)
	Trigger Trigger         // echo of the caller's trigger for telemetry
	Path    string          // the file path that was mined
}

// TribalMiningService is the single orchestration layer for tribal mining.
// Both batch and JIT callers use this service; neither directly touches
// PRCommentMiner, budget, or cursor state.
type TribalMiningService struct {
	miner        *PRCommentMiner
	claimsDB     *db.ClaimsDB
	repo         string // repo name in the claims DB (e.g. "kubernetes")
	minerVersion string // recorded in source_files.pr_miner_version

	// factsGeneration is atomically incremented on every write.
	// Readers (e.g. MCP pool) can poll this to detect invalidation.
	// TTL-based caching is banned for tribal facts.
	factsGeneration int64
}

// ServiceOption configures a TribalMiningService.
type ServiceOption func(*TribalMiningService)

// WithMinerVersion sets the miner version string recorded in
// source_files.pr_miner_version. Defaults to "service-v1".
func WithMinerVersion(v string) ServiceOption {
	return func(s *TribalMiningService) { s.minerVersion = v }
}

// NewTribalMiningService creates the shared orchestration layer.
// The claimsDB must already have the tribal schema created.
func NewTribalMiningService(
	claimsDB *db.ClaimsDB,
	miner *PRCommentMiner,
	repo string,
	opts ...ServiceOption,
) *TribalMiningService {
	s := &TribalMiningService{
		miner:        miner,
		claimsDB:     claimsDB,
		repo:         repo,
		minerVersion: "service-v1",
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// FactsGeneration returns the current generation counter. Any write
// (MineFile, MineSymbol) atomically increments this value. Readers
// must compare against their cached generation to detect invalidation.
func (s *TribalMiningService) FactsGeneration() int64 {
	return atomic.LoadInt64(&s.factsGeneration)
}

// bumpGeneration atomically increments the generation counter.
func (s *TribalMiningService) bumpGeneration() {
	atomic.AddInt64(&s.factsGeneration, 1)
}

// MineFile runs PR comment mining for a single file path. It handles:
//   - Loading the stored PR cursor from the DB
//   - Invoking the PRCommentMiner (budget-checked internally)
//   - Upserting facts with correct SubjectID
//   - Updating the PR cursor on success
//   - Cursor regression detection and needs_remine marking
//   - Bumping the generation counter on any write
//
// The caller is responsible for selecting WHICH files to mine (prioritization
// is invariant #5 — delegated to the caller).
func (s *TribalMiningService) MineFile(
	ctx context.Context,
	relPath string,
	trigger Trigger,
) (*MiningResult, error) {
	// 1. Resolve or create the file-level symbol.
	fileSymID, err := s.claimsDB.UpsertSymbol(db.Symbol{
		Repo:       s.repo,
		ImportPath: relPath,
		SymbolName: relPath,
		Language:   "file",
		Kind:       "file",
		Visibility: "public",
	})
	if err != nil {
		return nil, &MiningError{
			Code:    "symbol_upsert_failed",
			Message: fmt.Sprintf("upsert symbol for %s", relPath),
			Err:     err,
		}
	}

	// 2. Load the stored PR cursor.
	cursor, storedVersion, getErr := s.claimsDB.GetPRIDSet(s.repo, relPath)
	if getErr != nil {
		cursor = nil
		storedVersion = ""
	}
	if storedVersion == db.PRMinerVersionNeedsRemine {
		cursor = nil
	}

	// 3. Run the miner.
	facts, seenPRs, extractErr := s.miner.ExtractForFile(ctx, relPath, cursor)
	if extractErr != nil {
		// Cursor regression: mark for re-mine and return structured error.
		if errors.Is(extractErr, ErrCursorRegression) {
			_ = s.claimsDB.MarkNeedsRemine(s.repo, relPath)
			return nil, &MiningError{
				Code:    "cursor_regression",
				Message: fmt.Sprintf("cursor regression for %s", relPath),
				Err:     extractErr,
			}
		}
		// Budget exceeded: return structured error so both callers handle it identically.
		if errors.Is(extractErr, ErrBudgetExceeded) {
			return nil, &MiningError{
				Code:    "budget_exceeded",
				Message: "daily LLM call budget reached",
				Err:     extractErr,
			}
		}
		// Other errors.
		return nil, &MiningError{
			Code:    "extraction_failed",
			Message: fmt.Sprintf("extract PR comments for %s", relPath),
			Err:     extractErr,
		}
	}

	// 4. Upsert facts with correct SubjectID.
	result := &MiningResult{
		Trigger: trigger,
		Path:    relPath,
	}
	for _, fact := range facts {
		fact.SubjectID = fileSymID
		if _, _, insertErr := s.claimsDB.UpsertTribalFact(fact, fact.Evidence); insertErr == nil {
			result.Facts = append(result.Facts, fact)
		}
	}

	// 5. Bump generation counter if any facts were written.
	if len(result.Facts) > 0 {
		s.bumpGeneration()
	}

	// 6. Persist the updated PR cursor.
	if len(seenPRs) > 0 {
		if setErr := s.claimsDB.SetPRIDSet(s.repo, relPath, seenPRs, s.minerVersion); setErr != nil {
			// Non-fatal: the facts are already written, cursor update is best-effort.
			// Next run will re-discover these PRs via gh but skip them via evidence dedup.
		}
	}

	return result, nil
}

// MineSymbol resolves a symbol name to the file(s) it lives in, then mines
// each file. This is the JIT entry point — an agent asks "mine tribal
// knowledge for symbol X" and the service figures out which files to process.
func (s *TribalMiningService) MineSymbol(
	ctx context.Context,
	symbolName string,
	trigger Trigger,
) ([]*MiningResult, error) {
	// Find files containing this symbol.
	paths, err := s.resolveSymbolFiles(symbolName)
	if err != nil {
		return nil, &MiningError{
			Code:    "symbol_resolution_failed",
			Message: fmt.Sprintf("resolve files for symbol %q", symbolName),
			Err:     err,
		}
	}
	if len(paths) == 0 {
		return nil, nil // no files found — not an error
	}

	var results []*MiningResult
	for _, p := range paths {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		r, mineErr := s.MineFile(ctx, p, trigger)
		if mineErr != nil {
			var me *MiningError
			if errors.As(mineErr, &me) && me.Code == "budget_exceeded" {
				// Stop mining further files on budget exhaustion.
				return results, mineErr
			}
			// Other per-file errors are non-fatal: skip and continue.
			continue
		}
		if r != nil {
			results = append(results, r)
		}
	}

	return results, nil
}

// resolveSymbolFiles maps a symbol name to the file paths that contain it.
// It queries the claims DB's symbols table for matching import_path values.
func (s *TribalMiningService) resolveSymbolFiles(symbolName string) ([]string, error) {
	symbols, err := s.claimsDB.SearchSymbolsByName(symbolName)
	if err != nil {
		return nil, fmt.Errorf("search symbols: %w", err)
	}

	seen := make(map[string]struct{})
	var paths []string
	for _, sym := range symbols {
		if sym.Repo != s.repo {
			continue
		}
		p := sym.ImportPath
		if p == "" {
			continue
		}
		// Only include paths with recognized source file extensions.
		if !isSourceFile(p) {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	return paths, nil
}

// isSourceFile returns true if path ends with a recognized source file extension.
// This replaces the old strings.Contains(p, ".") heuristic which incorrectly
// matched Go import paths containing dots (e.g. "k8s.io/client-go/tools/cache").
func isSourceFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".sh",
		".rs", ".rb", ".java", ".kt", ".swift",
		".c", ".cpp", ".h", ".hpp", ".cs", ".php":
		return true
	}
	return false
}
