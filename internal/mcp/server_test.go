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
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid configuration",
			cfg: Config{
				Version:   "1.0.0",
				Logger:    slog.Default(),
				Providers: newEmbeddedRegistry(),
			},
			wantErr: false,
		},
		{
			name: "missing logger uses default",
			cfg: Config{
				Version:   "1.0.0",
				Providers: newEmbeddedRegistry(),
			},
			wantErr: false,
		},
		{
			name: "nil provider registry is allowed",
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
	t.Parallel()

	cfg := Config{
		Version:   "1.0.0",
		Logger:    slog.Default(),
		Providers: newEmbeddedRegistry(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)

	// The server should have registered tools - we verify this by ensuring
	// the MCP server was created (tools registration happens in NewServer)
	assert.NotNil(t, server.mcpServer)
}

func testProviderRegistry(t *testing.T, ids ...cloud.ProviderID) compiledRegistry {
	t.Helper()

	registrations := make([]providers.Registration, 0, len(ids))
	for _, id := range ids {
		registrations = append(registrations, providers.Registration{
			ID:    id,
			Build: func() (cloud.Provider, error) { return &stubProvider{id: id}, nil },
		})
	}

	registry, err := providers.New(registrations...)
	require.NoError(t, err)

	return compiledRegistry{registry: registry}
}

// The published tool names are exactly the three the plan names, and none of
// them bakes a cloud in: the cloud is an argument on every one.
func TestServerPublishesTheThreeCloudNeutralTools(t *testing.T) {
	t.Parallel()

	server, err := NewServer(Config{
		Version:   "1.0.0",
		Logger:    slog.Default(),
		Providers: testProviderRegistry(t, cloud.ProviderAWS, cloud.ProviderGCP),
	})
	require.NoError(t, err)

	tools := server.Tools()
	require.Len(t, tools, totalMCPTools)

	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
		assert.NotEmpty(t, tool.Description, "%s has no description", tool.Name)
		assert.Contains(t, tool.InputSchema.Properties, argCloud,
			"%s must take the cloud as an argument rather than implying one", tool.Name)
	}

	assert.Equal(t, []string{regionsToolName, listToolName, recommendToolName}, names,
		"the tool names are the plan's; renaming one is a client-visible contract change")

	assert.Equal(t, []cloud.ProviderID{cloud.ProviderAWS, cloud.ProviderGCP}, server.registeredProviders(),
		"available providers are reported in stable lexical order")
}

// Every tool is read-only, idempotent, non-destructive and open-world. A host
// decides whether to auto-approve a call from these hints, so an unset one is
// read as the mcp-go default — destructive and not idempotent — for a tool that
// only ever reads published price data.
func TestEveryToolDeclaresTheReadOnlyAnnotations(t *testing.T) {
	t.Parallel()

	server, err := NewServer(Config{Version: "1.0.0", Logger: slog.Default(), Providers: newEmbeddedRegistry()})
	require.NoError(t, err)

	tools := server.Tools()
	require.Len(t, tools, totalMCPTools)

	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()

			annotations := tool.Annotations
			require.NotNil(t, annotations.ReadOnlyHint, "readOnlyHint must be declared, not left to the default")
			require.NotNil(t, annotations.IdempotentHint, "idempotentHint must be declared")
			require.NotNil(t, annotations.DestructiveHint, "destructiveHint must be declared")
			require.NotNil(t, annotations.OpenWorldHint, "openWorldHint must be declared")

			assert.True(t, *annotations.ReadOnlyHint)
			assert.True(t, *annotations.IdempotentHint)
			assert.False(t, *annotations.DestructiveHint)
			assert.True(t, *annotations.OpenWorldHint)
		})
	}
}

// A binary composed without a registry still serves the AWS tools rather than
// failing to start.
func TestServerWithoutAProviderRegistryReportsNoProviders(t *testing.T) {
	t.Parallel()

	server, err := NewServer(Config{Version: "1.0.0", Logger: slog.Default()})
	require.NoError(t, err)

	assert.Nil(t, server.registeredProviders())
	assert.Len(t, server.mcpServer.ListTools(), totalMCPTools)
}

// A disabled provider is absent from the registration log rather than reported
// as something the server can answer for.
// A registered cloud is named at startup even when its snapshot turns out to be
// unusable: providers are built on first use, so the failure is not knowable
// here without decoding every catalogue. The request for that cloud is what
// reports it, which TestBrokenSnapshotDisablesOnlyItsOwnProvider covers.
func TestRegisteredProvidersAreNamedBeforeTheyAreBuilt(t *testing.T) {
	t.Parallel()

	registry, err := providers.New(providers.Registration{
		ID:    cloud.ProviderAzure,
		Build: func() (cloud.Provider, error) { return nil, errors.New("catalog hash mismatch") },
	})
	require.NoError(t, err)

	server, err := NewServer(Config{
		Version:   "1.0.0",
		Logger:    slog.Default(),
		Providers: compiledRegistry{registry: registry},
	})
	require.NoError(t, err)

	assert.Equal(t, []cloud.ProviderID{cloud.ProviderAzure}, server.registeredProviders())
}

// TestServeStdio_ContextCancellation tests that stdio server respects context cancellation
func TestServeStdio_ContextCancellation(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version:   "1.0.0",
		Logger:    slog.Default(),
		Providers: newEmbeddedRegistry(),
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
	t.Parallel()

	cfg := Config{
		Version:   "1.0.0",
		Logger:    slog.Default(),
		Providers: newEmbeddedRegistry(),
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
	t.Parallel()

	cfg := Config{
		Version:   "1.0.0",
		Logger:    slog.Default(),
		Providers: newEmbeddedRegistry(),
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
	t.Parallel()

	cfg := Config{
		Version:   "1.0.0",
		Logger:    slog.Default(),
		Providers: newEmbeddedRegistry(),
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
		Version:   "1.0.0",
		Logger:    slog.Default(),
		Providers: newEmbeddedRegistry(),
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
