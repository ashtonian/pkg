package core

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// ParserConfig holds shared configuration for filter and patch parsers.
//
// Its limits follow one rule, which Effective applies and every filter and
// patch parser reads through. Zero selects DefaultConfig's value for
// MaxDepth, MaxCost, MaxConditions and MaxArraySize, so no value forbids
// nesting altogether: the smallest depth is 1. Zero or less leaves
// MaxInputSize and MaxPatchCopyBytes unlimited. Zero derives
// MaxListBodySize from MaxInputSize (ListBodyLimit). A negative MaxDepth,
// MaxCost, MaxConditions, MaxArraySize or MaxListBodySize is invalid
// (Validate).
type ParserConfig struct {
	// MaxDepth bounds request structure depth: filter boolean nesting,
	// JSON filter document nesting and patch JSON Pointer segments.
	MaxDepth int
	// MaxCost bounds a filter's estimated cost: about 1 per indexed
	// condition, 10 per unindexed one, more for documents, relations,
	// unanchored patterns and $or.
	MaxCost int
	// MaxInputSize bounds a filter or patch body in bytes. Unlimited, a
	// request body is still bounded (ListBodyLimit, PatchBodyLimit).
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

// Now is the anchor relative-time tokens resolve against: Clock, or the
// wall clock in UTC when Clock is nil. A nil config reads the wall clock.
func (c *ParserConfig) Now() time.Time {
	if c == nil || c.Clock == nil {
		return time.Now().UTC()
	}
	return c.Clock()
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

// defaultMaxBodySize bounds a request body when MaxInputSize is unlimited.
// It is net/http's default header limit (http.DefaultMaxHeaderBytes), which
// bounds a list request sent in a URL.
const defaultMaxBodySize = 1 << 20

// ListBodyLimit returns the bytes a list body may hold: MaxListBodySize when
// it is positive; otherwise MaxInputSize plus 16 KiB for the members beside
// the filter, or 1 MiB when MaxInputSize is unlimited. A nil configuration
// is DefaultConfig.
func (c *ParserConfig) ListBodyLimit() int {
	if c == nil {
		c = DefaultConfig()
	}
	if c.MaxListBodySize > 0 {
		return c.MaxListBodySize
	}
	return c.bodyLimit(listBodyHeadroom)
}

// PatchBodyLimit returns the bytes a patch body may hold: MaxInputSize, the
// size the patch parsers hold a document to, or 1 MiB when MaxInputSize is
// unlimited. A nil configuration is DefaultConfig.
func (c *ParserConfig) PatchBodyLimit() int {
	if c == nil {
		c = DefaultConfig()
	}
	return c.bodyLimit(0)
}

// bodyLimit is the one rule a request body is bounded by: MaxInputSize plus
// headroom for the members beside what MaxInputSize bounds, or
// defaultMaxBodySize when MaxInputSize is unlimited. Nothing in front of a
// handler bounds a request body as header limits bound a URL, so a body is
// always bounded, whatever the parsers are allowed.
func (c *ParserConfig) bodyLimit(headroom int) int {
	switch {
	case c.MaxInputSize <= 0:
		return defaultMaxBodySize
	case c.MaxInputSize > math.MaxInt-headroom:
		return math.MaxInt
	}
	return c.MaxInputSize + headroom
}

// Effective returns the configuration a parser reads: c with each zero
// MaxDepth, MaxCost, MaxConditions and MaxArraySize replaced by
// DefaultConfig's; DefaultConfig for a nil c. It returns c itself when no
// limit needs a default, else a copy, and never changes c. It does not
// validate c.
func (c *ParserConfig) Effective() *ParserConfig {
	if c == nil {
		return DefaultConfig()
	}
	if c.MaxDepth != 0 && c.MaxCost != 0 && c.MaxConditions != 0 && c.MaxArraySize != 0 {
		return c
	}
	def := DefaultConfig()
	out := *c
	out.GeneratorOpts = slices.Clone(c.GeneratorOpts)
	for _, limit := range []struct{ value, def *int }{
		{&out.MaxDepth, &def.MaxDepth},
		{&out.MaxCost, &def.MaxCost},
		{&out.MaxConditions, &def.MaxConditions},
		{&out.MaxArraySize, &def.MaxArraySize},
	} {
		if *limit.value == 0 {
			*limit.value = *limit.def
		}
	}
	return &out
}

// Validate refuses a negative MaxDepth, MaxCost, MaxConditions,
// MaxArraySize or MaxListBodySize, naming the option and the value of each.
// A nil configuration is valid.
func (c *ParserConfig) Validate() error {
	if c == nil {
		return nil
	}
	var errs []error
	for _, limit := range []struct {
		option string
		value  int
	}{
		{"WithMaxDepth", c.MaxDepth},
		{"WithMaxCost", c.MaxCost},
		{"WithMaxConditions", c.MaxConditions},
		{"WithMaxArraySize", c.MaxArraySize},
		{"WithMaxListBodySize", c.MaxListBodySize},
	} {
		if limit.value < 0 {
			errs = append(errs, fmt.Errorf("%s(%d): a limit is never negative", limit.option, limit.value))
		}
	}
	return errors.Join(errs...)
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
// a document during typed apply. Default 1 MiB; zero or less is unlimited.
func WithMaxPatchCopyBytes(n int64) Option {
	return func(c *ParserConfig) { c.MaxPatchCopyBytes = n }
}

// WithMaxDepth sets the maximum request depth: filter boolean nesting, JSON
// filter document nesting and patch JSON Pointer segments. Zero selects the
// default; the smallest depth is 1.
func WithMaxDepth(depth int) Option {
	return func(c *ParserConfig) { c.MaxDepth = depth }
}

// WithMaxCost sets the maximum estimated query cost; zero selects the
// default.
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

// WithMaxConditions sets the maximum number of filter conditions; zero
// selects the default.
func WithMaxConditions(n int) Option {
	return func(c *ParserConfig) { c.MaxConditions = n }
}

// WithMaxArraySize bounds $in / $nin members, array $contains elements and
// the values in a JSON filter document; zero selects the default.
func WithMaxArraySize(n int) Option {
	return func(c *ParserConfig) { c.MaxArraySize = n }
}

// WithGeneratorOptions adds GeneratorOptions used by typed parsers after
// those already set; a later option overrides an earlier one it conflicts
// with.
func WithGeneratorOptions(opts ...GeneratorOption) Option {
	return func(c *ParserConfig) { c.GeneratorOpts = slices.Concat(c.GeneratorOpts, opts) }
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
