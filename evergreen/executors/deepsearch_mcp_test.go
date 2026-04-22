package executors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sjarmak/livedocs/evergreen"
)

// Compile-time: DeepSearchMCP implements evergreen.RefreshExecutor.
var _ evergreen.RefreshExecutor = (*DeepSearchMCP)(nil)

// mockCaller is a table-driven MCPCaller for executor tests. Each call
// consults toolResponses keyed by tool name and returns the first pending
// entry, or an error if toolErrors has one.
type mockCaller struct {
	toolResponses map[string][]string
	toolErrors    map[string][]error
	calls         []mockCall
}

type mockCall struct {
	tool string
	args map[string]any
}

func (m *mockCaller) CallTool(_ context.Context, tool string, args map[string]any) (string, error) {
	m.calls = append(m.calls, mockCall{tool: tool, args: args})

	if errs, ok := m.toolErrors[tool]; ok && len(errs) > 0 {
		err := errs[0]
		m.toolErrors[tool] = errs[1:]
		if err != nil {
			return "", err
		}
	}
	if resps, ok := m.toolResponses[tool]; ok && len(resps) > 0 {
		r := resps[0]
		m.toolResponses[tool] = resps[1:]
		return r, nil
	}
	return "", fmt.Errorf("mockCaller: no canned response for tool %q", tool)
}

// mockClaims is a controllable ClaimsReader for executor tests.
type mockClaims struct {
	// resolveMap maps "repo|file|lineStart-lineEnd" to symbol_id.
	resolveMap map[string]int64
	// resolveErr overrides with an error for unlisted keys; if nil, unlisted
	// keys return ErrSymbolNotFound.
	resolveErr error
}

func (c *mockClaims) GetSymbol(_ context.Context, _ string, _ int64) (*evergreen.SymbolState, error) {
	return nil, evergreen.ErrSymbolNotFound
}

func (c *mockClaims) ResolveSymbolByLocation(_ context.Context, repo, filePath string, lineStart, lineEnd int) (int64, error) {
	key := fmt.Sprintf("%s|%s|%d-%d", repo, filePath, lineStart, lineEnd)
	if id, ok := c.resolveMap[key]; ok {
		return id, nil
	}
	if c.resolveErr != nil {
		return 0, c.resolveErr
	}
	return 0, evergreen.ErrSymbolNotFound
}

// --- Constructor & options ------------------------------------------------

func TestNewDeepSearchMCP_NilCaller(t *testing.T) {
	if _, err := NewDeepSearchMCP(nil); err == nil {
		t.Fatal("expected error for nil caller, got nil")
	}
}

func TestNewDeepSearchMCP_Defaults(t *testing.T) {
	e, err := NewDeepSearchMCP(&mockCaller{})
	if err != nil {
		t.Fatal(err)
	}
	if e.toolName != DefaultDeepSearchTool {
		t.Errorf("toolName = %q, want %q", e.toolName, DefaultDeepSearchTool)
	}
	if e.readTool != DefaultDeepSearchReadTool {
		t.Errorf("readTool = %q, want %q", e.readTool, DefaultDeepSearchReadTool)
	}
	if e.claims != nil {
		t.Errorf("claims = %v, want nil", e.claims)
	}
	if e.Name() != backendName {
		t.Errorf("Name() = %q, want %q", e.Name(), backendName)
	}
}

func TestNewDeepSearchMCP_WithToolNames(t *testing.T) {
	e, err := NewDeepSearchMCP(&mockCaller{}, WithToolNames("sg_deepsearch", "sg_deepsearch_read"))
	if err != nil {
		t.Fatal(err)
	}
	if e.toolName != "sg_deepsearch" || e.readTool != "sg_deepsearch_read" {
		t.Errorf("got tool=%q read=%q", e.toolName, e.readTool)
	}
	// Empty strings are ignored rather than clearing the default.
	e, _ = NewDeepSearchMCP(&mockCaller{}, WithToolNames("", ""))
	if e.toolName != DefaultDeepSearchTool || e.readTool != DefaultDeepSearchReadTool {
		t.Errorf("empty overrides must not clear defaults, got tool=%q read=%q", e.toolName, e.readTool)
	}
}

