package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type keyConfig struct {
	Port   int `koanf:"port" alias:"old_port" default:"8" validate:"min=1,max=100"`
	Floor  int `koanf:"floor" default:"1"`
	Nested struct {
		Count int `koanf:"count" default:"2" validate:"min=1"`
	} `koanf:"nested"`
	Items []struct {
		Port int `koanf:"port" default:"3" validate:"min=1"`
	} `koanf:"items" format:"json" validate:"dive"`
	Names map[string]int `koanf:"names" validate:"dive,min=1"`
}

func (c *keyConfig) Validate() error {
	if c.Port < c.Floor {
		return errors.New("port is below floor")
	}
	return nil
}

func TestCheckKeyMatchesSchemaAndDoesNoSourceIO(t *testing.T) {
	provider, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		t.Error("provider read during key check")
		return nil, nil
	}, "")
	require.NoError(t, err)
	opts := []Option{WithBaseProvider(provider, nil), WithRequiredFile("/missing/config.yaml"),
		WithEnvPrefix("APP"), WithEnviron(func() []string { t.Error("environment read"); return nil }),
		WithSecretPopulator(func(context.Context, any) error { t.Error("secrets read"); return nil }),
		WithPostLoad(func(context.Context, *keyConfig) error { t.Error("post-load ran"); return nil }),
		WithUnknownKeys(func(context.Context, *UnknownKeysError) error { t.Error("unknown callback ran"); return nil })}
	for _, tc := range []struct {
		key      string
		value    any
		category error
	}{
		{"port", "9", nil}, {"PORT", 10, nil}, {"old_port", 11, nil},
		{"port", "invalid-secret-value", ErrValidation}, {"port", 101, ErrValidation},
		{"nested.count", 0, ErrValidation}, {"floor", 10, ErrValidation},
		{"prot", 4, ErrKey}, {"items[*].port", 3, ErrKey},
		{"items[0].port", 3, nil}, {"items[999999999].port", 3, ErrValidation},
		{`names["a.b"]`, 2, nil}, {`names["a.b"]`, 0, ErrValidation},
	} {
		t.Run(fmt.Sprintf("%s/%v", tc.key, tc.value), func(t *testing.T) {
			err := CheckKey[keyConfig](t.Context(), tc.key, tc.value, opts...)
			if tc.category == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.category)
			var ke *KeyError
			require.ErrorAs(t, err, &ke)
			assert.Equal(t, tc.category == ErrKey, ke.Unknown)
			assert.NotContains(t, err.Error(), "invalid-secret-value")
		})
	}
	require.NoError(t, CheckKey[keyConfig](t.Context(), "floor", 10, WithDefaults(map[string]any{"port": 20})))
	require.NoError(t, CheckKey[keyConfig](t.Context(), "port", "nine", WithDecodeHook(mapstructure.DecodeHookFuncType(func(from, to reflect.Type, v any) (any, error) {
		if from.Kind() == reflect.String && to.Kind() == reflect.Int && v == "nine" {
			return 9, nil
		}
		return v, nil
	}))))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, CheckKey[keyConfig](ctx, "port", 9, opts...), context.Canceled)
}

func TestWithInvalidEntriesPreservesLowerSourcesAndUnknowns(t *testing.T) {
	input := map[string]any{"port": "bad", "floor": "also-bad", "nested": map[string]any{"count": 4, "future": true},
		"items": []any{map[string]any{"port": "bad", "future": true}}, "future": true}
	var rejected []KeyError
	snap, err := LoadSnapshot[keyConfig](t.Context(),
		WithBaseProvider(confmap.Provider(map[string]any{"port": 7, "items": []any{map[string]any{"port": 5}}}, ""), nil),
		WithOverrideProvider(confmap.Provider(input, ""), nil, WithInvalidEntries(func(_ context.Context, entries []KeyError) error { rejected = entries; return nil })),
		WithAllowUnknownKeys())
	require.NoError(t, err)
	assert.Equal(t, 7, snap.Config.Port)
	assert.Equal(t, 4, snap.Config.Nested.Count)
	require.Len(t, snap.Config.Items, 1)
	assert.Equal(t, 5, snap.Config.Items[0].Port)
	require.Len(t, rejected, 3)
	assert.Equal(t, []string{"floor", "items", "port"}, []string{rejected[0].Key, rejected[1].Key, rejected[2].Key})
	assert.Equal(t, SourceBase, snap.Provenance["port"].Input.Kind)
	assert.Equal(t, SourceOverride, snap.Provenance["nested.count"].Input.Kind)
	require.NotNil(t, snap.UnknownKeys)
	assert.Equal(t, []UnknownKey{{Key: "future"}, {Key: "items[0].future"}, {Key: "nested.future"}}, snap.UnknownKeys.Keys)
	assert.Equal(t, "bad", input["port"], "provider data remains owned by the provider")
	for _, r := range rejected {
		assert.NotContains(t, r.Error(), "bad")
	}
}

