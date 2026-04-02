// Package extractor defines the language-agnostic interface for extracting
// structural claims from source code. Claims are polyglot-ready: the Claims DB
// does not know or care which source language produced them.
package extractor

import (
	"fmt"
	"time"
)

// Visibility describes the access level of a symbol within its language's
// scoping rules.
type Visibility string

const (
	VisibilityPublic      Visibility = "public"
	VisibilityInternal    Visibility = "internal"
	VisibilityPrivate     Visibility = "private"
	VisibilityReExported  Visibility = "re-exported"
	VisibilityConditional Visibility = "conditional"
)

// validVisibilities is the set of allowed visibility values.
var validVisibilities = map[Visibility]bool{
	VisibilityPublic:      true,
	VisibilityInternal:    true,
	VisibilityPrivate:     true,
	VisibilityReExported:  true,
	VisibilityConditional: true,
}

// IsValid reports whether v is a recognised Visibility value.
func (v Visibility) IsValid() bool { return validVisibilities[v] }

// SymbolKind classifies a symbol independent of language.
type SymbolKind string

const (
	KindType      SymbolKind = "type"
	KindFunc      SymbolKind = "func"
	KindConst     SymbolKind = "const"
	KindVar       SymbolKind = "var"
	KindClass     SymbolKind = "class"
	KindModule    SymbolKind = "module"
	KindMethod    SymbolKind = "method"
	KindField     SymbolKind = "field"
	KindEnum      SymbolKind = "enum"
	KindProperty  SymbolKind = "property"
	KindInterface SymbolKind = "interface"
)

// validKinds is the set of allowed SymbolKind values.
var validKinds = map[SymbolKind]bool{
	KindType: true, KindFunc: true, KindConst: true, KindVar: true,
	KindClass: true, KindModule: true, KindMethod: true, KindField: true,
	KindEnum: true, KindProperty: true, KindInterface: true,
}

// IsValid reports whether k is a recognised SymbolKind value.
func (k SymbolKind) IsValid() bool { return validKinds[k] }

// Predicate is a claim predicate describing a relationship or property.
type Predicate string

// Tree-sitter-safe predicates (fast path may emit these).
const (
	PredicateDefines     Predicate = "defines"
	PredicateImports     Predicate = "imports"
	PredicateExports     Predicate = "exports"
	PredicateHasDoc      Predicate = "has_doc"
	PredicateIsTest      Predicate = "is_test"
	PredicateIsGenerated Predicate = "is_generated"
)

// Deep-extractor-only predicates (require type resolution).
const (
	PredicateHasKind      Predicate = "has_kind"
	PredicateImplements   Predicate = "implements"
	PredicateHasSignature Predicate = "has_signature"
	PredicateEncloses     Predicate = "encloses"
)

// Semantic predicates (Tier 2 — LLM-generated).
const (
	PredicatePurpose      Predicate = "purpose"
	PredicateUsagePattern Predicate = "usage_pattern"
	PredicateComplexity   Predicate = "complexity"
	PredicateStability    Predicate = "stability"
)

// semanticOnly lists predicates restricted to LLM semantic extractors.
var semanticOnly = map[Predicate]bool{
	PredicatePurpose:      true,
	PredicateUsagePattern: true,
	PredicateComplexity:   true,
	PredicateStability:    true,
}

// IsSemantic reports whether p is a semantic (LLM-generated) predicate.
func (p Predicate) IsSemantic() bool { return semanticOnly[p] }

// treeSitterSafe lists predicates that tree-sitter extractors may emit.
var treeSitterSafe = map[Predicate]bool{
	PredicateDefines:     true,
	PredicateImports:     true,
	PredicateExports:     true,
	PredicateHasDoc:      true,
	PredicateIsTest:      true,
	PredicateIsGenerated: true,
}

// deepOnly lists predicates restricted to deep extractors.
var deepOnly = map[Predicate]bool{
	PredicateHasKind:      true,
	PredicateImplements:   true,
	PredicateHasSignature: true,
	PredicateEncloses:     true,
}

