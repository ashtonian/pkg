package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-viper/mapstructure/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapDefault_ElementEntriesMerge(t *testing.T) {
	type entry struct {
		Labels map[string]string `koanf:"labels" default:"team=core,tier=backend"`
	}
	type cfg struct {
		Items []entry           `koanf:"items"`
		Named map[string]*entry `koanf:"named"`
	}
	snap, err := LoadSnapshot[cfg](t.Context(), WithDefaults(map[string]any{
		"items": []any{map[string]any{"labels": map[string]any{"tier": "web"}}, map[string]any{"labels": nil}},
		"named": map[string]any{"a": map[string]any{"labels": map[string]any{"tier": "api"}}, "off": nil},
	}))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"team": "core", "tier": "web"}, snap.Config.Items[0].Labels)
	assert.Nil(t, snap.Config.Items[1].Labels)
	assert.Equal(t, map[string]string{"team": "core", "tier": "api"}, snap.Config.Named["a"].Labels)
	assert.Nil(t, snap.Config.Named["off"])
	assert.Equal(t, SourceTag, snap.Provenance[`items[0].labels["team"]`].Input.Kind)
	assert.Equal(t, SourceDefaults, snap.Provenance[`items[0].labels["tier"]`].Input.Kind)
}

func TestSep_TypedProviderContainers(t *testing.T) {
	type entry struct {
		Hosts []string `koanf:"hosts" alias:"old" sep:";"`
	}
	type cfg struct {
		Items  []entry          `koanf:"items"`
		Named  map[string]entry `koanf:"named"`
		Nested struct {
			Entry entry `koanf:"entry"`
		} `koanf:"nested"`
	}
	for _, data := range []mapSource{
		{"items": []map[string]string{{"old": "a;b"}}, "named": map[string]map[string]string{"x": {"old": "c;d"}}, "nested": map[string]map[string]string{"entry": {"old": "e;f"}}},
		{"items": [1]map[string]string{{"old": "a;b"}}, "named": map[string]map[string]any{"x": {"old": "c;d"}}, "nested": map[string]any{"entry": map[string]string{"old": "e;f"}}},
	} {
		snap, err := LoadSnapshot[cfg](t.Context(), WithBaseProvider(data, nil))
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b"}, snap.Config.Items[0].Hosts)
		assert.Equal(t, []string{"c", "d"}, snap.Config.Named["x"].Hosts)
		assert.Equal(t, []string{"e", "f"}, snap.Config.Nested.Entry.Hosts)
		assert.Equal(t, SourceBase, snap.Provenance["items[0].hosts[0]"].Input.Kind)
	}
}

func TestAlias_RefusesCaseFoldCollisions(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeFor[struct {
			A int `koanf:"a" alias:"NAME"`
			B int `koanf:"name"`
		}](),
		reflect.TypeFor[struct {
			B int `koanf:"name"`
			A int `koanf:"a" alias:"NAME"`
		}](),
		reflect.TypeFor[struct {
			A int `koanf:"a" alias:"old"`
			B int `koanf:"b" alias:"OLD"`
		}](),
		reflect.TypeFor[struct {
			A int `koanf:"a" alias:"A"`
		}](),
		reflect.TypeFor[struct {
			A int `koanf:"a" alias:"K"`
			B int `koanf:"b" alias:"K"`
		}](),
	}
	for _, typ := range types {
		_, err := rootSchema(typ)
		require.ErrorIs(t, err, ErrSchema)
	}
}

func TestEnvTag_RecursiveSchemasTerminateAndValidate(t *testing.T) {
	type tree map[string]tree
	type list []list
	type cfg struct {
		Tree tree `koanf:"tree"`
		List list `koanf:"list"`
	}
	_, err := Load[cfg](t.Context())
	require.NoError(t, err)
	type node struct {
		Port     int    `koanf:"port" env:"PORT"`
		Children []node `koanf:"children"`
	}
	_, err = Load[node](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "children[*].port")
}

