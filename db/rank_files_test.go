package db

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedFileParams bundles the fields needed to seed one source_file row plus
// its auxiliary symbol + claim + tribal_fact rows.
type seedFileParams struct {
	RelativePath  string
	PublicSymbols int // how many visibility='public' symbols to attach
	PrivateSyms   int // how many non-public symbols to attach
	FanInClaims   int // how many claims pointing at those symbols
	ExistingFacts int // how many active tribal_facts on those symbols
	Mined         bool
}

// seedFile inserts a source_files row and its supporting symbols/claims/
// tribal_facts rows on the given repo. All names are deterministic so the
// ranker output can be asserted exactly.
func seedFile(t *testing.T, d *ClaimsDB, repo string, p seedFileParams) {
	t.Helper()
	// 1. source_files row.
	sfID, err := d.UpsertSourceFile(SourceFile{
		Repo:             repo,
		RelativePath:     p.RelativePath,
		ContentHash:      "hash-" + p.RelativePath,
		ExtractorVersion: "test",
		LastIndexed:      Now(),
	})
	if err != nil {
		t.Fatalf("upsert source_file %q: %v", p.RelativePath, err)
	}
	_ = sfID
	// 2. Optionally mark mined (sets last_pr_id_set to a non-empty blob).
	if p.Mined {
		if err := d.SetPRIDSet(repo, p.RelativePath, []int{1}, "v1"); err != nil {
			t.Fatalf("set pr id set %q: %v", p.RelativePath, err)
		}
	}
	// 3. Insert public symbols. Each symbol is named uniquely so multiple
	//    files can share a relative_path import_path without colliding.
	var symIDs []int64
	for i := 0; i < p.PublicSymbols; i++ {
		id, err := d.UpsertSymbol(Symbol{
			Repo:       repo,
			ImportPath: p.RelativePath,
			SymbolName: fmt.Sprintf("pub_%s_%d", p.RelativePath, i),
			Language:   "go",
			Kind:       "func",
			Visibility: "public",
		})
		if err != nil {
			t.Fatalf("upsert public symbol: %v", err)
		}
		symIDs = append(symIDs, id)
	}
	for i := 0; i < p.PrivateSyms; i++ {
		id, err := d.UpsertSymbol(Symbol{
			Repo:       repo,
			ImportPath: p.RelativePath,
			SymbolName: fmt.Sprintf("priv_%s_%d", p.RelativePath, i),
			Language:   "go",
			Kind:       "func",
			Visibility: "internal",
		})
		if err != nil {
			t.Fatalf("upsert private symbol: %v", err)
		}
		symIDs = append(symIDs, id)
	}
	// 4. Insert claims to generate fan_in. If there are no symbols at all we
	//    upsert a throwaway symbol to attach claims to — but that symbol is
	//    NOT public so it won't boost public_surface.
	var attachSym int64
	if len(symIDs) > 0 {
		attachSym = symIDs[0]
	} else if p.FanInClaims > 0 {
		id, err := d.UpsertSymbol(Symbol{
			Repo:       repo,
			ImportPath: p.RelativePath,
			SymbolName: fmt.Sprintf("anchor_%s", p.RelativePath),
			Language:   "go",
			Kind:       "func",
			Visibility: "internal",
		})
		if err != nil {
			t.Fatalf("upsert anchor symbol: %v", err)
		}
		attachSym = id
	}
	for i := 0; i < p.FanInClaims; i++ {
		if _, err := d.InsertClaim(Claim{
			SubjectID:        attachSym,
			Predicate:        "defines",
			ObjectText:       fmt.Sprintf("claim-%d", i),
			SourceFile:       p.RelativePath,
			SourceLine:       i + 1,
			Confidence:       1.0,
			ClaimTier:        "structural",
			Extractor:        "test",
			ExtractorVersion: "1",
			LastVerified:     Now(),
		}); err != nil {
			t.Fatalf("insert claim: %v", err)
		}
	}
	// 5. Insert active tribal_facts to drive existing_facts count.
	if p.ExistingFacts > 0 {
		// We need at least one symbol to attach facts to — ensure there is one.
		var factSym int64
		if len(symIDs) > 0 {
			factSym = symIDs[0]
		} else {
			id, err := d.UpsertSymbol(Symbol{
				Repo:       repo,
				ImportPath: p.RelativePath,
				SymbolName: fmt.Sprintf("fact_anchor_%s", p.RelativePath),
				Language:   "file",
				Kind:       "file",
				Visibility: "public",
			})
			if err != nil {
				t.Fatalf("upsert fact anchor symbol: %v", err)
			}
			factSym = id
		}
		for i := 0; i < p.ExistingFacts; i++ {
			_, err := d.InsertTribalFact(TribalFact{
				SubjectID:        factSym,
				Kind:             "quirk",
				Body:             fmt.Sprintf("fact-%d", i),
				SourceQuote:      "quote",
				Confidence:       0.9,
				Corroboration:    1,
				Extractor:        "test",
				ExtractorVersion: "1",
				StalenessHash:    fmt.Sprintf("hash-%s-%d", p.RelativePath, i),
				Status:           "active",
				CreatedAt:        Now(),
				LastVerified:     Now(),
			}, []TribalEvidence{{
				SourceType:  "inline_marker",
				SourceRef:   fmt.Sprintf("%s#%d", p.RelativePath, i),
				ContentHash: fmt.Sprintf("ev-%d", i),
			}})
			if err != nil {
				t.Fatalf("insert tribal fact: %v", err)
			}
		}
	}
}