func TestWithInvalidEntriesJSONAndStrictUnknowns(t *testing.T) {
	for _, value := range []any{`not json`, `[{"port":0,"future":true}]`} {
		var rejected []KeyError
		_, err := Load[keyConfig](t.Context(), WithBaseProvider(confmap.Provider(map[string]any{"items": value, "future": 1}, ""), nil,
			WithInvalidEntries(func(_ context.Context, entries []KeyError) error { rejected = entries; return nil })))
		var unknown *UnknownKeysError
		require.ErrorAs(t, err, &unknown)
		require.Len(t, rejected, 1)
		assert.Equal(t, "items", rejected[0].Key)
		assert.Contains(t, unknown.Keys, UnknownKey{Key: "future"})
	}
}

func TestWithInvalidEntriesFailureBoundaries(t *testing.T) {
	called := false
	p, err := NewMapProvider(func(context.Context) (map[string]any, error) { called = true; return map[string]any{"port": 9}, nil }, "")
	require.NoError(t, err)
	got, err := Load[keyConfig](t.Context(), WithDefaults(map[string]any{"port": 0}), WithBaseProvider(p, nil, WithInvalidEntries(nil)))
	require.NoError(t, err, "an entry repairing defaults that fail is kept")
	assert.True(t, called)
	assert.Equal(t, 9, got.Port)
	failed := errors.New("read failed")
	_, err = Load[keyConfig](t.Context(), WithBaseProvider(failingProvider{err: failed}, nil, WithInvalidEntries(nil)))
	require.ErrorIs(t, err, ErrSource)
	require.ErrorIs(t, err, failed)
	_, err = Load[keyConfig](t.Context(), WithBaseProvider(p, nil, WithInvalidEntries(func(context.Context, []KeyError) error { return failed })))
	require.ErrorIs(t, err, ErrSource)
	require.ErrorIs(t, err, failed)
	// A Validate method's failure withholds no entry; the load fails with it.
	_, err = Load[keyConfig](t.Context(), WithBaseProvider(confmap.Provider(map[string]any{"port": 2, "floor": 3}, ""), nil, WithInvalidEntries(nil)))
	require.ErrorIs(t, err, ErrValidation)
}

func TestDecodeKeyReturnsDetachedValidatedConfiguration(t *testing.T) {
	input := map[string]int{"service": 4}
	got, err := DecodeKey[keyConfig](t.Context(), "names", input)
	require.NoError(t, err)
	assert.Equal(t, 8, got.Port)
	assert.Equal(t, 4, got.Names["service"])
	input["service"] = 99
	assert.Equal(t, 4, got.Names["service"])
	got.Names["service"] = 6
	assert.Equal(t, 99, input["service"])
	got, err = DecodeKey[keyConfig](t.Context(), "port", 0)
	require.ErrorIs(t, err, ErrValidation)
	assert.Nil(t, got)
}

type editServer struct {
	Host string `koanf:"host"`
	Port int    `koanf:"port"`
}

type editConfig struct {
	Addresses map[string]editServer `koanf:"addresses"`
	Primary   editServer            `koanf:"primary" alias:"main"`
	Pool      []editServer          `koanf:"pool"`
	Route     netip.Prefix          `koanf:"route"`
	Extra     any                   `koanf:"extra"`
}

