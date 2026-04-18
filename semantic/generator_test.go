package semantic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjarmak/livedocs/db"
)

// mockLLMClient returns a canned response for testing.
type mockLLMClient struct {
	response string
	err      error
	// captured inputs for assertion
	lastSystem string
	lastUser   string
}

func (m *mockLLMClient) Complete(_ context.Context, system, user string) (string, error) {
	m.lastSystem = system
	m.lastUser = user
	return m.response, m.err
}

// testDB creates a temporary claims DB with schema for testing.
func testDB(t *testing.T) *db.ClaimsDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	cdb, err := db.OpenClaimsDB(path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	return cdb
}

// seedPackage inserts a set of symbols and structural claims for testing.
func seedPackage(t *testing.T, cdb *db.ClaimsDB, importPath string, symbols []db.Symbol, claims map[string][]db.Claim) {
	t.Helper()
	for _, sym := range symbols {
		symID, err := cdb.UpsertSymbol(sym)
		if err != nil {
			t.Fatalf("upsert symbol %s: %v", sym.SymbolName, err)
		}
		for _, cl := range claims[sym.SymbolName] {
			cl.SubjectID = symID
			if _, err := cdb.InsertClaim(cl); err != nil {
				t.Fatalf("insert claim for %s: %v", sym.SymbolName, err)
			}
		}
	}
}

func TestParseLLMResponse_Valid(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"Pod": {SymbolName: "Pod", Language: "go", Kind: "type", Visibility: "public"},
		"Run": {SymbolName: "Run", Language: "go", Kind: "func", Visibility: "public"},
	}

	raw := `[
		{
			"subject_name": "Pod",
			"purpose": "Represents a running container group",
			"usage_pattern": "Created by controllers",
			"complexity": "complex",
			"stability": "stable"
		},
		{
			"subject_name": "Run",
			"purpose": "Starts the main server loop",
			"complexity": "moderate"
		}
	]`

	claims, err := parseLLMResponse(raw, symbolMap, "k8s.io/api/core/v1", "kubernetes/kubernetes")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Pod should produce 4 claims (purpose, usage_pattern, complexity, stability).
	// Run should produce 2 claims (purpose, complexity).
	if len(claims) != 6 {
		t.Fatalf("expected 6 claims, got %d", len(claims))
	}

	// Check confidence ranges.
	for _, c := range claims {
		if c.Confidence < 0.5 || c.Confidence > 0.8 {
			t.Errorf("claim %s/%s confidence %f outside [0.5, 0.8]",
				c.SubjectName, c.Predicate, c.Confidence)
		}
		if c.ClaimTier != "semantic" {
			t.Errorf("expected claim_tier=semantic, got %s", c.ClaimTier)
		}
		if c.Extractor != ExtractorName {
			t.Errorf("expected extractor=%s, got %s", ExtractorName, c.Extractor)
		}
	}
}

func TestParseLLMResponse_WithMarkdownFences(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"Foo": {SymbolName: "Foo", Language: "go", Kind: "func", Visibility: "public"},
	}

	raw := "```json\n" + `[{"subject_name": "Foo", "purpose": "Does things"}]` + "\n```"

	claims, err := parseLLMResponse(raw, symbolMap, "example.com/pkg", "test/repo")
	if err != nil {
		t.Fatalf("parse with fences: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claims))
	}
	if claims[0].ObjectText != "Does things" {
		t.Errorf("unexpected purpose: %s", claims[0].ObjectText)
	}
}

func TestParseLLMResponse_InvalidJSON(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"X": {SymbolName: "X", Language: "go", Kind: "type", Visibility: "public"},
	}
	_, err := parseLLMResponse("not json", symbolMap, "pkg", "repo")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseLLMResponse_UnknownSymbol(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"Known": {SymbolName: "Known", Language: "go", Kind: "func", Visibility: "public"},
	}
	raw := `[{"subject_name": "Unknown", "purpose": "Should be skipped"}]`
	claims, err := parseLLMResponse(raw, symbolMap, "pkg", "repo")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(claims) != 0 {
		t.Errorf("expected 0 claims for unknown symbol, got %d", len(claims))
	}
}

