package tribal_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor/tribal/normalize"
)

// corpusFact is the shape of each row in testdata/pilot_corpus.json.
type corpusFact struct {
	Subject string `json:"subject"`
	Kind    string `json:"kind"`
	Body    string `json:"body"`
}

// TestTribalCorroborationKnownFalseNegative asserts that the known word-order
// false-negative pair stays in separate cluster keys AND that the calibration
// sidecar records a body_token_jaccard >= 0.5 for the pair (so Phase 5 has
// the data it needs to justify switching to semantic embeddings).
func TestTribalCorroborationKnownFalseNegative(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "claims.db")
	main, err := db.OpenClaimsDB(mainPath)
	if err != nil {
		t.Fatalf("open main: %v", err)
	}
	defer main.Close()
	if err := main.CreateSchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := main.CreateTribalSchema(); err != nil {
		t.Fatalf("tribal schema: %v", err)
	}

	cd, err := db.OpenClusterDebugDB(mainPath)
	if err != nil {
		t.Fatalf("open debug sidecar: %v", err)
	}
	defer cd.Close()
	if err := cd.Attach(main); err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer cd.Detach(main)
	main.EnableClusterDebug(cd)
	defer main.DisableClusterDebug()

	subjectID, err := main.UpsertSymbol(db.Symbol{
		Repo:       "kubernetes/kubernetes",
		ImportPath: "pkg/scheduler/scheduler.go",
		SymbolName: "pkg/scheduler/scheduler.go",
		Language:   "file",
		Kind:       "file",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}

	fact := func(body string) db.TribalFact {
		return db.TribalFact{
			SubjectID:        subjectID,
			Kind:             "invariant",
			Body:             body,
			SourceQuote:      body,
			Confidence:       0.9,
			Corroboration:    1,
			Extractor:        "test_false_negative",
			ExtractorVersion: "1.0",
			StalenessHash:    body,
			Status:           "active",
			CreatedAt:        db.Now(),
			LastVerified:     db.Now(),
		}
	}
	evidence := func(ref string) []db.TribalEvidence {
		return []db.TribalEvidence{{
			SourceType:  "pr_comment",
			SourceRef:   ref,
			ContentHash: ref,
		}}
	}

	bodyA := "callers must hold the mutex"
	bodyB := "the mutex must be held by callers"

	if normalize.ScrubAndHash(bodyA) == normalize.ScrubAndHash(bodyB) {
		t.Fatal("known false-negative pair unexpectedly produced matching cluster keys")
	}

	if _, _, err := main.UpsertTribalFact(fact(bodyA), evidence("pr/a")); err != nil {
		t.Fatalf("upsert A: %v", err)
	}
	if _, _, err := main.UpsertTribalFact(fact(bodyB), evidence("pr/b")); err != nil {
		t.Fatalf("upsert B: %v", err)
	}

	// Two facts landed.
	var factCount int
	if err := main.DB().QueryRow(`SELECT COUNT(*) FROM tribal_facts`).Scan(&factCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if factCount != 2 {
		t.Fatalf("fact count = %d, want 2 (pair should NOT merge)", factCount)
	}

	// At least one calibration row for bodyB should record jaccard >= 0.5
	// against bodyA — proving the sidecar captured the word-order overlap
	// that the structural hash missed.
	pairs, err := cd.ListDebugPairs(100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pairs) < 2 {
		t.Fatalf("pair count = %d, want >= 2", len(pairs))
	}
	var maxJaccard float64
	for _, p := range pairs {
		if p.BodyTokenJaccard > maxJaccard {
			maxJaccard = p.BodyTokenJaccard
		}
	}
	// bodyA / bodyB share 5 tokens out of a union of 7 = 0.714.
	if maxJaccard < 0.5 {
		t.Errorf("max body_token_jaccard = %f, want >= 0.5", maxJaccard)
	}
}

// TestTribalCorroborationPilotCorpus is the real-corpus acceptance test:
// run >= 100 synthetic facts through UpsertTribalFact with the cluster
// debug sidecar enabled, then enforce the gate that at most 20% of
// un-merged fact pairs have body_token_jaccard > 0.7. The gate is the
// conservative normalization's self-check — if more than 20% of
// un-merged pairs look obviously related, the pipeline is
// under-clustering to a degree that Phase 5 embeddings cannot be delayed.
func TestTribalCorroborationPilotCorpus(t *testing.T) {
	corpusPath := filepath.Join("testdata", "pilot_corpus.json")
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var corpus []corpusFact
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(corpus) < 100 {
		t.Fatalf("corpus size = %d, want >= 100", len(corpus))
	}

	dir := t.TempDir()
	mainPath := filepath.Join(dir, "claims.db")
	main, err := db.OpenClaimsDB(mainPath)
	if err != nil {
		t.Fatalf("open main: %v", err)
	}
	defer main.Close()
	if err := main.CreateSchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := main.CreateTribalSchema(); err != nil {
		t.Fatalf("tribal schema: %v", err)
	}
	cd, err := db.OpenClusterDebugDB(mainPath)
	if err != nil {
		t.Fatalf("open cd: %v", err)
	}
	defer cd.Close()
	if err := cd.Attach(main); err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer cd.Detach(main)
	main.EnableClusterDebug(cd)
	defer main.DisableClusterDebug()

	// Upsert facts, building a per-subject symbol on the fly.
	subjectIDs := make(map[string]int64)
	ensureSubject := func(path string) int64 {
		if id, ok := subjectIDs[path]; ok {
			return id
		}
		id, err := main.UpsertSymbol(db.Symbol{
			Repo:       "kubernetes/kubernetes",
			ImportPath: path,
			SymbolName: path,
			Language:   "file",
			Kind:       "file",
			Visibility: "public",
		})
		if err != nil {
			t.Fatalf("symbol %s: %v", path, err)
		}
		subjectIDs[path] = id
		return id
	}
	for i, cf := range corpus {
		sid := ensureSubject(cf.Subject)
		fact := db.TribalFact{
			SubjectID:        sid,
			Kind:             cf.Kind,
			Body:             cf.Body,
			SourceQuote:      cf.Body,
			Confidence:       0.9,
			Corroboration:    1,
			Extractor:        "pilot_corpus",
			ExtractorVersion: "1.0",
			StalenessHash:    fmt.Sprintf("h%d", i),
			Status:           "active",
			CreatedAt:        db.Now(),
			LastVerified:     db.Now(),
		}
		ev := []db.TribalEvidence{{
			SourceType:  "pr_comment",
			SourceRef:   fmt.Sprintf("pr/%d", i),
			ContentHash: fmt.Sprintf("h%d", i),
		}}
		if _, _, err := main.UpsertTribalFact(fact, ev); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	// The gate: among un-merged pairs (calibration rows where the nearest
	// match has a DIFFERENT cluster key), at most 20% should have
	// body_token_jaccard > 0.7.
	pairs, err := cd.ListDebugPairs(100000)
	if err != nil {
		t.Fatalf("list pairs: %v", err)
	}
	unmerged := 0
	highJaccard := 0
	for _, p := range pairs {
		if p.NearestMatchID == 0 {
			// No neighbor to compare against (first fact of its cluster).
			continue
		}
		unmerged++
		if p.BodyTokenJaccard > 0.7 {
			highJaccard++
		}
	}
	if unmerged == 0 {
		t.Fatal("no un-merged pairs recorded — pilot corpus trivially passes the gate?")
	}
	ratio := float64(highJaccard) / float64(unmerged)
	t.Logf("pilot corpus: %d facts, %d un-merged pairs, %d with jaccard>0.7 (%.2f%%)",
		len(corpus), unmerged, highJaccard, ratio*100)
	if ratio > 0.20 {
		t.Errorf("high-jaccard un-merged ratio = %.2f%%, want <= 20%%", ratio*100)
	}
}
