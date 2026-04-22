package evergreen

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// stubClaims is a ClaimsReader controlled by explicit maps keyed by
// (repo, symbolID) for GetSymbol and (repo, filePath, lineStart-lineEnd) for
// ResolveSymbolByLocation. Missing entries return ErrSymbolNotFound unless
// an override error is set.
type stubClaims struct {
	symbols        map[string]*SymbolState
	locations      map[string]int64
	getErr         error // returned for any GetSymbol call when non-nil
	resolveErr     error // returned for any ResolveSymbolByLocation call when non-nil
	getCallCount   int
	resolveCallCount int
}

func (s *stubClaims) GetSymbol(_ context.Context, repo string, id int64) (*SymbolState, error) {
	s.getCallCount++
	if s.getErr != nil {
		return nil, s.getErr
	}
	key := fmt.Sprintf("%s|%d", repo, id)
	if state, ok := s.symbols[key]; ok {
		return state, nil
	}
	return nil, ErrSymbolNotFound
}

func (s *stubClaims) ResolveSymbolByLocation(_ context.Context, repo, filePath string, lineStart, lineEnd int) (int64, error) {
	s.resolveCallCount++
	if s.resolveErr != nil {
		return 0, s.resolveErr
	}
	key := fmt.Sprintf("%s|%s|%d-%d", repo, filePath, lineStart, lineEnd)
	if id, ok := s.locations[key]; ok {
		return id, nil
	}
	return 0, ErrSymbolNotFound
}

// --- Contract guards ------------------------------------------------------

func TestDetect_NilDoc(t *testing.T) {
	if _, err := Detect(context.Background(), nil, &stubClaims{}); err == nil {
		t.Fatal("expected error for nil doc")
	}
}

func TestDetect_NilClaims(t *testing.T) {
	if _, err := Detect(context.Background(), &Document{}, nil); err == nil {
		t.Fatal("expected error for nil claims")
	}
}

// --- Per-symbol classification -------------------------------------------

func id(v int64) *int64 { return &v }

func TestDetect_NoDrift(t *testing.T) {
	claims := &stubClaims{
		symbols: map[string]*SymbolState{
			"github.com/x/y|42": {SymbolID: 42, ContentHash: "c1", SignatureHash: "s1"},
		},
	}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{
				SymbolID:              id(42),
				Repo:                  "github.com/x/y",
				FilePath:              "f.go",
				LineStart:             10, LineEnd: 20,
				ContentHashAtRender:   "c1",
				SignatureHashAtRender: "s1",
			},
		},
	}
	got, err := Detect(context.Background(), doc, claims)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestDetect_SignatureChange_Hot(t *testing.T) {
	claims := &stubClaims{
		symbols: map[string]*SymbolState{
			"github.com/x/y|42": {SymbolID: 42, ContentHash: "c2", SignatureHash: "s2"},
		},
	}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{
				SymbolID:              id(42),
				Repo:                  "github.com/x/y",
				ContentHashAtRender:   "c1", // also changed, but signature change dominates
				SignatureHashAtRender: "s1",
			},
		},
	}
	got, _ := Detect(context.Background(), doc, claims)
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d", len(got))
	}
	if got[0].Severity != HotSeverity {
		t.Errorf("severity = %q, want hot", got[0].Severity)
	}
	if got[0].ChangeKind != SignatureChange {
		t.Errorf("change_kind = %q, want signature", got[0].ChangeKind)
	}
	if got[0].WasHash != "s1" || got[0].CurrentHash != "s2" {
		t.Errorf("hashes: was=%q current=%q, want s1/s2", got[0].WasHash, got[0].CurrentHash)
	}
}

func TestDetect_BodyOnlyChange_Warm(t *testing.T) {
	claims := &stubClaims{
		symbols: map[string]*SymbolState{
			"github.com/x/y|42": {SymbolID: 42, ContentHash: "c2", SignatureHash: "s1"},
		},
	}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{
				SymbolID:              id(42),
				Repo:                  "github.com/x/y",
				ContentHashAtRender:   "c1",
				SignatureHashAtRender: "s1",
			},
		},
	}
	got, _ := Detect(context.Background(), doc, claims)
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d", len(got))
	}
	if got[0].Severity != WarmSeverity || got[0].ChangeKind != BodyChange {
		t.Errorf("got %s/%s, want warm/body", got[0].Severity, got[0].ChangeKind)
	}
}

