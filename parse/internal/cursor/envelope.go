package cursor

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// Envelope carries a keyset payload and its format generation. Expiry and
// tenant metadata retain their established wire meaning.
type Envelope[T any] struct {
	// Generation is the caller's cursor format version. Checked on Decode via
	// [ExpectGeneration]; a mismatch yields [ErrCursorGeneration].
	Generation uint32 `msgpack:"g,omitempty"`

	// ExpiresAt is the unix-second wall clock after which Decode rejects the
	// cursor with [ErrCursorExpired]. Zero means no expiry.
	ExpiresAt int64 `msgpack:"e,omitempty"`

	// TenantHash is an optional fingerprint of the owning tenant. Checked on
	// Decode via [ExpectTenantHash]; a mismatch yields [ErrCursorTenantMismatch].
	// Zero means unrestricted.
	TenantHash uint64 `msgpack:"h,omitempty"`

	// Payload is the consumer's resume position.
	Payload T `msgpack:"p"`
}

type encodeConfig struct {
	generation   uint32
	ttl          time.Duration
	expiresAt    int64
	hasExpiresAt bool
	tenantHash   uint64
	now          func() time.Time
}

// EncodeOption configures [Encode].
type EncodeOption func(*encodeConfig)

// WithGeneration stamps the cursor's format generation.
func WithGeneration(g uint32) EncodeOption { return func(c *encodeConfig) { c.generation = g } }

// WithTTL sets the cursor to expire ttl after issuance (from the encode clock).
// A non-positive ttl leaves the cursor with no expiry. Overridden by
// [WithExpiresAt].
func WithTTL(ttl time.Duration) EncodeOption { return func(c *encodeConfig) { c.ttl = ttl } }

// WithExpiresAt stamps an explicit unix-second expiry, overriding [WithTTL].
// Use when the caller already knows the absolute expiry wall clock.
func WithExpiresAt(unixSec int64) EncodeOption {
	return func(c *encodeConfig) { c.expiresAt = unixSec; c.hasExpiresAt = true }
}

// WithTenantHash stamps the owning tenant's fingerprint.
func WithTenantHash(h uint64) EncodeOption { return func(c *encodeConfig) { c.tenantHash = h } }

// WithClock overrides the issuance clock. Default
// time.Now().UTC.
func WithClock(now func() time.Time) EncodeOption { return func(c *encodeConfig) { c.now = now } }

// Encode serializes payload into an opaque, URL-safe string via msgpack +
// base64url, wrapped in an [Envelope] carrying the configured metadata. The
// result round-trips through query strings without further escaping.
func Encode[T any](payload T, opts ...EncodeOption) (string, error) {
	cfg := encodeConfig{now: func() time.Time { return time.Now().UTC() }}
	for _, o := range opts {
		o(&cfg)
	}
	env := Envelope[T]{Generation: cfg.generation, TenantHash: cfg.tenantHash, Payload: payload}
	switch {
	case cfg.hasExpiresAt:
		env.ExpiresAt = cfg.expiresAt
	case cfg.ttl > 0:
		env.ExpiresAt = cfg.now().Add(cfg.ttl).Unix()
	}
	data, err := msgpack.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("%w: msgpack encode: %v", ErrInvalidCursor, err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

type decodeConfig struct {
	generation      uint32
	checkGeneration bool
	tenantHash      uint64
	checkTenant     bool
	now             func() time.Time
}

// DecodeOption configures [Decode].
type DecodeOption func(*decodeConfig)

// ExpectGeneration rejects a cursor whose generation differs from g with
// [ErrCursorGeneration]. Omit to skip the check.
func ExpectGeneration(g uint32) DecodeOption {
	return func(c *decodeConfig) { c.generation = g; c.checkGeneration = true }
}

// ExpectTenantHash rejects a cursor whose tenant fingerprint differs from h
// with [ErrCursorTenantMismatch]. Omit to skip the check.
func ExpectTenantHash(h uint64) DecodeOption {
	return func(c *decodeConfig) { c.tenantHash = h; c.checkTenant = true }
}

// WithDecodeClock overrides the expiry clock. Default
// time.Now().UTC.
func WithDecodeClock(now func() time.Time) DecodeOption {
	return func(c *decodeConfig) { c.now = now }
}

// Decode parses an opaque string back into its payload, verifying the
// configured generation, expiry, and tenant checks. Returns [ErrInvalidCursor]
// for a malformed token, [ErrCursorGeneration] / [ErrCursorExpired] /
// [ErrCursorTenantMismatch] for the corresponding rejections.
//
// The MessagePack structure is checked before decoding: a declared container
// or payload length longer than the input that remains is ErrInvalidCursor, so
// decoding allocates at most in proportion to the cursor's size. Bound the
// cursor's length before calling.
//
// An empty cursor is caller-semantic (first page vs error), so Decode does not
// special-case it — check for the empty cursor before calling.
func Decode[T any](cur string, opts ...DecodeOption) (T, error) {
	env, err := DecodeEnvelope[T](cur, opts...)
	if err != nil {
		var zero T
		return zero, err
	}
	return env.Payload, nil
}

// DecodeEnvelope is [Decode] that returns the full [Envelope] rather than only
// the payload, for consumers that reflect the generation / expiry / tenant
// metadata back onto their own cursor type. Runs the same checks as Decode.
func DecodeEnvelope[T any](cur string, opts ...DecodeOption) (Envelope[T], error) {
	var env Envelope[T]
	cfg := decodeConfig{now: func() time.Time { return time.Now().UTC() }}
	for _, o := range opts {
		o(&cfg)
	}
	data, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		return env, fmt.Errorf("%w: base64 decode: %v", ErrInvalidCursor, err)
	}
	if err := checkMsgpackBounds(data); err != nil {
		return env, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	if err := msgpack.Unmarshal(data, &env); err != nil {
		return env, fmt.Errorf("%w: msgpack decode: %v", ErrInvalidCursor, err)
	}
	if cfg.checkGeneration && env.Generation != cfg.generation {
		return env, ErrCursorGeneration
	}
	if env.ExpiresAt > 0 && cfg.now().Unix() > env.ExpiresAt {
		return env, ErrCursorExpired
	}
	if cfg.checkTenant && env.TenantHash != cfg.tenantHash {
		return env, ErrCursorTenantMismatch
	}
	return env, nil
}