// Validate refuses two addresses on one port, so a check judges the
// entries an edit leaves in place.
func (c *editConfig) Validate() error {
	seen := map[int]string{}
	for _, name := range slices.Sorted(maps.Keys(c.Addresses)) {
		other, ok := seen[c.Addresses[name].Port]
		if ok {
			return fmt.Errorf("addresses %s and %s share a port", other, name)
		}
		seen[c.Addresses[name].Port] = name
	}
	return nil
}

// editDefaults is one baseline in two shapes: typed Go values, or the
// nested maps a file decodes to.
func editDefaults(typed bool) map[string]any {
	if typed {
		baseline := map[string]any{
			"addresses": map[string]editServer{"first": {"a", 1}, "second": {"b", 2}},
			"primary":   editServer{"p", 5},
			"pool":      []editServer{{"x", 1}},
			"route":     netip.MustParsePrefix("10.0.0.0/8"),
			"extra":     map[string]any{"x": 0, "y": 2},
		}
		return baseline
	}
	baseline := map[string]any{
		"addresses": map[string]any{"first": map[string]any{"host": "a", "port": 1}, "second": map[string]any{"host": "b", "port": 2}},
		"primary":   map[string]any{"host": "p", "port": 5},
		"pool":      []any{map[string]any{"host": "x", "port": 1}},
		"route":     "10.0.0.0/8",
		"extra":     map[string]any{"x": 0, "y": 2},
	}
	return baseline
}

// A key set over defaults leaves everything else as the defaults hold it,
// whether they are typed values or nested maps: the candidate equals a load
// of the whole intended configuration. A map value merges into the value
// beneath key by key and anything else replaces it, as from a source.
func TestDecodeKey_EditsTypedDefaultsAsTheirMapShape(t *testing.T) {
	const (
		addresses = "{first: {host: a, port: 1}, second: {host: b, port: 2}}"
		primary   = "{host: p, port: 5}"
		pool      = "[{host: x, port: 1}]"
		route     = "10.0.0.0/8"
		extra     = "{x: 0, y: 2}"
	)
	doc := func(addresses, primary, pool, route, extra string) string {
		return "addresses: " + addresses + "\nprimary: " + primary + "\npool: " + pool + "\nroute: " + route + "\nextra: " + extra + "\n"
	}
	for _, tc := range []struct {
		name  string
		key   string
		value any
		want  string
	}{
		{"map entry field", `addresses["first"].port`, 9, doc("{first: {host: a, port: 9}, second: {host: b, port: 2}}", primary, pool, route, extra)},
		{"map entry as a map", `addresses["first"]`, map[string]any{"port": 9}, doc("{first: {host: a, port: 9}, second: {host: b, port: 2}}", primary, pool, route, extra)},
		{"map entry as a struct", `addresses["first"]`, editServer{Port: 9}, doc("{first: {host: '', port: 9}, second: {host: b, port: 2}}", primary, pool, route, extra)},
		{"new map entry", `addresses["third"]`, map[string]any{"host": "c", "port": 3}, doc("{first: {host: a, port: 1}, second: {host: b, port: 2}, third: {host: c, port: 3}}", primary, pool, route, extra)},
		{"null map entry", `addresses["second"]`, nil, doc("{first: {host: a, port: 1}, second: null}", primary, pool, route, extra)},
		{"section field", "primary.port", 6, doc(addresses, "{host: p, port: 6}", pool, route, extra)},
		{"section field by alias", "MAIN.Port", 6, doc(addresses, "{host: p, port: 6}", pool, route, extra)},
		{"section as a map", "primary", map[string]any{"port": 6}, doc(addresses, "{host: p, port: 6}", pool, route, extra)},
		{"null section field", "primary.host", nil, doc(addresses, "{host: null, port: 5}", pool, route, extra)},
		{"list element field", "pool[0].port", 7, doc(addresses, primary, "[{host: x, port: 7}]", route, extra)},
		{"appended element", "pool[1]", map[string]any{"host": "y"}, doc(addresses, primary, "[{host: x, port: 1}, {host: y}]", route, extra)},
		{"appended element field", "pool[1].host", "y", doc(addresses, primary, "[{host: x, port: 1}, {host: y}]", route, extra)},
		{"value decoded whole", "route", "192.0.2.0/24", doc(addresses, primary, pool, "192.0.2.0/24", extra)},
		{"interface map key", "extra.x", 1, doc(addresses, primary, pool, route, "{x: 1, y: 2}")},
		{"interface value", "extra", []any{1}, doc(addresses, primary, pool, route, "[1]")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "intended.yaml")
			writeFile(t, file, tc.want)
			want, err := Load[editConfig](t.Context(), WithFile(file))
			require.NoError(t, err)
			for _, typed := range []bool{true, false} {
				defaults := editDefaults(typed)
				got, err := DecodeKey[editConfig](t.Context(), tc.key, tc.value, WithDefaults(defaults))
				require.NoError(t, err, "typed defaults: %v", typed)
				assert.Equal(t, want, got, "typed defaults: %v", typed)
				assert.Equal(t, editDefaults(typed), defaults, "the caller's defaults keep their content")
			}
		})
	}
}

