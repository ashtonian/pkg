package config

import (
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/go-viper/mapstructure/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rangeConfig struct {
	Min int `koanf:"min" default:"0" validate:"min=0"`
	Max int `koanf:"max" default:"10" alias:"maximum"`
}

func (c *rangeConfig) Validate() error {
	if c.Min > c.Max {
		return errors.New("min exceeds max")
	}
	return nil
}

// A batch is validated once, as a whole, over the baseline it edits: a pair
// each valid alone can fail together, and a pair each invalid alone can
// pass together. Against defaults CheckKey agrees with each edit alone.
func TestDecodeEdits_JudgesTheBatchTogetherOverItsBaseline(t *testing.T) {
	stored := &rangeConfig{Min: 15, Max: 30}
	for _, tc := range []struct {
		name     string
		baseline *rangeConfig
		edits    map[string]any
		want     *rangeConfig // nil: the batch is refused
	}{
		{"defaults: each passes alone, the pair fails", nil, map[string]any{"min": 8, "max": 4}, nil},
		{"defaults: min alone fails", nil, map[string]any{"min": 12}, nil},
		{"defaults: the pair passes", nil, map[string]any{"min": 12, "max": 20}, &rangeConfig{12, 20}},
		{"baseline: min alone passes", stored, map[string]any{"min": 20}, &rangeConfig{20, 30}},
		{"baseline: max alone fails", stored, map[string]any{"max": 12}, nil},
		{"baseline: the pair fails", stored, map[string]any{"min": 8, "max": 4}, nil},
		{"baseline: the pair passes", stored, map[string]any{"min": 12, "max": 20}, &rangeConfig{12, 20}},
		{"baseline alone", stored, nil, &rangeConfig{15, 30}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeEdits(t.Context(), tc.baseline, tc.edits)
			assert.Equal(t, &rangeConfig{15, 30}, stored, "the baseline is only read")
			if tc.want != nil {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
				return
			}
			assert.Nil(t, got, "no partial candidate")
			require.ErrorIs(t, err, ErrValidation)
			var edits *EditsError
			require.ErrorAs(t, err, &edits)
			assert.Len(t, edits.Keys, len(tc.edits))
		})
	}
	require.NoError(t, CheckKey[rangeConfig](t.Context(), "min", 8))
	require.NoError(t, CheckKey[rangeConfig](t.Context(), "max", 4))
	require.ErrorIs(t, CheckKey[rangeConfig](t.Context(), "min", 12), ErrValidation)
}

