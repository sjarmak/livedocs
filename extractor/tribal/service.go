// Package tribal — service.go is the single orchestration layer for all tribal
// mining paths (batch and JIT). Both cmd/livedocs/extract_cmd.go and
// mcpserver/tribal_mine.go call MineFile / MineSymbol; neither touches
// the PR comment miner, DailyBudget, or cursor columns directly.
//
// The seven shared invariants this service enforces:
//  1. Handle ownership — the service owns the PR comment miner lifecycle
//     (the underlying miner type is unexported so external callers cannot
//     bypass the service and double-spend the daily budget)
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
	"log"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/sjarmak/livedocs/db"
	"golang.org/x/sync/singleflight"
)

// ErrMineThrottled is returned (wrapped in a MiningError with
// Code="mine_throttled") when MineFile's optional per-key rate limiter
// denies the request. It is deliberately distinct from ErrBudgetExceeded:
// the former signals a short-window throttle that callers may retry after
// backoff, while the latter signals a daily cap that will not clear until
// the budget window rolls over. Distinguishing them prevents callers from
// treating a transient rate-limit denial as a day-long outage.
var ErrMineThrottled = errors.New("mine throttled")

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
	case "mine_throttled":
		return "per-file mining rate limit reached; retry shortly"
	default:
		return e.Code
	}
}

func (e *MiningError) Unwrap() error { return e.Err }

// maxFailedErrorsCaptured caps the number of per-fact upsert errors retained
// on MiningResult.FailedErrors. FailedCount still reflects the true total so
// observability is preserved on pathological files; the slice is bounded to
// avoid unbounded growth (one error object per failure can accumulate heap
// pressure on very large, fully-failing files).
const maxFailedErrorsCaptured = 32

// MiningResult holds the outcome of a single file mining operation.
//
// Partial-failure semantics: MineFile returns a non-nil MiningResult and a
// nil error even when some UpsertTribalFact calls fail. Callers that need
// to detect partial failure MUST inspect FailedCount (> 0 signals at least
// one upsert failed). FailedErrors exposes up to maxFailedErrorsCaptured
// sanitized category strings for display or metrics; FailedCount is the
// authoritative total. This is additive to the original Facts/Trigger/Path
// surface so existing callers do not break.
//
// Sanitization contract (live_docs-m7v.21):
// Every entry in FailedErrors is produced by sanitizeUpsertError and is one
// of a fixed set of canonical category strings (e.g. "unique_constraint_
// violation", "database_error"). Raw error text — which may include SQLite
// schema details, offending row values, LLM-generated content, file paths,
// or symbol names — never reaches this field. Operators who need the raw
// error for debugging read the server-side log line emitted at the capture
// site in MineFile; callers should treat each entry as an opaque category
// tag.
type MiningResult struct {
	Facts        []db.TribalFact // newly mined facts (SubjectID already set)
	Trigger      Trigger         // echo of the caller's trigger for telemetry
	Path         string          // the file path that was mined
	FailedCount  int             // total UpsertTribalFact failures (uncapped)
	FailedErrors []string        // sanitized category strings, capped at maxFailedErrorsCaptured
}

// factUpserter is the narrow interface MineFile needs from the claims DB
// for fact persistence. Narrowing the dependency here serves two purposes:
// (1) it documents the exact side effect MineFile performs on the DB, and
// (2) it enables tests to inject failure paths without spinning up a
// broken sqlite connection. Production code uses *db.ClaimsDB directly
// via claimsDBUpserter; MineFile never references this interface
// outside of the injected field.
type factUpserter interface {
	UpsertTribalFact(fact db.TribalFact, evidence []db.TribalEvidence) (int64, bool, error)
}

// claimsDBUpserter adapts *db.ClaimsDB to the factUpserter interface.
// The adapter keeps the production call path identical to the previous
// direct method call — only the dispatch layer is abstracted.
type claimsDBUpserter struct {
	cdb *db.ClaimsDB
}

