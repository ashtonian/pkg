package config

import (
	"context"
	"net/netip"
	"reflect"
	"testing"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type remainPlugins struct {
	Host  string         `koanf:"host"`
	Extra map[string]any `koanf:",remain" reload:"live"`
}

type remainVault struct {
	Addr  string            `koanf:"addr"`
	Paths map[string]string `koanf:",remain" sensitive:"true" validate:"dive,min=3"`
}

type remainItem struct {
	Port int `koanf:"port" alias:"listen"`
}

type remainTyped struct {
	Name  string                `koanf:"name"`
	Items map[string]remainItem `koanf:",remain"`
}

type remainLabels struct {
	Team   string            `koanf:"team"`
	Values map[string]string `koanf:",remain" validate:"dive,min=3"`
}

type remainCfg struct {
	Plugins remainPlugins  `koanf:"plugins"`
	Vault   remainVault    `koanf:"vault"`
	Typed   remainTyped    `koanf:"typed"`
	Labels  remainLabels   `koanf:"labels"`
	Rest    map[string]any `koanf:",remain"`
}

// A key a remain field collects is owned by the field, as a key beneath an
// interface is owned by the interface's field.
func TestFieldFor_KeysARemainFieldCollects(t *testing.T) {
	for _, tt := range []struct {
		key       string
		path      string
		typ       reflect.Type
		index     []int
		reload    string
		sensitive bool
	}{
		{key: "plugins.foo", path: "plugins[*]", typ: reflect.TypeFor[map[string]any](), index: []int{0, 1}, reload: ReloadLive},
		{key: `plugins["a.b"]`, path: "plugins[*]", typ: reflect.TypeFor[map[string]any](), index: []int{0, 1}, reload: ReloadLive},
		{key: `plugins["foo"].bar[0]`, path: "plugins[*]", typ: reflect.TypeFor[map[string]any](), index: []int{0, 1}, reload: ReloadLive},
		{key: "plugins.api_token", path: "plugins[*]", typ: reflect.TypeFor[map[string]any](), index: []int{0, 1}, reload: ReloadLive, sensitive: true},
		{key: "vault.prod", path: "vault[*]", typ: reflect.TypeFor[map[string]string](), index: []int{1, 1}, reload: ReloadRestart, sensitive: true},
		{key: "typed.x", path: "typed[*]", typ: reflect.TypeFor[map[string]remainItem](), index: []int{2, 1}, reload: ReloadRestart},
		{key: "typed.x.port", path: "typed[*].port", typ: reflect.TypeFor[int](), reload: ReloadRestart},
		{key: "other", path: "[*]", typ: reflect.TypeFor[map[string]any](), index: []int{4}, reload: ReloadRestart},
	} {
		t.Run(tt.key, func(t *testing.T) {
			f, err := FieldFor[remainCfg](tt.key)
			require.NoError(t, err)
			assert.Equal(t, tt.path, f.Path)
			assert.Equal(t, tt.typ, f.Type)
			assert.Equal(t, tt.index, f.Index)
			assert.Equal(t, tt.reload, f.Reload)
			assert.Equal(t, tt.sensitive, f.Sensitive)
		})
	}
	for _, bad := range []string{
		"plugins.HOST",       // the field host, spelled another way
		`plugins["host"]`,    // the field host, spelled as a map key
		"typed.x.nope",       // no field of the element type
		"vault.prod.x",       // a string has no keys beneath it
		"plugins[0]",         // a struct's keys are not indexed
		`labels.team["x"]`,   // beneath a declared field, not the remain field
		"typed.x.port.inner", // beneath an int
	} {
		_, err := FieldFor[remainCfg](bad)
		assert.ErrorIs(t, err, ErrKey, bad)
	}
}

// Every key CanonicalKey accepts, FieldFor resolves, spelled canonically:
// the walks share the one decision of what a remain field collects.
func TestCanonicalKey_AndFieldForAgreeOnRemainKeys(t *testing.T) {
	for key, want := range map[string]string{
		`plugins["foo"]`:    "plugins.foo",
		`plugins["a.b"].c`:  `plugins["a.b"].c`,
		"plugins.foo.x":     "plugins.foo.x",
		"typed.x.PORT":      "typed.x.port",
		"typed.x.listen":    "typed.x.port",
		"vault.prod":        "vault.prod",
		`["a.b"]`:           `["a.b"]`,
		"other.deeper":      "other.deeper",
		`labels["eng ops"]`: "labels.eng ops",
		"labels.team":       "labels.team",
		`typed["a b"].port`: "typed.a b.port",
	} {
		got, err := CanonicalKey[remainCfg](key)
		if !assert.NoError(t, err, key) {
			continue
		}
		assert.Equal(t, want, got, key)
		_, err = FieldFor[remainCfg](got)
		assert.NoError(t, err, got)
	}
	for _, bad := range []string{"typed.x.nope", "vault.prod.x", `plugins["host"]`, "plugins.Host.below"} {
		_, err := CanonicalKey[remainCfg](bad)
		assert.ErrorIs(t, err, ErrKey, bad)
	}
}

// A sensitive remain field's keys may themselves be secrets: diagnostics
// name only the struct holding them. A remain field that is not sensitive
// has its keys named exactly.
func TestRemain_DiagnosticsNameCollectedKeys(t *testing.T) {
	const secret = "prod_db_master"
	_, err := DecodeEdits[remainCfg](t.Context(), nil, map[string]any{"vault." + secret + ".x": 1, "labels.eng.x": 1})
	var ee *EditsError
	require.ErrorAs(t, err, &ee)
	keys := make([]string, len(ee.Refused))
	for i, r := range ee.Refused {
		keys[i] = r.Key
	}
	assert.Equal(t, []string{"labels.eng.x", "vault"}, keys)
	assert.NotContains(t, err.Error(), secret)

	_, err = Decode[remainCfg](t.Context(), map[string]any{"vault": map[string]any{secret: "ab"}})
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "vault(min)")
	assert.NotContains(t, err.Error(), secret)

	_, err = Decode[remainCfg](t.Context(), map[string]any{"labels": map[string]any{"eng": "ab"}})
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "labels.eng(min=3)")
}

