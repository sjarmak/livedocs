// Package semantic generates Tier 2 semantic claims by sending structural
// claim context to an LLM and parsing the response. Semantic claims capture
// higher-level properties: purpose, usage patterns, complexity, and stability.
package semantic

import (
	"context"
	"fmt"

	"github.com/sjarmak/livedocs/db"
)

// Version is the semantic extractor version, used in claim provenance.
const Version = "0.1.0"

// ExtractorName is the extractor identifier stored in claims.
const ExtractorName = "llm-semantic"

// LLMClient abstracts the LLM API so tests can inject a mock.
type LLMClient interface {
	// Complete sends a prompt and returns the LLM's text response.
	Complete(ctx context.Context, system, user string) (string, error)
}

// GeneratorConfig holds the configuration for a semantic claim Generator.
type GeneratorConfig struct {
	// ClaimsDB is the per-repo claims database.
	ClaimsDB *db.ClaimsDB
	// Client is the LLM API client.
	Client LLMClient
	// Repo is the repository identifier (e.g. "kubernetes/kubernetes").
	Repo string
	// MaxSymbolsPerPrompt limits how many symbols are included in a single
	// prompt to avoid exceeding token limits. Zero means no limit.
	MaxSymbolsPerPrompt int
	// VerifyClient is the LLM client for adversarial review. If non-nil,
	// generated claims pass through a verification gate before storage.
	// May be the same client as Client or a different model.
	VerifyClient LLMClient
}

// Option is a functional option for GeneratorConfig.
type Option func(*GeneratorConfig)

// WithMaxSymbols sets the maximum number of symbols per prompt.
func WithMaxSymbols(n int) Option {
	return func(c *GeneratorConfig) { c.MaxSymbolsPerPrompt = n }
}

// WithVerification enables the adversarial verification gate using the
// given LLM client. Pass nil to disable verification.
func WithVerification(client LLMClient) Option {
	return func(c *GeneratorConfig) { c.VerifyClient = client }
}

// Generator produces semantic claims for packages by querying structural
// claims and sending them to an LLM for analysis.
type Generator struct {
	cfg GeneratorConfig
}

// NewGenerator creates a Generator with the given config and options.
func NewGenerator(claimsDB *db.ClaimsDB, client LLMClient, repo string, opts ...Option) (*Generator, error) {
	if claimsDB == nil {
		return nil, fmt.Errorf("semantic: claimsDB is required")
	}
	if client == nil {
		return nil, fmt.Errorf("semantic: LLM client is required")
	}
	if repo == "" {
		return nil, fmt.Errorf("semantic: repo is required")
	}
	cfg := GeneratorConfig{
		ClaimsDB:            claimsDB,
		Client:              client,
		Repo:                repo,
		MaxSymbolsPerPrompt: 100,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Generator{cfg: cfg}, nil
}

// PackageResult holds the outcome of generating semantic claims for one package.
type PackageResult struct {
	ImportPath     string
	ClaimsStored   int
	ClaimsRejected int            // claims rejected by verification gate
	Verification   *VerifySummary // nil if verification disabled
	Err            error
}
