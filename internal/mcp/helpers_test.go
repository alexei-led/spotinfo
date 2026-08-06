package mcp

import (
	"time"

	"spotinfo/internal/spot"
)

// newEmbeddedClient builds a real spot.Client that does not wait on AWS.
//
// Pricing comes from the embedded copy. The advisor feed is still requested —
// useEmbedded gates pricing only — but the 1ms timeout expires almost at once
// and the fetch falls back to the embedded copy, so the request never delays a
// test meaningfully.
//
// These tests exist to exercise the client's sync.Once and shared-provider
// concurrency, which the embedded path does faithfully, without the AWS
// dependency that made them slow and reliant on network reachability.
func newEmbeddedClient() *spot.Client {
	c := spot.NewWithOptions(time.Millisecond, true)
	// No AWS in unit tests. Without this, every instance the embedded feed does
	// not price triggers live-price enrichment, which blocks for livePriceTimeout
	// (10s) waiting on an API call that cannot succeed here.
	c.SetLivePriceProvider(nil)

	return c
}