func TestDiagnostics_WithholdSensitiveMapContents(t *testing.T) {
	type badDefault struct {
		Secrets map[string]string `koanf:"secrets" default:"FAKE_CREDENTIAL=1,FAKE_CREDENTIAL=2"`
	}
	_, err := Load[badDefault](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.NotContains(t, err.Error(), "FAKE_CREDENTIAL")
	type entry struct {
		Port int `koanf:"port" alias:"old"`
	}
	type cfg struct {
		Entries map[string]entry `koanf:"entries" sensitive:"true"`
	}
	for _, values := range []map[string]any{
		{"old": 1, "typo": 2}, {"old": 1, "port": 2}, {"port": "bad-value"},
	} {
		var logs bytes.Buffer
		var seen *UnknownKeysError
		_, err = Load[cfg](t.Context(), WithDefaults(map[string]any{"entries": map[string]any{"FAKE_CREDENTIAL": values}}),
			WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
			WithUnknownKeys(func(_ context.Context, u *UnknownKeysError) error { seen = u; return u }))
		require.ErrorIs(t, err, ErrValidation)
		assert.NotContains(t, err.Error(), "FAKE_CREDENTIAL")
		assert.NotContains(t, logs.String(), "FAKE_CREDENTIAL")
		if seen != nil {
			assert.Equal(t, []UnknownKey{{Key: "entries"}}, seen.Keys)
		}
	}
}

func TestValidation_WithholdsSensitiveOperands(t *testing.T) {
	type cfg struct {
		Password string `koanf:"password" validate:"eq=FAKE_CREDENTIAL"`
		Count    int    `koanf:"count" validate:"min=1"`
	}
	_, err := Load[cfg](t.Context())
	require.ErrorIs(t, err, ErrValidation)
	assert.NotContains(t, err.Error(), "FAKE_CREDENTIAL")
	assert.Contains(t, err.Error(), "password(eq)")
	assert.Contains(t, err.Error(), "count(min=1)")
}

func TestPrintEnv_UsesFieldSeparator(t *testing.T) {
	type inner struct {
		Hosts []string `koanf:"hosts" sep:";"`
	}
	type cfg struct {
		Inner inner            `koanf:"inner"`
		Whole []string         `koanf:"whole" sep:"none"`
		Named map[string]inner `koanf:"named"`
	}
	want := cfg{Inner: inner{Hosts: []string{"a,b", "c"}}, Whole: []string{"a,b;c"}, Named: map[string]inner{"x": {Hosts: []string{"d", "e"}}}}
	var out bytes.Buffer
	require.NoError(t, PrintEnv(&want, "APP", WithWriter(&out)))
	assert.Contains(t, out.String(), "export APP_INNER_HOSTS='a,b;c'")
	assert.Contains(t, out.String(), "export APP_WHOLE='a,b;c'")
	assert.Contains(t, out.String(), "export APP_NAMED_X_HOSTS='d;e'")
	got, err := Load[cfg](t.Context(), WithEnvPrefix("APP"), WithEnviron(func() []string { return []string{"APP_INNER_HOSTS=a,b;c", "APP_WHOLE=a,b;c", "APP_NAMED_X_HOSTS=d;e"} }))
	require.NoError(t, err)
	assert.Equal(t, want, *got)
	for _, hosts := range [][]string{{""}, {"a;b"}} {
		out.Reset()
		require.NoError(t, PrintEnv(&cfg{Inner: inner{Hosts: hosts}}, "APP", WithWriter(&out)))
		assert.NotContains(t, "\n"+out.String(), "\nexport APP_INNER_HOSTS=")
	}
	out.Reset()
	require.NoError(t, PrintEnv(&cfg{Whole: []string{"a", "b"}}, "APP", WithWriter(&out)))
	assert.NotContains(t, "\n"+out.String(), "\nexport APP_WHOLE=")
}

func TestUsage_ListsWholeCollectionsAndCommentsExamples(t *testing.T) {
	type cfg struct {
		Labels map[string]string `koanf:"labels" env:"LABELS"`
		Note   string            `koanf:"note" example:"one\ntwo"`
	}
	var out bytes.Buffer
	require.NoError(t, PrintUsage[cfg]("APP", WithWriter(&out), WithFormat(FormatJSON)))
	var rows []usageRow
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	assert.Equal(t, "labels", rows[0].Key)
	assert.Equal(t, "APP_LABELS", rows[0].Env)
	assert.Equal(t, []string{"LABELS"}, rows[0].AlsoEnv)
	out.Reset()
	require.NoError(t, PrintTemplate[cfg]("APP", WithWriter(&out)))
	assert.Contains(t, out.String(), "env APP_LABELS, LABELS")
	out.Reset()
	require.NoError(t, PrintUsage[cfg]("APP", WithWriter(&out), WithFormat(FormatEnv)))
	for line := range strings.SplitSeq(out.String(), "\n") {
		assert.True(t, line == "" || strings.HasPrefix(line, "#"), "uncommented example: %q", line)
	}
}

func TestPrintExplain_ScalarElements(t *testing.T) {
	type cfg struct {
		Hosts     []string `koanf:"hosts" default:"a,b"`
		Ports     [2]int   `koanf:"ports"`
		Passwords []string `koanf:"passwords" default:"FAKE_CREDENTIAL"`
		Port      int      `koanf:"port" default:"80"`
	}
	snap, err := LoadSnapshot[cfg](t.Context())
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, PrintExplain(snap, WithWriter(&out), WithFormat(FormatJSON)))
	var rows []explainRow
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	require.Len(t, rows, 6)
	assert.Equal(t, "hosts[0]", rows[0].Key)
	assert.Equal(t, "a", rows[0].Value)
	assert.Equal(t, "b", rows[1].Value)
	assert.Equal(t, "0", rows[2].Value)
	assert.Equal(t, redactedValue, rows[4].Value)
	assert.NotContains(t, out.String(), "FAKE_CREDENTIAL")
}

