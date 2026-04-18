package semantic

import (
	"context"
	"fmt"

	"github.com/sjarmak/livedocs/db"
	"github.com/sjarmak/livedocs/extractor"
)

// GenerateForPackage generates semantic claims for a single import path.
// It queries structural claims, builds a prompt, calls the LLM, parses the
// response, optionally verifies claims via adversarial review, and stores
// the resulting claims in the DB.
func (g *Generator) GenerateForPackage(ctx context.Context, importPath string) (PackageResult, error) {
	result := PackageResult{ImportPath: importPath}

	// 1. Query structural claims for this package.
	symbolClaims, err := g.cfg.ClaimsDB.GetStructuralClaimsByImportPath(importPath)
	if err != nil {
		return result, fmt.Errorf("query structural claims for %s: %w", importPath, err)
	}
	if len(symbolClaims) == 0 {
		return result, nil // nothing to analyse
	}

	// 2. Build the prompt.
	userPrompt := buildUserPrompt(importPath, symbolClaims, g.cfg.MaxSymbolsPerPrompt)

	// 3. Call the LLM.
	raw, err := g.cfg.Client.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return result, fmt.Errorf("LLM call for %s: %w", importPath, err)
	}

	// 4. Build symbol lookup map.
	symbolMap := make(map[string]db.Symbol, len(symbolClaims))
	for _, sc := range symbolClaims {
		symbolMap[sc.Symbol.SymbolName] = sc.Symbol
	}

	// 5. Parse the response into claims.
	claims, err := parseLLMResponse(raw, symbolMap, importPath, g.cfg.Repo)
	if err != nil {
		return result, fmt.Errorf("parse LLM response for %s: %w", importPath, err)
	}

	// 6. Verification gate (if configured).
	if g.cfg.VerifyClient != nil {
		verifier := NewVerifier(g.cfg.VerifyClient, g.cfg.ClaimsDB)
		verified, summary, verifyErr := verifier.Verify(ctx, claims, symbolMap, userPrompt)
		if verifyErr != nil {
			return result, fmt.Errorf("verify claims for %s: %w", importPath, verifyErr)
		}
		result.ClaimsRejected = summary.Rejected
		result.Verification = &summary
		claims = verified
	}

	// 7. Delete prior semantic claims from this extractor for idempotency.
	if err := g.cfg.ClaimsDB.DeleteClaimsByExtractorAndImportPath(ExtractorName, importPath); err != nil {
		return result, fmt.Errorf("delete old semantic claims for %s: %w", importPath, err)
	}

	// 8. Store claims.
	stored, err := g.storeClaims(claims)
	result.ClaimsStored = stored
	if err != nil {
		return result, fmt.Errorf("store semantic claims for %s: %w", importPath, err)
	}

	return result, nil
}

// storeClaims persists a slice of extractor.Claim values to the DB.
func (g *Generator) storeClaims(claims []extractor.Claim) (int, error) {
	stored := 0
	for _, claim := range claims {
		symID, err := g.cfg.ClaimsDB.UpsertSymbol(db.Symbol{
			Repo:       claim.SubjectRepo,
			ImportPath: claim.SubjectImportPath,
			SymbolName: claim.SubjectName,
			Language:   claim.Language,
			Kind:       string(claim.Kind),
			Visibility: string(claim.Visibility),
		})
		if err != nil {
			return stored, fmt.Errorf("upsert symbol %s: %w", claim.SubjectName, err)
		}

		_, err = g.cfg.ClaimsDB.InsertClaim(db.Claim{
			SubjectID:        symID,
			Predicate:        string(claim.Predicate),
			ObjectText:       claim.ObjectText,
			SourceFile:       claim.SourceFile,
			Confidence:       claim.Confidence,
			ClaimTier:        string(claim.ClaimTier),
			Extractor:        claim.Extractor,
			ExtractorVersion: claim.ExtractorVersion,
			LastVerified:     db.Now(),
		})
		if err != nil {
			return stored, fmt.Errorf("insert claim %s/%s: %w", claim.SubjectName, claim.Predicate, err)
		}
		stored++
	}
	return stored, nil
}
