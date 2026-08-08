// Package mcp provides Model Context Protocol server implementation for spotinfo.
package mcp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"spotinfo/internal/cloud"
)

// Constants for MCP server configuration
const (
	defaultMaxInterruptionRateParam = 100
	defaultLimitParam               = 10
	maxLimitParam                   = 50
	totalMCPTools                   = 3
	maxScoreValue                   = 10
	maxScoreTimeoutSeconds          = 300
	// defaultScoreTimeoutSeconds is the advertised score_timeout default. It is
	// declared here rather than imported from the AWS package: the published
	// input schema is a client contract, and find-spot-instances-v1-input-schema.json
	// pins this value.
	defaultScoreTimeoutSeconds = 30
)

// providerRegistry is the slice of the compiled provider registry this server
// needs, defined next to its consumer. Tools are registered against the neutral
// providers it hands out rather than against a provider SDK.
type providerRegistry interface {
	Get(id cloud.ProviderID) (cloud.Provider, error)
	Available() []cloud.Provider
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

// registerTools registers all spotinfo MCP tools
func (s *Server) registerTools() {
	s.logger.Debug("registering MCP tools")

	// Register find_spot_instances tool - combines search and lookup functionality
	findSpotInstancesTool := mcp.NewTool("find_spot_instances",
		mcp.WithDescription("Search for AWS EC2 Spot Instance options based on requirements. Returns pricing, savings, and interruption data."),
		mcp.WithArray(argRegions,
			mcp.Description("AWS regions to search (e.g., ['us-east-1', 'eu-west-1']). Use ['all'] or omit to search all regions"),
			mcp.Items(map[string]any{jsonSchemaType: jsonTypeString})),
		mcp.WithString(argInstanceTypes,
			mcp.Description("Instance type pattern - exact type (e.g., 'm5.large') or pattern (e.g., 't3.*', 'm5.*')")),
		mcp.WithNumber(argMinVCPU,
			mcp.Description("Minimum number of vCPUs required"),
			mcp.DefaultNumber(0)),
		mcp.WithNumber(argMinMemoryGB,
			mcp.Description("Minimum memory in gigabytes"),
			mcp.DefaultNumber(0)),
		mcp.WithNumber(argMaxPricePerHour,
			mcp.Description("Maximum spot price per hour in USD"),
			mcp.DefaultNumber(0)),
		mcp.WithNumber(argMaxInterruptionRate,
			mcp.Description("Maximum acceptable interruption rate percentage (0-100)"),
			mcp.DefaultNumber(defaultMaxInterruptionRateParam)),
		mcp.WithString(argSortBy,
			mcp.Description("Sort results by: 'price' (cheapest first), 'reliability' (lowest interruption first), 'savings' (highest savings first), 'score' (highest score first)"),
			mcp.DefaultString(sortByReliability)),
		mcp.WithNumber(argLimit,
			mcp.Description("Maximum number of results to return"),
			mcp.DefaultNumber(defaultLimitParam),
			mcp.Max(maxLimitParam)),
		mcp.WithBoolean(argWithScore,
			mcp.Description("Include AWS spot placement scores (experimental)"),
			mcp.DefaultBool(false)),
		mcp.WithNumber(argMinScore,
			mcp.Description("Filter: minimum spot placement score (1-10)"),
			mcp.DefaultNumber(0),
			mcp.Min(0),
			mcp.Max(maxScoreValue)),
		mcp.WithBoolean(argAZ,
			mcp.Description("Request AZ-level scores instead of region-level (use with --with-score)"),
			mcp.DefaultBool(false)),
		mcp.WithNumber(argScoreTimeout,
			mcp.Description("Timeout for score enrichment in seconds"),
			mcp.DefaultNumber(defaultScoreTimeoutSeconds),
			mcp.Min(1),
			mcp.Max(maxScoreTimeoutSeconds)),
	)

	findSpotInstancesHandler := NewFindSpotInstancesTool(s.providers, s.logger)
	s.mcpServer.AddTool(findSpotInstancesTool, findSpotInstancesHandler.Handle)

	// Register list_spot_regions tool
	// No arguments: the handler ignores the request and the underlying advisor
	// feed carries no human-readable region names. An include_names boolean was
	// advertised here with DefaultBool(true) but was never read by any handler,
	// so clients were told of a parameter that did nothing.
	listSpotRegionsTool := mcp.NewTool("list_spot_regions",
		mcp.WithDescription("List all AWS regions where EC2 Spot Instances are available"),
	)

	listSpotRegionsHandler := NewListSpotRegionsTool(s.providers, s.logger)
	s.mcpServer.AddTool(listSpotRegionsTool, listSpotRegionsHandler.Handle)

	s.registerRecommendTool()

	s.logger.Info("MCP tools registered",
		slog.Int("count", totalMCPTools),
		slog.Any("providers", s.availableProviders()))
}

// availableProviders names the providers this server can serve. Registration is
// unconditional so the advertised tool surface does not change with the data a
// binary happens to carry; a request for a disabled cloud reports why.
func (s *Server) availableProviders() []cloud.ProviderID {
	if s.providers == nil {
		return nil
	}

	available := s.providers.Available()

	ids := make([]cloud.ProviderID, 0, len(available))
	for _, provider := range available {
		ids = append(ids, provider.ID())
	}

	return ids
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
