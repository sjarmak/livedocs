package scip

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	scipb "github.com/scip-code/scip/bindings/go/scip"

	"github.com/live-docs/live_docs/db"
)

const (
	extractorName    = "scip-import"
	extractorVersion = "0.1.0"
)

// ImportResult summarizes the outcome of a SCIP index import.
type ImportResult struct {
	SymbolsImported  int
	ClaimsCreated    int
	DocumentsVisited int
	ExternalSymbols  int
	Errors           []error
}

// Importer reads a SCIP protobuf index and populates a claims DB.
type Importer struct {
	repo     string
	language string
	claimsDB *db.ClaimsDB
	xrefDB   *db.XRefDB // may be nil if cross-repo indexing is not needed
}

// NewImporter creates a new SCIP importer for the given repo.
func NewImporter(repo, language string, claimsDB *db.ClaimsDB, xrefDB *db.XRefDB) *Importer {
	return &Importer{
		repo:     repo,
		language: language,
		claimsDB: claimsDB,
		xrefDB:   xrefDB,
	}
}

// ImportFile imports a SCIP index from a file path.
func (imp *Importer) ImportFile(ctx context.Context, path string) (ImportResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return ImportResult{}, fmt.Errorf("open SCIP index %s: %w", path, err)
	}
	defer f.Close()
	return imp.ImportReader(ctx, f)
}

// ImportReader imports a SCIP index from an io.Reader using streaming parsing.
// This avoids loading the entire index into memory.
func (imp *Importer) ImportReader(ctx context.Context, r io.Reader) (ImportResult, error) {
	result := ImportResult{}

	visitor := &scipb.IndexVisitor{
		VisitMetadata: func(ctx context.Context, m *scipb.Metadata) error {
			// Metadata is informational; we don't need to store it.
			return nil
		},
		VisitDocument: func(ctx context.Context, doc *scipb.Document) error {
			result.DocumentsVisited++
			docResult, err := imp.importDocument(doc)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("document %s: %w", doc.GetRelativePath(), err))
				return nil // continue processing other documents
			}
			result.SymbolsImported += docResult.SymbolsImported
			result.ClaimsCreated += docResult.ClaimsCreated
			return nil
		},
		VisitExternalSymbol: func(ctx context.Context, si *scipb.SymbolInformation) error {
			result.ExternalSymbols++
			if err := imp.importExternalSymbol(si); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("external symbol %s: %w", si.GetSymbol(), err))
			}
			return nil
		},
	}

	if err := visitor.ParseStreaming(ctx, r); err != nil {
		return result, fmt.Errorf("parse SCIP index: %w", err)
	}

	return result, nil
}

