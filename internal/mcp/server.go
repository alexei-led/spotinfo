// Package mcp provides Model Context Protocol server implementation for spotinfo.
package mcp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"spotinfo/internal/spot"
)

// Constants for MCP server configuration
const (
	defaultMaxInterruptionRateParam = 100
	defaultLimitParam               = 10
	maxLimitParam                   = 50
	totalMCPTools                   = 2
	maxScoreValue                   = 10
	maxScoreTimeoutSeconds          = 300
)

// spotClient interface defined close to consumer for testing (following codebase patterns)
type spotClient interface {
	GetSpotSavings(ctx context.Context, opts ...spot.GetSpotSavingsOption) ([]spot.Advice, error)
}

// Server wraps the MCP server with spotinfo-specific configuration
type Server struct {
	mcpServer  *server.MCPServer
	logger     *slog.Logger
	spotClient spotClient
}

// Config holds MCP server configuration
type Config struct {
	Logger     *slog.Logger
	SpotClient spotClient
	Version    string
	Transport  string
	Port       string
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
		mcpServer:  mcpServer,
		logger:     cfg.Logger,
		spotClient: cfg.SpotClient,
	}

	// Register tools
	s.registerTools()

	return s, nil
}

// registerTools registers all spotinfo MCP tools
func (s *Server) registerTools() {
	s.logger.Debug("registering MCP tools")

	// Register find_spot_instances tool - combines search and lookup functionality
	findSpotInstancesTool := mcp.NewTool("find_spot_instances",
		mcp.WithDescription("Search for AWS EC2 Spot Instance options based on requirements. Returns pricing, savings, and interruption data."),
		mcp.WithArray("regions",
			mcp.Description("AWS regions to search (e.g., ['us-east-1', 'eu-west-1']). Use ['all'] or omit to search all regions"),
			mcp.Items(map[string]any{"type": "string"})),
		mcp.WithString("instance_types",
			mcp.Description("Instance type pattern - exact type (e.g., 'm5.large') or pattern (e.g., 't3.*', 'm5.*')")),
		mcp.WithNumber("min_vcpu",
			mcp.Description("Minimum number of vCPUs required"),
			mcp.DefaultNumber(0)),
		mcp.WithNumber("min_memory_gb",
			mcp.Description("Minimum memory in gigabytes"),
			mcp.DefaultNumber(0)),
		mcp.WithNumber("max_price_per_hour",
			mcp.Description("Maximum spot price per hour in USD"),
			mcp.DefaultNumber(0)),
		mcp.WithNumber("max_interruption_rate",
			mcp.Description("Maximum acceptable interruption rate percentage (0-100)"),
			mcp.DefaultNumber(defaultMaxInterruptionRateParam)),
		mcp.WithString("sort_by",
			mcp.Description("Sort results by: 'price' (cheapest first), 'reliability' (lowest interruption first), 'savings' (highest savings first), 'score' (highest score first)"),
			mcp.DefaultString("reliability")),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return"),
			mcp.DefaultNumber(defaultLimitParam),
			mcp.Max(maxLimitParam)),
		mcp.WithBoolean("with_score",
			mcp.Description("Include AWS spot placement scores (experimental)"),
			mcp.DefaultBool(false)),
		mcp.WithNumber("min_score",
			mcp.Description("Filter: minimum spot placement score (1-10)"),
			mcp.DefaultNumber(0),
			mcp.Min(0),
			mcp.Max(maxScoreValue)),
		mcp.WithBoolean("az",
			mcp.Description("Request AZ-level scores instead of region-level (use with --with-score)"),
			mcp.DefaultBool(false)),
		mcp.WithNumber("score_timeout",
			mcp.Description("Timeout for score enrichment in seconds"),
			mcp.DefaultNumber(spot.DefaultScoreTimeoutSeconds),
			mcp.Min(1),
			mcp.Max(maxScoreTimeoutSeconds)),
	)

	findSpotInstancesHandler := NewFindSpotInstancesTool(s.spotClient, s.logger)
	s.mcpServer.AddTool(findSpotInstancesTool, findSpotInstancesHandler.Handle)

	// Register list_spot_regions tool
	listSpotRegionsTool := mcp.NewTool("list_spot_regions",
		mcp.WithDescription("List all AWS regions where EC2 Spot Instances are available"),
		mcp.WithBoolean("include_names",
			mcp.Description("Include human-readable region names (e.g., 'US East (N. Virginia)')"),
			mcp.DefaultBool(true)),
	)

	listSpotRegionsHandler := NewListSpotRegionsTool(s.spotClient, s.logger)
	s.mcpServer.AddTool(listSpotRegionsTool, listSpotRegionsHandler.Handle)

	s.logger.Info("MCP tools registered", slog.Int("count", totalMCPTools))
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