func TestAlias_ProviderAnnotationsFollowRewrite(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port" alias:"old"`
	}
	p := &originProvider{data: map[string]any{"OLD": 80}, annotations: map[string]Source{"old": {Kind: SourceFlag, Name: "--old"}}}
	snap, err := LoadSnapshot[cfg](t.Context(), WithBaseProvider(p, nil))
	require.NoError(t, err)
	assert.Equal(t, 80, snap.Config.Port)
	assert.Equal(t, origin(SourceFlag, "--old"), snap.Provenance["port"])
	p.annotations["port"] = Source{Kind: SourceFlag, Name: "--port"}
	_, err = LoadSnapshot[cfg](t.Context(), WithBaseProvider(p, nil))
	require.ErrorIs(t, err, ErrSource)
}

func TestJSONParser_RejectsInexactIntegers(t *testing.T) {
	for _, number := range []string{"18446744073709551617", "-9223372036854775809"} {
		_, err := jsonParser{}.Unmarshal([]byte(`{"number":` + number + `}`))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), number)
	}
	file := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, file, `{"password":1e400}`)
	_, err := Load[struct {
		Password float64 `koanf:"password"`
	}](t.Context(), WithFile(file))
	require.ErrorIs(t, err, ErrFileParse)
	var pe *ParseError
	require.ErrorAs(t, err, &pe)
	assert.NotContains(t, pe.Err.Error(), "1e400")
}

func TestClosestField_RequiresSharedPosition(t *testing.T) {
	assert.Empty(t, closest("abc", func(yield func(string) bool) { yield("bca") }))
	assert.Equal(t, "a", closest("abc", func(yield func(string) bool) { yield("abcxx"); yield("a") }))
}

