// Package mcp provides Model Context Protocol server implementation for spotinfo.
package mcp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/server"
)

// Server wraps the MCP server with spotinfo-specific configuration
type Server struct {
	mcpServer *server.MCPServer
	logger    *slog.Logger
}

// Config holds MCP server configuration
type Config struct {
	Logger    *slog.Logger
	Version   string
	Transport string
	Port      string
}

// NewServer creates a new MCP server instance with spotinfo tools
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
	}

	// Register tools
	s.registerTools()

	return s, nil
}

// registerTools registers all spotinfo MCP tools
func (s *Server) registerTools() {
	s.logger.Debug("registering MCP tools")

	// TODO: Register actual tools in Phase 2
	// - spot_recommend: Find best spot instances based on requirements
	// - spot_lookup: Get pricing data for specific instances
	// - spot_regions: List available AWS regions

	s.logger.Info("MCP tools registered", slog.Int("count", 0))
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

	// TODO: Implement SSE transport in Phase 3.2
	// Example: sseServer := server.NewSSEServer(s.mcpServer)
	// return sseServer.Start(":" + port)
	return fmt.Errorf("SSE transport not yet implemented - coming in Phase 3.2")
}
