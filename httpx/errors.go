package httpx

import (
	"errors"
	"fmt"
)

// Sentinel errors for transport failures.
var (
	// ErrCircuitOpen is returned when the circuit breaker is open.
	ErrCircuitOpen = errors.New("httpx: circuit breaker is open")

	// ErrLimiterTimeout is returned when acquiring a slot times out.
	ErrLimiterTimeout = errors.New("httpx: limiter acquire timeout")

	// ErrResponseTooLarge is returned by the decoding helpers when a response
	// body exceeds WithMaxResponseBytes.
	ErrResponseTooLarge = errors.New("httpx: response body too large to decode")

	// ErrRateLimited is returned when the rate limit is exceeded.
	ErrRateLimited = errors.New("httpx: rate limit exceeded")

	// ErrAuthRefresh is returned when token refresh fails.
	ErrAuthRefresh = errors.New("httpx: auth token refresh failed")

	// ErrNoCodec is returned when no codec is registered for the response content type.
	ErrNoCodec = errors.New("httpx: no codec registered for content type")

	// ErrInvalidConfig is wrapped by every construction-time validation
	// failure returned from NewClient, NewStandard, NewBaseTransport, the
	// New*Transport constructors and Chain.Build.
	ErrInvalidConfig = errors.New("httpx: invalid configuration")
)

// invalidConfig builds an error that matches ErrInvalidConfig.
func invalidConfig(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}