// --- Orphan detection ----------------------------------------------------

func TestDetect_SymbolDeleted_Orphaned(t *testing.T) {
	// symbols map is empty — GetSymbol returns ErrSymbolNotFound.
	// locations map is empty too — ResolveSymbolByLocation also returns
	// ErrSymbolNotFound, so classifyMissingSymbol yields DeletedChange.
	claims := &stubClaims{}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{
				SymbolID:  id(42),
				Repo:      "github.com/x/y",
				FilePath:  "f.go",
				LineStart: 10, LineEnd: 20,
			},
		},
	}
	got, _ := Detect(context.Background(), doc, claims)
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d", len(got))
	}
	if got[0].Severity != OrphanedSeverity || got[0].ChangeKind != DeletedChange {
		t.Errorf("got %s/%s, want orphaned/deleted", got[0].Severity, got[0].ChangeKind)
	}
}

func TestDetect_SymbolRenamed_Orphaned(t *testing.T) {
	claims := &stubClaims{
		// GetSymbol(42) not found — original symbol gone.
		// ResolveSymbolByLocation returns a DIFFERENT id — rename.
		locations: map[string]int64{
			"github.com/x/y|f.go|10-20": 99,
		},
	}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{
				SymbolID:  id(42),
				Repo:      "github.com/x/y",
				FilePath:  "f.go",
				LineStart: 10, LineEnd: 20,
			},
		},
	}
	got, _ := Detect(context.Background(), doc, claims)
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d", len(got))
	}
	if got[0].Severity != OrphanedSeverity || got[0].ChangeKind != RenamedChange {
		t.Errorf("got %s/%s, want orphaned/renamed", got[0].Severity, got[0].ChangeKind)
	}
}

// --- Partial precision: empty hashes degrade gracefully ------------------

// Epl ships manifest entries without ContentHashAtRender/SignatureHashAtRender.
// Detect must not fabricate drift for empty hashes; Orphaned still fires.
func TestDetect_EmptyHashes_SkipsHotWarm(t *testing.T) {
	claims := &stubClaims{
		symbols: map[string]*SymbolState{
			"github.com/x/y|42": {SymbolID: 42, ContentHash: "whatever", SignatureHash: "whatever"},
		},
	}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{
				SymbolID:  id(42),
				Repo:      "github.com/x/y",
				FilePath:  "f.go",
				LineStart: 10, LineEnd: 20,
				// No hashes — OSS executor precision budget.
			},
		},
	}
	got, _ := Detect(context.Background(), doc, claims)
	if len(got) != 0 {
		t.Errorf("expected 0 findings for empty-hash entry with live symbol, got %+v", got)
	}
}

func TestDetect_EmptyHashes_OrphanStillFires(t *testing.T) {
	claims := &stubClaims{} // symbol absent
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{
				SymbolID:  id(42),
				Repo:      "github.com/x/y",
				FilePath:  "f.go",
				LineStart: 10, LineEnd: 20,
			},
		},
	}
	got, _ := Detect(context.Background(), doc, claims)
	if len(got) != 1 || got[0].Severity != OrphanedSeverity {
		t.Errorf("expected orphan finding with empty hashes, got %+v", got)
	}
}

// --- Location-only entries (no symbol_id) --------------------------------

func TestDetect_LocationOnly_HasOccupant_Skipped(t *testing.T) {
	claims := &stubClaims{
		locations: map[string]int64{
			"github.com/x/y|f.go|10-20": 77,
		},
	}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{
				// SymbolID is nil — mimicking epl without ClaimsReader injection
				Repo:      "github.com/x/y",
				FilePath:  "f.go",
				LineStart: 10, LineEnd: 20,
			},
		},
	}
	got, _ := Detect(context.Background(), doc, claims)
	if len(got) != 0 {
		t.Errorf("location-only entry with live occupant should produce no finding, got %+v", got)
	}
}

func TestDetect_LocationOnly_NothingThere_Orphaned(t *testing.T) {
	claims := &stubClaims{}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{
				Repo:      "github.com/x/y",
				FilePath:  "f.go",
				LineStart: 10, LineEnd: 20,
			},
		},
	}
	got, _ := Detect(context.Background(), doc, claims)
	if len(got) != 1 || got[0].Severity != OrphanedSeverity || got[0].ChangeKind != DeletedChange {
		t.Errorf("expected orphaned/deleted for empty location, got %+v", got)
	}
}

