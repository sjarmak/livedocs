package semantic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// AnthropicClient calls the Anthropic Messages API via HTTP.
type AnthropicClient struct {
	apiKey     string
	model      string
	maxTokens  int
	httpClient *http.Client
	baseURL    string
}

// AnthropicOption configures an AnthropicClient.
type AnthropicOption func(*AnthropicClient)

// WithModel sets the model to use (default: claude-sonnet-4-20250514).
func WithModel(model string) AnthropicOption {
	return func(c *AnthropicClient) { c.model = model }
}

// WithMaxTokens sets the max output tokens (default: 4096).
func WithMaxTokens(n int) AnthropicOption {
	return func(c *AnthropicClient) { c.maxTokens = n }
}

// WithHTTPClient sets a custom HTTP client (useful for testing).
func WithHTTPClient(hc *http.Client) AnthropicOption {
	return func(c *AnthropicClient) { c.httpClient = hc }
}

// WithBaseURL overrides the API base URL (useful for testing).
func WithBaseURL(url string) AnthropicOption {
	return func(c *AnthropicClient) { c.baseURL = url }
}

// NewAnthropicClient creates an AnthropicClient with the given API key.
func NewAnthropicClient(apiKey string, opts ...AnthropicOption) (*AnthropicClient, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("anthropic: API key is required")
	}
	c := &AnthropicClient{
		apiKey:     apiKey,
		model:      "claude-sonnet-4-20250514",
		maxTokens:  4096,
		httpClient: http.DefaultClient,
		baseURL:    "https://api.anthropic.com",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// messagesRequest is the Anthropic Messages API request body.
type messagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// messagesResponse is the relevant subset of the API response.
type messagesResponse struct {
	Content []contentBlock `json:"content"`
	Error   *apiError      `json:"error,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Complete sends a system+user prompt to the Anthropic Messages API and
// returns the text response.
func (c *AnthropicClient) Complete(ctx context.Context, system, user string) (string, error) {
	reqBody := messagesRequest{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System:    system,
		Messages:  []message{{Role: "user", Content: user}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("anthropic: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("anthropic: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic: HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("anthropic: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result messagesResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("anthropic: unmarshal response: %w", err)
	}
	if result.Error != nil {
		return "", fmt.Errorf("anthropic: API error: %s: %s", result.Error.Type, result.Error.Message)
	}

	// Concatenate all text content blocks.
	var text string
	for _, block := range result.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	return text, nil
}
