package mcp

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers"
)

// TestNewServer tests server creation with different configurations
func TestNewServer(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid configuration",
			cfg: Config{
				Version:    "1.0.0",
				Logger:     slog.Default(),
				SpotClient: newEmbeddedClient(),
			},
			wantErr: false,
		},
		{
			name: "missing logger uses default",
			cfg: Config{
				Version:    "1.0.0",
				SpotClient: newEmbeddedClient(),
			},
			wantErr: false,
		},
		{
			name: "nil spot client is allowed",
			cfg: Config{
				Version: "1.0.0",
				Logger:  slog.Default(),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, err := NewServer(tt.cfg)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, server)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, server)
			}
		})
	}
}

// TestServerToolRegistration verifies tools are registered during server creation
func TestServerToolRegistration(t *testing.T) {
	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: newEmbeddedClient(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)

	// The server should have registered tools - we verify this by ensuring
	// the MCP server was created (tools registration happens in NewServer)
	assert.NotNil(t, server.mcpServer)
}

// stubProvider stands in for a compiled provider: the server only asks for an
// identifier, so nothing here reaches a cloud.
type stubProvider struct{ id cloud.ProviderID }

func (p stubProvider) ID() cloud.ProviderID             { return p.id }
func (p stubProvider) Capabilities() cloud.Capabilities { return cloud.Capabilities{} }
func (p stubProvider) Query(context.Context, *cloud.Query) (cloud.Result, error) {
	return cloud.Result{Provider: p.id}, nil
}

func testProviderRegistry(t *testing.T, ids ...cloud.ProviderID) *providers.Registry {
	t.Helper()

	registrations := make([]providers.Registration, 0, len(ids))
	for _, id := range ids {
		registrations = append(registrations, providers.Registration{
			ID:    id,
			Build: func() (cloud.Provider, error) { return stubProvider{id: id}, nil },
		})
	}

	registry, err := providers.New(registrations...)
	require.NoError(t, err)

	return registry
}

// The server accepts the registry through a neutral interface. Task 3 does not
// change which tools are registered: the AWS v1 names must survive.
func TestServerAcceptsAProviderRegistryWithoutChangingAWSToolNames(t *testing.T) {
	server, err := NewServer(Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: newEmbeddedClient(),
		Providers:  testProviderRegistry(t, cloud.ProviderAWS, cloud.ProviderGCP),
	})
	require.NoError(t, err)

	tools := server.mcpServer.ListTools()
	assert.Contains(t, tools, "find_spot_instances")
	assert.Contains(t, tools, "list_spot_regions")
	assert.Len(t, tools, totalMCPTools)

	assert.Equal(t, []cloud.ProviderID{cloud.ProviderAWS, cloud.ProviderGCP}, server.availableProviders(),
		"available providers are reported in stable lexical order")
}

// A binary composed without a registry still serves the AWS tools rather than
// failing to start.
func TestServerWithoutAProviderRegistryReportsNoProviders(t *testing.T) {
	server, err := NewServer(Config{Version: "1.0.0", Logger: slog.Default(), SpotClient: newEmbeddedClient()})
	require.NoError(t, err)

	assert.Nil(t, server.availableProviders())
	assert.Len(t, server.mcpServer.ListTools(), totalMCPTools)
}

// A disabled provider is absent from the registration log rather than reported
// as something the server can answer for.
func TestDisabledProvidersAreNotReportedAsAvailable(t *testing.T) {
	registry, err := providers.New(providers.Registration{
		ID:    cloud.ProviderAzure,
		Build: func() (cloud.Provider, error) { return nil, errors.New("catalog hash mismatch") },
	})
	require.NoError(t, err)

	server, err := NewServer(Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: newEmbeddedClient(),
		Providers:  registry,
	})
	require.NoError(t, err)

	assert.Empty(t, server.availableProviders())
}

// TestServeStdio_ContextCancellation tests that stdio server respects context cancellation
func TestServeStdio_ContextCancellation(t *testing.T) {
	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: newEmbeddedClient(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)

	// Create context that we'll cancel
	ctx, cancel := context.WithCancel(context.Background())

	// Start server in goroutine
	done := make(chan error, 1)
	go func() {
		done <- server.ServeStdio(ctx)
	}()

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Server should shut down gracefully
	select {
	case err := <-done:
		// Any error is acceptable here since we're testing cancellation behavior
		t.Logf("ServeStdio returned with: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down within timeout")
	}
}

// TestServeSSE_ContextCancellation tests that SSE server respects context cancellation
func TestServeSSE_ContextCancellation(t *testing.T) {
	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: newEmbeddedClient(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)

	// Create context that we'll cancel
	ctx, cancel := context.WithCancel(context.Background())

	// Start server in goroutine
	done := make(chan error, 1)
	go func() {
		// Use port 0 to let OS choose available port
		done <- server.ServeSSE(ctx, "0")
	}()

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Server should shut down gracefully
	select {
	case err := <-done:
		// Should get context cancellation error
		assert.Error(t, err)
		t.Logf("ServeSSE returned with: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down within timeout")
	}
}

// TestServeSSE_InvalidPort tests error handling for invalid port
func TestServeSSE_InvalidPort(t *testing.T) {
	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: newEmbeddedClient(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Use invalid port
	err = server.ServeSSE(ctx, "invalid-port")
	assert.Error(t, err)
}

// TestServerConcurrentAccess tests that multiple operations can be performed concurrently
func TestServerConcurrentAccess(t *testing.T) {
	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: newEmbeddedClient(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)

	const numOperations = 5
	done := make(chan error, numOperations)

	// Perform concurrent server operations
	for range numOperations {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			// Each goroutine tries to start SSE server on different port
			port := "0" // Let OS choose port
			err := server.ServeSSE(ctx, port)
			done <- err
		}()
	}

	// Collect results - should either timeout or fail with port binding
	for i := range numOperations {
		err := <-done
		// Any error is acceptable - we're testing concurrent access doesn't panic
		t.Logf("Operation %d returned: %v", i, err)
	}
}

// BenchmarkServerCreation benchmarks server creation performance
func BenchmarkServerCreation(b *testing.B) {
	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: newEmbeddedClient(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		server, err := NewServer(cfg)
		if err != nil {
			b.Fatal(err)
		}
		_ = server
	}
}
