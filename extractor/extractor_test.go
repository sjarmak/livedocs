package extractor

import (
	"context"
	"testing"
	"time"
)

// mockExtractor is a test double that returns canned claims.
type mockExtractor struct {
	name    string
	version string
	claims  []Claim
	err     error
}

func (m *mockExtractor) Extract(_ context.Context, _ string, _ string) ([]Claim, error) {
	return m.claims, m.err
}
func (m *mockExtractor) ExtractBytes(_ context.Context, _ []byte, _ string, _ string) ([]Claim, error) {
	return m.claims, m.err
}
func (m *mockExtractor) Name() string    { return m.name }
func (m *mockExtractor) Version() string { return m.version }

// mockTreeSitterExtractor satisfies TreeSitterExtractor.
type mockTreeSitterExtractor struct {
	mockExtractor
}

func (m *mockTreeSitterExtractor) IsTreeSitter() {}

func TestValidateTreeSitterClaims_AllSafe(t *testing.T) {
	t.Parallel()
	claims := []Claim{
		{Predicate: PredicateDefines, SubjectName: "Foo"},
		{Predicate: PredicateImports, SubjectName: "Bar"},
		{Predicate: PredicateExports, SubjectName: "Baz"},
		{Predicate: PredicateHasDoc, SubjectName: "Qux"},
		{Predicate: PredicateIsTest, SubjectName: "Test1"},
		{Predicate: PredicateIsGenerated, SubjectName: "Gen1"},
	}
	if err := ValidateTreeSitterClaims(claims); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTreeSitterClaims_DeepOnlyRejected(t *testing.T) {
	t.Parallel()
	claims := []Claim{
		{Predicate: PredicateDefines, SubjectName: "Foo"},
		{Predicate: PredicateImplements, SubjectName: "Bar"},
	}
	err := ValidateTreeSitterClaims(claims)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	pbe, ok := err.(*PredicateBoundaryError)
	if !ok {
		t.Fatalf("expected *PredicateBoundaryError, got %T", err)
	}
	if pbe.Predicate != PredicateImplements {
		t.Errorf("got predicate %q, want %q", pbe.Predicate, PredicateImplements)
	}
	if pbe.Symbol != "Bar" {
		t.Errorf("got symbol %q, want %q", pbe.Symbol, "Bar")
	}
}

func TestValidateTreeSitterClaims_Empty(t *testing.T) {
	t.Parallel()
	if err := ValidateTreeSitterClaims(nil); err != nil {
		t.Fatalf("unexpected error on nil: %v", err)
	}
	if err := ValidateTreeSitterClaims([]Claim{}); err != nil {
		t.Fatalf("unexpected error on empty: %v", err)
	}
}

func TestExtractorInterface_MockSatisfies(t *testing.T) {
	t.Parallel()
	// Compile-time check that mock types satisfy the interfaces.
	var _ Extractor = (*mockExtractor)(nil)
	var _ Extractor = (*mockTreeSitterExtractor)(nil)
	var _ TreeSitterExtractor = (*mockTreeSitterExtractor)(nil)
}

func TestMockExtractor_ReturnsClaimsAndError(t *testing.T) {
	t.Parallel()
	now := time.Now()
	claims := []Claim{{
		SubjectRepo:       "test/repo",
		SubjectImportPath: "test/pkg",
		SubjectName:       "Foo",
		Language:          "go",
		Kind:              KindFunc,
		Visibility:        VisibilityPublic,
		Predicate:         PredicateDefines,
		SourceFile:        "foo.go",
		Confidence:        1.0,
		ClaimTier:         TierStructural,
		Extractor:         "mock",
		ExtractorVersion:  "0.0.1",
		LastVerified:      now,
	}}
	m := &mockExtractor{name: "mock", version: "0.0.1", claims: claims}
	got, err := m.Extract(context.Background(), "foo.go", "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	if got[0].SubjectName != "Foo" {
		t.Errorf("got name %q, want %q", got[0].SubjectName, "Foo")
	}
}
