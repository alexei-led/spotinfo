package cloud

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMoney(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		input     string
		wantNanos int64
		wantErr   error
	}{
		{name: "whole number", input: "3", wantNanos: 3_000_000_000},
		{name: "aws four digit price", input: "0.0416", wantNanos: 41_600_000},
		{name: "full scale", input: "0.123456789", wantNanos: 123_456_789},
		{name: "trailing zeroes are not precision", input: "1.500000000", wantNanos: 1_500_000_000},
		{name: "zero", input: "0", wantNanos: 0},
		{name: "surrounding space", input: " 0.25 ", wantNanos: 250_000_000},
		{name: "ten fractional digits", input: "0.1234567891", wantErr: ErrPrecisionLoss},
		{name: "negative", input: "-0.5", wantErr: ErrInvalidArgument},
		{name: "explicit plus", input: "+0.5", wantErr: ErrInvalidArgument},
		{name: "exponent", input: "1e-3", wantErr: ErrInvalidArgument},
		{name: "empty", input: "", wantErr: ErrInvalidArgument},
		{name: "leading dot", input: ".5", wantErr: ErrInvalidArgument},
		{name: "trailing dot", input: "5.", wantErr: ErrInvalidArgument},
		{name: "not a number", input: "N/A*", wantErr: ErrInvalidArgument},
		{name: "overflows int64 nanos", input: "9223372036", wantErr: ErrInvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			money, err := ParseMoney(test.input)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				assert.Zero(t, money.Nanos())

				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.wantNanos, money.Nanos())
		})
	}
}

func TestMoneyFromFloat(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		input     float64
		wantNanos int64
		wantErr   error
	}{
		{name: "feed price keeps its decimal form", input: 0.0416, wantNanos: 41_600_000},
		{name: "three digit price", input: 2.568, wantNanos: 2_568_000_000},
		{name: "zero", input: 0, wantNanos: 0},
		{name: "finer than the scale fails instead of rounding", input: 0.0000000001, wantErr: ErrPrecisionLoss},
		{name: "negative", input: -0.01, wantErr: ErrInvalidArgument},
		{name: "NaN", input: math.NaN(), wantErr: ErrInvalidArgument},
		{name: "infinity", input: math.Inf(1), wantErr: ErrInvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			money, err := MoneyFromFloat(test.input)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.wantNanos, money.Nanos())
		})
	}
}

func TestMoneyRendersCanonicalDecimalString(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"0":           "0.000000000",
		"0.031":       "0.031000000",
		"0.0416":      "0.041600000",
		"12.5":        "12.500000000",
		"0.000000001": "0.000000001",
	} {
		money, err := ParseMoney(input)
		require.NoError(t, err)
		assert.Equal(t, want, money.String())

		encoded, err := json.Marshal(money)
		require.NoError(t, err)
		assert.JSONEq(t, `"`+want+`"`, string(encoded))

		var decoded Money
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		assert.Equal(t, money, decoded, "canonical form must round-trip")
	}
}

func TestMoneyUnmarshalRejectsNonCanonicalJSON(t *testing.T) {
	t.Parallel()

	var money Money
	require.ErrorIs(t, json.Unmarshal([]byte(`0.031`), &money), ErrInvalidArgument)
	require.ErrorIs(t, json.Unmarshal([]byte(`"0.1234567891"`), &money), ErrPrecisionLoss)

	// null is how the v2 contract spells an absent price; decoding it must not
	// fail, and must not invent a zero amount either.
	holder := struct {
		Amount   Money  `json:"amount"`
		Optional *Money `json:"optional"`
	}{}
	require.NoError(t, json.Unmarshal([]byte(`{"amount":null,"optional":null}`), &holder))
	assert.True(t, holder.Amount.IsZero())
	assert.Nil(t, holder.Optional)
}

func TestMoneyFloat64IsLossyBridgeOnly(t *testing.T) {
	t.Parallel()

	money, err := ParseMoney("0.0416")
	require.NoError(t, err)
	assert.InDelta(t, 0.0416, money.Float64(), 1e-12)
	assert.False(t, money.IsZero())
	assert.True(t, Money{}.IsZero())
}
