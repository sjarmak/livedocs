// Package sourcegraph provides a SourcegraphClient that satisfies
// semantic.LLMClient by proxying requests through the Sourcegraph MCP
// server's deepsearch tool.
package sourcegraph

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// LowConfidenceSentinel is returned when the deepsearch response does not
// reference the target symbol, indicating the result is likely irrelevant.
const LowConfidenceSentinel = "[LOW_CONFIDENCE] Deepsearch response did not reference target symbol"

// defaultCommand is the default MCP server command when SOURCEGRAPH_MCP_COMMAND
// is not set.
const defaultCommand = "npx"

// defaultArgs are the default arguments for the MCP server command.
var defaultArgs = []string{"-y", "@sourcegraph/mcp"}

// callRequest is an internal message sent from Complete() to the worker goroutine.
type callRequest struct {
	ctx    context.Context
	system string
	user   string
	result chan<- callResult
}

// callResult is the response from the worker goroutine back to the caller.
type callResult struct {
	text string
	err  error
}

// SourcegraphClient implements semantic.LLMClient by spawning a Sourcegraph
// MCP server subprocess and calling its "deepsearch" tool. All MCP calls are
// serialized through a single worker goroutine; the subprocess is spawned
// lazily on the first call and restarted automatically if it crashes.
type SourcegraphClient struct {
	command     string
	args        []string
	accessToken string
	endpoint    string

	mu      sync.Mutex
	started bool
	reqCh   chan *callRequest
	done    chan struct{}
}

// Option configures a SourcegraphClient.
type Option func(*SourcegraphClient)

// WithCommand overrides the MCP server command and arguments.
func WithCommand(command string, args ...string) Option {
	return func(c *SourcegraphClient) {
		c.command = command
		c.args = args
	}
}

// NewSourcegraphClient creates a SourcegraphClient. It reads configuration
// from environment variables but does not spawn the subprocess until the first
// Complete() call (lazy spawn).
//
// Environment variables:
//   - SOURCEGRAPH_MCP_COMMAND: full command string (default: "npx -y @sourcegraph/mcp")
//   - SRC_ACCESS_TOKEN: required Sourcegraph access token
//   - SRC_ENDPOINT: optional Sourcegraph endpoint URL
func NewSourcegraphClient(opts ...Option) (*SourcegraphClient, error) {
	cmd, args := parseCommand(os.Getenv("SOURCEGRAPH_MCP_COMMAND"))

	c := &SourcegraphClient{
		command:     cmd,
		args:        args,
		accessToken: os.Getenv("SRC_ACCESS_TOKEN"),
		endpoint:    os.Getenv("SRC_ENDPOINT"),
		reqCh:       make(chan *callRequest, 16),
		done:        make(chan struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// parseCommand splits a SOURCEGRAPH_MCP_COMMAND value into command and args.
// If the value is empty, the defaults are returned.
func parseCommand(envVal string) (string, []string) {
	if envVal == "" {
		return defaultCommand, append([]string(nil), defaultArgs...)
	}
	parts := strings.Fields(envVal)
	if len(parts) == 1 {
		return parts[0], nil
	}
	return parts[0], parts[1:]
}

// Complete sends a deepsearch query to the Sourcegraph MCP server and returns
// the text result. It satisfies the semantic.LLMClient interface.
//
// If SRC_ACCESS_TOKEN is empty, Complete returns an error immediately (graceful
// degradation). The user prompt is forwarded as the deepsearch query; the
// system prompt is currently unused by the MCP tool but reserved for future use.
func (c *SourcegraphClient) Complete(ctx context.Context, system, user string) (string, error) {
	if c.accessToken == "" {
		return "", fmt.Errorf("sourcegraph: SRC_ACCESS_TOKEN is not set; deepsearch is unavailable")
	}

	c.ensureStarted()

	resultCh := make(chan callResult, 1)
	req := &callRequest{
		ctx:    ctx,
		system: system,
		user:   user,
		result: resultCh,
	}

	select {
	case c.reqCh <- req:
	case <-ctx.Done():
		return "", fmt.Errorf("sourcegraph: context cancelled before send: %w", ctx.Err())
	}

	select {
	case res := <-resultCh:
		return res.text, res.err
	case <-ctx.Done():
		return "", fmt.Errorf("sourcegraph: context cancelled waiting for result: %w", ctx.Err())
	}
}

// ensureStarted launches the worker goroutine exactly once.
func (c *SourcegraphClient) ensureStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		c.started = true
		go c.run()
	}
}

// Close shuts down the worker goroutine and releases resources. It is safe to
// call multiple times but not concurrently with Complete().
func (c *SourcegraphClient) Close() error {
	c.mu.Lock()
	wasStarted := c.started
	c.mu.Unlock()

	if wasStarted {
		close(c.reqCh)
		<-c.done
	}
	return nil
}

// run is the single worker goroutine. It processes requests sequentially,
// spawning and restarting the MCP subprocess as needed.
func (c *SourcegraphClient) run() {
	defer close(c.done)

	var mcpClient client.MCPClient
	defer func() {
		if mcpClient != nil {
			if err := mcpClient.Close(); err != nil {
				log.Printf("sourcegraph: error closing MCP client: %v", err)
			}
		}
	}()

	for req := range c.reqCh {
		text, err := c.handleRequest(req.ctx, &mcpClient, req.system, req.user)
		req.result <- callResult{text: text, err: err}
	}
}

// handleRequest processes a single request. If the MCP client is nil or has
// failed, it attempts to spawn a new one. On MCP call failure, it tears down
// the client so the next request triggers a fresh spawn.
func (c *SourcegraphClient) handleRequest(
	ctx context.Context,
	mcpClient *client.MCPClient,
	system, user string,
) (string, error) {
	// Ensure MCP client is connected.
	if *mcpClient == nil {
		spawned, err := c.spawnMCP(ctx)
		if err != nil {
			return "", fmt.Errorf("sourcegraph: failed to spawn MCP server: %w", err)
		}
		*mcpClient = spawned
	}

	// Build the deepsearch tool call.
	toolReq := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "deepsearch",
			Arguments: map[string]any{
				"query": user,
			},
		},
	}

	result, err := (*mcpClient).CallTool(ctx, toolReq)
	if err != nil {
		// Process isolation: tear down broken client, return error to caller.
		c.closeMCPClient(mcpClient)
		return "", fmt.Errorf("sourcegraph: deepsearch call failed: %w", err)
	}

	if result.IsError {
		text := extractText(result)
		c.closeMCPClient(mcpClient)
		return "", fmt.Errorf("sourcegraph: deepsearch returned error: %s", text)
	}

	text := extractText(result)

	// Validation gate: check that the response references the target symbol.
	symbol := extractSymbol(user)
	if symbol != "" && !strings.Contains(strings.ToLower(text), strings.ToLower(symbol)) {
		return LowConfidenceSentinel, nil
	}

	return text, nil
}

