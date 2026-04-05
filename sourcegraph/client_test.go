package sourcegraph

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// mockMCPClient is a test double for client.MCPClient.
type mockMCPClient struct {
	initializeFunc func(ctx context.Context, req mcp.InitializeRequest) (*mcp.InitializeResult, error)
	callToolFunc   func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
	closeFunc      func() error
	closed         atomic.Bool
}

func (m *mockMCPClient) Initialize(ctx context.Context, req mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	if m.initializeFunc != nil {
		return m.initializeFunc(ctx, req)
	}
	return &mcp.InitializeResult{}, nil
}

func (m *mockMCPClient) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if m.callToolFunc != nil {
		return m.callToolFunc(ctx, req)
	}
	return &mcp.CallToolResult{}, nil
}

func (m *mockMCPClient) Close() error {
	m.closed.Store(true)
	if m.closeFunc != nil {
		return m.closeFunc()
	}
	return nil
}

func (m *mockMCPClient) Ping(ctx context.Context) error { return nil }

func (m *mockMCPClient) ListResources(ctx context.Context, req mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
	return nil, nil
}
func (m *mockMCPClient) ListResourcesByPage(ctx context.Context, req mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
	return nil, nil
}
func (m *mockMCPClient) ListResourceTemplates(ctx context.Context, req mcp.ListResourceTemplatesRequest) (*mcp.ListResourceTemplatesResult, error) {
	return nil, nil
}
func (m *mockMCPClient) ListResourceTemplatesByPage(ctx context.Context, req mcp.ListResourceTemplatesRequest) (*mcp.ListResourceTemplatesResult, error) {
	return nil, nil
}
func (m *mockMCPClient) ReadResource(ctx context.Context, req mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	return nil, nil
}
func (m *mockMCPClient) Subscribe(ctx context.Context, req mcp.SubscribeRequest) error { return nil }
func (m *mockMCPClient) Unsubscribe(ctx context.Context, req mcp.UnsubscribeRequest) error {
	return nil
}
func (m *mockMCPClient) ListPrompts(ctx context.Context, req mcp.ListPromptsRequest) (*mcp.ListPromptsResult, error) {
	return nil, nil
}
func (m *mockMCPClient) ListPromptsByPage(ctx context.Context, req mcp.ListPromptsRequest) (*mcp.ListPromptsResult, error) {
	return nil, nil
}
func (m *mockMCPClient) GetPrompt(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return nil, nil
}
func (m *mockMCPClient) ListTools(ctx context.Context, req mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	return nil, nil
}
func (m *mockMCPClient) ListToolsByPage(ctx context.Context, req mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	return nil, nil
}
func (m *mockMCPClient) SetLevel(ctx context.Context, req mcp.SetLevelRequest) error { return nil }
func (m *mockMCPClient) Complete(ctx context.Context, req mcp.CompleteRequest) (*mcp.CompleteResult, error) {
	return nil, nil
}
func (m *mockMCPClient) OnNotification(handler func(notification mcp.JSONRPCNotification)) {}

// --- Tests ---

func TestGracefulDegradation_MissingToken(t *testing.T) {
	c := &SourcegraphClient{
		command:     "echo",
		accessToken: "", // empty token
		reqCh:       make(chan *callRequest, 16),
		done:        make(chan struct{}),
	}

	_, err := c.Complete(context.Background(), "system", "user query")
	if err == nil {
		t.Fatal("expected error for missing SRC_ACCESS_TOKEN")
	}
	if !strings.Contains(err.Error(), "SRC_ACCESS_TOKEN") {
		t.Fatalf("error should mention SRC_ACCESS_TOKEN, got: %v", err)
	}
}

func TestValidationGate_SymbolPresent(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent("The function FooBar does X and Y."),
		},
	}
	text := extractText(result)
	symbol := extractSymbol("What does `FooBar` do?")

	if symbol != "FooBar" {
		t.Fatalf("expected symbol FooBar, got %q", symbol)
	}
	if !strings.Contains(strings.ToLower(text), strings.ToLower(symbol)) {
		t.Fatal("expected text to contain symbol")
	}
}