func (a *claimsDBUpserter) UpsertTribalFact(
	fact db.TribalFact, evidence []db.TribalEvidence,
) (int64, bool, error) {
	return a.cdb.UpsertTribalFact(fact, evidence)
}

// TribalMiningService is the single orchestration layer for tribal mining.
// Both batch and JIT callers use this service; neither directly touches the
// underlying PR comment miner, budget, or cursor state.
type TribalMiningService struct {
	miner        *prCommentMiner
	claimsDB     *db.ClaimsDB
	upserter     factUpserter // fact persistence seam; defaults to claimsDB
	repo         string       // repo name in the claims DB (e.g. "kubernetes")
	minerVersion string       // recorded in source_files.pr_miner_version

	// factsGeneration is atomically incremented on every write.
	// Readers (e.g. MCP pool) can poll this to detect invalidation.
	// TTL-based caching is banned for tribal facts.
	factsGeneration int64

	// mineFileGroup dedups concurrent MineFile calls for the same relPath.
	// Without it, N goroutines asking for the same file each load the cursor,
	// each call ExtractForFile, each upsert — charging the DailyBudget N times
	// for a single unit of work (live_docs-m7v.17). Waiters receive the
	// first caller's (result, err) pair; see the MineFile doc-comment for
	// the shared-result-read-only contract.
	mineFileGroup singleflight.Group

	// mineLimiter optionally bounds the rate at which DISTINCT relPaths may
	// enter the singleflight group, to prevent an adversarial caller from
	// enumerating synthetic relPaths and bloating the internal singleflight
	// map beyond reason. Nil means unbounded (backward compatible). Callers
	// wire one with WithMineLimiter. See extractor/tribal/limiter.go for
	// the LRU-bounded token-bucket implementation.
	mineLimiter *KeyedLimiter
}

// PRMinerConfig configures the PR comment miner that the service owns.
// External callers construct a TribalMiningService by passing this config;
// the miner itself is unexported so it cannot be instantiated outside this
// package and therefore cannot bypass the service's cursor/budget/generation
// bookkeeping.
type PRMinerConfig struct {
	// RepoOwner is the GitHub repository owner (e.g. "kubernetes").
	RepoOwner string
	// RepoName is the GitHub repository name (e.g. "kubernetes").
	RepoName string
	// Client is the LLM client used for comment classification.
	Client LLMClient
	// Model is the model identifier stored in fact provenance.
	Model string
	// DailyBudget is the maximum number of LLM calls per day. Zero means unlimited.
	DailyBudget int
	// RunCommand is the command runner. If nil, defaultCommandRunner is used.
	RunCommand CommandRunner
}

// ServiceOption configures a TribalMiningService.
type ServiceOption func(*TribalMiningService)

// WithMinerVersion sets the miner version string recorded in
// source_files.pr_miner_version. Defaults to "service-v1".
func WithMinerVersion(v string) ServiceOption {
	return func(s *TribalMiningService) { s.minerVersion = v }
}

// withFactUpserter overrides the fact persistence layer. Unexported because
// it is a test seam; production callers always use the default (claimsDB).
func withFactUpserter(u factUpserter) ServiceOption {
	return func(s *TribalMiningService) { s.upserter = u }
}

// WithMineLimiter installs a per-relPath rate limiter at the MineFile entry.
// When the limiter denies a request, MineFile returns a MiningError with
// Code="mine_throttled" WITHOUT entering singleflight or calling the miner,
// which bounds the singleflight map's key-space against adversarial callers
// enumerating synthetic relPaths.
//
// Passing nil (or omitting the option) disables rate limiting; this is the
// backward-compatible default for in-process CLI callers where the
// singleflight key-space equals the repo file count. Exposed callers that
// accept untrusted relPath values (e.g. MCP handlers) should install one.
//
// The limiter's Allow() method is used (non-blocking); denials surface as a
// synchronous MiningError. Do not share the same KeyedLimiter across
// multiple TribalMiningService instances if you expect distinct DailyBudget
// accounting per instance.
func WithMineLimiter(lim *KeyedLimiter) ServiceOption {
	return func(s *TribalMiningService) { s.mineLimiter = lim }
}

