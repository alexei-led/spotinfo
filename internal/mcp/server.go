// Package mcp provides Model Context Protocol server implementation for spotinfo.
package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"spotinfo/internal/cloud"
)

// totalMCPTools is the published tool count: one per CLI command, plus the
// region enumeration neither command covers.
const totalMCPTools = 3

// providerRegistry is the slice of the compiled provider registry this server
// needs, defined next to its consumer. Tools are registered against the neutral
// providers it hands out rather than against a provider SDK.
//
// Get takes the request's data policy because one server answers many clients:
// a snapshot-only question from one of them must not put the process into
// snapshot-only mode for the rest. The composition root decides how to build
// and reuse a client per policy; this side only states which one it needs.
type providerRegistry interface {
	Get(id cloud.ProviderID, policy cloud.FetchPolicy) (cloud.Provider, error)
	Registered() []cloud.ProviderID
}

// Server wraps the MCP server with spotinfo-specific configuration
type Server struct {
	mcpServer *server.MCPServer
	logger    *slog.Logger
	providers providerRegistry
}

// Config holds MCP server configuration
type Config struct {
	Logger    *slog.Logger
	Providers providerRegistry
	Version   string
	Transport string
	Port      string
}

// NewServer creates a new MCP server instance with spotinfo tools
//
//nolint:gocritic // Config is the public constructor shape; a pointer would break every caller to save one startup copy.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	// Create MCP server with tool capabilities
	mcpServer := server.NewMCPServer(
		"spotinfo",
		cfg.Version,
		server.WithToolCapabilities(true),
		server.WithLogging(),
	)

	s := &Server{
		mcpServer: mcpServer,
		logger:    cfg.Logger,
		providers: cfg.Providers,
	}

	// Register tools
	s.registerTools()

	return s, nil
}

// registerTools registers all spotinfo MCP tools. Each mirrors the CLI and
// takes the cloud as an argument, so the same question asked either way is the
// same document.
func (s *Server) registerTools() {
	s.logger.Debug("registering MCP tools")

	s.registerListTool()
	s.registerRecommendTool()
	s.registerRegionsTool()

	s.logger.Info("MCP tools registered",
		slog.Int("count", totalMCPTools),
		slog.Any("providers", s.registeredProviders()))
}

// Tools reports the tool definitions this server publishes, in stable name
// order.
//
// It is exported for the vocabulary test in cmd/spotinfo: mcpArgName lives in
// package main and cannot be imported here, so "every argument name is derived
// from its CLI flag" has to be asserted from the side that owns the vocabulary,
// against what this server actually registered rather than against a second
// list that could drift from it.
func (s *Server) Tools() []mcp.Tool {
	registered := s.mcpServer.ListTools()

	tools := make([]mcp.Tool, 0, len(registered))
	for _, name := range slices.Sorted(maps.Keys(registered)) {
		tools = append(tools, registered[name].Tool)
	}

	return tools
}

// registeredProviders names the clouds this binary was built to serve, for the
// startup log. Registration is unconditional so the advertised tool surface does
// not change with the data a binary happens to carry; a request for a cloud
// whose snapshot is unusable reports why.
//
// It reports registration, not health, because providers are built on first use:
// asking whether each one loads would decode every catalogue at startup, which
// is exactly the cost the registry defers.
func (s *Server) registeredProviders() []cloud.ProviderID {
	if s.providers == nil {
		return nil
	}

	return s.providers.Registered()
}

// ServeStdio starts the MCP server with stdio transport
func (s *Server) ServeStdio(ctx context.Context) error {
	s.logger.Info("starting MCP server with stdio transport")

	// Use the global ServeStdio function
	return server.ServeStdio(s.mcpServer)
}

// ServeSSE starts the MCP server with SSE transport on specified port
func (s *Server) ServeSSE(ctx context.Context, port string) error {
	s.logger.Info("starting MCP server with SSE transport", slog.String("port", port))

	// Create SSE server using the built-in mcp-go library support
	sseServer := server.NewSSEServer(s.mcpServer)

	// Start SSE server - this will block until context is cancelled or error occurs
	errChan := make(chan error, 1)
	go func() {
		errChan <- sseServer.Start(":" + port)
	}()

	// Wait for context cancellation or server error
	select {
	case <-ctx.Done():
		s.logger.Info("SSE server context cancelled, shutting down")
		return ctx.Err()
	case err := <-errChan:
		if err != nil {
			s.logger.Error("SSE server failed", slog.Any("error", err))
			return fmt.Errorf("SSE server failed: %w", err)
		}
		return nil
	}
}
