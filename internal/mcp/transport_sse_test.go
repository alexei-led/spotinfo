package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/spot"
)

// TestSSETransportBasic tests basic SSE transport functionality
func TestSSETransportBasic(t *testing.T) {
	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: spot.New(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	assert.NotNil(t, server)

	// Create context with timeout for the test
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Test SSE server startup and shutdown
	errChan := make(chan error, 1)
	go func() {
		// Use a random port for testing
		errChan <- server.ServeSSE(ctx, "0") // Port 0 lets OS choose available port
	}()

	// Wait for either timeout or server error
	select {
	case err := <-errChan:
		// If we get context.Canceled, that's expected (timeout)
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSE server did not start or respond within timeout")
	}
}

// TestSSETransportContextCancellation tests graceful shutdown
func TestSSETransportContextCancellation(t *testing.T) {
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
	errChan := make(chan error, 1)
	go func() {
		errChan <- server.ServeSSE(ctx, "0")
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Cancel context to trigger shutdown
	cancel()

	// Wait for server to shut down
	select {
	case err := <-errChan:
		// Should get context canceled error
		assert.True(t, errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled"))
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down within timeout")
	}
}

// TestSSETransportPortBinding tests port binding behavior
func TestSSETransportPortBinding(t *testing.T) {
	tests := []struct {
		name        string
		port        string
		expectError bool
	}{
		{
			name:        "valid port",
			port:        "0", // Let OS choose
			expectError: false,
		},
		{
			name:        "invalid port",
			port:        "invalid",
			expectError: true,
		},
		{
			name:        "port too high",
			port:        "99999",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Version:    "1.0.0",
				Logger:     slog.Default(),
				SpotClient: spot.New(),
			}

			server, err := NewServer(cfg)
			require.NoError(t, err)

			// Create context with short timeout
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()

			err = server.ServeSSE(ctx, tt.port)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				// For valid ports, we expect timeout/cancellation, not startup errors
				assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
			}
		})
	}
}

// TestSSETransportWithMockClient tests SSE with mock spot client
func TestSSETransportWithMockClient(t *testing.T) {
	mockClient := newMockspotClient(t)

	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: mockClient,
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Test that server can start with mock client
	errChan := make(chan error, 1)
	go func() {
		errChan <- server.ServeSSE(ctx, "0")
	}()

	select {
	case err := <-errChan:
		// Should get timeout/cancellation, not startup error
		assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
	case <-time.After(2 * time.Second):
		t.Fatal("test timed out")
	}
}

// TestSSEServerCreation tests that SSE server can be created properly
func TestSSEServerCreation(t *testing.T) {
	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: spot.New(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	assert.NotNil(t, server)
	assert.NotNil(t, server.mcpServer)

	// Verify we can call SSE method without panicking
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err = server.ServeSSE(ctx, "8080")
	assert.Error(t, err) // Should get context canceled error
	assert.Contains(t, err.Error(), "context canceled")
}

// TestSSEConcurrentAccess tests concurrent access to SSE server
func TestSSEConcurrentAccess(t *testing.T) {
	cfg := Config{
		Version:    "1.0.0",
		Logger:     slog.Default(),
		SpotClient: spot.New(),
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)

	const numGoroutines = 5
	errChan := make(chan error, numGoroutines)

	// Start multiple SSE servers concurrently (they should all fail with port binding)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()

			// Use same port to force binding conflicts (except first one)
			port := "18080" // Use a specific port to create conflicts
			err := server.ServeSSE(ctx, port)
			errChan <- err
		}()
	}

	// Collect results
	var errors []error
	for i := 0; i < numGoroutines; i++ {
		err := <-errChan
		if err != nil {
			errors = append(errors, err)
		}
	}

	// Should have errors (either timeout or port binding issues)
	assert.NotEmpty(t, errors, "should have some errors from concurrent access")
}

// TestSSEWithDifferentConfigurations tests different server configurations
func TestSSEWithDifferentConfigurations(t *testing.T) {
	tests := []struct {
		name    string
		version string
		logger  *slog.Logger
	}{
		{
			name:    "with version",
			version: "2.0.0",
			logger:  slog.Default(),
		},
		{
			name:    "empty version",
			version: "",
			logger:  slog.Default(),
		},
		{
			name:    "nil logger",
			version: "1.0.0",
			logger:  nil, // Should use default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Version:    tt.version,
				Logger:     tt.logger,
				SpotClient: spot.New(),
			}

			server, err := NewServer(cfg)
			require.NoError(t, err)
			assert.NotNil(t, server)

			// Test that SSE can start with this configuration
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()

			err = server.ServeSSE(ctx, "0")
			// Should get timeout, not a configuration error
			assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
		})
	}
}

// Mock HTTP test to simulate SSE endpoint behavior
func TestSSEEndpointSimulation(t *testing.T) {
	// This test simulates what the SSE endpoint would do
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Write a test event
		fmt.Fprintf(w, "data: {\"type\":\"test\",\"message\":\"hello\"}\n\n")

		// Flush if possible
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	})

	// Create test server
	server := httptest.NewServer(handler)
	defer server.Close()

	// Make request to SSE endpoint
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	require.NoError(t, err)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Verify SSE headers
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "keep-alive", resp.Header.Get("Connection"))

	// This test demonstrates the expected behavior of SSE endpoints
	// The actual mcp-go library handles this internally
}

// BenchmarkSSEServerCreation benchmarks server creation performance
func BenchmarkSSEServerCreation(b *testing.B) {
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
		_ = server // Use the server to avoid optimization
	}
}

// TestSSETransportErrorHandling tests error scenarios
func TestSSETransportErrorHandling(t *testing.T) {
	tests := []struct {
		name          string
		setupServer   func() (*Server, error)
		port          string
		expectedError string
	}{
		{
			name: "nil spot client",
			setupServer: func() (*Server, error) {
				cfg := Config{
					Version:    "1.0.0",
					Logger:     slog.Default(),
					SpotClient: nil, // nil client
				}
				return NewServer(cfg)
			},
			port:          "0",
			expectedError: "", // Should not error on creation, only when client is used
		},
		{
			name: "normal configuration",
			setupServer: func() (*Server, error) {
				cfg := Config{
					Version:    "1.0.0",
					Logger:     slog.Default(),
					SpotClient: spot.New(),
				}
				return NewServer(cfg)
			},
			port:          "0",
			expectedError: "", // Should work fine
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, err := tt.setupServer()
			require.NoError(t, err) // Server creation should always succeed

			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()

			err = server.ServeSSE(ctx, tt.port)

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				// Should get timeout/cancellation, not an actual error
				assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
			}
		})
	}
}