// closeMCPClient tears down the current MCP client and sets the pointer to nil
// so a fresh one is spawned on the next request.
func (c *SourcegraphClient) closeMCPClient(mcpClient *client.MCPClient) {
	if *mcpClient != nil {
		if err := (*mcpClient).Close(); err != nil {
			log.Printf("sourcegraph: error closing failed MCP client: %v", err)
		}
		*mcpClient = nil
	}
}

// spawnMCP creates a new MCP client subprocess and initializes it.
func (c *SourcegraphClient) spawnMCP(ctx context.Context) (client.MCPClient, error) {
	env := os.Environ()
	// Ensure the access token and endpoint are set in the subprocess environment.
	env = append(env,
		"SRC_ACCESS_TOKEN="+c.accessToken,
	)
	if c.endpoint != "" {
		env = append(env, "SRC_ENDPOINT="+c.endpoint)
	}

	mcpCli, err := client.NewStdioMCPClient(c.command, env, c.args...)
	if err != nil {
		return nil, fmt.Errorf("failed to create stdio client: %w", err)
	}

	initReq := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "livedocs-sourcegraph",
				Version: "0.1.0",
			},
		},
	}
	if _, err := mcpCli.Initialize(ctx, initReq); err != nil {
		mcpCli.Close()
		return nil, fmt.Errorf("failed to initialize MCP client: %w", err)
	}

	return mcpCli, nil
}

// extractText concatenates all text content blocks from a CallToolResult.
func extractText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, c := range result.Content {
		if tc, ok := mcp.AsTextContent(c); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// extractSymbol attempts to extract the primary symbol name from the user
// prompt. It looks for common patterns like "symbol X" or the last word that
// looks like an identifier (contains a dot or uppercase letter).
func extractSymbol(user string) string {
	// Simple heuristic: look for backtick-delimited identifiers first.
	if idx := strings.Index(user, "`"); idx >= 0 {
		end := strings.Index(user[idx+1:], "`")
		if end >= 0 {
			return user[idx+1 : idx+1+end]
		}
	}

	// Fall back to the last word containing a dot (qualified name like pkg.Symbol).
	words := strings.Fields(user)
	for i := len(words) - 1; i >= 0; i-- {
		if strings.Contains(words[i], ".") {
			return strings.Trim(words[i], "?.,;:!\"'()")
		}
	}

	// Fall back to the last capitalized word (likely a type/function name).
	for i := len(words) - 1; i >= 0; i-- {
		w := strings.Trim(words[i], "?.,;:!\"'()")
		if len(w) > 0 && w[0] >= 'A' && w[0] <= 'Z' {
			return w
		}
	}

	return ""
}