// Each source names a remain field's typed entry by its element type's
// keys, so two spellings of one key merge by precedence.
func TestLoad_ARemainFieldsTypedEntriesAreSpelledByTheirType(t *testing.T) {
	got, err := Decode[remainCfg](t.Context(),
		map[string]any{"typed": map[string]any{"x": map[string]any{"port": 1}}},
		WithOverrideProvider(confmap.Provider(map[string]any{"typed": map[string]any{"x": map[string]any{"PORT": 2}}}, ""), nil))
	require.NoError(t, err)
	assert.Equal(t, 2, got.Typed.Items["x"].Port)
}

// A value under a key a remain field collects merges as an interface
// field's does: a struct whose state the merge's copy would lose is carried
// as its text.
func TestLoad_ARemainFieldsValuesSurviveTheMerge(t *testing.T) {
	got, err := Decode[remainCfg](t.Context(), map[string]any{"plugins": map[string]any{"addr": netip.MustParseAddr("10.0.0.1")}})
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", got.Plugins.Extra["addr"])
}

// An applier owns keys Diff names, and Diff compares no key a remain
// field collects.
func TestApplier_RefusesAPrefixARemainFieldCollects(t *testing.T) {
	load := func(context.Context) (*remainCfg, error) { return &remainCfg{}, nil }
	apply := func(context.Context, ApplyEvent[remainCfg]) (ApplyResult, error) { return ApplyResult{}, nil }
	for _, prefix := range []string{"plugins.foo", `plugins["foo"].bar`, "other"} {
		_, err := NewReloaderFunc(t.Context(), load, WithApplier(Applier[remainCfg]{Name: "a", Prefixes: []string{prefix}, Apply: apply}))
		require.ErrorIs(t, err, ErrKey, prefix)
		assert.Contains(t, err.Error(), "a key a remain field collects", prefix)
	}
}

// An origin provider may annotate a key a remain field collects.
func TestProvenance_AnnotatesAKeyARemainFieldCollects(t *testing.T) {
	data := map[string]any{"plugins": map[string]any{"foo": 1}, "typed": map[string]any{"x": map[string]any{"port": 2}}}
	annotations := map[string]Source{"plugins.foo": {Kind: SourceFlag, Name: "--foo"}, "typed.x.PORT": {Kind: SourceFlag, Name: "--port"}}
	_, err := LoadSnapshot[remainCfg](t.Context(), WithBaseProvider(&originProvider{data: data, annotations: annotations}, nil), WithSkipValidation())
	require.NoError(t, err)
}

// A remain field's sensitive and reload tags are the keys it collects', so
// they are checked as any field's are.
func TestSchema_ChecksARemainFieldsTags(t *testing.T) {
	type sensitiveTypo struct {
		Rest map[string]any `koanf:",remain" sensitive:"yes"`
	}
	type reloadTypo struct {
		Rest map[string]any `koanf:",remain" reload:"live now"`
	}
	type shared struct {
		Rest map[string]any `koanf:",remain" reload:"live"`
	}
	type sharedTwice struct {
		A shared `koanf:"a"`
		B shared `koanf:"b"`
	}
	_, err := Decode[sensitiveTypo](t.Context(), nil)
	assert.ErrorIs(t, err, ErrSchema)
	_, err = FieldFor[reloadTypo]("x")
	assert.ErrorIs(t, err, ErrSchema)
	_, err = FieldFor[sharedTwice]("a.x")
	assert.ErrorIs(t, err, ErrSchema)
}
