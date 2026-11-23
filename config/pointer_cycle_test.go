package config

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selfPointer points at itself; loopA and loopB point at each other. Neither
// chain of pointer types ever reaches a type that is not a pointer.
type (
	selfPointer *selfPointer
	loopA       *loopB
	loopB       *loopA
)

type selfPointerCfg struct {
	Name   string        `koanf:"name"`
	Skip   selfPointer   `koanf:"-"`
	Keyed  selfPointer   `koanf:"keyed"`
	Into   *selfPointer  `koanf:"-"`
	Mutual loopA         `koanf:"-"`
	Dived  []selfPointer `koanf:"dived" validate:"dive"`
}

// Every walk over a type ends where its pointer chain loops back on itself:
// the validator's rule pre-check, decoding, key checks and the generators.
// A walk that followed such a chain would never return, and the pre-check
// would block every later load of the type behind it.
func TestPointerCycle_WalksEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(ctx context.Context) error
	}{
		{"Decode", func(ctx context.Context) error {
			got, err := Decode[selfPointerCfg](ctx, map[string]any{"name": "x"})
			if err == nil {
				assert.Equal(t, "x", got.Name)
			}
			return err
		}},
		{"Load again", func(ctx context.Context) error { _, err := Load[selfPointerCfg](ctx); return err }},
		{"CheckKey", func(ctx context.Context) error { return CheckKey[selfPointerCfg](ctx, "name", "y") }},
		{"NewReloader", func(ctx context.Context) error {
			r, err := NewReloader[selfPointerCfg](ctx, nil)
			if r != nil {
				require.NoError(t, r.Stop(ctx))
			}
			return err
		}},
		{"Usage", func(context.Context) error { _, err := Usage[selfPointerCfg]("APP"); return err }},
		{"JSONSchema", func(context.Context) error { _, err := JSONSchema[selfPointerCfg]("APP"); return err }},
		{"PrintEnv", func(context.Context) error {
			return PrintEnv(&selfPointerCfg{}, "APP", WithWriter(&bytes.Buffer{}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, tc.run(t.Context()))
		})
	}
}

// A config value that is itself such a pointer, holding its own address,
// holds no struct: the redacting and tree views show nothing, and PrintEnv
// refuses it as it refuses any value that is not a config struct.
func TestPointerCycle_ValueThatHoldsItselfIsNotAConfig(t *testing.T) {
	var p selfPointer
	p = &p

	tree, err := Tree(p)
	require.NoError(t, err)
	assert.Empty(t, tree)
	assert.Empty(t, RedactedTree(p))

	redacted, err := RedactedMapChecked(p)
	require.NoError(t, err)
	assert.Empty(t, redacted)
	var out bytes.Buffer
	require.NoError(t, PrintRedacted(p, WithWriter(&out), WithFormat(FormatYAML)))
	assert.Empty(t, out.String())

	err = PrintEnv(p, "APP", WithWriter(&out))
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "not a struct")
}