func TestValidationGate_SymbolMissing(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent("This response talks about something else entirely."),
		},
	}
	text := extractText(result)
	symbol := extractSymbol("What does `FooBar` do?")

	if symbol != "FooBar" {
		t.Fatalf("expected symbol FooBar, got %q", symbol)
	}
	if strings.Contains(strings.ToLower(text), strings.ToLower(symbol)) {
		t.Fatal("expected text NOT to contain symbol for this test")
	}
}

func TestValidationGate_QualifiedName(t *testing.T) {
	symbol := extractSymbol("Describe the function pkg.MyFunc in detail")
	if symbol != "pkg.MyFunc" {
		t.Fatalf("expected symbol pkg.MyFunc, got %q", symbol)
	}
}

func TestValidationGate_CapitalizedWord(t *testing.T) {
	symbol := extractSymbol("Describe the function MyStruct in detail")
	if symbol != "MyStruct" {
		t.Fatalf("expected symbol MyStruct, got %q", symbol)
	}
}

func TestValidationGate_NoSymbol(t *testing.T) {
	symbol := extractSymbol("just some lowercase words here")
	if symbol != "" {
		t.Fatalf("expected empty symbol, got %q", symbol)
	}
}

func TestExtractText(t *testing.T) {
	tests := []struct {
		name   string
		result *mcp.CallToolResult
		want   string
	}{
		{
			name:   "nil result",
			result: nil,
			want:   "",
		},
		{
			name: "single text block",
			result: &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent("hello"),
				},
			},
			want: "hello",
		},
		{
			name: "multiple text blocks",
			result: &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent("hello"),
					mcp.NewTextContent("world"),
				},
			},
			want: "hello\nworld",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractText(tt.result)
			if got != tt.want {
				t.Errorf("extractText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		input   string
		wantCmd string
		wantN   int // expected number of args
	}{
		{"", defaultCommand, len(defaultArgs)},
		{"custom-bin", "custom-bin", 0},
		{"npx -y @sourcegraph/mcp", "npx", 2},
	}
	for _, tt := range tests {
		cmd, args := parseCommand(tt.input)
		if cmd != tt.wantCmd {
			t.Errorf("parseCommand(%q) cmd = %q, want %q", tt.input, cmd, tt.wantCmd)
		}
		if len(args) != tt.wantN {
			t.Errorf("parseCommand(%q) args len = %d, want %d", tt.input, len(args), tt.wantN)
		}
	}
}

// testableClient creates a SourcegraphClient with an injected MCP client
// factory for testing the worker loop without spawning real subprocesses.
func testableClient(factory func(ctx context.Context) (*mockMCPClient, error)) *SourcegraphClient {
	c := &SourcegraphClient{
		command:     "echo",
		accessToken: "test-token",
		reqCh:       make(chan *callRequest, 16),
		done:        make(chan struct{}),
	}

	// Override: start a custom worker that uses the factory.
	c.mu.Lock()
	c.started = true
	c.mu.Unlock()

	go func() {
		defer close(c.done)
		var mock *mockMCPClient

		for req := range c.reqCh {
			if mock == nil || mock.closed.Load() {
				var err error
				mock, err = factory(req.ctx)
				if err != nil {
					req.result <- callResult{err: fmt.Errorf("sourcegraph: failed to spawn MCP server: %w", err)}
					continue
				}
			}

			toolReq := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      "deepsearch",
					Arguments: map[string]any{"query": req.user},
				},
			}

			result, err := mock.CallTool(req.ctx, toolReq)
			if err != nil {
				mock.Close()
				req.result <- callResult{err: fmt.Errorf("sourcegraph: deepsearch call failed: %w", err)}
				continue
			}

			if result.IsError {
				text := extractText(result)
				mock.Close()
				req.result <- callResult{err: fmt.Errorf("sourcegraph: deepsearch returned error: %s", text)}
				continue
			}

			text := extractText(result)

			symbol := extractSymbol(req.user)
			if symbol != "" && !strings.Contains(strings.ToLower(text), strings.ToLower(symbol)) {
				req.result <- callResult{text: LowConfidenceSentinel}
				continue
			}

			req.result <- callResult{text: text}
		}

		if mock != nil && !mock.closed.Load() {
			mock.Close()
		}
	}()

	return c
}