// importDocument processes a single SCIP document (source file).
func (imp *Importer) importDocument(doc *scipb.Document) (ImportResult, error) {
	result := ImportResult{}
	relPath := doc.GetRelativePath()
	language := doc.GetLanguage()
	if language == "" {
		language = imp.language
	}

	// Record the source file.
	_, err := imp.claimsDB.UpsertSourceFile(db.SourceFile{
		Repo:             imp.repo,
		RelativePath:     relPath,
		ContentHash:      "", // SCIP index does not provide content hashes
		ExtractorVersion: extractorVersion,
		LastIndexed:      db.Now(),
	})
	if err != nil {
		return result, fmt.Errorf("upsert source file: %w", err)
	}

	// Delete previous claims from this extractor for this file (idempotent re-import).
	if err := imp.claimsDB.DeleteClaimsByExtractorAndFile(extractorName, relPath); err != nil {
		return result, fmt.Errorf("delete old claims: %w", err)
	}

	// Process symbols defined in this document.
	for _, si := range doc.GetSymbols() {
		symbolStr := si.GetSymbol()
		if scipb.IsLocalSymbol(symbolStr) {
			continue // skip local symbols
		}

		decomp, err := DecomposeSymbol(symbolStr)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("decompose %s: %w", symbolStr, err))
			continue
		}

		// Prefer SCIP kind over descriptor-inferred kind.
		kind := decomp.Kind
		if si.GetKind() != scipb.SymbolInformation_UnspecifiedKind {
			kind = MapSCIPKind(si.GetKind())
		}

		visibility := MapVisibility(decomp.SymbolName, strings.ToLower(language))

		symID, err := imp.claimsDB.UpsertSymbol(db.Symbol{
			Repo:        imp.repo,
			ImportPath:  decomp.ImportPath,
			SymbolName:  decomp.SymbolName,
			Language:    strings.ToLower(language),
			Kind:        kind,
			Visibility:  visibility,
			DisplayName: si.GetDisplayName(),
			SCIPSymbol:  symbolStr,
		})
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("upsert symbol %s: %w", symbolStr, err))
			continue
		}
		result.SymbolsImported++

		// Create "defines" claim.
		if _, err := imp.claimsDB.InsertClaim(db.Claim{
			SubjectID:        symID,
			Predicate:        "defines",
			SourceFile:       relPath,
			Confidence:       1.0,
			ClaimTier:        "structural",
			Extractor:        extractorName,
			ExtractorVersion: extractorVersion,
			LastVerified:     db.Now(),
		}); err != nil {
			result.Errors = append(result.Errors, err)
		} else {
			result.ClaimsCreated++
		}

		// Create "has_kind" claim.
		if _, err := imp.claimsDB.InsertClaim(db.Claim{
			SubjectID:        symID,
			Predicate:        "has_kind",
			ObjectText:       kind,
			SourceFile:       relPath,
			Confidence:       1.0,
			ClaimTier:        "structural",
			Extractor:        extractorName,
			ExtractorVersion: extractorVersion,
			LastVerified:     db.Now(),
		}); err != nil {
			result.Errors = append(result.Errors, err)
		} else {
			result.ClaimsCreated++
		}

		// Create "has_doc" claims from documentation.
		for _, docStr := range si.GetDocumentation() {
			if docStr == "" {
				continue
			}
			if _, err := imp.claimsDB.InsertClaim(db.Claim{
				SubjectID:        symID,
				Predicate:        "has_doc",
				ObjectText:       docStr,
				SourceFile:       relPath,
				Confidence:       0.85,
				ClaimTier:        "structural",
				Extractor:        extractorName,
				ExtractorVersion: extractorVersion,
				LastVerified:     db.Now(),
			}); err != nil {
				result.Errors = append(result.Errors, err)
			} else {
				result.ClaimsCreated++
			}
		}

		// Create "implements" claims from relationships.
		for _, rel := range si.GetRelationships() {
			if rel.GetIsImplementation() {
				if _, err := imp.claimsDB.InsertClaim(db.Claim{
					SubjectID:        symID,
					Predicate:        "implements",
					ObjectText:       rel.GetSymbol(),
					SourceFile:       relPath,
					Confidence:       1.0,
					ClaimTier:        "structural",
					Extractor:        extractorName,
					ExtractorVersion: extractorVersion,
					LastVerified:     db.Now(),
				}); err != nil {
					result.Errors = append(result.Errors, err)
				} else {
					result.ClaimsCreated++
				}
			}
		}

		// Update cross-repo xref index if available.
		if imp.xrefDB != nil {
			key := SymbolKey(decomp.ImportPath, decomp.SymbolName)
			if err := imp.xrefDB.UpsertXRef(db.XRef{
				SymbolKey: key,
				Repo:      imp.repo,
				SymbolID:  symID,
			}); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("upsert xref: %w", err))
			}
		}
	}

	return result, nil
}

// importExternalSymbol processes a symbol defined in an external package.
// These provide hover documentation for symbols not in the current index.
func (imp *Importer) importExternalSymbol(si *scipb.SymbolInformation) error {
	symbolStr := si.GetSymbol()
	if scipb.IsLocalSymbol(symbolStr) {
		return nil
	}

	decomp, err := DecomposeSymbol(symbolStr)
	if err != nil {
		return fmt.Errorf("decompose external %s: %w", symbolStr, err)
	}

	kind := decomp.Kind
	if si.GetKind() != scipb.SymbolInformation_UnspecifiedKind {
		kind = MapSCIPKind(si.GetKind())
	}

	language := imp.language
	visibility := MapVisibility(decomp.SymbolName, language)

	symID, err := imp.claimsDB.UpsertSymbol(db.Symbol{
		Repo:        imp.repo,
		ImportPath:  decomp.ImportPath,
		SymbolName:  decomp.SymbolName,
		Language:    language,
		Kind:        kind,
		Visibility:  visibility,
		DisplayName: si.GetDisplayName(),
		SCIPSymbol:  symbolStr,
	})
	if err != nil {
		return err
	}

	// Store documentation from external symbols.
	for _, docStr := range si.GetDocumentation() {
		if docStr == "" {
			continue
		}
		if _, err := imp.claimsDB.InsertClaim(db.Claim{
			SubjectID:        symID,
			Predicate:        "has_doc",
			ObjectText:       docStr,
			SourceFile:       "(external)",
			Confidence:       0.85,
			ClaimTier:        "structural",
			Extractor:        extractorName,
			ExtractorVersion: extractorVersion,
			LastVerified:     db.Now(),
		}); err != nil {
			return err
		}
	}

	// Update xref index.
	if imp.xrefDB != nil {
		key := SymbolKey(decomp.ImportPath, decomp.SymbolName)
		if err := imp.xrefDB.UpsertXRef(db.XRef{
			SymbolKey: key,
			Repo:      imp.repo,
			SymbolID:  symID,
		}); err != nil {
			return fmt.Errorf("upsert xref for external: %w", err)
		}
	}

	return nil
}