// --- Fuzzy entries are skipped for per-entry signal ----------------------

func TestDetect_FuzzyEntriesIgnored(t *testing.T) {
	claims := &stubClaims{}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{Repo: "github.com/x/y", CommitSHA: "abc", Fuzzy: true},
		},
	}
	got, _ := Detect(context.Background(), doc, claims)
	if len(got) != 0 {
		t.Errorf("fuzzy entries should not produce per-entry findings, got %+v", got)
	}
	if claims.getCallCount != 0 || claims.resolveCallCount != 0 {
		t.Errorf("fuzzy entries should not touch ClaimsReader, got get=%d resolve=%d",
			claims.getCallCount, claims.resolveCallCount)
	}
}

// --- Age-based Cold ------------------------------------------------------

func TestDetect_AgeExceeded_Cold(t *testing.T) {
	claims := &stubClaims{}
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	doc := &Document{
		ID:              "d",
		MaxAgeDays:      7,
		LastRefreshedAt: now.Add(-8 * 24 * time.Hour),
	}
	got, _ := Detect(context.Background(), doc, claims, WithNow(now))
	if len(got) != 1 || got[0].Severity != ColdSeverity || got[0].ChangeKind != AgeChange {
		t.Errorf("expected cold/age, got %+v", got)
	}
	if got[0].Entry != nil {
		t.Errorf("age-based cold finding should not reference a specific entry")
	}
}

func TestDetect_AgeUnderThreshold_NoFinding(t *testing.T) {
	claims := &stubClaims{}
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	doc := &Document{
		ID:              "d",
		MaxAgeDays:      7,
		LastRefreshedAt: now.Add(-6 * 24 * time.Hour),
	}
	got, _ := Detect(context.Background(), doc, claims, WithNow(now))
	if len(got) != 0 {
		t.Errorf("expected no finding under age threshold, got %+v", got)
	}
}

func TestDetect_ZeroMaxAge_NoAgeFinding(t *testing.T) {
	claims := &stubClaims{}
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	doc := &Document{
		ID:              "d",
		MaxAgeDays:      0, // disables age-based cold
		LastRefreshedAt: now.Add(-365 * 24 * time.Hour),
	}
	got, _ := Detect(context.Background(), doc, claims, WithNow(now))
	if len(got) != 0 {
		t.Errorf("MaxAgeDays=0 must disable age tier, got %+v", got)
	}
}

func TestDetect_ZeroLastRefreshed_NoAgeFinding(t *testing.T) {
	// Newly-created documents that haven't ever refreshed shouldn't trip
	// age-based cold against the epoch.
	claims := &stubClaims{}
	doc := &Document{ID: "d", MaxAgeDays: 7}
	got, _ := Detect(context.Background(), doc, claims, WithNow(time.Now()))
	if len(got) != 0 {
		t.Errorf("zero LastRefreshedAt must not trip age tier, got %+v", got)
	}
}

// --- Error propagation ---------------------------------------------------

func TestDetect_GetSymbolErrorPropagates(t *testing.T) {
	boom := errors.New("backend down")
	claims := &stubClaims{getErr: boom}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{SymbolID: id(42), Repo: "r"},
		},
	}
	_, err := Detect(context.Background(), doc, claims)
	if !errors.Is(err, boom) {
		t.Errorf("expected wrapped backend error, got %v", err)
	}
}

func TestDetect_LocationOnlyResolveErrorPropagates(t *testing.T) {
	boom := errors.New("resolver down")
	claims := &stubClaims{resolveErr: boom}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{Repo: "r", FilePath: "f.go", LineStart: 1, LineEnd: 2},
		},
	}
	_, err := Detect(context.Background(), doc, claims)
	if !errors.Is(err, boom) {
		t.Errorf("expected wrapped resolver error, got %v", err)
	}
}

// Orphan-path ResolveSymbolByLocation errors are tolerated (fall back to
// DeletedChange) because rename detection is best-effort.
func TestDetect_OrphanPath_ResolveErrorTolerated(t *testing.T) {
	boom := errors.New("resolver down")
	claims := &stubClaims{resolveErr: boom} // GetSymbol returns ErrSymbolNotFound (default)
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{SymbolID: id(42), Repo: "r", FilePath: "f.go", LineStart: 1, LineEnd: 2},
		},
	}
	got, err := Detect(context.Background(), doc, claims)
	if err != nil {
		t.Fatalf("expected rename-path resolver error to be tolerated, got %v", err)
	}
	if len(got) != 1 || got[0].ChangeKind != DeletedChange {
		t.Errorf("expected orphan/deleted fallback, got %+v", got)
	}
}