func TestTags_ReloadDiffAndApplier(t *testing.T) {
	type Embedded struct {
		Hosts []string `koanf:"hosts" alias:"old" sep:";" default:"a;b" env:"HOSTS"`
	}
	type cfg struct {
		Embedded  `koanf:",squash"`
		MaxTokens int `koanf:"max_tokens" sensitive:"false" default:"10" example:"20"`
	}
	var value = "c;d"
	var applied []string
	fail := true
	r, err := NewReloader[cfg](t.Context(), []Option{WithEnvPrefix("APP"), WithEnviron(func() []string { return []string{"HOSTS=" + value} })}, WithProvenance(), WithApplier(Applier[cfg]{Name: "hosts", Prefixes: []string{"hosts"}, Apply: func(_ context.Context, ev ApplyEvent[cfg]) (ApplyResult, error) {
		if fail {
			fail = false
			return ApplyResult{}, errors.New("retry")
		}
		applied = append([]string(nil), ev.Desired.Hosts...)
		return ApplyResult{}, nil
	}}))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Stop(context.Background())) })
	value = "e;f"
	result, err := r.Reload(t.Context())
	require.ErrorIs(t, err, ErrApply)
	assert.Equal(t, []string{"e", "f"}, result.Snapshot.Config.Hosts)
	_, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"e", "f"}, applied)
	snap := r.Snapshot()
	assert.Equal(t, Source{Kind: SourceEnv, Name: "HOSTS"}, snap.Provenance["hosts[0]"].Input)
	var out bytes.Buffer
	require.NoError(t, PrintExplain(snap, WithWriter(&out)))
	assert.NotContains(t, out.String(), redactedValue)
}

func TestSep_CustomHookSeesSplitInput(t *testing.T) {
	type cfg struct {
		Tagged []string `koanf:"tagged" sep:";"`
		Plain  []string `koanf:"plain"`
	}
	var sawSplit bool
	hook := mapstructure.DecodeHookFuncType(func(from, to reflect.Type, data any) (any, error) {
		if to == reflect.TypeFor[[]string]() {
			if from.Kind() == reflect.Slice {
				sawSplit = true
			}
			if from.Kind() == reflect.String {
				return []string{"hook"}, nil
			}
		}
		return data, nil
	})
	got, err := Load[cfg](t.Context(), WithDefaults(map[string]any{"tagged": "a;b", "plain": "a,b"}), WithDecodeHook(hook))
	require.NoError(t, err)
	assert.True(t, sawSplit)
	assert.Equal(t, []string{"a", "b"}, got.Tagged)
	assert.Equal(t, []string{"hook"}, got.Plain)
}

