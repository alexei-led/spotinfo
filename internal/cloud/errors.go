package cloud

import "errors"

// ErrUnsupportedCapability reports a request a provider is known not to answer:
// an operating system it does not price, or an observation it does not publish.
// It is distinct from ErrInvalidArgument, which reports a value outside the
// neutral vocabulary entirely.
var ErrUnsupportedCapability = errors.New("unsupported capability")

// ErrDataUnavailable reports a recognised provider whose data cannot be served:
// its snapshot is missing, unreadable, or failed validation. A provider in this
// state is never silently replaced by another provider or by zero prices.
var ErrDataUnavailable = errors.New("data unavailable")

// ErrorCode is the stable machine-readable classification a consumer reports
// for a failure. The values are the contract every spotinfo surface shares.
type ErrorCode string

const (
	// CodeInvalidArgument reports a value outside the neutral vocabulary.
	CodeInvalidArgument ErrorCode = "INVALID_ARGUMENT"
	// CodeUnsupportedCapability reports a valid request a provider cannot answer.
	CodeUnsupportedCapability ErrorCode = "UNSUPPORTED_CAPABILITY"
	// CodeDataUnavailable reports a recognised provider with unusable data.
	CodeDataUnavailable ErrorCode = "DATA_UNAVAILABLE"
	// CodeInternal reports an unclassified failure.
	CodeInternal ErrorCode = "INTERNAL"
)

// CodeOf classifies an error by the neutral sentinel it carries. An error
// carrying no sentinel is CodeInternal: guessing a friendlier classification
// would report a bug as if it were user error. A nil error has no code.
func CodeOf(err error) ErrorCode {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrInvalidArgument):
		return CodeInvalidArgument
	case errors.Is(err, ErrUnsupportedCapability):
		return CodeUnsupportedCapability
	case errors.Is(err, ErrDataUnavailable):
		return CodeDataUnavailable
	default:
		return CodeInternal
	}
}
