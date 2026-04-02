package semantic

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/live-docs/live_docs/db"
	"github.com/live-docs/live_docs/extractor"
)

func makeClaim(name string, pred extractor.Predicate, text string) extractor.Claim {
	return extractor.Claim{
		SubjectRepo:       "test/repo",
		SubjectImportPath: "example.com/pkg",
		SubjectName:       name,
		Language:          "go",
		Kind:              extractor.KindType,
		Visibility:        extractor.VisibilityPublic,
		Predicate:         pred,
		ObjectText:        text,
		SourceFile:        "llm-semantic",
		Confidence:        0.7,
		ClaimTier:         extractor.TierSemantic,
		Extractor:         ExtractorName,
		ExtractorVersion:  Version,
		LastVerified:      time.Now().UTC(),
	}
}

func TestVerifier_StructuralReject_FabricatedSymbol(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"RealSymbol": {SymbolName: "RealSymbol"},
	}

	claims := []extractor.Claim{
		makeClaim("FakeSymbol", extractor.PredicatePurpose, "This symbol does not exist"),
	}

	mock := &mockLLMClient{response: `[]`}
	v := NewVerifier(mock, nil)

	accepted, summary, err := v.Verify(context.Background(), claims, symbolMap, "context")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if len(accepted) != 0 {
		t.Errorf("expected 0 accepted claims for fabricated symbol, got %d", len(accepted))
	}
	if summary.Rejected != 1 {
		t.Errorf("expected 1 rejected, got %d", summary.Rejected)
	}
	if summary.Results[0].Reason != "symbol not found in structural claims" {
		t.Errorf("unexpected reason: %s", summary.Results[0].Reason)
	}
}

func TestVerifier_AdversarialReject_WrongPurpose(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"Pod": {SymbolName: "Pod", Language: "go", Kind: "type"},
	}

	claims := []extractor.Claim{
		makeClaim("Pod", extractor.PredicatePurpose, "Handles network routing"),
	}

	mock := &mockLLMClient{
		response: `[{"subject_name": "Pod", "predicate": "purpose", "verdict": "reject", "reason": "Pod represents a container group, not network routing"}]`,
	}
	v := NewVerifier(mock, nil)

	accepted, summary, err := v.Verify(context.Background(), claims, symbolMap, "Symbol: Pod (kind=type)")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if len(accepted) != 0 {
		t.Errorf("expected 0 accepted, got %d", len(accepted))
	}
	if summary.Rejected != 1 {
		t.Errorf("expected 1 rejected, got %d", summary.Rejected)
	}
}

func TestVerifier_AdversarialAccept_CorrectClaim(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"Pod": {SymbolName: "Pod", Language: "go", Kind: "type"},
	}

	claims := []extractor.Claim{
		makeClaim("Pod", extractor.PredicatePurpose, "Represents a group of containers"),
	}

	mock := &mockLLMClient{
		response: `[{"subject_name": "Pod", "predicate": "purpose", "verdict": "accept", "reason": "Consistent with structural evidence"}]`,
	}
	v := NewVerifier(mock, nil)

	accepted, summary, err := v.Verify(context.Background(), claims, symbolMap, "Symbol: Pod (kind=type)")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if len(accepted) != 1 {
		t.Fatalf("expected 1 accepted, got %d", len(accepted))
	}
	if summary.Accepted != 1 {
		t.Errorf("expected 1 accepted in summary, got %d", summary.Accepted)
	}
	if accepted[0].Confidence != 0.7 {
		t.Errorf("confidence should be unchanged at 0.7, got %f", accepted[0].Confidence)
	}
}

func TestVerifier_AdversarialDowngrade_WeakClaim(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"Run": {SymbolName: "Run", Language: "go", Kind: "func"},
	}

	claims := []extractor.Claim{
		makeClaim("Run", extractor.PredicatePurpose, "Main entry point"),
	}

	mock := &mockLLMClient{
		response: `[{"subject_name": "Run", "predicate": "purpose", "verdict": "downgrade", "reason": "Plausible but no evidence it is the main entry point"}]`,
	}
	v := NewVerifier(mock, nil)

	accepted, summary, err := v.Verify(context.Background(), claims, symbolMap, "Symbol: Run (kind=func)")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if len(accepted) != 1 {
		t.Fatalf("expected 1 accepted (downgraded), got %d", len(accepted))
	}
	if summary.Downgraded != 1 {
		t.Errorf("expected 1 downgraded, got %d", summary.Downgraded)
	}

	expectedConfidence := 0.7 * confidenceDowngradeFactor
	if accepted[0].Confidence != expectedConfidence {
		t.Errorf("expected confidence %f after downgrade, got %f", expectedConfidence, accepted[0].Confidence)
	}
}

