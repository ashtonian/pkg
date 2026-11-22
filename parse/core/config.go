package core

import (
	"math"
	"time"
)

// ParserConfig holds shared configuration for filter and patch parsers.
type ParserConfig struct {
	// MaxDepth bounds request structure depth: filter boolean nesting,
	// JSON filter document nesting and patch JSON Pointer segments.
	MaxDepth int
	// MaxCost bounds a filter's estimated cost: about 1 per indexed
	// condition, 10 per unindexed one, more for documents, relations,
	// unanchored patterns and $or.
	MaxCost int
	// MaxInputSize bounds a filter or patch body in bytes.
	MaxInputSize int
	// MaxListBodySize bounds a list request sent as a POST body, in bytes;
	// zero derives the bound from MaxInputSize (ListBodyLimit).
	MaxListBodySize int
	// MaxConditions bounds a filter's leaf conditions.
	MaxConditions int
	// MaxArraySize bounds $in / $nin members, array $contains elements and
	// the values in a JSON filter document.
	MaxArraySize int
	// MaxPatchCopyBytes bounds the total bytes a patch's copy operations may
	// add to a document during typed apply (copy amplification guard).
	MaxPatchCopyBytes int64
	GeneratorOpts     []GeneratorOption
	Codec             Codec            // Wire format codec. Nil = encoding/json fallback.
	Clock             func() time.Time // Relative-time anchor (`now-7d`). Nil = time.Now().UTC.
}

// GetCodec returns the configured codec, falling back to the package
// default (StdlibJSON) when none is configured.
func (c *ParserConfig) GetCodec() Codec {
	if c != nil && c.Codec != nil {
		return c.Codec
	}
	return defaultCodec
}

// listBodyHeadroom is the room a derived list body limit leaves beside the
// filter, which MaxInputSize bounds: a cursor (at most 4 KiB), the sort,
// search and name lists, and the members' JSON.
const listBodyHeadroom = 16 << 10

// defaultMaxListBodySize bounds a list body when MaxInputSize is unlimited.
// It is net/http's default header limit (http.DefaultMaxHeaderBytes), which
// bounded the same request in a URL.
const defaultMaxListBodySize = 1 << 20

// ListBodyLimit returns the bytes a list body may hold: MaxListBodySize when
// it is positive; otherwise MaxInputSize plus 16 KiB for the members beside
// the filter, or 1 MiB when MaxInputSize is unlimited. Nothing in front of a
// handler bounds a request body as header limits bound a URL, so a list body
// is always bounded.
func (c *ParserConfig) ListBodyLimit() int {
	switch {
	case c.MaxListBodySize > 0:
		return c.MaxListBodySize
	case c.MaxInputSize <= 0:
		return defaultMaxListBodySize
	case c.MaxInputSize > math.MaxInt-listBodyHeadroom:
		return math.MaxInt
	}
	return c.MaxInputSize + listBodyHeadroom
}

// Option configures a Parser.
type Option func(*ParserConfig)

// DefaultConfig returns a ParserConfig with sensible defaults.
func DefaultConfig() *ParserConfig {
	return &ParserConfig{
		MaxDepth:          5,
		MaxCost:           50,
		MaxInputSize:      64 * 1024,
		MaxConditions:     20,
		MaxArraySize:      100,
		MaxPatchCopyBytes: 1 << 20,
	}
}

// WithMaxPatchCopyBytes bounds the bytes a patch's copy operations may add to
// a document during typed apply. Default 1 MiB.
func WithMaxPatchCopyBytes(n int64) Option {
	return func(c *ParserConfig) { c.MaxPatchCopyBytes = n }
}

// WithMaxDepth sets the maximum request depth: filter boolean nesting, JSON
// filter document nesting and patch JSON Pointer segments.
func WithMaxDepth(depth int) Option {
	return func(c *ParserConfig) { c.MaxDepth = depth }
}

// WithMaxCost sets the maximum estimated query cost.
func WithMaxCost(cost int) Option {
	return func(c *ParserConfig) { c.MaxCost = cost }
}

// WithMaxInputSize sets the maximum size of a filter or patch body in bytes;
// zero or less is unlimited. A list body's bound derives from it unless
// WithMaxListBodySize sets one (ListBodyLimit).
func WithMaxInputSize(size int) Option {
	return func(c *ParserConfig) { c.MaxInputSize = size }
}

// WithMaxListBodySize bounds a list request sent as a POST body (parse's
// Resource.ParseListBody), in bytes. Zero, the default, derives the bound
// from MaxInputSize (ListBodyLimit); a list body is never unbounded, and
// parse.NewResource refuses a negative size. Set it to at least MaxInputSize
// plus room for the members beside the filter, or a filter that a GET
// carries is refused in a body.
func WithMaxListBodySize(size int) Option {
	return func(c *ParserConfig) { c.MaxListBodySize = size }
}

// WithMaxConditions sets the maximum number of filter conditions.
func WithMaxConditions(n int) Option {
	return func(c *ParserConfig) { c.MaxConditions = n }
}

// WithMaxArraySize bounds $in / $nin members, array $contains elements and
// the values in a JSON filter document.
func WithMaxArraySize(n int) Option {
	return func(c *ParserConfig) { c.MaxArraySize = n }
}

// WithGeneratorOptions sets GeneratorOptions used by typed parsers.
func WithGeneratorOptions(opts ...GeneratorOption) Option {
	return func(c *ParserConfig) { c.GeneratorOpts = opts }
}

// WithClock pins the anchor for relative-time tokens (`now`, `now-7d`).
// Default: time.Now().UTC. Tests pass a frozen clock for deterministic
// resolution.
func WithClock(now func() time.Time) Option {
	return func(c *ParserConfig) { c.Clock = now }
}

// WithCodec sets the codec that decodes filter and patch bodies and
// round-trips documents during typed patch application. Default:
// StdlibJSON (numbers decode exactly). Cursors do not use it: their
// encoding is owned by the keyset cursor codec.
//
//	core.WithCodec(myMsgpackCodec{})
func WithCodec(codec Codec) Option {
	return func(c *ParserConfig) { c.Codec = codec }
}
