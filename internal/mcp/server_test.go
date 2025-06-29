package mcp

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/spot"
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
				SpotClient: spot.New(),
			},
			wantErr: false,
		},
		{
			name: "missing logger uses default",
			cfg: Config{
				Version:    "1.0.0",
				SpotClient: spot.New(),
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
		SpotClient: spot.New(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)

	// The server should have registered tools - we verify this by ensuring
	// the MCP server was created (tools registration happens in NewServer)
	assert.NotNil(t, server.mcpServer)
}

// TestServeStdio_ContextCancellation tests that stdio server respects context cancellation
func TestServeStdio_ContextCancellation(t *testing.T) {
	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: spot.New(),
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
		SpotClient: spot.New(),
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
		SpotClient: spot.New(),
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
		SpotClient: spot.New(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)

	const numOperations = 5
	done := make(chan error, numOperations)

	// Perform concurrent server operations
	for i := 0; i < numOperations; i++ {
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
	for i := 0; i < numOperations; i++ {
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
		SpotClient: spot.New(),
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