func TestDecodeKey_ListIndexBoundsAndDetachedDefaults(t *testing.T) {
	for _, typed := range []bool{true, false} {
		defaults := editDefaults(typed)
		_, err := DecodeKey[editConfig](t.Context(), "pool[2].host", "z", WithDefaults(defaults))
		var ke *KeyError
		require.ErrorAs(t, err, &ke)
		assert.False(t, ke.Unknown)
		assert.Equal(t, "pool[2].host", ke.Key)
		require.ErrorIs(t, err, ErrValidation, "an index past the next element")
		var problem *keyProblem
		require.ErrorAs(t, ke.Err, &problem, "refused as an index, not as a candidate that fails to decode")
		_, err = DecodeKey[editConfig](t.Context(), "pool[-1].host", "z", WithDefaults(defaults))
		require.ErrorIs(t, err, ErrKey)
		_, err = DecodeKey[editConfig](t.Context(), "route.bits", 4, WithDefaults(defaults))
		require.ErrorAs(t, err, &ke)
		assert.Equal(t, KeyError{Key: "route.bits", Unknown: true}, KeyError{Key: ke.Key, Unknown: ke.Unknown}, "a value decoded from text has no key to set")

		opt := WithDefaults(defaults)
		got, err := DecodeKey[editConfig](t.Context(), "pool[0].port", 7, opt)
		require.NoError(t, err)
		assert.Equal(t, []editServer{{"x", 7}}, got.Pool, "typed defaults: %v: the index edits that element of the defaults' list", typed)
		want := *got
		want.Pool = slices.Clone(got.Pool)
		want.Addresses = maps.Clone(got.Addresses)
		want.Extra = maps.Clone(got.Extra.(map[string]any))
		got.Pool[0].Host = "changed"
		got.Addresses["first"] = editServer{}
		got.Extra.(map[string]any)["x"] = 99
		assert.Equal(t, editDefaults(typed), defaults, "the candidate shares nothing with the caller's defaults")
		again, err := DecodeKey[editConfig](t.Context(), "pool[0].port", 7, opt)
		require.NoError(t, err)
		assert.Equal(t, want, *again, "typed defaults: %v: the candidate shares nothing with the option's defaults", typed)
	}
	got, err := DecodeKey[editConfig](t.Context(), "pool[0].port", 3)
	require.NoError(t, err)
	assert.Equal(t, []editServer{{Port: 3}}, got.Pool, "index 0 appends to a list nothing set")
}

// CheckKey judges the whole candidate, so a rule relating an edited entry to
// one it leaves in place sees that entry, typed or not.
func TestCheckKey_ValidatorSeesTheSiblingsAnEditKeeps(t *testing.T) {
	for _, typed := range []bool{true, false} {
		defaults := WithDefaults(editDefaults(typed))
		require.ErrorIs(t, CheckKey[editConfig](t.Context(), `addresses["first"].port`, 2, defaults), ErrValidation,
			"typed defaults: %v: the edit collides with the second address", typed)
		require.NoError(t, CheckKey[editConfig](t.Context(), `addresses["first"].port`, 3, defaults))
	}
}

