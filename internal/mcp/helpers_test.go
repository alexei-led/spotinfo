package mcp

import (
	"time"

	"spotinfo/internal/spot"
)

// newEmbeddedClient builds a real spot.Client that never touches the network:
// pricing comes from the embedded copy, and the tiny timeout makes the advisor
// fetch fall back to embedded immediately. These tests exist to exercise the
// client's sync.Once and shared-provider concurrency, which the embedded path
// does faithfully — while removing an AWS dependency that made them slow and
// dependent on network reachability.
func newEmbeddedClient() *spot.Client {
	c := spot.NewWithOptions(time.Millisecond, true)
	// No AWS in unit tests. Without this, every instance the embedded feed does
	// not price triggers live-price enrichment, which blocks for livePriceTimeout
	// (10s) waiting on an API call that cannot succeed here.
	c.SetLivePriceProvider(nil)

	return c
}