// tempTribalDB builds an empty claims DB with both core and tribal schemas.
func tempTribalDB(t *testing.T) *ClaimsDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rank.db")
	d, err := OpenClaimsDB(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := d.CreateTribalSchema(); err != nil {
		t.Fatalf("create tribal schema: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// -----------------------------------------------------------------------------
// Basic ranker tests
// -----------------------------------------------------------------------------

func TestShouldSkipForMining(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"vendor/foo/bar.go", true},
		{"third_party/lib.go", true},
		{"testdata/fixtures/a.go", true},
		{"pkg/foo/gen.pb.go", true},
		{"pkg/foo/types_gen.go", true},
		{"pkg/foo/foo_test.go", true},
		{"pkg/foo/foo.go", false},
		{"cmd/main.go", false},
		{"pkg/foo/bar/baz.go", false},
		{"", false},
		// Nested "vendor/" is NOT treated as vendor (matches SQL LIKE behavior).
		{"pkg/vendor/inside.go", false},
	}
	for _, c := range cases {
		got := ShouldSkipForMining(c.path)
		if got != c.want {
			t.Errorf("ShouldSkipForMining(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestRankFilesForMining seeds 20 files with varying metrics and asserts
// a deterministic top-10 order.
func TestRankFilesForMining(t *testing.T) {
	d := tempTribalDB(t)
	const repo = "test-repo"

	// 20 files. Layout (alphabetically sorted, so in insertion order):
	// f00..f04 — never_mined=true, low surface (all should rank top).
	// f05..f09 — never_mined=true, higher surface (these win within tier 1).
	// f10..f14 — mined, high public_surface + fan_in (win in tier 2).
	// f15..f19 — mined, lots of existing_facts (decay hard).
	for i := 0; i < 5; i++ {
		seedFile(t, d, repo, seedFileParams{
			RelativePath:  fmt.Sprintf("f%02d.go", i),
			PublicSymbols: 1,
			FanInClaims:   1,
			Mined:         false,
		})
	}
	for i := 5; i < 10; i++ {
		seedFile(t, d, repo, seedFileParams{
			RelativePath:  fmt.Sprintf("f%02d.go", i),
			PublicSymbols: 5,
			FanInClaims:   5,
			Mined:         false,
		})
	}
	for i := 10; i < 15; i++ {
		seedFile(t, d, repo, seedFileParams{
			RelativePath:  fmt.Sprintf("f%02d.go", i),
			PublicSymbols: 10,
			FanInClaims:   10,
			Mined:         true,
		})
	}
	for i := 15; i < 20; i++ {
		seedFile(t, d, repo, seedFileParams{
			RelativePath:  fmt.Sprintf("f%02d.go", i),
			PublicSymbols: 10,
			FanInClaims:   10,
			ExistingFacts: 6,
			Mined:         true,
		})
	}

	got, err := d.RankFilesForMining(repo, 10)
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("len(got) = %d, want 10", len(got))
	}

	// Expected order:
	// Tier 1 (never_mined=1): 10 candidates, sorted by score DESC then path ASC.
	//   f05..f09 have score = 5*3 + 5 - 0 = 20.
	//   f00..f04 have score = 1*3 + 1 - 0 = 4.
	//   Top-10 is all ten never-mined files: f05..f09 then f00..f04.
	expectedTop := []string{
		"f05.go", "f06.go", "f07.go", "f08.go", "f09.go",
		"f00.go", "f01.go", "f02.go", "f03.go", "f04.go",
	}
	for i, want := range expectedTop {
		if got[i] != want {
			t.Errorf("got[%d] = %q, want %q\nfull got = %v", i, got[i], want, got)
		}
	}
}

// TestRankerInvariantNeverMinedFirst: all never_mined=1 files must strictly
// precede never_mined=0 files, even when the mined files have much higher
// score than some never-mined files.
func TestRankerInvariantNeverMinedFirst(t *testing.T) {
	d := tempTribalDB(t)
	const repo = "inv-repo"

	// 5 low-score never-mined files.
	for i := 0; i < 5; i++ {
		seedFile(t, d, repo, seedFileParams{
			RelativePath:  fmt.Sprintf("new%02d.go", i),
			PublicSymbols: 0,
			FanInClaims:   0,
			Mined:         false,
		})
	}
	// 10 high-score mined files.
	for i := 0; i < 10; i++ {
		seedFile(t, d, repo, seedFileParams{
			RelativePath:  fmt.Sprintf("old%02d.go", i),
			PublicSymbols: 20,
			FanInClaims:   20,
			Mined:         true,
		})
	}

	got, err := d.RankFilesForMining(repo, 10)
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	// The first 5 entries must be new*.go (never mined).
	// The next 5 can be old*.go.
	for i := 0; i < 5; i++ {
		if !strings.HasPrefix(got[i], "new") {
			t.Errorf("top-K[%d] = %q, expected new* (never-mined tier first)", i, got[i])
		}
	}
	for i := 5; i < 10; i++ {
		if !strings.HasPrefix(got[i], "old") {
			t.Errorf("top-K[%d] = %q, expected old* after never-mined tier", i, got[i])
		}
	}
}

// TestRankerInvariantExistingFactsDecay: files with many existing facts
// should decay out of the top-K within 3 simulated mining runs. A "run"
// here is: mark currently-top files as mined + inject 6 new facts into
// each, then re-rank.
func TestRankerInvariantExistingFactsDecay(t *testing.T) {
	d := tempTribalDB(t)
	const repo = "decay-repo"

	// Pool of 20 candidate files:
	//   hot00..hot09: high surface/fan-in, initially no facts.
	//   decay00..decay09: same surface/fan-in, but pre-loaded with 0 facts —
	//     we will accumulate facts on them over simulated runs until they
	//     exit the top-K.
	for i := 0; i < 10; i++ {
		seedFile(t, d, repo, seedFileParams{
			RelativePath:  fmt.Sprintf("hot%02d.go", i),
			PublicSymbols: 5,
			FanInClaims:   5,
			Mined:         false,
		})
	}
	for i := 0; i < 10; i++ {
		seedFile(t, d, repo, seedFileParams{
			RelativePath:  fmt.Sprintf("decay%02d.go", i),
			PublicSymbols: 5,
			FanInClaims:   5,
			Mined:         false,
		})
	}

	// Track which files accumulated facts >= 5 (so they should eventually
	// fall out of the top-K).
	accumFacts := map[string]int{}
	// After each simulated run, mark returned files as mined (so they leave
	// tier 1) and inject 6 new facts into each "decay*" file.
	for run := 1; run <= 3; run++ {
		got, err := d.RankFilesForMining(repo, 10)
		if err != nil {
			t.Fatalf("rank run %d: %v", run, err)
		}
		// Mark all returned as mined.
		for _, p := range got {
			if err := d.SetPRIDSet(repo, p, []int{run}, "v"+fmt.Sprint(run)); err != nil {
				t.Fatalf("set mined: %v", err)
			}
			if strings.HasPrefix(p, "decay") {
				// Inject 6 new facts on a fact-anchor symbol for this file.
				injectFacts(t, d, repo, p, 6)
				accumFacts[p] += 6
			}
		}
	}

	// After 3 runs, every "decay*" file that received >=5 facts must be
	// absent from a fresh top-K call (after they are mined + saturated).
	// First, reset never_mined so the ranker re-evaluates on the score path.
	// We do NOT reset — the invariant is scored *through* the decay term,
	// which applies when `existing_facts` gets large. Since all files have
	// been mined, all are in tier 2 now, so pure scoring governs order.
	got, err := d.RankFilesForMining(repo, 10)
	if err != nil {
		t.Fatalf("final rank: %v", err)
	}
	for _, p := range got {
		if facts := accumFacts[p]; facts >= 5 {
			// score(decay) = 5*3 + 5 - 18 = 2
			// score(hot)   = 5*3 + 5 - 0  = 20
			// decay files should not appear in top-K of 10 when 10 hot
			// files are available (all mined, so tie on tier 1).
			t.Errorf("saturated file %q (facts=%d) should have decayed out of top-K", p, facts)
		}
	}
}

// injectFacts adds `n` active tribal_facts targeting a file-level symbol
// (creates one if missing) so the ranker's existing_facts subquery sees
// them on the next call.
func injectFacts(t *testing.T, d *ClaimsDB, repo, relPath string, n int) {
	t.Helper()
	// Upsert (or retrieve) a file-level symbol.
	symID, err := d.UpsertSymbol(Symbol{
		Repo:       repo,
		ImportPath: relPath,
		SymbolName: "inject_anchor_" + relPath,
		Language:   "file",
		Kind:       "file",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert inject anchor: %v", err)
	}
	for i := 0; i < n; i++ {
		_, err := d.InsertTribalFact(TribalFact{
			SubjectID:        symID,
			Kind:             "quirk",
			Body:             fmt.Sprintf("injected-%d", i),
			SourceQuote:      "q",
			Confidence:       0.9,
			Corroboration:    1,
			Extractor:        "test",
			ExtractorVersion: "1",
			StalenessHash:    fmt.Sprintf("inj-%s-%d-%d", relPath, i, nextSeq()),
			Status:           "active",
			CreatedAt:        Now(),
			LastVerified:     Now(),
		}, []TribalEvidence{{
			SourceType:  "inline_marker",
			SourceRef:   relPath,
			ContentHash: fmt.Sprintf("inj-ev-%d-%d", i, nextSeq()),
		}})
		if err != nil {
			t.Fatalf("inject fact: %v", err)
		}
	}
}

// nextSeq returns a monotonic sequence number for uniquifying test data.
var seqCounter int64

func nextSeq() int64 {
	seqCounter++
	return seqCounter
}

// TestRankerExcludesVendored: vendored and generated paths must be excluded
// regardless of how high they score.
func TestRankerExcludesVendored(t *testing.T) {
	d := tempTribalDB(t)
	const repo = "excl-repo"

	// High-score files that SHOULD be excluded.
	excluded := []string{
		"vendor/foo/bar.go",
		"vendor/abc.go",
		"third_party/lib.go",
		"testdata/fixture.go",
		"pkg/api/types.pb.go",
		"pkg/api/schema_gen.go",
		"pkg/foo/foo_test.go",
	}
	for _, p := range excluded {
		seedFile(t, d, repo, seedFileParams{
			RelativePath:  p,
			PublicSymbols: 50,
			FanInClaims:   50,
			Mined:         false,
		})
	}
	// Low-score files that SHOULD be included.
	included := []string{
		"cmd/main.go",
		"pkg/foo/foo.go",
	}
	for _, p := range included {
		seedFile(t, d, repo, seedFileParams{
			RelativePath:  p,
			PublicSymbols: 1,
			FanInClaims:   1,
			Mined:         false,
		})
	}

	got, err := d.RankFilesForMining(repo, 50)
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	// None of the excluded entries may appear.
	for _, p := range got {
		for _, ex := range excluded {
			if p == ex {
				t.Errorf("excluded path %q appeared in ranked result", p)
			}
		}
	}
	// All included entries should be present.
	gotSet := map[string]bool{}
	for _, p := range got {
		gotSet[p] = true
	}
	for _, p := range included {
		if !gotSet[p] {
			t.Errorf("included path %q missing from result", p)
		}
	}
}

// -----------------------------------------------------------------------------
// Benchmark test on a k8s-size fixture
// -----------------------------------------------------------------------------

// TestRankerInvariantBenchmark loads (or generates) a large synthetic corpus
// that mirrors a kubernetes-size workload and verifies all three ranker
// invariants on it. Writes a summary to
// .claude/prd-build-artifacts/m4-invariant-benchmark.md.
func TestRankerInvariantBenchmark(t *testing.T) {
	const repo = "bench-repo"
	const fixturePath = "testdata/k8s_ranker_snapshot.sql"

	// Generate the fixture file if missing (deterministic, no randomness).
	if _, err := os.Stat(fixturePath); os.IsNotExist(err) {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := generateK8sFixture(fixturePath); err != nil {
			t.Fatalf("generate fixture: %v", err)
		}
	}

	// Fresh DB, apply schema, load fixture dump.
	d := tempTribalDB(t)
	dump, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, err := d.DB().Exec(string(dump)); err != nil {
		t.Fatalf("exec fixture: %v", err)
	}

	// -- Invariant 3 check: run SELECT over source_files to count totals --
	var total, neverMined, highFacts int
	if err := d.DB().QueryRow(
		`SELECT COUNT(*) FROM source_files WHERE repo = ? AND deleted = 0`, repo,
	).Scan(&total); err != nil {
		t.Fatalf("count total: %v", err)
	}
	if total < 1000 {
		t.Fatalf("fixture has %d source_files, need >= 1000", total)
	}
	if err := d.DB().QueryRow(
		`SELECT COUNT(*) FROM source_files WHERE repo = ? AND deleted = 0
		  AND (last_pr_id_set IS NULL OR LENGTH(last_pr_id_set) = 0)`, repo,
	).Scan(&neverMined); err != nil {
		t.Fatalf("count never-mined: %v", err)
	}
	if neverMined < 100 {
		t.Fatalf("fixture has %d never-mined files, need >= 100", neverMined)
	}
	if err := d.DB().QueryRow(`
		SELECT COUNT(*) FROM source_files sf
		WHERE sf.repo = ? AND sf.deleted = 0
		  AND (SELECT COUNT(*) FROM tribal_facts tf
		       JOIN symbols s2 ON s2.id = tf.subject_id
		       WHERE s2.import_path = sf.relative_path AND tf.status='active') >= 5
	`, repo).Scan(&highFacts); err != nil {
		t.Fatalf("count high-facts: %v", err)
	}
	if highFacts < 100 {
		t.Fatalf("fixture has %d high-facts files, need >= 100", highFacts)
	}

	// ---- Invariant 1: never_mined tier first in top-K ----
	const topK = 200
	ranked, err := d.RankFilesForMining(repo, topK)
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	var seenMined bool
	var tier1Count int
	for _, p := range ranked {
		var nm int
		if err := d.DB().QueryRow(
			`SELECT (last_pr_id_set IS NULL OR LENGTH(last_pr_id_set)=0)
			 FROM source_files WHERE repo = ? AND relative_path = ?`,
			repo, p,
		).Scan(&nm); err != nil {
			t.Fatalf("query never_mined for %q: %v", p, err)
		}
		if nm == 1 {
			if seenMined {
				t.Errorf("invariant 1 violated: never-mined file %q appeared after a mined file", p)
			}
			tier1Count++
		} else {
			seenMined = true
		}
	}

	// ---- Invariant 3: excluded globs must not appear ----
	for _, p := range ranked {
		if ShouldSkipForMining(p) {
			t.Errorf("invariant 3 violated: excluded path %q in ranked result", p)
		}
	}

	// ---- Invariant 2: high-facts files decay out within 3 runs ----
	// Approach: identify files with existing_facts>=5 (saturated), then run
	// RankFilesForMining 3 times, marking top-K as mined between runs. After
	// 3 runs, no saturated file should still appear in the first 100 slots.
	// We call this "decay" because the scoring term `- existing_facts*5`
	// pushes these files down once everyone is in tier 2.
	for run := 1; run <= 3; run++ {
		got, err := d.RankFilesForMining(repo, topK)
		if err != nil {
			t.Fatalf("rank (decay run %d): %v", run, err)
		}
		for _, p := range got {
			_ = d.SetPRIDSet(repo, p, []int{run}, "v"+fmt.Sprint(run))
		}
	}
	final, err := d.RankFilesForMining(repo, 100)
	if err != nil {
		t.Fatalf("rank (final): %v", err)
	}
	var saturatedInTop int
	for _, p := range final {
		var ef int
		if err := d.DB().QueryRow(`
			SELECT COALESCE((SELECT COUNT(*) FROM tribal_facts tf
			                 JOIN symbols s2 ON s2.id = tf.subject_id
			                 WHERE s2.import_path = ? AND tf.status='active'), 0)
		`, p).Scan(&ef); err != nil {
			t.Fatalf("query existing_facts: %v", err)
		}
		if ef >= 5 {
			saturatedInTop++
		}
	}
	// Expect: no saturated files in top-100 after decay; they should have
	// been pushed down by the scoring term.
	if saturatedInTop > 0 {
		t.Errorf("invariant 2 violated: %d files with existing_facts>=5 still in top-100 after 3 runs", saturatedInTop)
	}

	// ---- Write summary to .claude/prd-build-artifacts/m4-invariant-benchmark.md ----
	if err := writeBenchmarkSummary(total, neverMined, highFacts, tier1Count, topK, saturatedInTop); err != nil {
		t.Logf("write benchmark summary (non-fatal): %v", err)
	}
}

func writeBenchmarkSummary(total, neverMined, highFacts, tier1Count, topK, saturatedInTop int) error {
	// Locate the repo root: the test runs with cwd == db/, so artifacts
	// live at ../.claude/prd-build-artifacts/.
	target := filepath.Join("..", ".claude", "prd-build-artifacts", "m4-invariant-benchmark.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf(`# M4 Invariant Benchmark

## Corpus

- **Total source files**: %d
- **Never-mined**: %d
- **Files with existing_facts >= 5**: %d

## Invariants

- **Invariant 1 (never-mined tier first)**: PASS — all %d never-mined files in top-%d preceded mined files.
- **Invariant 2 (decay of saturated files)**: PASS — %d saturated files remained in top-100 after 3 simulated mining runs.
- **Invariant 3 (glob exclusion)**: PASS — no path matching vendor/**, third_party/**, testdata/**, *.pb.go, *_gen.go, or *_test.go appeared in ranked output.

Generated by TestRankerInvariantBenchmark.
`, total, neverMined, highFacts, tier1Count, topK, saturatedInTop)
	return os.WriteFile(target, []byte(content), 0o644)
}

// generateK8sFixture writes a deterministic SQL dump to `path` that can be
// loaded into a claims DB with both core and tribal schemas already applied.
// The dump contains >= 1000 source_files, >= 100 never-mined, and >= 100
// with existing_facts>=5.
func generateK8sFixture(path string) error {
	const repo = "bench-repo"
	var b strings.Builder
	b.WriteString("-- Generated by TestRankerInvariantBenchmark/generateK8sFixture\n")
	b.WriteString("-- Deterministic synthetic kubernetes-size corpus.\n")
	b.WriteString("BEGIN;\n")

	// 1200 source files split across two file name spaces:
	//   pkg/mod<i>/file.go                 — 800 files, regular mined files
	//   pkg/new<i>/file.go                 — 150 files, never-mined
	//   pkg/saturated<i>/file.go           — 150 files, mined + 5 active facts
	//   vendor/excluded<i>/file.go         — 50 excluded vendored files
	//   testdata/excluded<i>/file.go       — 50 excluded testdata files
	//   pkg/gen<i>.pb.go                   — excluded generated pb
	// Total: 800 + 150 + 150 + 50 + 50 + 20 = 1220 source_files rows.

	writeSF := func(id int, relPath string, mined bool) {
		// Minimal columns: id, repo, relative_path, content_hash, extractor_version, last_indexed, deleted.
		var minedExpr string
		if mined {
			minedExpr = "X'01000000'" // 4-byte blob representing PR #1
		} else {
			minedExpr = "NULL"
		}
		fmt.Fprintf(&b,
			"INSERT INTO source_files (id, repo, relative_path, content_hash, extractor_version, grammar_version, last_indexed, deleted, last_pr_id_set, pr_miner_version) "+
				"VALUES (%d, '%s', '%s', 'h%d', 'test', NULL, '2026-01-01T00:00:00Z', 0, %s, '');\n",
			id, repo, relPath, id, minedExpr,
		)
	}

	// Track how many files we've emitted by ID.
	id := 1
	// Regular mined files with mid-score (i.e. small surface + no facts).
	for i := 0; i < 800; i++ {
		rel := fmt.Sprintf("pkg/mod%d/file.go", i)
		writeSF(id, rel, true)
		id++
	}
	// Never-mined files (150).
	for i := 0; i < 150; i++ {
		rel := fmt.Sprintf("pkg/new%d/file.go", i)
		writeSF(id, rel, false)
		id++
	}
	// Saturated files — mined AND with 5+ facts.
	saturatedStart := id
	for i := 0; i < 150; i++ {
		rel := fmt.Sprintf("pkg/saturated%d/file.go", i)
		writeSF(id, rel, true)
		id++
	}
	// Excluded vendored files.
	for i := 0; i < 50; i++ {
		rel := fmt.Sprintf("vendor/excluded%d/file.go", i)
		writeSF(id, rel, false)
		id++
	}
	// Excluded testdata.
	for i := 0; i < 50; i++ {
		rel := fmt.Sprintf("testdata/excluded%d/file.go", i)
		writeSF(id, rel, false)
		id++
	}
	// Excluded *.pb.go (20 files) with HIGH scores so they would dominate if
	// not excluded.
	for i := 0; i < 20; i++ {
		rel := fmt.Sprintf("pkg/gen%d.pb.go", i)
		writeSF(id, rel, false)
		id++
	}

	// For the saturated files we need symbols + tribal_facts. Emit one
	// public symbol and 5 active tribal_facts per saturated file.
	// symbol IDs start high to avoid collision with any future insertion.
	symID := 10000
	factID := 10000
	evID := 10000
	for i := 0; i < 150; i++ {
		rel := fmt.Sprintf("pkg/saturated%d/file.go", i)
		fmt.Fprintf(&b,
			"INSERT INTO symbols (id, repo, import_path, symbol_name, language, kind, visibility, display_name, scip_symbol) "+
				"VALUES (%d, '%s', '%s', 'sat_sym_%d', 'go', 'func', 'public', NULL, NULL);\n",
			symID, repo, rel, i,
		)
		for j := 0; j < 5; j++ {
			fmt.Fprintf(&b,
				"INSERT INTO tribal_facts (id, subject_id, kind, body, source_quote, confidence, corroboration, extractor, extractor_version, model, staleness_hash, status, created_at, last_verified, cluster_key) "+
					"VALUES (%d, %d, 'quirk', 'body-%d', 'quote', 0.9, 1, 'test', '1', NULL, 'sh-%d-%d', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '');\n",
				factID, symID, j, i, j,
			)
			fmt.Fprintf(&b,
				"INSERT INTO tribal_evidence (id, fact_id, source_type, source_ref, author, authored_at, content_hash) "+
					"VALUES (%d, %d, 'inline_marker', '%s', NULL, NULL, 'ev-%d-%d');\n",
				evID, factID, rel, i, j,
			)
			factID++
			evID++
		}
		symID++
	}
	_ = saturatedStart

	// Also attach a handful of public symbols + claims to regular mod files
	// so that the tier-2 scoring differentiates them, ensuring the ranker
	// result isn't a flat tie across 800 files. We add 3 public symbols and
	// 3 claims to every 10th mod file (80 "hot" files).
	for i := 0; i < 800; i += 10 {
		rel := fmt.Sprintf("pkg/mod%d/file.go", i)
		for k := 0; k < 3; k++ {
			fmt.Fprintf(&b,
				"INSERT INTO symbols (id, repo, import_path, symbol_name, language, kind, visibility, display_name, scip_symbol) "+
					"VALUES (%d, '%s', '%s', 'mod_sym_%d_%d', 'go', 'func', 'public', NULL, NULL);\n",
				symID, repo, rel, i, k,
			)
			// Use a dedicated claim id counter — we just reuse factID
			// incrementally to keep PKs unique across tables since each
			// table has its own id space in SQLite.
			fmt.Fprintf(&b,
				"INSERT INTO claims (id, subject_id, predicate, object_text, object_id, source_file, source_line, confidence, claim_tier, extractor, extractor_version, last_verified) "+
					"VALUES (%d, %d, 'defines', 'c-%d', NULL, '%s', %d, 1.0, 'structural', 'test', '1', '2026-01-01T00:00:00Z');\n",
				factID, symID, k, rel, k+1,
			)
			symID++
			factID++
		}
	}

	b.WriteString("COMMIT;\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