func TestParseLLMResponse_InvalidEnumValues(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"X": {SymbolName: "X", Language: "go", Kind: "type", Visibility: "public"},
	}
	raw := `[{"subject_name": "X", "complexity": "INVALID", "stability": "NOPE", "purpose": "Valid purpose"}]`
	claims, err := parseLLMResponse(raw, symbolMap, "pkg", "repo")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Only purpose should survive; complexity and stability should be filtered.
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim (purpose only), got %d", len(claims))
	}
	if string(claims[0].Predicate) != "purpose" {
		t.Errorf("expected purpose claim, got %s", claims[0].Predicate)
	}
}

func TestBuildUserPrompt(t *testing.T) {
	symbolClaims := []db.SymbolWithClaims{
		{
			Symbol: db.Symbol{SymbolName: "Pod", Kind: "type", Visibility: "public"},
			Claims: []db.Claim{
				{Predicate: "has_doc", ObjectText: "Pod is a top-level resource"},
				{Predicate: "has_signature", ObjectText: "type Pod struct"},
				{Predicate: "implements", ObjectText: "runtime.Object"},
			},
		},
		{
			Symbol: db.Symbol{SymbolName: "NewPod", Kind: "func", Visibility: "public"},
			Claims: []db.Claim{
				{Predicate: "has_signature", ObjectText: "func NewPod() *Pod"},
			},
		},
	}

	prompt := buildUserPrompt("k8s.io/api/core/v1", symbolClaims, 0)

	if !strings.Contains(prompt, "Package: k8s.io/api/core/v1") {
		t.Error("expected package header in prompt")
	}
	if !strings.Contains(prompt, "Symbol: Pod") {
		t.Error("expected Pod symbol in prompt")
	}
	if !strings.Contains(prompt, "doc: Pod is a top-level resource") {
		t.Error("expected doc claim in prompt")
	}
	if !strings.Contains(prompt, "implements: runtime.Object") {
		t.Error("expected implements claim in prompt")
	}
	if !strings.Contains(prompt, "Symbol: NewPod") {
		t.Error("expected NewPod symbol in prompt")
	}
}

func TestBuildUserPrompt_Truncation(t *testing.T) {
	var syms []db.SymbolWithClaims
	for i := 0; i < 10; i++ {
		syms = append(syms, db.SymbolWithClaims{
			Symbol: db.Symbol{SymbolName: fmt.Sprintf("Sym%d", i), Kind: "func", Visibility: "public"},
			Claims: []db.Claim{{Predicate: "defines", ObjectText: "x"}},
		})
	}

	prompt := buildUserPrompt("pkg", syms, 3)
	if !strings.Contains(prompt, "7 more symbols omitted") {
		t.Error("expected truncation notice in prompt")
	}
	// Should contain Sym0, Sym1, Sym2 but not Sym3+
	if !strings.Contains(prompt, "Sym2") {
		t.Error("expected Sym2 in prompt")
	}
	if strings.Contains(prompt, "Sym3") {
		t.Error("Sym3 should be omitted")
	}
}

func TestGenerateForPackage_EndToEnd(t *testing.T) {
	cdb := testDB(t)

	// Seed structural claims.
	symbols := []db.Symbol{
		{Repo: "test/repo", ImportPath: "example.com/pkg", SymbolName: "Foo",
			Language: "go", Kind: "type", Visibility: "public"},
		{Repo: "test/repo", ImportPath: "example.com/pkg", SymbolName: "Bar",
			Language: "go", Kind: "func", Visibility: "public"},
	}
	claimsMap := map[string][]db.Claim{
		"Foo": {
			{Predicate: "defines", ObjectText: "type Foo", SourceFile: "foo.go",
				Confidence: 1.0, ClaimTier: "structural", Extractor: "go-deep",
				ExtractorVersion: "1.0", LastVerified: db.Now()},
			{Predicate: "has_doc", ObjectText: "Foo does things", SourceFile: "foo.go",
				Confidence: 0.9, ClaimTier: "structural", Extractor: "go-deep",
				ExtractorVersion: "1.0", LastVerified: db.Now()},
		},
		"Bar": {
			{Predicate: "defines", ObjectText: "func Bar()", SourceFile: "bar.go",
				Confidence: 1.0, ClaimTier: "structural", Extractor: "go-deep",
				ExtractorVersion: "1.0", LastVerified: db.Now()},
		},
	}
	seedPackage(t, cdb, "example.com/pkg", symbols, claimsMap)

	// Mock LLM response.
	mock := &mockLLMClient{
		response: `[
			{"subject_name": "Foo", "purpose": "Main data type", "complexity": "simple", "stability": "stable"},
			{"subject_name": "Bar", "purpose": "Utility function", "usage_pattern": "Called at startup"}
		]`,
	}

	gen, err := NewGenerator(cdb, mock, "test/repo")
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}

	result, err := gen.GenerateForPackage(context.Background(), "example.com/pkg")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Foo: purpose + complexity + stability = 3
	// Bar: purpose + usage_pattern = 2
	if result.ClaimsStored != 5 {
		t.Errorf("expected 5 stored claims, got %d", result.ClaimsStored)
	}

	// Verify claims are in DB with predicate "purpose".
	purposeClaims, err := cdb.GetClaimsByPredicate("purpose")
	if err != nil {
		t.Fatalf("get purpose claims: %v", err)
	}
	if len(purposeClaims) != 2 {
		t.Errorf("expected 2 purpose claims in DB, got %d", len(purposeClaims))
	}
	for _, c := range purposeClaims {
		if c.ClaimTier != "semantic" {
			t.Errorf("expected claim_tier=semantic, got %s", c.ClaimTier)
		}
	}

	// Verify the prompt included structural context.
	if !strings.Contains(mock.lastUser, "Foo") {
		t.Error("expected Foo in prompt sent to LLM")
	}
}

