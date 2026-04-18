package main

import (
	"context"
	"errors"
)

// errLLMNotAvailable is returned by stub constructors to signal "client
// unavailable in this environment" (analogous to Claude CLI missing on PATH
// or ANTHROPIC_API_KEY unset).
var errLLMNotAvailable = errors.New("llm client not available")

// stubLLMClient implements semantic.LLMClient for tests; Complete is never
// called because buildMiningFactory tests only exercise construction.
type stubLLMClient struct{}

func (s *stubLLMClient) Complete(_ context.Context, _, _ string) (string, error) {
	return "", errors.New("stubLLMClient.Complete should not be called in factory construction tests")
}

// fakeLLMClientFactory is the default llmClientFactory used by tests: the CLI
// path succeeds, so the factory never needs ANTHROPIC_API_KEY.
var fakeLLMClientFactory = llmClientFactory{
	newCLI: func(_ string) (llmClient, error) {
		return &stubLLMClient{}, nil
	},
	newAPI: func(_, _ string) (llmClient, error) {
		return &stubLLMClient{}, nil
	},
}
