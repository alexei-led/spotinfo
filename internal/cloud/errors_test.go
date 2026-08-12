package cloud

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCodeOfClassifiesByNeutralSentinel(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		err  error
		name string
		want ErrorCode
	}{
		{name: "no error", err: nil, want: ""},
		{name: "invalid argument", err: fmt.Errorf("%w: bad", ErrInvalidArgument), want: CodeInvalidArgument},
		{name: "unsupported capability", err: fmt.Errorf("%w: risk", ErrUnsupportedCapability), want: CodeUnsupportedCapability},
		{name: "data unavailable", err: fmt.Errorf("%w: gcp", ErrDataUnavailable), want: CodeDataUnavailable},
		{name: "wrapped twice", err: fmt.Errorf("query: %w", fmt.Errorf("%w: gcp", ErrDataUnavailable)), want: CodeDataUnavailable},
		{name: "unclassified", err: errors.New("connection reset"), want: CodeInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, CodeOf(test.err))
		})
	}
}

// The sentinels must stay distinct: a consumer branches on them to decide
// whether a request was wrong, unsupported, or served by unusable data.
func TestNeutralSentinelsAreDistinct(t *testing.T) {
	t.Parallel()

	assert.NotErrorIs(t, ErrUnsupportedCapability, ErrInvalidArgument)
	assert.NotErrorIs(t, ErrDataUnavailable, ErrInvalidArgument)
	assert.NotErrorIs(t, ErrDataUnavailable, ErrUnsupportedCapability)
}
