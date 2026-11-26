//go:build unit

package core_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

func TestCheckText(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		refused    bool
	}{
		{"empty", "", false},
		{"text", "thermostat", false},
		{"other controls", "\x01\x1f\x7f", false},
		{"replacement character", "�", false},
		{"astral plane", "\U0001F600", false},
		{"U+0000", "a\x00b", true},
		{"a stray continuation byte", "a\x80", true},
		{"a truncated sequence", "a\xc3", true},
		{"a surrogate half", "\xed\xa0\x80", true},
		{"an overlong encoding", "\xc0\xaf", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := core.CheckText(tc.text)
			if !tc.refused {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, core.ErrInvalidText)
		})
	}
}

// CheckValueText reaches every string of a normalized value and says where
// the refused one stands.
func TestCheckValueText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		where string
	}{
		{"a set member", []any{"a", "b\x00"}, "[1]: "},
		{"a key column", core.Key{"a", "\xff"}, "[1]: "},
		{"a member name", map[string]any{"e\x00nv": "prod"}, `member name "e\x00nv": `},
		{"a nested member", map[string]any{"labels": []any{map[string]any{"role": "\xff"}}}, "labels: [0]: role: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := core.CheckValueText(tc.value)
			require.ErrorIs(t, err, core.ErrInvalidText)
			assert.Contains(t, err.Error(), tc.where)
		})
	}
	for _, valid := range []any{nil, true, int64(1), "a", []any{"a", map[string]any{"k": []any{"v"}}}, core.Key{"a", int64(2)}} {
		assert.NoError(t, core.CheckValueText(valid))
	}
}
