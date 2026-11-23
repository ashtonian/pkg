package config

import (
	"context"
	"strings"
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
					data := map[string]any{"name": "above"}
					return data, nil
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

// cancelingValidator ends the load's context as it validates, and passes.
type cancelingValidator struct{ cancel context.CancelCauseFunc }

func (v cancelingValidator) Struct(any) error {
	v.cancel(errShutdown)
	return nil
}

// cancelingLater runs the default validator's rules and ends the load's
// context as it validates for the second time: the judge's test of an entry
// its first judgement blamed.
type cancelingLater struct {
	cancel context.CancelCauseFunc
	calls  *int
}

func (v cancelingLater) Struct(cfg any) error {
	*v.calls++
	if *v.calls == 2 {
		v.cancel(errShutdown)
	}
	return defaultValidator().Struct(cfg)
}

// A load whose context ends inside a stage — the validator as the judge or
// the load runs it, or a report a later source's report would follow —
// fails as abandoned, as at every other stage, the cause still reachable.
func TestLoad_EndedInsideAStageFailsAsAbandoned(t *testing.T) {
	entries := func(data map[string]any, opts ...SourceOption) Option {
		return WithBaseEntries(readEntries(data), opts...)
	}
	tests := []struct {
		name string
		opts func(cancel context.CancelCauseFunc) []Option
	}{
		{"judging entries", func(cancel context.CancelCauseFunc) []Option {
			opts := []Option{
				entries(map[string]any{"name": "a"}, WithInvalidEntries(func(context.Context, []KeyError) error { return nil })),
				WithValidator(cancelingValidator{cancel}),
			}
			return opts
		}},
		{"testing a blamed entry", func(cancel context.CancelCauseFunc) []Option {
			opts := []Option{
				entries(map[string]any{"port": 0, "name": "a"}, WithInvalidEntries(func(context.Context, []KeyError) error { return nil })),
				WithValidator(cancelingLater{cancel: cancel, calls: new(int)}),
			}
			return opts
		}},
		{"reporting withheld entries", func(cancel context.CancelCauseFunc) []Option {
			opts := []Option{
				entries(map[string]any{"name": "a"}, WithInvalidEntries(func(context.Context, []KeyError) error { cancel(errShutdown); return nil })),
				entries(map[string]any{"port": 2}, WithInvalidEntries(func(context.Context, []KeyError) error { return nil })),
			}
			return opts
		}},
		{"reporting unknown keys", func(cancel context.CancelCauseFunc) []Option {
			opts := []Option{
				entries(map[string]any{"bogus": 1}, WithUnknownEntries(func(context.Context, []UnknownKey) error { cancel(errShutdown); return nil })),
				entries(map[string]any{"stale": 1}, WithUnknownEntries(func(context.Context, []UnknownKey) error { return nil })),
			}
			return opts
		}},
		{"validating", func(cancel context.CancelCauseFunc) []Option {
			opts := []Option{WithValidator(cancelingValidator{cancel})}
			return opts
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			_, err := Load[cancelJudgedCfg](ctx, tt.opts(cancel)...)
			require.ErrorIs(t, err, errShutdown)
			require.ErrorIs(t, err, context.Canceled)
			assert.Equal(t, 1, strings.Count(err.Error(), "loading config abandoned"), err.Error())
		})
	}
}