// --- Ordering ------------------------------------------------------------

// Mixed findings are sorted Orphaned > Hot > Warm > Cold. Within a severity,
// findings preserve manifest-entry order.
func TestDetect_FindingsOrderedBySeverity(t *testing.T) {
	claims := &stubClaims{
		symbols: map[string]*SymbolState{
			"r|hot1":  {SymbolID: 0, ContentHash: "c", SignatureHash: "sNEW"},
			"r|warm1": {SymbolID: 0, ContentHash: "cNEW", SignatureHash: "s"},
			"r|warm2": {SymbolID: 0, ContentHash: "cNEW", SignatureHash: "s"},
		},
	}
	// Use distinct repo|id keys so stubClaims map-key works.
	claims.symbols = map[string]*SymbolState{
		"r|1": {SymbolID: 1, ContentHash: "c", SignatureHash: "sNEW"},  // hot
		"r|2": {SymbolID: 2, ContentHash: "cNEW", SignatureHash: "s"},  // warm
		// 3 absent -> orphaned
	}
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			// Order below is deliberately: hot, warm, orphaned.
			{SymbolID: id(1), Repo: "r", ContentHashAtRender: "c", SignatureHashAtRender: "s"},
			{SymbolID: id(2), Repo: "r", ContentHashAtRender: "c", SignatureHashAtRender: "s"},
			{SymbolID: id(3), Repo: "r", FilePath: "f.go", LineStart: 1, LineEnd: 2},
		},
		MaxAgeDays:      1,
		LastRefreshedAt: now.Add(-10 * 24 * time.Hour), // triggers cold/age too
	}
	got, err := Detect(context.Background(), doc, claims, WithNow(now))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 findings, got %d: %+v", len(got), got)
	}
	wantOrder := []Severity{OrphanedSeverity, HotSeverity, WarmSeverity, ColdSeverity}
	for i, want := range wantOrder {
		if got[i].Severity != want {
			t.Errorf("got[%d].Severity = %s, want %s (full=%+v)", i, got[i].Severity, want, got)
		}
	}
}

func TestDetect_WithinSeverity_StableByManifestOrder(t *testing.T) {
	claims := &stubClaims{
		symbols: map[string]*SymbolState{
			"r|1": {SymbolID: 1, ContentHash: "cNEW", SignatureHash: "s"},
			"r|2": {SymbolID: 2, ContentHash: "cNEW", SignatureHash: "s"},
			"r|3": {SymbolID: 3, ContentHash: "cNEW", SignatureHash: "s"},
		},
	}
	doc := &Document{
		ID: "d",
		Manifest: []ManifestEntry{
			{SymbolID: id(1), Repo: "r", FilePath: "first.go", ContentHashAtRender: "c", SignatureHashAtRender: "s"},
			{SymbolID: id(2), Repo: "r", FilePath: "second.go", ContentHashAtRender: "c", SignatureHashAtRender: "s"},
			{SymbolID: id(3), Repo: "r", FilePath: "third.go", ContentHashAtRender: "c", SignatureHashAtRender: "s"},
		},
	}
	got, _ := Detect(context.Background(), doc, claims)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	for i, want := range []string{"first.go", "second.go", "third.go"} {
		if got[i].Entry == nil || got[i].Entry.FilePath != want {
			t.Errorf("got[%d].Entry.FilePath != %q", i, want)
		}
	}
}

// --- Empty manifest ------------------------------------------------------

func TestDetect_EmptyManifest_StillEmitsAgeFinding(t *testing.T) {
	claims := &stubClaims{}
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	doc := &Document{
		ID:              "d",
		MaxAgeDays:      1,
		LastRefreshedAt: now.Add(-48 * time.Hour),
	}
	got, _ := Detect(context.Background(), doc, claims, WithNow(now))
	if len(got) != 1 || got[0].Severity != ColdSeverity {
		t.Errorf("expected single cold finding for aged empty-manifest doc, got %+v", got)
	}
}
