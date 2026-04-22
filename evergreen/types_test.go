package evergreen

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestDocument_JSONRoundTrip exercises Document marshalling with both precise
// and fuzzy manifest entries, optional ExternalID, and the full enum surface.
// JSON field names are part of the public contract — assert the wire shape.
func TestDocument_JSONRoundTrip(t *testing.T) {
	extID := "sg-evergreen-v1-42"
	sym := int64(1234)

	orig := Document{
		ID:     "doc-abc",
		Query:  "how does the kubernetes informer pattern work?",
		RenderedAnswer: "Informers keep a local cache in sync with...",
		Manifest: []ManifestEntry{
			{
				SymbolID:              &sym,
				Repo:                  "github.com/kubernetes/kubernetes",
				CommitSHA:             "deadbeef",
				FilePath:              "staging/src/k8s.io/client-go/tools/cache/shared_informer.go",
				ContentHashAtRender:   "h-content",
				SignatureHashAtRender: "h-sig",
				LineStart:             137,
				LineEnd:               200,
			},
			{
				Repo:      "github.com/kubernetes/api",
				CommitSHA: "cafef00d",
				Fuzzy:     true,
			},
		},
		Status:          StaleStatus,
		RefreshPolicy:   AlertPolicy,
		MaxAgeDays:      30,
		CreatedAt:       time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
		LastRefreshedAt: time.Date(2026, 4, 15, 8, 30, 0, 0, time.UTC),
		ExternalID:      &extID,
		Backend:         "sourcegraph-evergreen",
	}

	buf, err := json.Marshal(&orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	s := string(buf)
	for _, want := range []string{
		`"id":"doc-abc"`,
		`"status":"stale"`,
		`"refresh_policy":"alert"`,
		`"max_age_days":30`,
		`"external_id":"sg-evergreen-v1-42"`,
		`"backend":"sourcegraph-evergreen"`,
		`"symbol_id":1234`,
		`"fuzzy":true`,
		`"signature_hash_at_render":"h-sig"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %q, got: %s", want, s)
		}
	}

	var round Document
	if err := json.Unmarshal(buf, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig, round) {
		t.Errorf("roundtrip mismatch\n orig: %#v\nround: %#v", orig, round)
	}
}

// TestDocument_OmitEmpty verifies that optional fields are omitted from JSON
// when unset, so wire payloads stay compact and nullable fields remain
// distinguishable from zero values.
func TestDocument_OmitEmpty(t *testing.T) {
	minimal := Document{
		ID:              "d",
		Query:           "q",
		RenderedAnswer:  "a",
		Status:          FreshStatus,
		RefreshPolicy:   ManualPolicy,
		CreatedAt:       time.Unix(0, 0).UTC(),
		LastRefreshedAt: time.Unix(0, 0).UTC(),
	}
	buf, err := json.Marshal(&minimal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(buf)
	for _, forbidden := range []string{
		`"external_id"`,
		`"backend"`,
		`"max_age_days"`,
	} {
		if strings.Contains(s, forbidden) {
			t.Errorf("expected %s to be omitted when zero, got: %s", forbidden, s)
		}
	}
	// manifest is not omitempty — it's a primary field that should render
	// explicitly as [] or null so consumers can rely on its presence.
	if !strings.Contains(s, `"manifest":null`) && !strings.Contains(s, `"manifest":[]`) {
		t.Errorf("expected manifest field present, got: %s", s)
	}
}

func TestManifestEntry_FuzzyMarshal(t *testing.T) {
	fuzzy := ManifestEntry{
		Repo:      "github.com/example/x",
		CommitSHA: "abc123",
		Fuzzy:     true,
	}
	buf, err := json.Marshal(&fuzzy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(buf)
	if strings.Contains(s, `"symbol_id"`) {
		t.Errorf("fuzzy entry must not serialize symbol_id: %s", s)
	}
	if strings.Contains(s, `"content_hash_at_render"`) {
		t.Errorf("fuzzy entry must not serialize content_hash_at_render: %s", s)
	}
	if !strings.Contains(s, `"fuzzy":true`) {
		t.Errorf("expected fuzzy=true in output: %s", s)
	}
}

func TestFinding_DocumentScopedMarshal(t *testing.T) {
	// An age-based cold finding has no Entry — Entry must omit cleanly.
	f := Finding{
		DocumentID: "d1",
		Severity:   ColdSeverity,
		ChangeKind: AgeChange,
		Detail:     "document exceeded max_age_days=30",
	}
	buf, err := json.Marshal(&f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(buf)
	if strings.Contains(s, `"entry"`) {
		t.Errorf("document-scoped finding must omit entry field: %s", s)
	}
	if strings.Contains(s, `"was_hash"`) || strings.Contains(s, `"current_hash"`) {
		t.Errorf("document-scoped finding must omit hash fields: %s", s)
	}
}

func TestFinding_PerEntryMarshal(t *testing.T) {
	sym := int64(42)
	entry := &ManifestEntry{
		SymbolID:  &sym,
		Repo:      "r",
		CommitSHA: "c",
		FilePath:  "f.go",
	}
	f := Finding{
		DocumentID:  "d1",
		Severity:    HotSeverity,
		ChangeKind:  SignatureChange,
		Entry:       entry,
		WasHash:     "was",
		CurrentHash: "now",
		Detail:      "signature changed",
	}
	buf, err := json.Marshal(&f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(buf)
	for _, want := range []string{
		`"severity":"hot"`,
		`"change_kind":"signature"`,
		`"was_hash":"was"`,
		`"current_hash":"now"`,
		`"entry":{`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %q, got: %s", want, s)
		}
	}
}

func TestRefreshResult_RoundTrip(t *testing.T) {
	ext := "v-99"
	r := RefreshResult{
		RenderedAnswer: "new prose",
		Manifest: []ManifestEntry{
			{Repo: "r", CommitSHA: "s", Fuzzy: true},
		},
		Backend:    "sourcegraph-evergreen",
		ExternalID: &ext,
		Metadata:   map[string]any{"tokens": float64(1234)},
	}
	buf, err := json.Marshal(&r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round RefreshResult
	if err := json.Unmarshal(buf, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(r, round) {
		t.Errorf("roundtrip mismatch\n orig: %#v\nround: %#v", r, round)
	}
}

// TestEnumStrings pins the wire values of every exported enum. Renaming any
// of these constants silently would break adapters — the test enforces an
// explicit ack (update the test) on any change.
func TestEnumStrings(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"FreshStatus", string(FreshStatus), "fresh"},
		{"StaleStatus", string(StaleStatus), "stale"},
		{"OrphanedStatus", string(OrphanedStatus), "orphaned"},
		{"RefreshingStatus", string(RefreshingStatus), "refreshing"},

		{"AlertPolicy", string(AlertPolicy), "alert"},
		{"ManualPolicy", string(ManualPolicy), "manual"},
		{"AutoPolicy", string(AutoPolicy), "auto"},

		{"HotSeverity", string(HotSeverity), "hot"},
		{"WarmSeverity", string(WarmSeverity), "warm"},
		{"ColdSeverity", string(ColdSeverity), "cold"},
		{"OrphanedSeverity", string(OrphanedSeverity), "orphaned"},

		{"NoChange", string(NoChange), "none"},
		{"SignatureChange", string(SignatureChange), "signature"},
		{"BodyChange", string(BodyChange), "body"},
		{"DeletedChange", string(DeletedChange), "deleted"},
		{"RenamedChange", string(RenamedChange), "renamed"},
		{"AgeChange", string(AgeChange), "age"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (changing wire values is a breaking change)", c.name, c.got, c.want)
		}
	}
}

// TestEnumWireValuesUnique pins the cross-type uniqueness policy: every
// enum wire value must be unique across DocStatus, RefreshPolicy, Severity,
// and ChangeKind, with exactly ONE intentional exception: "orphaned" is
// shared by OrphanedStatus and OrphanedSeverity (both express the same
// semantic condition at different granularities).
//
// If this test starts failing, either a new constant has collided unintentionally
// or the intentional-collision allowlist needs updating after explicit review.
func TestEnumWireValuesUnique(t *testing.T) {
	type entry struct {
		kind  string
		value string
	}
	all := []entry{
		{"DocStatus", string(FreshStatus)},
		{"DocStatus", string(StaleStatus)},
		{"DocStatus", string(OrphanedStatus)},
		{"DocStatus", string(RefreshingStatus)},
		{"RefreshPolicy", string(AlertPolicy)},
		{"RefreshPolicy", string(ManualPolicy)},
		{"RefreshPolicy", string(AutoPolicy)},
		{"Severity", string(HotSeverity)},
		{"Severity", string(WarmSeverity)},
		{"Severity", string(ColdSeverity)},
		{"Severity", string(OrphanedSeverity)},
		{"ChangeKind", string(NoChange)},
		{"ChangeKind", string(SignatureChange)},
		{"ChangeKind", string(BodyChange)},
		{"ChangeKind", string(DeletedChange)},
		{"ChangeKind", string(RenamedChange)},
		{"ChangeKind", string(AgeChange)},
	}
	allowed := map[string]map[string]bool{
		"orphaned": {"DocStatus": true, "Severity": true},
	}
	byValue := map[string][]string{}
	for _, e := range all {
		byValue[e.value] = append(byValue[e.value], e.kind)
	}
	for v, kinds := range byValue {
		if len(kinds) == 1 {
			continue
		}
		ok := true
		allow, listed := allowed[v]
		if !listed {
			ok = false
		} else {
			for _, k := range kinds {
				if !allow[k] {
					ok = false
					break
				}
			}
		}
		if !ok {
			t.Errorf("wire value %q collides across %v without allowlisted reason", v, kinds)
		}
	}
}

func TestNewDocumentID(t *testing.T) {
	a := NewDocumentID()
	b := NewDocumentID()
	if a == b {
		t.Errorf("expected distinct IDs, got %q twice", a)
	}
	if len(a) != len("doc-")+32 {
		t.Errorf("expected 'doc-' + 32 hex chars, got %q (len=%d)", a, len(a))
	}
	if a[:4] != "doc-" {
		t.Errorf("expected 'doc-' prefix, got %q", a)
	}
}

func TestSentinelErrors(t *testing.T) {
	// errors.Is identity is the contract adapters rely on.
	for _, e := range []error{ErrNotFound, ErrSymbolNotFound, ErrRateLimited, ErrOrphaned} {
		wrapped := wrapErr(e)
		if !errors.Is(wrapped, e) {
			t.Errorf("errors.Is must recognize wrapped %v", e)
		}
	}
	// Distinct sentinels must not alias each other.
	if errors.Is(ErrNotFound, ErrSymbolNotFound) {
		t.Error("ErrNotFound and ErrSymbolNotFound must be distinct")
	}
	if errors.Is(ErrRateLimited, ErrOrphaned) {
		t.Error("ErrRateLimited and ErrOrphaned must be distinct")
	}
}

// wrapErr simulates what an implementation would do when surfacing a sentinel.
type wrappedErr struct {
	inner error
	msg   string
}

func (w wrappedErr) Error() string { return w.msg + ": " + w.inner.Error() }
func (w wrappedErr) Unwrap() error { return w.inner }

func wrapErr(e error) error { return wrappedErr{inner: e, msg: "wrapped"} }