// Keys that cannot all apply refuse the batch whole with one *EditsError,
// every refused key in it in key order, whichever stage refused it: unknown
// keys, also inside a value; one value set under two spellings; an index
// past the next element; a key beneath a value that cannot hold it; keys
// selecting both elements and fields of one value. A refused key matches
// ErrKey; no candidate was judged, so the error does not match
// ErrValidation.
func TestDecodeEdits_RefusesKeysThatCannotAllApply(t *testing.T) {
	baseline := &editConfig{Primary: editServer{"p", 5}, Pool: []editServer{{"x", 1}}}
	for _, tc := range []struct {
		name    string
		edits   map[string]any
		refused map[string]bool // refused key → Unknown
	}{
		{"unknown keys", map[string]any{"prot": 1, "primary.prot": 2, "primary.port": 3}, map[string]bool{"primary.prot": true, "prot": true}},
		{"a wildcard", map[string]any{"pool[*].port": 1}, map[string]bool{"pool[*].port": true}},
		{"an unknown key inside a value", map[string]any{"primary": map[string]any{"bogus": 1, "port": 2}}, map[string]bool{"primary.bogus": true}},
		{"unknown and index refusals together", map[string]any{"prot": 1, "pool[5].host": "z"}, map[string]bool{"pool[5].host": false, "prot": true}},
		{"spelled by alias", map[string]any{"primary.port": 6, "MAIN.port": 7}, map[string]bool{"primary.port": false}},
		{"spelled by case", map[string]any{"primary.port": 6, "PRIMARY.Port": 7}, map[string]bool{"primary.port": false}},
		{"an index past the next element", map[string]any{"pool[2].host": "z"}, map[string]bool{"pool[2].host": false}},
		{"an index past the end of a list the batch sets", map[string]any{"pool": []any{map[string]any{"host": "a"}}, "pool[2].host": "z"}, map[string]bool{"pool[2].host": false}},
		{"a key beneath a number", map[string]any{"primary": 5, "primary.port": 7}, map[string]bool{"primary.port": false}},
		{"a key beneath nil", map[string]any{"primary": nil, "primary.port": 7}, map[string]bool{"primary.port": false}},
		{"an element of a value holding no elements", map[string]any{"pool": map[string]any{"host": "h"}, "pool[0].port": 1}, map[string]bool{"pool[0].port": false}},
		{"an element and a key of one interface", map[string]any{"extra.x": 1, "extra[0]": 2}, map[string]bool{"extra[0]": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeEdits(t.Context(), baseline, tc.edits)
			assert.Nil(t, got)
			var edits *EditsError
			require.ErrorAs(t, err, &edits)
			refused := map[string]bool{}
			var order []string
			for _, ke := range edits.Refused {
				refused[ke.Key] = ke.Unknown
				order = append(order, ke.Key)
				assert.Equal(t, ke.Unknown, errors.Is(&ke, ErrKey), ke.Key)
			}
			assert.Equal(t, tc.refused, refused)
			assert.IsNonDecreasing(t, order)
			assert.ErrorIs(t, err, ErrKey)
			assert.NotErrorIs(t, err, ErrValidation)
			assert.Empty(t, edits.Keys)
			for key := range tc.refused {
				assert.Contains(t, err.Error(), key)
			}
		})
	}
	assert.Equal(t, &editConfig{Primary: editServer{"p", 5}, Pool: []editServer{{"x", 1}}}, baseline)
}

// nil sets the zero value, as an explicit null from any source does.
func TestDecodeEdits_NilSetsTheZeroValue(t *testing.T) {
	baseline := &editConfig{Primary: editServer{"p", 5}, Pool: []editServer{{"x", 1}}, Extra: 3}
	got, err := DecodeEdits(t.Context(), baseline, map[string]any{"primary.port": nil, "pool": nil, "extra": nil})
	require.NoError(t, err)
	assert.Equal(t, &editConfig{Primary: editServer{"p", 0}}, got)

	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "primary: {host: p, port: 5}\npool: [{host: x, port: 1}]\nextra: 3\n")
	loaded, err := Load[editConfig](t.Context(), WithFile(file), WithOverrideEntries(readEntries(map[string]any{"primary.port": nil, "pool": nil, "extra": nil})))
	require.NoError(t, err)
	assert.Equal(t, got, loaded)
}

