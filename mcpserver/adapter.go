// Package mcpserver adapter.go isolates all mcp-go imports behind stable
// interfaces. When mcp-go (pre-1.0) introduces breaking API changes, only
// this file needs updating. Tool handlers depend on ToolRequest / ToolResult
// interfaces instead of mcp.CallToolRequest / mcp.CallToolResult directly.
package mcpserver

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---------------------------------------------------------------------------
// Stable interfaces — tool handlers depend on these, not on mcp-go types.
// ---------------------------------------------------------------------------

// ToolRequest abstracts mcp.CallToolRequest so handlers never import mcp-go.
type ToolRequest interface {
	// GetString returns the string argument for key, or defaultValue if absent.
	GetString(key, defaultValue string) string
	// RequireString returns the string argument for key, or an error if absent.
	RequireString(key string) (string, error)
	// GetInt returns the int argument for key, or defaultValue if absent.
	GetInt(key string, defaultValue int) int
	// RequireInt returns the int argument for key, or an error if absent.
	RequireInt(key string) (int, error)
	// GetFloat returns the float argument for key, or defaultValue if absent.
	GetFloat(key string, defaultValue float64) float64
	// RequireFloat returns the float argument for key, or an error if absent.
	RequireFloat(key string) (float64, error)
	// GetBool returns the bool argument for key, or defaultValue if absent.
	GetBool(key string, defaultValue bool) bool
	// RequireBool returns the bool argument for key, or an error if absent.
	RequireBool(key string) (bool, error)
	// GetArguments returns the raw argument map.
	GetArguments() map[string]any
}

// ToolResult abstracts mcp.CallToolResult. Handlers return this interface.
type ToolResult interface {
	// IsError reports whether the result represents an error.
	IsError() bool
	// Text returns the first text content, or "" if none.
	Text() string
	// Unwrap returns the underlying *mcp.CallToolResult for transport.
	Unwrap() *mcp.CallToolResult
}

// ToolHandler is the adapter-level handler signature. Handlers accept a
// ToolRequest (not mcp.CallToolRequest) and return a ToolResult.
type ToolHandler func(ctx context.Context, req ToolRequest) (ToolResult, error)

// ToolDef describes a tool registration: its name, description, parameters,
// and handler. This avoids exposing mcp.Tool in the handler layer.
type ToolDef struct {
	Name        string
	Description string
	Params      []ParamDef
	Handler     ToolHandler
}

// ParamDef describes one tool parameter.
type ParamDef struct {
	Name        string
	Type        ParamType
	Required    bool
	Description string
}

// ParamType enumerates supported parameter types.
type ParamType int

const (
	ParamString ParamType = iota
	ParamNumber
	ParamBool
)

// ToolRegistry abstracts the mcp-go server so callers can register tools
// and serve without importing mcp-go.
type ToolRegistry interface {
	// Register adds a tool to the server.
	Register(def ToolDef)
	// Serve starts the server over stdio (blocks).
	Serve() error
	// Underlying returns the raw *server.MCPServer for cases that need
	// direct access (e.g. custom transports). Callers accepting this
	// interface should avoid calling Underlying when possible.
	Underlying() *server.MCPServer
}

// ---------------------------------------------------------------------------
// Concrete implementations
// ---------------------------------------------------------------------------

// requestAdapter wraps mcp.CallToolRequest to satisfy ToolRequest.
type requestAdapter struct {
	raw mcp.CallToolRequest
}

func (r *requestAdapter) GetString(key, defaultValue string) string {
	return r.raw.GetString(key, defaultValue)
}

func (r *requestAdapter) RequireString(key string) (string, error) {
	return r.raw.RequireString(key)
}

func (r *requestAdapter) GetInt(key string, defaultValue int) int {
	return r.raw.GetInt(key, defaultValue)
}

func (r *requestAdapter) RequireInt(key string) (int, error) {
	return r.raw.RequireInt(key)
}

func (r *requestAdapter) GetFloat(key string, defaultValue float64) float64 {
	return r.raw.GetFloat(key, defaultValue)
}

func (r *requestAdapter) RequireFloat(key string) (float64, error) {
	return r.raw.RequireFloat(key)
}