// A map keyed by integers is keyed by their text as it merges, so an edit
// or a higher source sets one entry of it and the others keep theirs.
func TestDecodeKey_IntegerKeyedMapMergesByEntry(t *testing.T) {
	type cfg struct {
		IDs map[int]string `koanf:"ids"`
	}
	defaults := WithDefaults(map[string]any{"ids": map[int]string{1: "a", 2: "b"}})
	got, err := DecodeKey[cfg](t.Context(), `ids["1"]`, "z", defaults)
	require.NoError(t, err)
	assert.Equal(t, map[int]string{1: "z", 2: "b"}, got.IDs)

	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "ids: {2: c}\n")
	loaded, err := Load[cfg](t.Context(), defaults, WithFile(file))
	require.NoError(t, err)
	assert.Equal(t, map[int]string{1: "a", 2: "c"}, loaded.IDs)
}

type aliasedServer struct {
	Host string `koanf:"host"`
	Port int    `koanf:"port" alias:"listen"`
}

type integerKeyed struct {
	Ports map[int]aliasedServer    `koanf:"ports"`
	Named map[string]aliasedServer `koanf:"named"`
}

// An edit beneath a map keyed by integers spells its keys as a file's are
// spelled: through aliases and letter case, inside its value and in the key,
// with the integer read as a load reads a file's key — 0x50 is 80, and 080
// no integer — as a map keyed by strings does.
func TestDecodeKey_EditsBeneathAnIntegerKeyedMapResolve(t *testing.T) {
	defaults := WithDefaults(map[string]any{
		"ports": map[int]aliasedServer{80: {"a", 1}},
		"named": map[string]aliasedServer{"x": {"a", 1}},
	})
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "ports: {80: {Port: 9}}\n")
	fromFile, err := Load[integerKeyed](t.Context(), defaults, WithFile(file))
	require.NoError(t, err)
	require.Equal(t, aliasedServer{"a", 9}, fromFile.Ports[80])
	for _, tc := range []struct {
		key   string
		value any
	}{
		{`ports["80"]`, map[string]any{"Port": 9}},
		{`ports["80"]`, map[string]any{"listen": 9}},
		{`ports["80"].Port`, 9},
		{`ports["0x50"].listen`, 9},
	} {
		got, err := DecodeKey[integerKeyed](t.Context(), tc.key, tc.value, defaults)
		require.NoError(t, err, tc.key)
		assert.Equal(t, fromFile.Ports, got.Ports, tc.key)
		named, err := DecodeKey[integerKeyed](t.Context(), strings.Replace(strings.Replace(tc.key, `ports["0x50"]`, `named["x"]`, 1), `ports["80"]`, `named["x"]`, 1), tc.value, defaults)
		require.NoError(t, err, tc.key)
		assert.Equal(t, aliasedServer{"a", 9}, named.Named["x"], tc.key)
	}
	for _, key := range []string{`ports["eighty"].port`, `ports["080"].port`} {
		_, err = DecodeKey[integerKeyed](t.Context(), key, 9, defaults)
		var ke *KeyError
		require.ErrorAs(t, err, &ke, key)
		assert.True(t, ke.Unknown, key)
		require.ErrorIs(t, err, ErrKey, key)
	}
}

// CanonicalKey spells a key one way: fields by their keys whatever alias or
// letter case named them, map keys by the text of the key they decode to,
// keys beneath an interface as fields where they can be. A key naming
// nothing is refused.
func TestCanonicalKey(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"MAIN.Port", "primary.port"},
		{`addresses["a.b"].HOST`, `addresses["a.b"].host`},
		{`extra["x"]`, "extra.x"},
		{`extra["a.b"]`, `extra["a.b"]`},
		{"pool[3].host", "pool[3].host"},
	} {
		got, err := CanonicalKey[editConfig](tc.key)
		require.NoError(t, err, tc.key)
		assert.Equal(t, tc.want, got, tc.key)
	}
	got, err := CanonicalKey[integerKeyed](`ports["0x50"].listen`)
	require.NoError(t, err)
	assert.Equal(t, `ports["80"].port`, got)
	_, err = CanonicalKey[integerKeyed](`ports["080"].port`)
	require.ErrorIs(t, err, ErrKey, "the decoder reads no integer from 080")
	for _, key := range []string{"prot", "pool[*].host", "", "primary..port", `ports["x"]`} {
		_, err := CanonicalKey[editConfig](key)
		var ke *KeyError
		require.ErrorAs(t, err, &ke, key)
		assert.True(t, ke.Unknown, key)
		assert.ErrorIs(t, err, ErrKey, key)
	}
}