// NewTribalMiningService creates the shared orchestration layer.
// The claimsDB must already have the tribal schema created.
//
// The miner is constructed internally from cfg and is not reachable by
// callers; this is how the service enforces that every LLM call runs through
// its cursor/budget/generation bookkeeping.
func NewTribalMiningService(
	claimsDB *db.ClaimsDB,
	cfg PRMinerConfig,
	repo string,
	opts ...ServiceOption,
) *TribalMiningService {
	return newServiceWithMiner(claimsDB, &prCommentMiner{
		RepoOwner:   cfg.RepoOwner,
		RepoName:    cfg.RepoName,
		Client:      cfg.Client,
		Model:       cfg.Model,
		DailyBudget: cfg.DailyBudget,
		RunCommand:  cfg.RunCommand,
	}, repo, opts...)
}

// newServiceWithMiner is a package-private constructor that accepts an
// already-built *prCommentMiner. It exists so in-package tests can keep a
// handle to the miner for white-box assertions (e.g. preloading callCount
// to exercise the budget-exceeded path). Production code uses
// NewTribalMiningService, which takes the exported PRMinerConfig and builds
// the miner internally — the miner type is unexported so external callers
// cannot reach this helper.
func newServiceWithMiner(
	claimsDB *db.ClaimsDB,
	miner *prCommentMiner,
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
	if s.upserter == nil {
		s.upserter = &claimsDBUpserter{cdb: claimsDB}
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
//   - Invoking the internal PR comment miner (budget-checked internally)
//   - Upserting facts with correct SubjectID
//   - Updating the PR cursor on success
//   - Cursor regression detection and needs_remine marking
//   - Bumping the generation counter on any write
//
// The caller is responsible for selecting WHICH files to mine (prioritization
// is invariant #5 — delegated to the caller).
//
// Concurrent-call semantics (live_docs-m7v.17):
// MineFile uses a singleflight.Group keyed by relPath to dedup concurrent
// requests for the same file. When N goroutines call MineFile(relPath)
// simultaneously, the miner's ExtractForFile runs ONCE and all N callers
// receive the same *MiningResult and error. This is a correctness fix:
// without dedup, every caller would independently load the cursor, fetch PR
// comments, call the LLM, and upsert — charging the DailyBudget N times
// for a single unit of work.
//
// Shared-result read-only contract:
// Because N callers receive a pointer to the SAME *MiningResult, callers
// MUST treat the returned value as read-only. Mutating Facts, FailedErrors,
// or any other field risks data races across goroutines that did not
// synchronize their writes. If a caller needs a mutable copy, it must
// copy-on-write.
//
// Cancellation caveat:
// singleflight.Do runs the shared work in the first caller's context. If
// that context is cancelled while waiters are parked, the shared op is
// cancelled and ALL waiters observe the cancellation error. This is an
// accepted trade-off vs. the alternative (unbounded duplicated budget
// spend). Callers that cannot tolerate first-caller cancellation should
// serialize their calls upstream.
//
// Optional rate limiting:
// If the service was constructed with WithMineLimiter, each MineFile call
// first consults the limiter via Allow(relPath). A denial returns a
// MiningError with Code="mine_throttled" WITHOUT entering singleflight or
// touching the miner, which bounds the singleflight key-space against
// adversarial callers enumerating synthetic relPaths.
func (s *TribalMiningService) MineFile(
	ctx context.Context,
	relPath string,
	trigger Trigger,
) (*MiningResult, error) {
	// Rate-limit gate: check BEFORE entering singleflight so denied requests
	// never register a key in the dedup map. This is the bounded-keyspace
	// invariant that keeps the singleflight map from growing under
	// adversarial enumeration of distinct relPaths.
	if s.mineLimiter != nil && !s.mineLimiter.Allow(relPath) {
		return nil, &MiningError{
			Code: "mine_throttled",
			// relPath is attacker-controllable when the mining service is
			// reachable through an MCP tool. Quote with %q so newlines or
			// control chars in a hostile path cannot inject log lines that
			// render the Message field with %v.
			Message: fmt.Sprintf("mine rate limit reached for %q", relPath),
			Err:     ErrMineThrottled,
		}
	}

	// Dedup concurrent callers for the same relPath: only the first caller
	// runs mineFileOnce; waiters receive the shared (result, err) pair.
	// singleflight.Group auto-removes the key when Do returns, so the map
	// never exceeds the number of in-flight distinct keys. The optional
	// limiter above further bounds that set against hostile callers.
	//
	// The third return value (shared bool) is intentionally discarded:
	// callers currently do not need to distinguish a first-run result from
	// a waiter's shared result. If a future telemetry hook wants to count
	// "deduped N waiters into 1 call," capture it here rather than adding
	// a separate instrumentation path.
	v, err, _ := s.mineFileGroup.Do(relPath, func() (any, error) {
		return s.mineFileOnce(ctx, relPath, trigger)
	})
	if err != nil {
		// err from mineFileOnce is already a *MiningError; return as-is.
		return nil, err
	}
	// v is *MiningResult from mineFileOnce; an untyped-nil is impossible
	// because mineFileOnce always returns (result, nil) or (nil, error).
	result, _ := v.(*MiningResult)
	return result, nil
}

// mineFileOnce is the actual mining implementation. It is invoked at most
// once per in-flight relPath via singleflight. Callers must NOT invoke
// this directly — always go through MineFile so concurrent dedup and
// rate limiting apply uniformly.
func (s *TribalMiningService) mineFileOnce(
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

	// 4. Upsert facts with correct SubjectID. Each failure is recorded on
	//    the result so callers observe partial success (see MiningResult
	//    partial-failure semantics). We never abort on first failure —
	//    the remaining facts may still succeed, and a single poison fact
	//    should not block valid neighbors on the same file.
	result := &MiningResult{
		Trigger: trigger,
		Path:    relPath,
	}
	for _, fact := range facts {
		fact.SubjectID = fileSymID
		_, _, insertErr := s.upserter.UpsertTribalFact(fact, fact.Evidence)
		if insertErr == nil {
			result.Facts = append(result.Facts, fact)
			continue
		}
		result.FailedCount++
		// Sanitization boundary (live_docs-m7v.21): never append the raw
		// error. Map to a canonical category string so callers cannot
		// observe SQLite schema details, offending values, LLM echo, file
		// paths, or symbol names via MiningResult.FailedErrors. The slice
		// is also bounded by maxFailedErrorsCaptured; FailedCount remains
		// uncapped as the authoritative total.
		if len(result.FailedErrors) < maxFailedErrorsCaptured {
			result.FailedErrors = append(result.FailedErrors, sanitizeUpsertError(insertErr))
			// Operator log: emit the raw error server-side so maintainers
			// retain full debuggability. %q defeats log injection via
			// embedded newlines or control characters in wrapped error
			// messages. We cap operator logging at the same threshold as
			// the retained-error slice so a pathological file cannot flood
			// logs; remaining failures are summarized once below via
			// FailedCount. This log line is the operator's authoritative
			// debugging surface — the MiningResult field is caller-facing.
			log.Printf(
				"tribal.MineFile: upsert failure repo=%q path=%q err=%q",
				s.repo, relPath, insertErr.Error(),
			)
		}
	}
	// If we hit the cap, emit a single summary line so operators can see
	// the true failure count without scanning for per-failure log lines.
	if result.FailedCount > maxFailedErrorsCaptured {
		log.Printf(
			"tribal.MineFile: upsert failures exceeded retention cap repo=%q path=%q total_failed=%d logged=%d",
			s.repo, relPath, result.FailedCount, maxFailedErrorsCaptured,
		)
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
// Uses exact-match lookup (not LIKE) because symbolName comes from MCP
// callers — wildcards (%, _) in LIKE patterns would fan out to every symbol
// in the repo and drain the DailyBudget with a single crafted call.
func (s *TribalMiningService) resolveSymbolFiles(symbolName string) ([]string, error) {
	symbols, err := s.claimsDB.GetSymbolsByExactName(symbolName)
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
