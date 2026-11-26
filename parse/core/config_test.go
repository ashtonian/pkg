//go:build unit

package core_test

import (
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

func TestParserConfig_ListBodyLimit(t *testing.T) {
	tests := []struct {
		name string
		opts []core.Option
		want int
	}{
		{"the default input size with headroom", nil, 64<<10 + 16<<10},
		{"a raised input size with headroom", []core.Option{core.WithMaxInputSize(1 << 20)}, 1<<20 + 16<<10},
		{"an unlimited input size", []core.Option{core.WithMaxInputSize(0)}, 1 << 20},
		{"a negative input size, which is unlimited", []core.Option{core.WithMaxInputSize(-1)}, 1 << 20},
		{"an input size the headroom would overflow", []core.Option{core.WithMaxInputSize(math.MaxInt - 1)}, math.MaxInt},
		{"a set list body size", []core.Option{core.WithMaxListBodySize(4096)}, 4096},
		{"a set list body size beside an unlimited input size", []core.Option{core.WithMaxInputSize(0), core.WithMaxListBodySize(4096)}, 4096},
		{"a zero list body size, which derives", []core.Option{core.WithMaxListBodySize(0)}, 64<<10 + 16<<10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := core.DefaultConfig()
			for _, opt := range tt.opts {
				opt(cfg)
			}
			assert.Equal(t, tt.want, cfg.ListBodyLimit())
		})
	}
}

// Zero selects the default for the count limits and nothing else; a
// configuration that needs no default is returned as it is.
func TestParserConfig_Effective(t *testing.T) {
	def := core.DefaultConfig()
	assert.Equal(t, def, (*core.ParserConfig)(nil).Effective())

	zero := &core.ParserConfig{MaxDepth: 0, MaxCost: 0, MaxConditions: 0, MaxArraySize: 0, MaxInputSize: 0, MaxPatchCopyBytes: 0, MaxListBodySize: 0}
	got := zero.Effective()
	assert.Equal(t, def.MaxDepth, got.MaxDepth)
	assert.Equal(t, def.MaxCost, got.MaxCost)
	assert.Equal(t, def.MaxConditions, got.MaxConditions)
	assert.Equal(t, def.MaxArraySize, got.MaxArraySize)
	assert.Zero(t, got.MaxInputSize, "zero input size stays unlimited")
	assert.Zero(t, got.MaxPatchCopyBytes, "zero copy budget stays unlimited")
	assert.Zero(t, got.MaxListBodySize, "zero list body size stays derived")
	assert.Zero(t, zero.MaxDepth, "the receiver is unchanged")

	handBuilt := &core.ParserConfig{MaxDepth: 10}
	got = handBuilt.Effective()
	assert.Equal(t, 10, got.MaxDepth)
	assert.Equal(t, def.MaxCost, got.MaxCost, "a hand-built configuration's unset limits are the defaults")

	set := &core.ParserConfig{MaxDepth: 1, MaxCost: 2, MaxConditions: 3, MaxArraySize: 4}
	assert.Same(t, set, set.Effective())
}

func TestParserConfig_Validate(t *testing.T) {
	assert.NoError(t, (*core.ParserConfig)(nil).Validate())
	assert.NoError(t, (&core.ParserConfig{}).Validate(), "zero is a default, not an error")
	assert.NoError(t, (&core.ParserConfig{MaxInputSize: -1, MaxPatchCopyBytes: -1}).Validate(), "zero or less is unlimited")
	err := (&core.ParserConfig{MaxDepth: -1, MaxCost: -2, MaxConditions: -3, MaxArraySize: -4, MaxListBodySize: -5}).Validate()
	for _, want := range []string{
		"WithMaxDepth(-1): a limit is never negative",
		"WithMaxCost(-2)",
		"WithMaxConditions(-3)",
		"WithMaxArraySize(-4)",
		"WithMaxListBodySize(-5)",
	} {
		assert.ErrorContains(t, err, want)
	}
}

func TestParserConfig_NilListBodyLimit(t *testing.T) {
	assert.Equal(t, core.DefaultConfig().ListBodyLimit(), (*core.ParserConfig)(nil).ListBodyLimit())
}

// A patch body is bounded as a list body is: by MaxInputSize, or 1 MiB
// when the parsers are unlimited.
func TestParserConfig_PatchBodyLimit(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  *core.ParserConfig
		want int
	}{
		{"the default input size", core.DefaultConfig(), 64 << 10},
		{"a set input size", &core.ParserConfig{MaxInputSize: 3 << 20}, 3 << 20},
		{"an unlimited input size", &core.ParserConfig{MaxInputSize: 0}, 1 << 20},
		{"a negative input size, which is unlimited", &core.ParserConfig{MaxInputSize: -1}, 1 << 20},
		{"a list body size, which bounds lists only", &core.ParserConfig{MaxListBodySize: 4096}, 1 << 20},
		{"no configuration", nil, 64 << 10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cfg.PatchBodyLimit())
		})
	}
}

func TestWithGeneratorOptions_Appends(t *testing.T) {
	cfg := core.DefaultConfig()
	core.WithGeneratorOptions(core.WithColumnTag("sql"))(cfg)
	core.WithGeneratorOptions(core.WithFieldNameTag("bson"))(cfg)
	assert.Len(t, cfg.GeneratorOpts, 2)
	gen := core.NewSchemaGenerator(cfg.GeneratorOpts...)
	assert.Equal(t, "bson", gen.FieldNameTag())
	type row struct {
		Name string `sql:"full_name" bson:"name"`
	}
	walk, err := gen.StructFields(reflect.TypeFor[row]())
	require.NoError(t, err)
	column, ok, err := gen.FieldColumn(slices.Collect(walk.Fields())[0])
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "full_name", column, "the first option still applies")
}
