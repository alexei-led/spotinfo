package mcp

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spotinfo/internal/spot"
)

// TestStdioTransport_ContextCancellation tests that stdio transport respects context cancellation
func TestStdioTransport_ContextCancellation(t *testing.T) {
	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: spot.New(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)

	// Create context that we'll cancel quickly
	ctx, cancel := context.WithCancel(context.Background())

	// Start server in goroutine
	done := make(chan error, 1)
	go func() {
		done <- server.ServeStdio(ctx)
	}()

	// Cancel context immediately (stdio will block waiting for input)
	cancel()

	// Server should shut down gracefully
	select {
	case err := <-done:
		// We expect some error due to context cancellation or stdin handling
		t.Logf("ServeStdio returned: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down within timeout")
	}
}

// Note: Full stdio protocol testing should be done in integration tests,
// not unit tests, as it requires actual stdin/stdout handling and is
// difficult to mock properly without overly complex test setup.