func TestVerifier_MixedVerdicts(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"Foo": {SymbolName: "Foo", Language: "go", Kind: "type"},
		"Bar": {SymbolName: "Bar", Language: "go", Kind: "func"},
	}

	claims := []extractor.Claim{
		makeClaim("Foo", extractor.PredicatePurpose, "Data container"),
		makeClaim("Foo", extractor.PredicateComplexity, "simple"),
		makeClaim("Bar", extractor.PredicatePurpose, "Utility function"),
		makeClaim("Bogus", extractor.PredicatePurpose, "Should not exist"),
	}

	mock := &mockLLMClient{
		response: `[
			{"subject_name": "Foo", "predicate": "purpose", "verdict": "accept", "reason": "ok"},
			{"subject_name": "Foo", "predicate": "complexity", "verdict": "reject", "reason": "actually complex"},
			{"subject_name": "Bar", "predicate": "purpose", "verdict": "downgrade", "reason": "weak evidence"}
		]`,
	}
	v := NewVerifier(mock, nil)

	accepted, summary, err := v.Verify(context.Background(), claims, symbolMap, "context")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Bogus: rejected structurally (1)
	// Foo/complexity: rejected by adversarial (1)
	// Foo/purpose: accepted (1)
	// Bar/purpose: downgraded (1)
	if summary.Rejected != 2 {
		t.Errorf("expected 2 rejected, got %d", summary.Rejected)
	}
	if summary.Accepted != 1 {
		t.Errorf("expected 1 accepted, got %d", summary.Accepted)
	}
	if summary.Downgraded != 1 {
		t.Errorf("expected 1 downgraded, got %d", summary.Downgraded)
	}
	if len(accepted) != 2 {
		t.Errorf("expected 2 accepted claims (accept+downgrade), got %d", len(accepted))
	}
}

func TestVerifier_NoVerdictFromReviewer_DefaultsToAccept(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"X": {SymbolName: "X", Language: "go", Kind: "type"},
	}

	claims := []extractor.Claim{
		makeClaim("X", extractor.PredicatePurpose, "Something useful"),
	}

	// Reviewer returns empty array — no explicit verdict.
	mock := &mockLLMClient{response: `[]`}
	v := NewVerifier(mock, nil)

	accepted, summary, err := v.Verify(context.Background(), claims, symbolMap, "context")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if len(accepted) != 1 {
		t.Errorf("expected 1 accepted (default), got %d", len(accepted))
	}
	if summary.Accepted != 1 {
		t.Errorf("expected 1 accepted in summary, got %d", summary.Accepted)
	}
}

func TestVerifier_LLMError(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"X": {SymbolName: "X", Language: "go", Kind: "type"},
	}

	claims := []extractor.Claim{
		makeClaim("X", extractor.PredicatePurpose, "Something"),
	}

	mock := &mockLLMClient{err: fmt.Errorf("service unavailable")}
	v := NewVerifier(mock, nil)

	_, _, err := v.Verify(context.Background(), claims, symbolMap, "context")
	if err == nil {
		t.Error("expected error when LLM fails")
	}
	if !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("expected service unavailable in error, got: %v", err)
	}
}

func TestVerifier_InvalidJSON(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"X": {SymbolName: "X", Language: "go", Kind: "type"},
	}

	claims := []extractor.Claim{
		makeClaim("X", extractor.PredicatePurpose, "Something"),
	}

	mock := &mockLLMClient{response: "not valid json"}
	v := NewVerifier(mock, nil)

	_, _, err := v.Verify(context.Background(), claims, symbolMap, "context")
	if err == nil {
		t.Error("expected error for invalid JSON from reviewer")
	}
}

func TestVerifier_AllStructurallyRejected_NoLLMCall(t *testing.T) {
	symbolMap := map[string]db.Symbol{
		"Real": {SymbolName: "Real", Language: "go", Kind: "type"},
	}

	claims := []extractor.Claim{
		makeClaim("Fake1", extractor.PredicatePurpose, "nope"),
		makeClaim("Fake2", extractor.PredicatePurpose, "nope"),
	}

	mock := &mockLLMClient{response: `[]`}
	v := NewVerifier(mock, nil)

	accepted, summary, err := v.Verify(context.Background(), claims, symbolMap, "context")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(accepted) != 0 {
		t.Errorf("expected 0 accepted, got %d", len(accepted))
	}
	if summary.Rejected != 2 {
		t.Errorf("expected 2 rejected, got %d", summary.Rejected)
	}
	// LLM should NOT have been called since all claims were structurally rejected.
	if mock.lastUser != "" {
		t.Error("LLM should not have been called when all claims are structurally rejected")
	}
}

func TestParseVerifyResponse_Valid(t *testing.T) {
	raw := `[
		{"subject_name": "X", "predicate": "purpose", "verdict": "accept", "reason": "ok"},
		{"subject_name": "Y", "predicate": "stability", "verdict": "reject", "reason": "wrong"}
	]`
	results, err := parseVerifyResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Verdict != VerdictAccept {
		t.Errorf("expected accept, got %s", results[0].Verdict)
	}
	if results[1].Verdict != VerdictReject {
		t.Errorf("expected reject, got %s", results[1].Verdict)
	}
}

