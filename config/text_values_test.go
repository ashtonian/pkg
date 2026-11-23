package config

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type durationText string

// A json.Number, as a provider decoding JSON with UseNumber returns it,
// decodes as the number it holds wherever a number belongs — a duration
// included, as a file's number does — and as its text elsewhere; any named
// string type bound for a duration is its string.
func TestLoad_JSONNumbersAndNamedStrings(t *testing.T) {
	type numbers struct {
		Wait    time.Duration `koanf:"wait"`
		Timeout time.Duration `koanf:"timeout"`
		Large   int64         `koanf:"large"`
		Ratio   float64       `koanf:"ratio"`
		Ports   []uint16      `koanf:"ports"`
		Label   string        `koanf:"label"`
		Raw     json.Number   `koanf:"raw"`
	}
	provider, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		data := map[string]any{
			"wait": json.Number("5"), "timeout": durationText("2s"), "large": json.Number("9007199254740993"),
			"ratio": json.Number("1.5"), "ports": []any{json.Number("80"), json.Number("443")},
			"label": json.Number("1.50"), "raw": json.Number("1e3"),
		}
		return data, nil
	}, "")
	require.NoError(t, err)
	got, err := Load[numbers](t.Context(), WithOverrideProvider(provider, nil))
	require.NoError(t, err)
	assert.Equal(t, numbers{
		Wait: 5, Timeout: 2 * time.Second, Large: 9007199254740993, Ratio: 1.5,
		Ports: []uint16{80, 443}, Label: "1.50", Raw: "1e3",
	}, *got)

	provider, err = NewMapProvider(func(context.Context) (map[string]any, error) {
		data := map[string]any{"wait": json.Number("1" + "00000000000000000000")}
		return data, nil
	}, "")
	require.NoError(t, err)
	_, err = Load[numbers](t.Context(), WithOverrideProvider(provider, nil))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "wait")
	assert.NotContains(t, err.Error(), "100000000000000000000")
}