func (r *requestAdapter) GetBool(key string, defaultValue bool) bool {
	return r.raw.GetBool(key, defaultValue)
}

func (r *requestAdapter) RequireBool(key string) (bool, error) {
	return r.raw.RequireBool(key)
}

func (r *requestAdapter) GetArguments() map[string]any {
	return r.raw.GetArguments()
}

// resultAdapter wraps *mcp.CallToolResult to satisfy ToolResult.
type resultAdapter struct {
	raw *mcp.CallToolResult
}

func (r *resultAdapter) IsError() bool {
	return r.raw.IsError
}

func (r *resultAdapter) Text() string {
	if len(r.raw.Content) == 0 {
		return ""
	}
	if tc, ok := r.raw.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

func (r *resultAdapter) Unwrap() *mcp.CallToolResult {
	return r.raw
}

// WrapRequest creates a ToolRequest from an mcp.CallToolRequest.
func WrapRequest(req mcp.CallToolRequest) ToolRequest {
	return &requestAdapter{raw: req}
}

// WrapResult creates a ToolResult from an *mcp.CallToolResult.
func WrapResult(res *mcp.CallToolResult) ToolResult {
	return &resultAdapter{raw: res}
}

// NewTextResult creates a successful text ToolResult.
func NewTextResult(text string) ToolResult {
	return &resultAdapter{raw: mcp.NewToolResultText(text)}
}

// NewErrorResult creates an error ToolResult.
func NewErrorResult(text string) ToolResult {
	return &resultAdapter{raw: mcp.NewToolResultError(text)}
}

// NewErrorResultf creates a formatted error ToolResult.
func NewErrorResultf(format string, args ...any) ToolResult {
	return &resultAdapter{raw: mcp.NewToolResultError(fmt.Sprintf(format, args...))}
}

// ---------------------------------------------------------------------------
// Registry implementation
// ---------------------------------------------------------------------------

// mcpRegistry is the concrete ToolRegistry backed by server.MCPServer.
type mcpRegistry struct {
	srv *server.MCPServer
}

// NewRegistry creates a ToolRegistry backed by a new mcp-go MCPServer.
func NewRegistry(name, version string) ToolRegistry {
	srv := server.NewMCPServer(
		name,
		version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)
	return &mcpRegistry{srv: srv}
}

// Register converts a ToolDef into mcp-go types and registers it.
func (r *mcpRegistry) Register(def ToolDef) {
	tool := buildTool(def)
	handler := adaptHandler(def.Handler)
	r.srv.AddTool(tool, handler)
}

// Serve starts the server over stdio.
func (r *mcpRegistry) Serve() error {
	return server.ServeStdio(r.srv)
}

// Underlying returns the raw MCPServer.
func (r *mcpRegistry) Underlying() *server.MCPServer {
	return r.srv
}

// ---------------------------------------------------------------------------
// Internal helpers — translate adapter types to mcp-go types.
// ---------------------------------------------------------------------------

// buildTool converts a ToolDef into an mcp.Tool.
func buildTool(def ToolDef) mcp.Tool {
	opts := []mcp.ToolOption{
		mcp.WithDescription(def.Description),
	}
	for _, p := range def.Params {
		opts = append(opts, paramOption(p))
	}
	return mcp.NewTool(def.Name, opts...)
}

// paramOption converts a ParamDef into the appropriate mcp.ToolOption.
func paramOption(p ParamDef) mcp.ToolOption {
	var propOpts []mcp.PropertyOption
	if p.Required {
		propOpts = append(propOpts, mcp.Required())
	}
	if p.Description != "" {
		propOpts = append(propOpts, mcp.Description(p.Description))
	}

	switch p.Type {
	case ParamNumber:
		return mcp.WithNumber(p.Name, propOpts...)
	case ParamBool:
		return mcp.WithBoolean(p.Name, propOpts...)
	default: // ParamString
		return mcp.WithString(p.Name, propOpts...)
	}
}

// adaptHandler wraps a ToolHandler into a server.ToolHandlerFunc.
func adaptHandler(h ToolHandler) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := h(ctx, WrapRequest(req))
		if err != nil {
			return nil, err
		}
		return result.Unwrap(), nil
	}
}
