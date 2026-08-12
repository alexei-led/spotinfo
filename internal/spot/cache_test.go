package spot

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The advisor window is long and the price window short, because the advisor
// document barely changes and takes a second to transfer, while prices are
// rewritten through the day and transfer in a tenth of that. The cache
// mechanics these two values drive live in internal/feedcache and are tested
// there.
func TestPriceIsCachedFarMoreBrieflyThanAdvisor(t *testing.T) {
	t.Parallel()

	assert.Less(t, priceCacheTTL, advisorCacheTTL)
	assert.LessOrEqual(t, priceCacheTTL, time.Hour)
}
