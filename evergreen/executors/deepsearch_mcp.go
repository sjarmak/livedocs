package executors

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sjarmak/livedocs/evergreen"
)

// MCPCaller is the minimal view of a Sourcegraph MCP client the deepsearch
// executor depends on. *sourcegraph.SourcegraphClient satisfies it; tests
// pass a mock. Keeping the interface narrow avoids a hard dependency on the
// sourcegraph subprocess machinery for executor unit tests.
type MCPCaller interface {
	CallTool(ctx context.Context, toolName string, args map[string]any) (string, error)
}

// DefaultDeepSearchTool and DefaultDeepSearchReadTool are the tool names used
// when NewDeepSearchMCP is called without explicit overrides. They match the
// names sourcegraph's MCP server exposes in non-MCPV1 mode; the sg_-prefixed
// variants are set via options.
const (
	DefaultDeepSearchTool     = "deepsearch"
	DefaultDeepSearchReadTool = "deepsearch_read"
	backendName               = "deepsearch-mcp"
)

// DeepSearchMCP implements evergreen.RefreshExecutor by calling a Sourcegraph
// MCP server's deepsearch and deepsearch_read tools and parsing the resulting
// markdown for citation entries.
//
// Precision budget (see bead live_docs-8yc.6 audit): entries carry
// repo, file_path, and line range. SymbolID is resolved via an optional
// ClaimsReader. ContentHashAtRender and SignatureHashAtRender are left empty
// because the MCP wire does not expose them; bead live_docs-8yc.9 tracks the
// Phase 2 hash-capture follow-up.
//
// When the markdown sources section is absent or unparseable, Refresh falls
// back to one fuzzy manifest entry per repo mentioned in the prose.
type DeepSearchMCP struct {
	caller    MCPCaller
	claims    evergreen.ClaimsReader // optional; nil disables symbol_id resolution
	toolName  string
	readTool  string
}

// Option configures a DeepSearchMCP executor.
type Option func(*DeepSearchMCP)

// WithClaimsReader injects a ClaimsReader used to resolve symbol_id for each
// parsed citation. When nil (the default), manifest entries have SymbolID == nil
// but are not marked fuzzy unless the source link itself could not be parsed.
func WithClaimsReader(r evergreen.ClaimsReader) Option {
	return func(e *DeepSearchMCP) { e.claims = r }
}

// WithToolNames overrides the MCP tool names for the deepsearch and
// deepsearch_read tools. Use this to switch to the sg_-prefixed MCPV1 names
// when the Sourcegraph server is running in that mode.
func WithToolNames(deepsearch, deepsearchRead string) Option {
	return func(e *DeepSearchMCP) {
		if deepsearch != "" {
			e.toolName = deepsearch
		}
		if deepsearchRead != "" {
			e.readTool = deepsearchRead
		}
	}
}