func TestParseVerifyResponse_UnknownVerdict(t *testing.T) {
	raw := `[{"subject_name": "X", "predicate": "purpose", "verdict": "maybe", "reason": "unsure"}]`
	results, err := parseVerifyResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if results[0].Verdict != VerdictReject {
		t.Errorf("expected unknown verdict to be treated as reject, got %s", results[0].Verdict)
	}
}

func TestParseVerifyResponse_WithMarkdownFences(t *testing.T) {
	raw := "```json\n" + `[{"subject_name": "X", "predicate": "purpose", "verdict": "accept", "reason": "ok"}]` + "\n```"
	results, err := parseVerifyResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestBuildVerifyPrompt(t *testing.T) {
	claims := []extractor.Claim{
		makeClaim("Foo", extractor.PredicatePurpose, "Does things"),
		makeClaim("Bar", extractor.PredicateStability, "stable"),
	}

	prompt := buildVerifyPrompt(claims, "Symbol: Foo (kind=type)\nSymbol: Bar (kind=func)")
	if !strings.Contains(prompt, "Structural Evidence") {
		t.Error("expected structural evidence header")
	}
	if !strings.Contains(prompt, "Claims to Review") {
		t.Error("expected claims to review header")
	}
	if !strings.Contains(prompt, "Foo") {
		t.Error("expected Foo in prompt")
	}
	if !strings.Contains(prompt, "Bar") {
		t.Error("expected Bar in prompt")
	}
}

func TestGenerateForPackage_WithVerification(t *testing.T) {
	cdb := testDB(t)

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
		},
		"Bar": {
			{Predicate: "defines", ObjectText: "func Bar()", SourceFile: "bar.go",
				Confidence: 1.0, ClaimTier: "structural", Extractor: "go-deep",
				ExtractorVersion: "1.0", LastVerified: db.Now()},
		},
	}
	seedPackage(t, cdb, "example.com/pkg", symbols, claimsMap)

	// Generation LLM returns claims for both symbols.
	genMock := &mockLLMClient{
		response: `[
			{"subject_name": "Foo", "purpose": "Main data type", "complexity": "simple"},
			{"subject_name": "Bar", "purpose": "Utility function"}
		]`,
	}

	// Verification LLM accepts Foo/purpose, rejects Foo/complexity, accepts Bar/purpose.
	verifyMock := &mockLLMClient{
		response: `[
			{"subject_name": "Foo", "predicate": "purpose", "verdict": "accept", "reason": "ok"},
			{"subject_name": "Foo", "predicate": "complexity", "verdict": "reject", "reason": "not enough evidence"},
			{"subject_name": "Bar", "predicate": "purpose", "verdict": "accept", "reason": "ok"}
		]`,
	}

	gen, err := NewGenerator(cdb, genMock, "test/repo", WithVerification(verifyMock))
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}

	result, err := gen.GenerateForPackage(context.Background(), "example.com/pkg")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Foo/purpose: accepted, Foo/complexity: rejected, Bar/purpose: accepted = 2 stored
	if result.ClaimsStored != 2 {
		t.Errorf("expected 2 stored claims, got %d", result.ClaimsStored)
	}
	if result.ClaimsRejected != 1 {
		t.Errorf("expected 1 rejected claim, got %d", result.ClaimsRejected)
	}
	if result.Verification == nil {
		t.Fatal("expected verification summary to be set")
	}
	if result.Verification.Accepted != 2 {
		t.Errorf("expected 2 accepted in verification, got %d", result.Verification.Accepted)
	}
	if result.Verification.Rejected != 1 {
		t.Errorf("expected 1 rejected in verification, got %d", result.Verification.Rejected)
	}

	// Verify only accepted claims are in DB.
	purposeClaims, _ := cdb.GetClaimsByPredicate("purpose")
	if len(purposeClaims) != 2 {
		t.Errorf("expected 2 purpose claims in DB, got %d", len(purposeClaims))
	}
	complexityClaims, _ := cdb.GetClaimsByPredicate("complexity")
	if len(complexityClaims) != 0 {
		t.Errorf("expected 0 complexity claims in DB (rejected), got %d", len(complexityClaims))
	}
}

func TestGenerateForPackage_WithoutVerification_Unchanged(t *testing.T) {
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

	// No WithVerification — should behave exactly as before.
	gen, _ := NewGenerator(cdb, mock, "r")
	result, err := gen.GenerateForPackage(context.Background(), "pkg")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if result.ClaimsStored != 1 {
		t.Errorf("expected 1 claim without verification, got %d", result.ClaimsStored)
	}
	if result.Verification != nil {
		t.Error("expected nil verification when not configured")
	}
	if result.ClaimsRejected != 0 {
		t.Errorf("expected 0 rejected without verification, got %d", result.ClaimsRejected)
	}
}