// A baseline holding a value that does not marshal to text is the caller's
// fault, not a refused edit or an invalid candidate.
func TestDecodeEdits_BaselineThatDoesNotMarshal(t *testing.T) {
	type cfg struct {
		Value failingText `koanf:"value"`
		Port  int         `koanf:"port"`
	}
	_, err := DecodeEdits(t.Context(), &cfg{Value: failingText{n: -1}}, map[string]any{"port": 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "value")
	assert.NotErrorIs(t, err, ErrValidation)
	assert.NotErrorIs(t, err, ErrKey)
	var edits *EditsError
	assert.NotErrorAs(t, err, &edits)
}

// Edits apply in key order, an index by its number, so one batch appends
// several elements — pool[10] after pool[9] — and edits to one element,
// entry or section merge.
func TestDecodeEdits_AppliesEditsInKeyOrder(t *testing.T) {
	baseline := &editConfig{
		Addresses: map[string]editServer{"first": {"a", 1}},
		Pool:      []editServer{{"x", 1}},
		Route:     netip.MustParsePrefix("10.0.0.0/8"),
		Extra:     []any{1},
	}
	edits := map[string]any{
		"pool[0].port":            7,
		"pool[1].host":            "y",
		"pool[1].port":            2,
		"pool[2]":                 editServer{"z", 3},
		`addresses["first"].host`: "b",
		`addresses["second"]`:     map[string]any{"port": 2},
		`addresses["third"].host`: "c",
		`addresses["third"].port`: 3,
		"primary.host":            "p",
		"PRIMARY.port":            5,
		"route":                   "192.0.2.0/24",
		"extra[1]":                2,
	}
	want := []editServer{{"x", 7}, {"y", 2}, {"z", 3}}
	for i := 3; i <= 10; i++ {
		edits["pool["+strconv.Itoa(i)+"].port"] = i
		want = append(want, editServer{Port: i})
	}
	got, err := DecodeEdits(t.Context(), baseline, edits)
	require.NoError(t, err)
	assert.Equal(t, &editConfig{
		Addresses: map[string]editServer{"first": {"b", 1}, "second": {"", 2}, "third": {"c", 3}},
		Primary:   editServer{"p", 5},
		Pool:      want,
		Route:     netip.MustParsePrefix("192.0.2.0/24"),
		Extra:     []any{1, 2},
	}, got)
}

// A key beneath another applies after it, setting what it names in the
// value the other set: merged into the baseline when that value is a map,
// in place of the baseline's when it replaces it.
func TestDecodeEdits_KeyBeneathAnotherAppliesAfterIt(t *testing.T) {
	baseline := &editConfig{Primary: editServer{"p", 5}, Pool: []editServer{{"x", 1}}, Extra: 3}
	for _, tc := range []struct {
		name  string
		edits map[string]any
		check func(t *testing.T, got *editConfig)
	}{
		{"a map, then a key beneath it", map[string]any{"primary": map[string]any{"port": 6}, "primary.port": 7}, func(t *testing.T, got *editConfig) {
			assert.Equal(t, editServer{"p", 7}, got.Primary)
		}},
		{"a struct, then a key beneath it", map[string]any{"primary": editServer{Port: 6}, "primary.port": 7}, func(t *testing.T, got *editConfig) {
			assert.Equal(t, editServer{"", 7}, got.Primary, "the struct replaced the baseline's section")
		}},
		{"three keys, each beneath the one before", map[string]any{"addresses": map[string]any{}, `addresses["a"]`: editServer{Host: "h"}, `addresses["a"].port`: 2}, func(t *testing.T, got *editConfig) {
			assert.Equal(t, map[string]editServer{"a": {"h", 2}}, got.Addresses)
		}},
		{"a list, then an element appended to it", map[string]any{"pool": []any{map[string]any{"host": "a"}}, "pool[1].host": "b", "pool[0].port": 3}, func(t *testing.T, got *editConfig) {
			assert.Equal(t, []editServer{{"a", 3}, {"b", 0}}, got.Pool, "the list replaced the baseline's")
		}},
		{"an element, then a key beneath it", map[string]any{"pool[0]": map[string]any{"host": "y"}, "pool[0].port": 4}, func(t *testing.T, got *editConfig) {
			assert.Equal(t, []editServer{{"y", 4}}, got.Pool)
		}},
		{"an interface's map, then a key beneath it", map[string]any{"extra": map[string]any{"y": 1}, "extra.x": 2}, func(t *testing.T, got *editConfig) {
			assert.Equal(t, map[string]any{"x": 2, "y": 1}, got.Extra)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeEdits(t.Context(), baseline, tc.edits)
			require.NoError(t, err)
			tc.check(t, got)
		})
	}
}

// A batch of one is DecodeKey: the same candidate, with the decode hooks
// applied; its failures name keys and never values.
func TestDecodeEdits_OneEditIsDecodeKey(t *testing.T) {
	nine := WithDecodeHook(mapstructure.DecodeHookFuncType(func(from, to reflect.Type, v any) (any, error) {
		if from.Kind() == reflect.String && to.Kind() == reflect.Int && v == "nine" {
			return 9, nil
		}
		return v, nil
	}))
	for _, tc := range []struct {
		key   string
		value any
	}{
		{"port", "nine"}, {"old_port", 11}, {"nested.count", 4}, {"items[0].port", 3}, {`names["a.b"]`, 2},
	} {
		want, err := DecodeKey[keyConfig](t.Context(), tc.key, tc.value, nine)
		require.NoError(t, err, tc.key)
		got, err := DecodeEdits[keyConfig](t.Context(), nil, map[string]any{tc.key: tc.value}, nine)
		require.NoError(t, err, tc.key)
		assert.Equal(t, want, got, tc.key)
	}

	_, err := DecodeEdits[keyConfig](t.Context(), nil, map[string]any{"port": "invalid-secret-value", "floor": 2})
	var edits *EditsError
	require.ErrorAs(t, err, &edits)
	assert.Equal(t, []string{"floor", "port"}, edits.Keys)
	assert.Empty(t, edits.Refused)
	assert.ErrorIs(t, err, ErrValidation)
	assert.NotErrorIs(t, err, ErrKey)
	assert.NotContains(t, err.Error(), "invalid-secret-value")
	_, err = DecodeKey[keyConfig](t.Context(), "port", "invalid-secret-value")
	assert.NotContains(t, err.Error(), "invalid-secret-value")
}

// The candidate shares nothing with the baseline or the edits, and decoding
// a baseline with no edits gives it back.
func TestDecodeEdits_CandidateIsDetached(t *testing.T) {
	baseline := &editConfig{
		Addresses: map[string]editServer{"first": {"a", 1}},
		Primary:   editServer{"p", 5},
		Pool:      []editServer{{"x", 1}},
		Route:     netip.MustParsePrefix("10.0.0.0/8"),
		Extra:     map[string]any{"x": []any{1}},
	}
	got, err := DecodeEdits(t.Context(), baseline, nil)
	require.NoError(t, err)
	assert.Equal(t, baseline, got)

	value := map[string]any{"y": []any{2}}
	got, err = DecodeEdits(t.Context(), baseline, map[string]any{"extra": value, "pool[0].port": 2})
	require.NoError(t, err)
	got.Addresses["first"] = editServer{}
	got.Pool[0].Host = "changed"
	extra := got.Extra.(map[string]any)
	extra["x"].([]any)[0] = 97
	extra["y"].([]any)[0] = 99
	value["y"].([]any)[0] = 98
	assert.Equal(t, map[string]any{"x": []any{97}, "y": []any{99}}, extra, "the caller's edit value is copied")
	assert.Equal(t, &editConfig{
		Addresses: map[string]editServer{"first": {"a", 1}},
		Primary:   editServer{"p", 5},
		Pool:      []editServer{{"x", 1}},
		Route:     netip.MustParsePrefix("10.0.0.0/8"),
		Extra:     map[string]any{"x": []any{1}},
	}, baseline)
}

type remainSection struct {
	Name string         `koanf:"name"`
	Rest map[string]any `koanf:",remain"`
}

// A key a section's remain field collects is a key: an edit sets it, and
// what lies beneath it, as a source would.
func TestDecodeEdits_KeysARemainFieldCollects(t *testing.T) {
	type cfg struct {
		Section remainSection `koanf:"section"`
	}
	got, err := DecodeEdits[cfg](t.Context(), nil, map[string]any{"section.extra": 1, `section["a.b"].c`: 2, "section.name": "n"})
	require.NoError(t, err)
	assert.Equal(t, remainSection{Name: "n", Rest: map[string]any{"extra": 1, "a.b": map[string]any{"c": 2}}}, got.Section)
	one, err := DecodeKey[cfg](t.Context(), "section.extra", 1)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"extra": 1}, one.Section.Rest)
}
