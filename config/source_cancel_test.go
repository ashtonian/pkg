package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancelJudgedCfg has a rule an entry can break, so WithInvalidEntries
// judges a source's entries, and a key a higher source can shadow.
type cancelJudgedCfg struct {
	Port int    `koanf:"port" default:"5" validate:"min=1"`
	Name string `koanf:"name"`
}

// A load whose context ends while a WithInvalidEntries source is read
// returns the context's cause and reports nothing of that source or after
// it: no withheld entries, not even none, and no shadowed or unknown
// input. A live load reports each.
func TestWithInvalidEntries_EndedContextReportsNothing(t *testing.T) {
	type reports struct{ withheld, shadowed, unknown int }
	sources := map[string]func(read func(context.Context) (map[string]any, error), opts ...SourceOption) Option{
		"a provider": func(read func(context.Context) (map[string]any, error), opts ...SourceOption) Option {
			p, err := NewMapProvider(read, "")
			require.NoError(t, err)
			return WithBaseProvider(p, nil, opts...)
		},
		"entries": WithBaseEntries,
	}
	for name, source := range sources {
		t.Run(name, func(t *testing.T) {
			load := func(ctx context.Context, data map[string]any, reading func()) (reports, error) {
				var n reports
				above, err := NewMapProvider(func(context.Context) (map[string]any, error) {
					return map[string]any{"name": "above"}, nil
				}, "")
				require.NoError(t, err)
				_, err = Load[cancelJudgedCfg](ctx,
					source(func(context.Context) (map[string]any, error) { reading(); return data, nil },
						WithInvalidEntries(func(context.Context, []KeyError) error { n.withheld++; return nil })),
					WithOverrideProvider(above, nil),
					WithShadowedInputs(func(context.Context, *ShadowedInputsError) error { n.shadowed++; return nil }),
					WithUnknownKeys(func(context.Context, *UnknownKeysError) error { n.unknown++; return nil }),
				)
				return n, err
			}
			invalid := map[string]any{"port": 0, "name": "below", "bogus": 1}
			n, err := load(t.Context(), invalid, func() {})
			require.NoError(t, err)
			assert.Equal(t, reports{withheld: 1, shadowed: 1, unknown: 1}, n)

			for _, data := range []map[string]any{{}, invalid} {
				ctx, cancel := context.WithCancelCause(context.Background())
				n, err := load(ctx, data, func() { cancel(errShutdown) })
				require.ErrorIs(t, err, errShutdown, "%v", data)
				assert.Equal(t, reports{}, n, "%v", data)
			}
		})
	}
}