func TestGenerateForPackage_Idempotent(t *testing.T) {
	cdb := testDB(t)

	symbols := []db.Symbol{
		{Repo: "r", ImportPath: "pkg", SymbolName: "X",
			Language: "go", Kind: "type", Visibility: "public"},
	}
	claimsMap := map[string][]db.Claim{
		"X": {
			{Predicate: "defines", ObjectText: "type X", SourceFile: "x.go",
				Confidence: 1.0, ClaimTier: "structural", Extractor: "go-deep",
				ExtractorVersion: "1.0", LastVerified: db.Now()},
		},
	}
	seedPackage(t, cdb, "pkg", symbols, claimsMap)

	mock := &mockLLMClient{
		response: `[{"subject_name": "X", "purpose": "Test type"}]`,
	}

	gen, _ := NewGenerator(cdb, mock, "r")

	// Run twice — should be idempotent.
	gen.GenerateForPackage(context.Background(), "pkg")
	result, err := gen.GenerateForPackage(context.Background(), "pkg")
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}
	if result.ClaimsStored != 1 {
		t.Errorf("expected 1 claim on second run, got %d", result.ClaimsStored)
	}

	// Should have exactly 1 purpose claim, not 2.
	purposeClaims, _ := cdb.GetClaimsByPredicate("purpose")
	if len(purposeClaims) != 1 {
		t.Errorf("expected 1 purpose claim after idempotent re-run, got %d", len(purposeClaims))
	}
}

func TestGenerateForPackage_EmptyPackage(t *testing.T) {
	cdb := testDB(t)
	mock := &mockLLMClient{response: "[]"}
	gen, _ := NewGenerator(cdb, mock, "r")

	result, err := gen.GenerateForPackage(context.Background(), "nonexistent/pkg")
	if err != nil {
		t.Fatalf("generate empty: %v", err)
	}
	if result.ClaimsStored != 0 {
		t.Errorf("expected 0 claims for empty package, got %d", result.ClaimsStored)
	}
}

func TestGenerateForPackage_LLMError(t *testing.T) {
	cdb := testDB(t)

	symbols := []db.Symbol{
		{Repo: "r", ImportPath: "pkg", SymbolName: "X",
			Language: "go", Kind: "type", Visibility: "public"},
	}
	seedPackage(t, cdb, "pkg", symbols, map[string][]db.Claim{
		"X": {{Predicate: "defines", ObjectText: "type X", SourceFile: "x.go",
			Confidence: 1.0, ClaimTier: "structural", Extractor: "go-deep",
			ExtractorVersion: "1.0", LastVerified: db.Now()}},
	})

	mock := &mockLLMClient{err: fmt.Errorf("rate limited")}
	gen, _ := NewGenerator(cdb, mock, "r")

	_, err := gen.GenerateForPackage(context.Background(), "pkg")
	if err == nil {
		t.Error("expected error when LLM fails")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("expected rate limited error, got: %v", err)
	}
}

