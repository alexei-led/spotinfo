package cloud

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// MoneyScale is the number of fractional decimal digits Money stores exactly.
	// A source that publishes finer amounts must raise it deliberately.
	MoneyScale = 9
	// nanosPerUnit is 10^MoneyScale — one currency unit in nano units.
	nanosPerUnit = 1_000_000_000
	// maxWholeUnits keeps whole units × nanosPerUnit inside int64.
	maxWholeUnits = 9_223_372_035
)

// ErrPrecisionLoss reports an amount that cannot be stored without rounding.
// A source that needs more precision must be fixed, not silently truncated.
var ErrPrecisionLoss = errors.New("amount needs more fractional digits than the fixed-point scale")

// Money is an exact, non-negative currency amount stored as integer nano units
// (10^-9). spotinfo uses it for USD-per-hour rates; currency and billing unit
// travel with the observation, not with the number.
//
// Fixed point rather than float64 because provider feeds publish decimal
// strings that binary floats cannot represent exactly, which would make price
// equality, ordering, and rendering depend on accumulated representation error.
// Ceiling: 9 fractional digits, which covers every AWS price string observed
// (maximum 4). A source needing more must fail with ErrPrecisionLoss, and the
// scale must be raised deliberately rather than rounded away.
type Money struct {
	nanos int64
}

// ParseMoney converts a plain decimal string such as "0.0416" to Money.
// It rejects signs, exponents, and anything finer than the fixed-point scale.
func ParseMoney(text string) (Money, error) {
	trimmed := strings.TrimSpace(text)
	whole, fraction, hasFraction := strings.Cut(trimmed, ".")

	if !decimalDigits(whole) || (hasFraction && !decimalDigits(fraction)) {
		return Money{}, fmt.Errorf("%w: %q is not a non-negative decimal amount", ErrInvalidArgument, text)
	}
	if len(fraction) > MoneyScale {
		return Money{}, fmt.Errorf("%w: %q exceeds %d fractional digits", ErrPrecisionLoss, text, MoneyScale)
	}

	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || units > maxWholeUnits {
		return Money{}, fmt.Errorf("%w: %q is out of range", ErrInvalidArgument, text)
	}

	nanos := int64(0)
	if hasFraction {
		nanos, err = strconv.ParseInt(fraction+strings.Repeat("0", MoneyScale-len(fraction)), 10, 64)
		if err != nil {
			return Money{}, fmt.Errorf("%w: %q is out of range", ErrInvalidArgument, text)
		}
	}

	return Money{nanos: units*nanosPerUnit + nanos}, nil
}

// MoneyFromFloat converts a float64 from a legacy or SDK API. It routes through
// the shortest decimal string that round-trips the float, so a price the feed
// published as "0.0416" converts exactly instead of inheriting binary noise.
// A value that genuinely needs more than the fixed-point scale fails.
func MoneyFromFloat(value float64) (Money, error) {
	money, err := ParseMoney(strconv.FormatFloat(value, 'f', -1, 64))
	if err != nil {
		return Money{}, fmt.Errorf("convert %v: %w", value, err)
	}

	return money, nil
}

// Nanos returns the amount in nano units — the exact stored value.
func (m Money) Nanos() int64 { return m.nanos }

// IsZero reports whether the amount is exactly zero.
func (m Money) IsZero() bool { return m.nanos == 0 }

// Float64 converts back to float64 for legacy APIs that only accept floats.
// Lossy by definition: never use it for storage, comparison, or rendering.
func (m Money) Float64() float64 { return float64(m.nanos) / nanosPerUnit }

// String renders the canonical decimal form with exactly MoneyScale fractional
// digits, so the same amount always serialises to the same bytes.
func (m Money) String() string {
	return fmt.Sprintf("%d.%09d", m.nanos/nanosPerUnit, m.nanos%nanosPerUnit)
}

// MarshalJSON encodes Money as its canonical decimal string. Encoding as a JSON
// number would hand the value back to a float64 in every consumer.
func (m Money) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(m.String())), nil
}

// UnmarshalJSON decodes the canonical decimal string form. JSON null is a
// no-op, per the json.Unmarshaler convention: an absent amount is expressed by
// the field being absent or nil, not by a Money value.
func (m *Money) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}

	text, err := strconv.Unquote(string(data))
	if err != nil {
		return fmt.Errorf("%w: money must be a decimal string, got %s", ErrInvalidArgument, data)
	}

	money, err := ParseMoney(text)
	if err != nil {
		return err
	}
	*m = money

	return nil
}

func decimalDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, char := range text {
		if char < '0' || char > '9' {
			return false
		}
	}

	return true
}