func TestLifecycle_SpawnCallShutdown(t *testing.T) {
	spawnCount := atomic.Int32{}

	c := testableClient(func(ctx context.Context) (*mockMCPClient, error) {
		spawnCount.Add(1)
		return &mockMCPClient{
			callToolFunc: func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						mcp.NewTextContent("FooBar is a function that does X"),
					},
				}, nil
			},
		}, nil
	})
	defer c.Close()

	ctx := context.Background()
	text, err := c.Complete(ctx, "system", "What does `FooBar` do?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "FooBar") {
		t.Fatalf("expected response containing FooBar, got: %s", text)
	}
	if spawnCount.Load() != 1 {
		t.Fatalf("expected 1 spawn, got %d", spawnCount.Load())
	}

	// Second call should reuse the same client.
	_, err = c.Complete(ctx, "system", "What does `FooBar` do?")
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if spawnCount.Load() != 1 {
		t.Fatalf("expected still 1 spawn after reuse, got %d", spawnCount.Load())
	}
}

func TestSerialization_ConcurrentCalls(t *testing.T) {
	var mu sync.Mutex
	var active int
	maxActive := 0

	c := testableClient(func(ctx context.Context) (*mockMCPClient, error) {
		return &mockMCPClient{
			callToolFunc: func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				mu.Lock()
				active++
				if active > maxActive {
					maxActive = active
				}
				mu.Unlock()

				time.Sleep(10 * time.Millisecond)

				mu.Lock()
				active--
				mu.Unlock()

				return &mcp.CallToolResult{
					Content: []mcp.Content{
						mcp.NewTextContent("Result for FooBar query"),
					},
				}, nil
			},
		}, nil
	})
	defer c.Close()

	ctx := context.Background()
	const n = 5
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = c.Complete(ctx, "system", "Tell me about `FooBar`")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d failed: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if maxActive > 1 {
		t.Fatalf("expected max 1 concurrent call, got %d", maxActive)
	}
}

func TestRestartOnFailure(t *testing.T) {
	spawnCount := atomic.Int32{}
	callCount := atomic.Int32{}

	c := testableClient(func(ctx context.Context) (*mockMCPClient, error) {
		spawnCount.Add(1)
		return &mockMCPClient{
			callToolFunc: func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				n := callCount.Add(1)
				if n == 1 {
					// First call fails (simulating subprocess crash).
					return nil, fmt.Errorf("connection lost")
				}
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						mcp.NewTextContent("FooBar recovered result"),
					},
				}, nil
			},
		}, nil
	})
	defer c.Close()

	ctx := context.Background()

	// First call should fail.
	_, err := c.Complete(ctx, "system", "What does `FooBar` do?")
	if err == nil {
		t.Fatal("expected error on first call")
	}
	if !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("expected 'connection lost' error, got: %v", err)
	}

	// Second call should succeed after restart.
	text, err := c.Complete(ctx, "system", "What does `FooBar` do?")
	if err != nil {
		t.Fatalf("expected second call to succeed, got: %v", err)
	}
	if !strings.Contains(text, "FooBar") {
		t.Fatalf("expected FooBar in response, got: %s", text)
	}

	if spawnCount.Load() < 2 {
		t.Fatalf("expected at least 2 spawns (original + restart), got %d", spawnCount.Load())
	}
}

func TestValidationGate_Integration(t *testing.T) {
	c := testableClient(func(ctx context.Context) (*mockMCPClient, error) {
		return &mockMCPClient{
			callToolFunc: func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						mcp.NewTextContent("This response talks about unrelated topics only."),
					},
				}, nil
			},
		}, nil
	})
	defer c.Close()

	ctx := context.Background()
	text, err := c.Complete(ctx, "system", "What does `MySpecificFunc` do?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != LowConfidenceSentinel {
		t.Fatalf("expected low-confidence sentinel, got: %s", text)
	}
}

func TestContextCancellation(t *testing.T) {
	c := &SourcegraphClient{
		command:     "echo",
		accessToken: "test-token",
		reqCh:       make(chan *callRequest), // unbuffered — blocks forever
		done:        make(chan struct{}),
	}
	c.mu.Lock()
	c.started = true
	c.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := c.Complete(ctx, "system", "query")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "context cancelled") {
		t.Fatalf("expected context cancelled error, got: %v", err)
	}
}