// A struct field is spelled as a field: a map key's spelling of one is
// refused at the root as beneath it, a struct's remain field collecting
// only what no field reads.
func TestCanonicalKey_RefusesAFieldSpelledAsAMapKey(t *testing.T) {
	type cfg struct {
		Port   int `koanf:"port"`
		Limits struct {
			Max int `koanf:"max"`
		} `koanf:"limits"`
		Open struct {
			Max  int            `koanf:"max"`
			Rest map[string]any `koanf:",remain"`
		} `koanf:"open"`
	}
	for _, key := range []string{`["port"]`, `["limits"].max`, `limits["max"]`, `open["max"]`, `["a.b"]`} {
		_, err := CanonicalKey[cfg](key)
		var ke *KeyError
		require.ErrorAs(t, err, &ke, key)
		assert.True(t, ke.Unknown, key)
		require.ErrorIs(t, err, ErrKey, key)
	}
	got, err := CanonicalKey[cfg](`open["other"]`)
	require.NoError(t, err)
	assert.Equal(t, "open.other", got)
}

// A refused edit names its key, and every key inside its value that no
// field reads, in the key grammar, whatever the names hold.
func TestDecodeEdits_RefusedKeysParse(t *testing.T) {
	type entry struct {
		Port int `koanf:"port"`
	}
	type cfg struct {
		Server entry            `koanf:"server"`
		Named  map[string]entry `koanf:"named"`
		Items  []entry          `koanf:"items"`
	}
	odd := map[string]any{"a.b": 1, "": 2, "port.x": 3, `q"z`: 4, "a b": 5}
	_, err := DecodeEdits[cfg](t.Context(), nil, map[string]any{
		"server":       odd,
		`named["x.y"]`: odd,
		"items":        []any{odd},
		`["a.b"]`:      1,
	})
	var edits *EditsError
	require.ErrorAs(t, err, &edits)
	require.NotEmpty(t, edits.Refused)
	keys := map[string]bool{}
	for _, r := range edits.Refused {
		_, err := ParseKey(r.Key)
		require.NoError(t, err, r.Key)
		keys[r.Key] = true
	}
	assert.True(t, keys[`server["a.b"]`], "%v", keys)
	assert.True(t, keys[`named["x.y"]["port.x"]`], "%v", keys)
	assert.True(t, keys[`items[0][""]`], "%v", keys)
	assert.True(t, keys[`["a.b"]`], "%v", keys)
}

// A key's spelling does not depend on validation: a validate tag the
// default validator cannot read, on another field, leaves CanonicalKey as it
// is.
func TestCanonicalKey_IgnoresValidateTags(t *testing.T) {
	type misspeltRule struct {
		Port  int `koanf:"port" alias:"listen"`
		Other int `koanf:"other" validate:"requird"`
	}
	_, err := Load[misspeltRule](t.Context())
	require.ErrorIs(t, err, ErrSchema, "the tag is one the default validator refuses")
	got, err := CanonicalKey[misspeltRule]("LISTEN")
	require.NoError(t, err)
	assert.Equal(t, "port", got)
	got, err = CanonicalKey[misspeltRule]("other")
	require.NoError(t, err)
	assert.Equal(t, "other", got)
}

type beneathInterfaceSection struct {
	Limits map[string]int `koanf:"limits" validate:"dive,max=5"`
}

type beneathInterfaceConfig struct {
	Extra any            `koanf:"extra"`
	Tags  map[string]any `koanf:"tags"`
}

