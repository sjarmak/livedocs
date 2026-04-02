package extractor

import (
	"strings"
	"testing"
	"time"
)

func TestVisibility_IsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		v    Visibility
		want bool
	}{
		{VisibilityPublic, true},
		{VisibilityInternal, true},
		{VisibilityPrivate, true},
		{VisibilityReExported, true},
		{VisibilityConditional, true},
		{"unknown", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.v.IsValid(); got != tt.want {
			t.Errorf("Visibility(%q).IsValid() = %v, want %v", tt.v, got, tt.want)
		}
	}
}

func TestSymbolKind_IsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		k    SymbolKind
		want bool
	}{
		{KindType, true},
		{KindFunc, true},
		{KindConst, true},
		{KindVar, true},
		{KindClass, true},
		{KindModule, true},
		{KindMethod, true},
		{KindField, true},
		{KindEnum, true},
		{KindProperty, true},
		{"unknown", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.k.IsValid(); got != tt.want {
			t.Errorf("SymbolKind(%q).IsValid() = %v, want %v", tt.k, got, tt.want)
		}
	}
}

func TestPredicate_IsValid(t *testing.T) {
	t.Parallel()
	valid := []Predicate{
		PredicateDefines, PredicateImports, PredicateExports,
		PredicateHasDoc, PredicateIsTest, PredicateIsGenerated,
		PredicateHasKind, PredicateImplements, PredicateHasSignature, PredicateEncloses,
	}
	for _, p := range valid {
		if !p.IsValid() {
			t.Errorf("Predicate(%q).IsValid() = false, want true", p)
		}
	}
	if Predicate("bogus").IsValid() {
		t.Error("Predicate(\"bogus\").IsValid() = true, want false")
	}
}

func TestPredicate_Boundary(t *testing.T) {
	t.Parallel()
	treeSitter := []Predicate{
		PredicateDefines, PredicateImports, PredicateExports,
		PredicateHasDoc, PredicateIsTest, PredicateIsGenerated,
	}
	for _, p := range treeSitter {
		if !p.IsTreeSitterSafe() {
			t.Errorf("Predicate(%q).IsTreeSitterSafe() = false, want true", p)
		}
		if p.IsDeepOnly() {
			t.Errorf("Predicate(%q).IsDeepOnly() = true, want false", p)
		}
	}

	deep := []Predicate{
		PredicateHasKind, PredicateImplements, PredicateHasSignature, PredicateEncloses,
	}
	for _, p := range deep {
		if p.IsTreeSitterSafe() {
			t.Errorf("Predicate(%q).IsTreeSitterSafe() = true, want false", p)
		}
		if !p.IsDeepOnly() {
			t.Errorf("Predicate(%q).IsDeepOnly() = false, want true", p)
		}
	}
}

func TestClaimTier_IsValid(t *testing.T) {
	t.Parallel()
	if !TierStructural.IsValid() {
		t.Error("TierStructural.IsValid() = false")
	}
	if !TierSemantic.IsValid() {
		t.Error("TierSemantic.IsValid() = false")
	}
	if ClaimTier("bogus").IsValid() {
		t.Error("ClaimTier(\"bogus\").IsValid() = true")
	}
}

func validClaim() Claim {
	return Claim{
		SubjectRepo:       "kubernetes/kubernetes",
		SubjectImportPath: "k8s.io/api/core/v1",
		SubjectName:       "Pod",
		Language:          "go",
		Kind:              KindType,
		Visibility:        VisibilityPublic,
		Predicate:         PredicateDefines,
		SourceFile:        "staging/src/k8s.io/api/core/v1/types.go",
		Confidence:        1.0,
		ClaimTier:         TierStructural,
		Extractor:         "go-deep",
		ExtractorVersion:  "1.0.0",
		LastVerified:      time.Now(),
	}
}

func TestClaim_Validate_Valid(t *testing.T) {
	t.Parallel()
	c := validClaim()
	if err := c.Validate(); err != nil {
		t.Fatalf("valid claim failed validation: %v", err)
	}
}

func TestClaim_Validate_MissingFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Claim)
		substr string
	}{
		{"missing repo", func(c *Claim) { c.SubjectRepo = "" }, "subject_repo"},
		{"missing import_path", func(c *Claim) { c.SubjectImportPath = "" }, "subject_import_path"},
		{"missing name", func(c *Claim) { c.SubjectName = "" }, "subject_name"},
		{"missing language", func(c *Claim) { c.Language = "" }, "language"},
		{"invalid kind", func(c *Claim) { c.Kind = "bad" }, "invalid kind"},
		{"invalid visibility", func(c *Claim) { c.Visibility = "bad" }, "invalid visibility"},
		{"invalid predicate", func(c *Claim) { c.Predicate = "bad" }, "invalid predicate"},
		{"missing source_file", func(c *Claim) { c.SourceFile = "" }, "source_file"},
		{"confidence too low", func(c *Claim) { c.Confidence = -0.1 }, "confidence"},
		{"confidence too high", func(c *Claim) { c.Confidence = 1.1 }, "confidence"},
		{"invalid tier", func(c *Claim) { c.ClaimTier = "bad" }, "claim_tier"},
		{"missing extractor", func(c *Claim) { c.Extractor = "" }, "extractor"},
		{"missing extractor_version", func(c *Claim) { c.ExtractorVersion = "" }, "extractor_version"},
		{"zero last_verified", func(c *Claim) { c.LastVerified = time.Time{} }, "last_verified"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := validClaim()
			tt.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.substr)
			}
			if got := err.Error(); !strings.Contains(got, tt.substr) {
				t.Errorf("error %q does not contain %q", got, tt.substr)
			}
		})
	}
}