// --- Refresh contract guards ---------------------------------------------

func TestRefresh_NilDoc(t *testing.T) {
	e, _ := NewDeepSearchMCP(&mockCaller{})
	if _, err := e.Refresh(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil doc, got nil")
	}
}

func TestRefresh_EmptyQuery(t *testing.T) {
	e, _ := NewDeepSearchMCP(&mockCaller{})
	if _, err := e.Refresh(context.Background(), &evergreen.Document{Query: "   "}); err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
}

// --- Refresh: deepsearch call failure bubbles up -------------------------

func TestRefresh_DeepsearchFails(t *testing.T) {
	wantErr := errors.New("transport boom")
	caller := &mockCaller{
		toolErrors: map[string][]error{DefaultDeepSearchTool: {wantErr}},
	}
	e, _ := NewDeepSearchMCP(caller)
	_, err := e.Refresh(context.Background(), &evergreen.Document{Query: "q"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped %v, got %v", wantErr, err)
	}
}

// --- Refresh: no Link in prose -> fuzzy fallback from prose --------------

func TestRefresh_NoLinkFuzzyFallback(t *testing.T) {
	prose := "# Some answer\nSee github.com/kubernetes/kubernetes for details, also github.com/kubernetes/kubernetes."
	caller := &mockCaller{
		toolResponses: map[string][]string{DefaultDeepSearchTool: {prose}},
	}
	e, _ := NewDeepSearchMCP(caller)
	res, err := e.Refresh(context.Background(), &evergreen.Document{Query: "q"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(caller.calls) != 1 {
		t.Errorf("expected only deepsearch to be called without a link, got %d calls", len(caller.calls))
	}
	if res.RenderedAnswer != prose {
		t.Errorf("RenderedAnswer mismatch")
	}
	if len(res.Manifest) != 1 {
		t.Fatalf("want 1 fuzzy entry (deduped), got %d", len(res.Manifest))
	}
	if !res.Manifest[0].Fuzzy {
		t.Error("expected Fuzzy=true")
	}
	if res.Manifest[0].Repo != "github.com/kubernetes/kubernetes" {
		t.Errorf("repo = %q", res.Manifest[0].Repo)
	}
	if res.ExternalID != nil {
		t.Errorf("ExternalID should be nil without a link, got %v", *res.ExternalID)
	}
}

// --- Refresh: deepsearch_read failure keeps link, fuzzy manifest ---------

func TestRefresh_ReadToolFailsKeepsLink(t *testing.T) {
	prose := "# Title\n\nAnswer body references github.com/example/foo.\nLink: https://sourcegraph.com/deepsearch/abc123\n"
	caller := &mockCaller{
		toolResponses: map[string][]string{DefaultDeepSearchTool: {prose}},
		toolErrors:    map[string][]error{DefaultDeepSearchReadTool: {errors.New("read boom")}},
	}
	e, _ := NewDeepSearchMCP(caller)
	res, err := e.Refresh(context.Background(), &evergreen.Document{Query: "q"})
	if err != nil {
		t.Fatalf("read failure should be non-fatal, got %v", err)
	}
	if res.ExternalID == nil || *res.ExternalID != "https://sourcegraph.com/deepsearch/abc123" {
		t.Errorf("ExternalID not preserved, got %v", res.ExternalID)
	}
	if len(res.Manifest) != 1 || !res.Manifest[0].Fuzzy {
		t.Errorf("expected 1 fuzzy entry, got %+v", res.Manifest)
	}
}

// --- Refresh: happy path, markdown sources parsed precisely --------------

const realisticMarkdown = `---
title: Informer pattern
share_url: https://sourcegraph.com/deepsearch/abc123
created: 2026-04-22T12:00:00Z
updated: 2026-04-22T12:05:00Z
---

# how does the informer pattern work?

Informers keep a local cache in sync with ...

## Sources

- [shared_informer.go](/github.com/kubernetes/kubernetes/-/blob/staging/src/k8s.io/client-go/tools/cache/shared_informer.go?L137-200)
- [reflector.go](/github.com/kubernetes/kubernetes/-/blob/staging/src/k8s.io/client-go/tools/cache/reflector.go?L55)
- [README.md](/github.com/kubernetes/api/-/blob/README.md?L1-10)

## Suggested Follow-ups

- What triggers a cache resync?
`

func TestRefresh_HappyPathMarkdownSources(t *testing.T) {
	caller := &mockCaller{
		toolResponses: map[string][]string{
			DefaultDeepSearchTool: {
				"# Informer pattern\n\nPreliminary answer.\nLink: https://sourcegraph.com/deepsearch/abc123\n",
			},
			DefaultDeepSearchReadTool: {realisticMarkdown},
		},
	}
	e, _ := NewDeepSearchMCP(caller)
	res, err := e.Refresh(context.Background(), &evergreen.Document{Query: "how does the informer pattern work?"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(caller.calls) != 2 {
		t.Errorf("expected 2 tool calls (deepsearch + deepsearch_read), got %d", len(caller.calls))
	}
	if res.RenderedAnswer != realisticMarkdown {
		t.Error("expected rendered answer to prefer the richer markdown")
	}
	if res.Backend != backendName {
		t.Errorf("Backend = %q, want %q", res.Backend, backendName)
	}
	if res.ExternalID == nil || *res.ExternalID != "https://sourcegraph.com/deepsearch/abc123" {
		t.Errorf("ExternalID = %v", res.ExternalID)
	}
	if len(res.Manifest) != 3 {
		t.Fatalf("want 3 manifest entries, got %d: %+v", len(res.Manifest), res.Manifest)
	}
	// First entry has a range.
	e0 := res.Manifest[0]
	if e0.Repo != "github.com/kubernetes/kubernetes" {
		t.Errorf("entry[0].Repo = %q", e0.Repo)
	}
	if e0.FilePath != "staging/src/k8s.io/client-go/tools/cache/shared_informer.go" {
		t.Errorf("entry[0].FilePath = %q", e0.FilePath)
	}
	if e0.LineStart != 137 || e0.LineEnd != 200 {
		t.Errorf("entry[0] lines = %d-%d, want 137-200", e0.LineStart, e0.LineEnd)
	}
	if e0.Fuzzy {
		t.Error("entry[0] should not be fuzzy")
	}
	// Second entry has a single line (start == end).
	e1 := res.Manifest[1]
	if e1.LineStart != 55 || e1.LineEnd != 55 {
		t.Errorf("single-line entry lines = %d-%d, want 55-55", e1.LineStart, e1.LineEnd)
	}
	// No symbol_id resolution without ClaimsReader.
	for _, e := range res.Manifest {
		if e.SymbolID != nil {
			t.Errorf("SymbolID must be nil without ClaimsReader, got %v", *e.SymbolID)
		}
	}
}

// --- Refresh: sources section absent -> fuzzy from read markdown ---------

func TestRefresh_SourcesMissingFuzzyFromRead(t *testing.T) {
	readWithoutSources := "# Title\n\nPlenty of prose mentioning github.com/example/nosources, repeatedly github.com/example/nosources.\n"
	caller := &mockCaller{
		toolResponses: map[string][]string{
			DefaultDeepSearchTool:     {"# T\nLink: https://sg/x\n"},
			DefaultDeepSearchReadTool: {readWithoutSources},
		},
	}
	e, _ := NewDeepSearchMCP(caller)
	res, err := e.Refresh(context.Background(), &evergreen.Document{Query: "q"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Manifest) != 1 || !res.Manifest[0].Fuzzy {
		t.Errorf("expected fuzzy fallback, got %+v", res.Manifest)
	}
}

// --- Refresh: triple fallback — answer prose rescues empty read ----------

// When the read markdown has no parseable sources AND no bare repo mentions,
// the executor must fall back to extracting repo mentions from the ORIGINAL
// deepsearch answer prose. Without this, drift detection is silently
// suppressed for the document.
func TestRefresh_TripleFallbackToAnswerProse(t *testing.T) {
	answer := "# T\nThe answer references github.com/example/proseonly throughout.\nLink: https://sg/x\n"
	readSparse := "# Title\n\nThe rendered answer is present but contains no repo hostnames or parseable source links.\n"
	caller := &mockCaller{
		toolResponses: map[string][]string{
			DefaultDeepSearchTool:     {answer},
			DefaultDeepSearchReadTool: {readSparse},
		},
	}
	e, _ := NewDeepSearchMCP(caller)
	res, err := e.Refresh(context.Background(), &evergreen.Document{Query: "q"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Manifest) != 1 || !res.Manifest[0].Fuzzy {
		t.Fatalf("expected 1 fuzzy entry from answer prose, got %+v", res.Manifest)
	}
	if res.Manifest[0].Repo != "github.com/example/proseonly" {
		t.Errorf("repo = %q, want github.com/example/proseonly", res.Manifest[0].Repo)
	}
}

// --- Refresh: dedup same (repo, file, range) -----------------------------

func TestRefresh_DedupsIdenticalSourceEntries(t *testing.T) {
	dup := `## Sources
- [a](/github.com/x/y/-/blob/f.go?L1-2)
- [b](/github.com/x/y/-/blob/f.go?L1-2)
- [c](/github.com/x/y/-/blob/f.go?L3)
`
	caller := &mockCaller{
		toolResponses: map[string][]string{
			DefaultDeepSearchTool:     {"Link: https://sg/x\n"},
			DefaultDeepSearchReadTool: {dup},
		},
	}
	e, _ := NewDeepSearchMCP(caller)
	res, _ := e.Refresh(context.Background(), &evergreen.Document{Query: "q"})
	if len(res.Manifest) != 2 {
		t.Errorf("expected 2 entries after dedup, got %d: %+v", len(res.Manifest), res.Manifest)
	}
}

// --- Refresh: unparseable links are skipped, not fatal -------------------

func TestRefresh_UnparseableLinksSkipped(t *testing.T) {
	mixed := `## Sources
- [bad](https://somewhere.else/file.go)
- [bad](/no-dash-blob-marker.go)
- [good](/github.com/x/y/-/blob/f.go?L1)
`
	caller := &mockCaller{
		toolResponses: map[string][]string{
			DefaultDeepSearchTool:     {"Link: https://sg/x\n"},
			DefaultDeepSearchReadTool: {mixed},
		},
	}
	e, _ := NewDeepSearchMCP(caller)
	res, _ := e.Refresh(context.Background(), &evergreen.Document{Query: "q"})
	if len(res.Manifest) != 1 {
		t.Fatalf("want 1 parseable entry, got %d: %+v", len(res.Manifest), res.Manifest)
	}
	if res.Manifest[0].Repo != "github.com/x/y" {
		t.Errorf("repo = %q", res.Manifest[0].Repo)
	}
}

// --- Refresh: ClaimsReader resolves symbol_id per entry ------------------

func TestRefresh_ClaimsReaderResolvesSymbolID(t *testing.T) {
	md := `## Sources
- [a](/github.com/x/y/-/blob/a.go?L10-20)
- [b](/github.com/x/y/-/blob/b.go?L5)
`
	claims := &mockClaims{
		resolveMap: map[string]int64{
			"github.com/x/y|a.go|10-20": 101,
			"github.com/x/y|b.go|5-5":   202,
		},
	}
	caller := &mockCaller{
		toolResponses: map[string][]string{
			DefaultDeepSearchTool:     {"Link: https://sg/x\n"},
			DefaultDeepSearchReadTool: {md},
		},
	}
	e, _ := NewDeepSearchMCP(caller, WithClaimsReader(claims))
	res, _ := e.Refresh(context.Background(), &evergreen.Document{Query: "q"})
	if len(res.Manifest) != 2 {
		t.Fatalf("want 2 entries, got %d", len(res.Manifest))
	}
	if res.Manifest[0].SymbolID == nil || *res.Manifest[0].SymbolID != 101 {
		t.Errorf("entry[0].SymbolID = %v, want 101", res.Manifest[0].SymbolID)
	}
	if res.Manifest[1].SymbolID == nil || *res.Manifest[1].SymbolID != 202 {
		t.Errorf("entry[1].SymbolID = %v, want 202", res.Manifest[1].SymbolID)
	}
}

// --- Refresh: ClaimsReader errors are tolerated --------------------------

func TestRefresh_ClaimsReaderErrorsTolerated(t *testing.T) {
	md := `## Sources
- [a](/github.com/x/y/-/blob/a.go?L10)
`
	claims := &mockClaims{resolveErr: errors.New("claims boom")}
	caller := &mockCaller{
		toolResponses: map[string][]string{
			DefaultDeepSearchTool:     {"Link: https://sg/x\n"},
			DefaultDeepSearchReadTool: {md},
		},
	}
	e, _ := NewDeepSearchMCP(caller, WithClaimsReader(claims))
	res, err := e.Refresh(context.Background(), &evergreen.Document{Query: "q"})
	if err != nil {
		t.Fatalf("claims errors must not fail Refresh, got %v", err)
	}
	if res.Manifest[0].SymbolID != nil {
		t.Errorf("SymbolID must stay nil when resolver errors, got %v", *res.Manifest[0].SymbolID)
	}
}

// --- Unit: parseSourceLink -----------------------------------------------

func TestParseSourceLink_Table(t *testing.T) {
	cases := []struct {
		name          string
		link          string
		wantOK        bool
		wantRepo      string
		wantFile      string
		wantStart     int
		wantEnd       int
	}{
		{"range", "/github.com/k8s/k8s/-/blob/foo/bar.go?L1-200", true, "github.com/k8s/k8s", "foo/bar.go", 1, 200},
		{"single line", "/github.com/x/y/-/blob/f.go?L55", true, "github.com/x/y", "f.go", 55, 55},
		{"range with fragment", "/github.com/x/y/-/blob/f.go?L10-20#tab-overview", true, "github.com/x/y", "f.go", 10, 20},
		{"deep path", "/gitlab.com/a/b/c/d/-/blob/deep/nested/file.go?L7", true, "gitlab.com/a/b/c/d", "deep/nested/file.go", 7, 7},
		// Future query-param tolerance: L may not be the first param.
		{"rev prefix", "/github.com/x/y/-/blob/f.go?rev=abc&L10-20", true, "github.com/x/y", "f.go", 10, 20},
		{"multi-prefix", "/github.com/x/y/-/blob/f.go?a=1&b=2&L5", true, "github.com/x/y", "f.go", 5, 5},
		{"missing line anchor", "/github.com/x/y/-/blob/f.go", false, "", "", 0, 0},
		{"missing blob marker", "/github.com/x/y/f.go?L1", false, "", "", 0, 0},
		{"absolute URL not relative", "https://sourcegraph.com/github.com/x/y/-/blob/f.go?L1", false, "", "", 0, 0},
		{"bad range (end < start) falls back to start", "/github.com/x/y/-/blob/f.go?L10-5", true, "github.com/x/y", "f.go", 10, 10},
		{"zero line rejected", "/github.com/x/y/-/blob/f.go?L0", false, "", "", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ent, ok := parseSourceLink(c.link)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if ent.Repo != c.wantRepo {
				t.Errorf("Repo = %q, want %q", ent.Repo, c.wantRepo)
			}
			if ent.FilePath != c.wantFile {
				t.Errorf("FilePath = %q, want %q", ent.FilePath, c.wantFile)
			}
			if ent.LineStart != c.wantStart {
				t.Errorf("LineStart = %d, want %d", ent.LineStart, c.wantStart)
			}
			if ent.LineEnd != c.wantEnd {
				t.Errorf("LineEnd = %d, want %d", ent.LineEnd, c.wantEnd)
			}
		})
	}
}

// --- Unit: extractLink ---------------------------------------------------

func TestExtractLink_Table(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"standard", "# T\nBody\nLink: https://sg/x\n", "https://sg/x"},
		{"lowercase prefix", "link: https://sg/y\n", "https://sg/y"},
		{"indented", "   Link:   https://sg/z\n", "https://sg/z"},
		{"absent", "no link here", ""},
		{"embedded in sentence only", "See the link at https://sg/w for more", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractLink(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// --- Unit: fuzzyManifestFromProse ----------------------------------------

func TestFuzzyManifestFromProse(t *testing.T) {
	in := "See github.com/foo/bar and gitlab.com/baz/qux and github.com/foo/bar again; bitbucket.org/a/b."
	got := fuzzyManifestFromProse(in)
	if len(got) != 3 {
		t.Fatalf("want 3 distinct repos, got %d: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, e := range got {
		if !e.Fuzzy {
			t.Error("expected Fuzzy=true for prose-derived entries")
		}
		seen[e.Repo] = true
	}
	for _, want := range []string{"github.com/foo/bar", "gitlab.com/baz/qux", "bitbucket.org/a/b"} {
		if !seen[want] {
			t.Errorf("missing %q in %+v", want, got)
		}
	}
}

func TestFuzzyManifestFromProse_Empty(t *testing.T) {
	if got := fuzzyManifestFromProse(""); got != nil {
		t.Errorf("empty prose should return nil, got %v", got)
	}
	if got := fuzzyManifestFromProse("no repo references here"); got != nil {
		t.Errorf("no references should return nil, got %v", got)
	}
}

// --- Unit: parseMarkdownSources deduplication + ordering -----------------

func TestParseMarkdownSources_DedupAndOrder(t *testing.T) {
	md := `
Some intro.

## Sources

- [a](/github.com/x/y/-/blob/a.go?L1)
- [b](/github.com/x/y/-/blob/b.go?L1)
- [a-dup](/github.com/x/y/-/blob/a.go?L1)
- [c](/github.com/x/y/-/blob/c.go?L1)
`
	got := parseMarkdownSources(md)
	if len(got) != 3 {
		t.Fatalf("want 3 after dedup, got %d", len(got))
	}
	wantFiles := []string{"a.go", "b.go", "c.go"}
	for i, want := range wantFiles {
		if got[i].FilePath != want {
			t.Errorf("got[%d].FilePath = %q, want %q", i, got[i].FilePath, want)
		}
	}
}

// Sanity: the sources regex does not match unrelated markdown bullet lists.
func TestParseMarkdownSources_DoesNotMatchBareBullets(t *testing.T) {
	md := `
- first item
- [link without url]()
- [text](not-a-sourcegraph-link)
`
	got := parseMarkdownSources(md)
	if len(got) != 0 {
		t.Errorf("expected no matches, got %d: %+v", len(got), got)
	}
}

// --- Sanity: confirm mock caller records args correctly ------------------

func TestMockCaller_ArgsPropagate(t *testing.T) {
	caller := &mockCaller{
		toolResponses: map[string][]string{
			DefaultDeepSearchTool:     {"Link: https://sg/x\n"},
			DefaultDeepSearchReadTool: {"## Sources\n- [a](/github.com/x/y/-/blob/a.go?L1)\n"},
		},
	}
	e, _ := NewDeepSearchMCP(caller)
	_, err := e.Refresh(context.Background(), &evergreen.Document{Query: "my specific query"})
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(caller.calls))
	}
	if caller.calls[0].tool != DefaultDeepSearchTool {
		t.Errorf("first call tool = %q", caller.calls[0].tool)
	}
	if q, _ := caller.calls[0].args["question"].(string); q != "my specific query" {
		t.Errorf("first call question arg = %q", q)
	}
	if caller.calls[1].tool != DefaultDeepSearchReadTool {
		t.Errorf("second call tool = %q", caller.calls[1].tool)
	}
	if id, _ := caller.calls[1].args["identifier"].(string); !strings.Contains(id, "https://sg/x") {
		t.Errorf("second call identifier arg = %q", id)
	}
}