// allPredicates is the union of treeSitterSafe, deepOnly, and semanticOnly.
var allPredicates = func() map[Predicate]bool {
	m := make(map[Predicate]bool, len(treeSitterSafe)+len(deepOnly)+len(semanticOnly))
	for p := range treeSitterSafe {
		m[p] = true
	}
	for p := range deepOnly {
		m[p] = true
	}
	for p := range semanticOnly {
		m[p] = true
	}
	return m
}()

// IsValid reports whether p is a recognised predicate.
func (p Predicate) IsValid() bool { return allPredicates[p] }

// IsTreeSitterSafe reports whether p may be emitted by a tree-sitter extractor.
func (p Predicate) IsTreeSitterSafe() bool { return treeSitterSafe[p] }

// IsDeepOnly reports whether p requires a deep extractor.
func (p Predicate) IsDeepOnly() bool { return deepOnly[p] }

// ClaimTier classifies a claim's origin.
type ClaimTier string

const (
	TierStructural ClaimTier = "structural"
	TierSemantic   ClaimTier = "semantic"
)

// IsValid reports whether t is a recognised ClaimTier.
func (t ClaimTier) IsValid() bool {
	return t == TierStructural || t == TierSemantic
}

// Claim is a single assertion about a symbol extracted from source code.
// Claims are language-agnostic: the same struct represents a Go function
// definition and a Python class definition.
type Claim struct {
	// Subject identity (composite primary key).
	SubjectRepo       string     `json:"subject_repo"`        // e.g. "kubernetes/kubernetes"
	SubjectImportPath string     `json:"subject_import_path"` // e.g. "k8s.io/api/core/v1"
	SubjectName       string     `json:"subject_name"`        // e.g. "Pod"
	Language          string     `json:"language"`            // e.g. "go", "typescript", "python"
	Kind              SymbolKind `json:"kind"`                // e.g. KindType, KindFunc
	Visibility        Visibility `json:"visibility"`

	// Claim content.
	Predicate  Predicate `json:"predicate"`
	ObjectText string    `json:"object_text,omitempty"` // free-text object (e.g. docstring)
	ObjectName string    `json:"object_name,omitempty"` // symbolic object (e.g. imported package)

	// Provenance.
	SourceFile       string    `json:"source_file"`
	SourceLine       int       `json:"source_line,omitempty"`
	Confidence       float64   `json:"confidence"`
	ClaimTier        ClaimTier `json:"claim_tier"`
	Extractor        string    `json:"extractor"`         // e.g. "go-deep", "tree-sitter-go"
	ExtractorVersion string    `json:"extractor_version"` // e.g. "1.2.0"

	// Optional SCIP symbol (secondary index).
	SCIPSymbol string `json:"scip_symbol,omitempty"`

	// Timestamp of extraction.
	LastVerified time.Time `json:"last_verified"`
}

// Validate checks that all required fields are present and values are within
// allowed sets. It returns an error describing the first problem found.
func (c Claim) Validate() error {
	if c.SubjectRepo == "" {
		return fmt.Errorf("claim: subject_repo is required")
	}
	if c.SubjectImportPath == "" {
		return fmt.Errorf("claim: subject_import_path is required")
	}
	if c.SubjectName == "" {
		return fmt.Errorf("claim: subject_name is required")
	}
	if c.Language == "" {
		return fmt.Errorf("claim: language is required")
	}
	if !c.Kind.IsValid() {
		return fmt.Errorf("claim: invalid kind %q", c.Kind)
	}
	if !c.Visibility.IsValid() {
		return fmt.Errorf("claim: invalid visibility %q", c.Visibility)
	}
	if !c.Predicate.IsValid() {
		return fmt.Errorf("claim: invalid predicate %q", c.Predicate)
	}
	if c.SourceFile == "" {
		return fmt.Errorf("claim: source_file is required")
	}
	if c.Confidence < 0 || c.Confidence > 1 {
		return fmt.Errorf("claim: confidence must be in [0, 1], got %f", c.Confidence)
	}
	if !c.ClaimTier.IsValid() {
		return fmt.Errorf("claim: invalid claim_tier %q", c.ClaimTier)
	}
	if c.Extractor == "" {
		return fmt.Errorf("claim: extractor is required")
	}
	if c.ExtractorVersion == "" {
		return fmt.Errorf("claim: extractor_version is required")
	}
	if c.LastVerified.IsZero() {
		return fmt.Errorf("claim: last_verified is required")
	}
	return nil
}