// Every output names a key beneath an interface as CanonicalKey spells it: a
// field wherever its name can be one, a quoted map key otherwise, whatever
// shape the interface's value has. A map the schema declares keeps its
// quoted entries above the interface.
func TestCanonicalKey_IsHowEveryOutputNamesAKeyBeneathAnInterface(t *testing.T) {
	var want []string
	for _, key := range []string{`extra["a.b"]`, `extra["l"][0]["y"]`, `extra["s"]["limits"]["k"]`, `extra["x"]`, `tags["t"]["u"]`} {
		got, err := CanonicalKey[beneathInterfaceConfig](key)
		require.NoError(t, err)
		want = append(want, got)
	}
	require.Equal(t, []string{`extra["a.b"]`, "extra.l[0].y", "extra.s.limits.k", "extra.x", `tags["t"].u`}, want)
	config := func(n int) *beneathInterfaceConfig {
		cfg := &beneathInterfaceConfig{
			Extra: map[string]any{
				"x": n, "a.b": n,
				"l": []any{map[string]any{"y": n}},
				"s": beneathInterfaceSection{Limits: map[string]int{"k": n}},
			},
			Tags: map[string]any{"t": map[string]any{"u": n}},
		}
		return cfg
	}

	t.Run("printed", func(t *testing.T) {
		printed, err := RedactedMapChecked(config(1))
		require.NoError(t, err)
		assert.ElementsMatch(t, want, slices.Collect(maps.Keys(printed)))
	})
	t.Run("diffed", func(t *testing.T) {
		changes, err := Diff(config(1), config(2))
		require.NoError(t, err)
		var keys []string
		for _, c := range changes {
			keys = append(keys, c.Key)
		}
		assert.Equal(t, want, keys)
	})
	t.Run("a value that does not marshal to text", func(t *testing.T) {
		_, err := DecodeEdits[beneathInterfaceConfig](t.Context(), nil, map[string]any{
			"extra": map[string]any{"x": failingText{n: -1}, "a.b": failingText{n: -1}, "ok": failingText{n: 1}},
		})
		var ee *EditsError
		require.ErrorAs(t, err, &ee)
		require.Len(t, ee.Refused, 1)
		assert.Contains(t, ee.Refused[0].Error(), `values that do not marshal to text: extra["a.b"], extra.x`)
	})
	t.Run("a validation failure", func(t *testing.T) {
		key, err := CanonicalKey[beneathInterfaceConfig](`extra["limits"]["k"]`)
		require.NoError(t, err)
		require.Equal(t, "extra.limits.k", key)
		baseline := &beneathInterfaceConfig{Extra: beneathInterfaceSection{Limits: map[string]int{"k": 9}}}
		_, err = DecodeEdits(t.Context(), baseline, nil)
		var ee *EditsError
		require.ErrorAs(t, err, &ee)
		assert.Contains(t, ee.Err.Error(), key+"(max)")
	})
}

// undivable's validate tag parses but cannot apply to its field: the
// validator meets it only as it validates a value.
type undivable struct {
	Count int `koanf:"count" validate:"dive"`
}

// interfaceHolder's extra holds whatever a decode hook makes of it, an
// undivable among them, which the validator reaches only through a value.
type interfaceHolder struct {
	Port  int `koanf:"port" default:"1" validate:"min=1"`
	Extra any `koanf:"extra"`
}

// A validate tag the validator cannot apply is the declaration's fault, no
// key's or batch's: DecodeKey, DecodeEdits and a load whose sources withhold
// invalid entries return ErrSchema, not ErrValidation, and no *KeyError or
// *EditsError — also when only an entry's value brings the validator to it.
func TestKeyChecks_ValidatorDeclarationErrorsAreSchemaErrors(t *testing.T) {
	assertSchema := func(t *testing.T, err error) {
		t.Helper()
		require.ErrorIs(t, err, ErrSchema)
		require.NotErrorIs(t, err, ErrValidation)
		_, isKey := errors.AsType[*KeyError](err)
		_, isEdits := errors.AsType[*EditsError](err)
		assert.False(t, isKey || isEdits, "%#v", err)
	}
	provider, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		entries := map[string]any{"count": 1}
		return entries, nil
	}, "")
	require.NoError(t, err)

	_, err = DecodeKey[undivable](t.Context(), "count", 1)
	assertSchema(t, err)
	_, err = DecodeEdits[undivable](t.Context(), nil, map[string]any{"count": 1})
	assertSchema(t, err)
	_, err = DecodeEdits(t.Context(), &undivable{Count: 2}, map[string]any{"count": 1})
	assertSchema(t, err)
	_, err = Load[undivable](t.Context(), WithBaseEntries(readEntries(map[string]any{"count": 1}), WithInvalidEntries(nil)))
	assertSchema(t, err)
	_, err = Load[undivable](t.Context(), WithBaseProvider(provider, nil, WithInvalidEntries(nil)))
	assertSchema(t, err)

	brokenInside := WithDecodeHook(func(_, to reflect.Type, data any) (any, error) {
		m, ok := data.(map[string]any)
		if ok && to.Kind() == reflect.Interface && m["broken"] != nil {
			return undivable{}, nil
		}
		return data, nil
	})
	_, err = Load[interfaceHolder](t.Context(), brokenInside,
		WithBaseEntries(readEntries(map[string]any{"extra.broken": true}), WithInvalidEntries(nil)))
	assertSchema(t, err)
	_, err = DecodeKey[interfaceHolder](t.Context(), "extra.broken", true, brokenInside)
	assertSchema(t, err)
}