// NewDeepSearchMCP constructs a DeepSearchMCP executor wrapping caller.
// caller is required and must not be nil; options are applied in order.
func NewDeepSearchMCP(caller MCPCaller, opts ...Option) (*DeepSearchMCP, error) {
	if caller == nil {
		return nil, errors.New("executors: NewDeepSearchMCP requires a non-nil MCPCaller")
	}
	e := &DeepSearchMCP{
		caller:   caller,
		toolName: DefaultDeepSearchTool,
		readTool: DefaultDeepSearchReadTool,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

// Name implements evergreen.RefreshExecutor.
func (e *DeepSearchMCP) Name() string { return backendName }

// Refresh runs the saved query through the sourcegraph MCP deepsearch tool,
// fetches the rendered markdown via deepsearch_read, parses citations, and
// returns a RefreshResult.
//
// Errors from the first deepsearch call are fatal. Errors from deepsearch_read
// or markdown parsing are non-fatal: the caller still gets the prose answer
// plus a fuzzy manifest derived from the prose.
func (e *DeepSearchMCP) Refresh(ctx context.Context, doc *evergreen.Document) (evergreen.RefreshResult, error) {
	if doc == nil {
		return evergreen.RefreshResult{}, errors.New("executors: Refresh requires a non-nil Document")
	}
	if strings.TrimSpace(doc.Query) == "" {
		return evergreen.RefreshResult{}, errors.New("executors: Document.Query is empty")
	}

	answerText, err := e.caller.CallTool(ctx, e.toolName, map[string]any{"question": doc.Query})
	if err != nil {
		return evergreen.RefreshResult{}, fmt.Errorf("executors: %s call failed: %w", e.toolName, err)
	}

	// Extract the share URL that ToText() renders as "Link: <url>".
	link := extractLink(answerText)

	// If we have no link, we can't call deepsearch_read — fall back to a
	// fuzzy manifest derived from the prose.
	if link == "" {
		return evergreen.RefreshResult{
			RenderedAnswer: answerText,
			Manifest:       fuzzyManifestFromProse(answerText),
			Backend:        backendName,
		}, nil
	}

	readText, readErr := e.caller.CallTool(ctx, e.readTool, map[string]any{"identifier": link})
	if readErr != nil {
		// Non-fatal: keep the answer, fall back to fuzzy manifest.
		return evergreen.RefreshResult{
			RenderedAnswer: answerText,
			Manifest:       fuzzyManifestFromProse(answerText),
			Backend:        backendName,
			ExternalID:     strPtr(link),
		}, nil
	}

	entries := parseMarkdownSources(readText)
	if len(entries) == 0 {
		// Sources section missing or unparseable — fuzzy fallback from the
		// read markdown first, which usually has more repo mentions.
		entries = fuzzyManifestFromProse(readText)
	}
	if len(entries) == 0 {
		// Neither read markdown nor its prose yielded anything; fall back
		// to the original answer prose. Without this, a doc with no parseable
		// citations silently returns an empty manifest, which the detector
		// interprets as "no dependencies" — suppressing all drift signals.
		entries = fuzzyManifestFromProse(answerText)
	}

	// Optional symbol_id resolution per entry. Failures are tolerated —
	// SymbolID simply stays nil.
	if e.claims != nil {
		for i := range entries {
			if entries[i].Fuzzy || entries[i].LineStart == 0 {
				continue
			}
			symID, err := e.claims.ResolveSymbolByLocation(
				ctx, entries[i].Repo, entries[i].FilePath,
				entries[i].LineStart, entries[i].LineEnd,
			)
			if err == nil {
				s := symID
				entries[i].SymbolID = &s
			}
		}
	}

	// Prefer the richer markdown as the rendered answer when available.
	rendered := readText
	if strings.TrimSpace(rendered) == "" {
		rendered = answerText
	}

	return evergreen.RefreshResult{
		RenderedAnswer: rendered,
		Manifest:       entries,
		Backend:        backendName,
		ExternalID:     strPtr(link),
	}, nil
}

// linkLineRe matches the "Link: <url>" line emitted by DeepsearchResponse.ToText.
// Case-insensitive on the prefix so small formatting drift upstream doesn't
// break extraction silently.
var linkLineRe = regexp.MustCompile(`(?im)^\s*Link:\s*(\S+)\s*$`)

func extractLink(prose string) string {
	m := linkLineRe.FindStringSubmatch(prose)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// sourceLineRe matches a single markdown list entry of the form
//   - [<label>](<link>)
// as emitted by FormatConversationAsMarkdown in the sourcegraph MCP server.
//
// The URL capture is terminated by the first ')', which assumes sourcegraph
// percent-encodes literal parens in source paths (its SourceItem.Link is a
// "relative path, starting with /" per the internal schema). If upstream
// ever emits unencoded '(' or ')' in paths, those entries will be silently
// dropped; future audit hook.
var sourceLineRe = regexp.MustCompile(`(?m)^\s*-\s+\[([^\]]+)\]\(([^)]+)\)\s*$`)

// sourceLinkRe parses a sourcegraph relative link of the form
//   /<repo>/-/blob/<file_path>?[<prefix>&]L<start>[-<end>][#...]
// The optional <prefix> tolerates future query params such as ?rev=X&L10.
// Captures: 1=repo, 2=file_path, 3=line_start, 4=line_end (optional).
var sourceLinkRe = regexp.MustCompile(`^/([^?#]+?)/-/blob/([^?#]+)\?(?:[^#]*&)?L(\d+)(?:-(\d+))?(?:[#&].*)?$`)

// parseMarkdownSources extracts citation entries from the ## Sources section
// of the markdown document produced by sg_deepsearch_read.
//
// The parser scans for list entries anywhere in the document (not just inside
// a ## Sources heading) because the upstream markdown generator may add
// non-Sources list items and we'd rather over-match parseable links than
// silently drop them. Entries whose links do not match the sourcegraph
// relative-blob-link shape are skipped; unparseable documents fall back to
// fuzzy extraction one level up.
func parseMarkdownSources(md string) []evergreen.ManifestEntry {
	if md == "" {
		return nil
	}
	lineMatches := sourceLineRe.FindAllStringSubmatch(md, -1)
	if len(lineMatches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(lineMatches))
	entries := make([]evergreen.ManifestEntry, 0, len(lineMatches))

	for _, m := range lineMatches {
		link := strings.TrimSpace(m[2])
		if link == "" {
			continue
		}
		ent, ok := parseSourceLink(link)
		if !ok {
			continue
		}
		// Deduplicate on (repo, file_path, line_start, line_end).
		// Invariant: parseSourceLink rejects LineStart == 0, so keys with
		// "0-0" cannot collide legitimately here. If that guard is relaxed,
		// this dedup key must be revisited.
		key := ent.Repo + "|" + ent.FilePath + "|" +
			strconv.Itoa(ent.LineStart) + "-" + strconv.Itoa(ent.LineEnd)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, ent)
	}
	return entries
}

func parseSourceLink(link string) (evergreen.ManifestEntry, bool) {
	m := sourceLinkRe.FindStringSubmatch(link)
	if len(m) < 4 {
		return evergreen.ManifestEntry{}, false
	}
	repo := m[1]
	filePath := m[2]
	lineStart, err := strconv.Atoi(m[3])
	if err != nil || lineStart <= 0 {
		return evergreen.ManifestEntry{}, false
	}
	lineEnd := lineStart
	if m[4] != "" {
		if v, err := strconv.Atoi(m[4]); err == nil && v >= lineStart {
			lineEnd = v
		}
	}
	return evergreen.ManifestEntry{
		Repo:      repo,
		FilePath:  filePath,
		LineStart: lineStart,
		LineEnd:   lineEnd,
	}, true
}

// repoMentionRe matches common sourcegraph repo references in prose, e.g.
//   github.com/kubernetes/kubernetes
//   sourcegraph.com/search?q=...  (deliberately skipped via prefix filter)
// The pattern is intentionally conservative — the fuzzy fallback is a last
// resort that shouldn't fabricate repo references.
var repoMentionRe = regexp.MustCompile(`\b(github\.com|gitlab\.com|bitbucket\.org)/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+\b`)

// fuzzyManifestFromProse is the last-resort fallback when no parseable source
// links are found. It emits one fuzzy manifest entry per distinct repo
// mentioned in the prose. CommitSHA is left empty (unknown) — consumers
// interpret that as "repo-level drift only" in the detector.
func fuzzyManifestFromProse(prose string) []evergreen.ManifestEntry {
	if prose == "" {
		return nil
	}
	matches := repoMentionRe.FindAllString(prose, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	entries := make([]evergreen.ManifestEntry, 0, len(matches))
	for _, r := range matches {
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		entries = append(entries, evergreen.ManifestEntry{
			Repo:  r,
			Fuzzy: true,
		})
	}
	return entries
}

func strPtr(s string) *string { return &s }