func TestGenerateBatch(t *testing.T) {
	cdb := testDB(t)

	// Seed two packages.
	for _, pkg := range []string{"pkg/a", "pkg/b"} {
		symbols := []db.Symbol{
			{Repo: "r", ImportPath: pkg, SymbolName: "Sym",
				Language: "go", Kind: "func", Visibility: "public"},
		}
		seedPackage(t, cdb, pkg, symbols, map[string][]db.Claim{
			"Sym": {{Predicate: "defines", ObjectText: "func Sym()", SourceFile: "s.go",
				Confidence: 1.0, ClaimTier: "structural", Extractor: "go-deep",
				ExtractorVersion: "1.0", LastVerified: db.Now()}},
		})
	}

	mock := &mockLLMClient{
		response: `[{"subject_name": "Sym", "purpose": "Does something"}]`,
	}
	gen, _ := NewGenerator(cdb, mock, "r")

	br, err := gen.GenerateBatch(context.Background(), []string{"pkg/a", "pkg/b", "pkg/nonexistent"})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(br.Packages) != 3 {
		t.Fatalf("expected 3 package results, got %d", len(br.Packages))
	}
	if br.TotalClaims != 2 {
		t.Errorf("expected 2 total claims, got %d", br.TotalClaims)
	}
	if br.TotalSkipped != 1 {
		t.Errorf("expected 1 skipped (nonexistent), got %d", br.TotalSkipped)
	}
}

func TestGenerateBatch_ContextCancellation(t *testing.T) {
	cdb := testDB(t)

	symbols := []db.Symbol{
		{Repo: "r", ImportPath: "pkg", SymbolName: "X",
			Language: "go", Kind: "type", Visibility: "public"},
	}
	seedPackage(t, cdb, "pkg", symbols, map[string][]db.Claim{
		"X": {{Predicate: "defines", ObjectText: "type X", SourceFile: "x.go",
			Confidence: 1.0, ClaimTier: "structural", Extractor: "go-deep",
			ExtractorVersion: "1.0", LastVerified: db.Now()}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	mock := &mockLLMClient{response: `[]`}
	gen, _ := NewGenerator(cdb, mock, "r")

	_, err := gen.GenerateBatch(ctx, []string{"pkg"})
	if err == nil {
		t.Error("expected context cancellation error")
	}
}

func TestNewGenerator_Validation(t *testing.T) {
	cdb := testDB(t)
	mock := &mockLLMClient{}

	if _, err := NewGenerator(nil, mock, "r"); err == nil {
		t.Error("expected error for nil claimsDB")
	}
	if _, err := NewGenerator(cdb, nil, "r"); err == nil {
		t.Error("expected error for nil client")
	}
	if _, err := NewGenerator(cdb, mock, ""); err == nil {
		t.Error("expected error for empty repo")
	}
}

func TestStripMarkdownFences(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no fences", `[{"x":1}]`, `[{"x":1}]`},
		{"json fences", "```json\n[{\"x\":1}]\n```", `[{"x":1}]`},
		{"plain fences", "```\n[{\"x\":1}]\n```", `[{"x":1}]`},
		{"with whitespace", "  ```json\n[{\"x\":1}]\n```  ", `[{"x":1}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMarkdownFences(tt.input)
			if got != tt.expected {
				t.Errorf("stripMarkdownFences(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestAnthropicClient_Complete(t *testing.T) {
	// Create a test server that returns a canned response.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request headers.
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key=test-key, got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("unexpected anthropic-version: %s", r.Header.Get("anthropic-version"))
		}

		resp := messagesResponse{
			Content: []contentBlock{{Type: "text", Text: `[{"subject_name":"X","purpose":"test"}]`}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewAnthropicClient("test-key", WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	text, err := client.Complete(context.Background(), "system msg", "user msg")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !strings.Contains(text, "test") {
		t.Errorf("unexpected response: %s", text)
	}
}

func TestAnthropicClient_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"type":"rate_limit","message":"too fast"}}`))
	}))
	defer server.Close()

	client, _ := NewAnthropicClient("test-key", WithBaseURL(server.URL))
	_, err := client.Complete(context.Background(), "", "test")
	if err == nil {
		t.Error("expected error for 429 response")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("expected 429 in error, got: %v", err)
	}
}

func TestAnthropicClient_EmptyKey(t *testing.T) {
	_, err := NewAnthropicClient("")
	if err == nil {
		t.Error("expected error for empty API key")
	}
}