type limitedSection struct {
	Port  int `koanf:"port"`
	Limit int `koanf:"-"`
}

type limitedConfig struct {
	Section limitedSection `koanf:"section"`
}

// Validate reads the unkeyed Limit, which only a typed section sets.
func (c *limitedConfig) Validate() error {
	if c.Section.Port > c.Section.Limit {
		return fmt.Errorf("port %d is over the limit %d", c.Section.Port, c.Section.Limit)
	}
	return nil
}

// A candidate holds the koanf:"-" fields of the typed sections beneath its
// edits as a load does, so a rule reading one judges it as Load would: the
// defaults' typed section for CheckKey, DecodeKey, DecodeEdits over the
// defaults and a WithInvalidEntries filter, the baseline's for DecodeEdits
// over one, and an edit's own typed section replaces both.
func TestCheckKey_CandidateKeepsUnkeyedFieldsAsALoadDoes(t *testing.T) {
	defaults := WithDefaults(map[string]any{"section": limitedSection{Port: 1, Limit: 10}})
	loaded, err := Load[limitedConfig](t.Context(), defaults)
	require.NoError(t, err)
	require.Equal(t, 10, loaded.Section.Limit)

	require.NoError(t, CheckKey[limitedConfig](t.Context(), "section.port", 5, defaults))
	require.ErrorIs(t, CheckKey[limitedConfig](t.Context(), "section.port", 11, defaults), ErrValidation)
	got, err := DecodeKey[limitedConfig](t.Context(), "section.port", 5, defaults)
	require.NoError(t, err)
	assert.Equal(t, limitedSection{Port: 5, Limit: 10}, got.Section)
	got, err = DecodeEdits[limitedConfig](t.Context(), nil, map[string]any{"section.port": 5}, defaults)
	require.NoError(t, err)
	assert.Equal(t, limitedSection{Port: 5, Limit: 10}, got.Section)

	baseline := &limitedConfig{Section: limitedSection{Port: 1, Limit: 20}}
	got, err = DecodeEdits(t.Context(), baseline, map[string]any{"section.port": 15}, defaults)
	require.NoError(t, err)
	assert.Equal(t, limitedSection{Port: 15, Limit: 20}, got.Section)

	got, err = DecodeKey[limitedConfig](t.Context(), "section", limitedSection{Port: 3, Limit: 4}, defaults)
	require.NoError(t, err)
	assert.Equal(t, limitedSection{Port: 3, Limit: 4}, got.Section)
	require.ErrorIs(t, CheckKey[limitedConfig](t.Context(), "section", limitedSection{Port: 5, Limit: 4}, defaults), ErrValidation)

	var rejected []KeyError
	filtered, err := Load[limitedConfig](t.Context(), defaults,
		WithBaseProvider(mapSource{"section": map[string]any{"port": 5}}, nil, WithInvalidEntries(func(_ context.Context, entries []KeyError) error {
			rejected = entries
			return nil
		})))
	require.NoError(t, err)
	assert.Empty(t, rejected)
	assert.Equal(t, limitedSection{Port: 5, Limit: 10}, filtered.Section)
}