func TestDiagnostics_DoNotPrintStructTags(t *testing.T) {
	type cfg struct {
		Items []struct {
			Password string `koanf:"password" default:"FAKE_CREDENTIAL" example:"FAKE_EXAMPLE"`
		} `koanf:"items"`
	}
	anonymous := struct {
		Password string `koanf:"password" default:"FAKE_CREDENTIAL" example:"FAKE_EXAMPLE"`
	}{}
	unknown := &UnknownKeysError{Type: reflect.TypeOf(anonymous), Keys: []UnknownKey{{Key: "typo"}}}
	assert.NotContains(t, unknown.Error(), "FAKE_CREDENTIAL")
	assert.NotContains(t, unknown.Error(), "FAKE_EXAMPLE")
	_, err := Load[struct {
		Password int `koanf:"password" example:"FAKE_EXAMPLE"`
	}](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.NotContains(t, err.Error(), "FAKE_EXAMPLE")
	for _, print := range []func(*bytes.Buffer) error{
		func(b *bytes.Buffer) error { return PrintUsage[cfg]("APP", WithWriter(b)) },
		func(b *bytes.Buffer) error { return PrintTemplate[cfg]("APP", WithWriter(b)) },
		func(b *bytes.Buffer) error { return PrintRedacted(&cfg{}, WithWriter(b)) },
		func(b *bytes.Buffer) error { raw, err := JSONSchema[cfg](); b.Write(raw); return err },
		func(b *bytes.Buffer) error {
			changes, err := Diff(&cfg{}, &cfg{Items: []struct {
				Password string `koanf:"password" default:"FAKE_CREDENTIAL" example:"FAKE_EXAMPLE"`
			}{{}}})
			if err == nil {
				err = json.NewEncoder(b).Encode(changes)
			}
			return err
		},
	} {
		var out bytes.Buffer
		require.NoError(t, print(&out))
		assert.NotContains(t, out.String(), "FAKE_CREDENTIAL")
		assert.NotContains(t, out.String(), "FAKE_EXAMPLE")
	}
}

func TestEnv_RefusesWholeMapAndEntryTogether(t *testing.T) {
	type cfg struct {
		Labels map[string]string `koanf:"labels" env:"LABELS"`
	}
	for _, whole := range []string{"APP_LABELS", "LABELS"} {
		for _, environ := range [][]string{{whole + "=a=FAKE_CREDENTIAL", "APP_LABELS_B=entry"}, {"APP_LABELS_B=entry", whole + "=a=FAKE_CREDENTIAL"}} {
			called := false
			_, err := Load[cfg](t.Context(), WithEnvPrefix("APP"), WithEnviron(func() []string { return environ }), WithUnknownKeys(func(context.Context, *UnknownKeysError) error { called = true; return nil }))
			require.ErrorIs(t, err, ErrValidation)
			assert.NotContains(t, err.Error(), "FAKE_CREDENTIAL")
			assert.Contains(t, err.Error(), "set both a value and keys beneath it")
			assert.False(t, called)
		}
	}
}

func TestFileParser_SingleParserMigration(t *testing.T) {
	type cfg struct {
		Host string `koanf:"host"`
	}
	for _, suffix := range []string{".yaml", ".yml", ".json", ".custom"} {
		file := filepath.Join(t.TempDir(), "config"+suffix)
		writeFile(t, file, "host=local\n")
		got, err := Load[cfg](t.Context(), WithRequiredFile(file), WithFileParser("", kvParser{}), WithFileParser(".yaml", nil), WithFileParser(".yml", nil), WithFileParser(".json", nil))
		require.NoError(t, err)
		assert.Equal(t, "local", got.Host)
	}
}

func TestProvenance_DiagnosticsWithholdSensitiveEntryNames(t *testing.T) {
	type cfg struct {
		Entries map[string]int `koanf:"entries" sensitive:"true"`
	}
	p := &originProvider{data: map[string]any{"entries": map[string]any{}}, annotations: map[string]Source{`entries["FAKE_CREDENTIAL"]`: {Kind: SourceFlag, Name: "--entry"}}}
	_, err := LoadSnapshot[cfg](t.Context(), WithBaseProvider(p, nil))
	require.ErrorIs(t, err, ErrSource)
	assert.NotContains(t, err.Error(), "FAKE_CREDENTIAL")
}

func TestTags_ValidateElementsOfNonStringMaps(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeFor[struct {
			Entries map[int]struct {
				Port int `koanf:"port" sensitive:"yes"`
			}
		}](),
		reflect.TypeFor[struct {
			Entries map[int]struct {
				Port int `koanf:"port" env:"PORT"`
			}
		}](),
		reflect.TypeFor[struct {
			Entries map[int]struct {
				Port int `koanf:"port" sep:";"`
			}
		}](),
		reflect.TypeFor[struct {
			Entries map[int]struct {
				Port int `koanf:"port" example:"FAKE_EXAMPLE"`
			}
		}](),
		reflect.TypeFor[struct {
			Entries map[int]struct {
				A int `koanf:"a" alias:"b"`
				B int `koanf:"b"`
			}
		}](),
		reflect.TypeFor[struct {
			Entries map[int]struct {
				Labels map[string]string `koanf:"labels" default:"invalid"`
			}
		}](),
	}
	for _, typ := range types {
		_, err := rootSchema(typ)
		require.ErrorIs(t, err, ErrSchema)
		assert.NotContains(t, err.Error(), "FAKE_EXAMPLE")
	}
}
