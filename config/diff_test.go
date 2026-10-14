package config

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyPath_RoundTrip(t *testing.T) {
	for _, key := range []string{
		"db.host",
		"items[0].port",
		`named["primary"].port`,
		`limits["/upload"]`,
		`named["a.b"]["c]d"][3]`,
		`named["0"]`,
		`named[""]`,
		`named["quote\"d"].x`,
		"items[*].port",
	} {
		p, err := parseKeyPath(key)
		require.NoError(t, err, key)
		assert.Equal(t, key, p.String())
	}
	for _, bad := range []string{
		"", ".db", "db.", "db..host", "[0]", "db[01]", "db[-1]", "db[x]", `db["x"`, `db["x"]y`, "db.a*b",
	} {
		_, err := parseKeyPath(bad)
		assert.ErrorIs(t, err, ErrKey, bad)
	}
}

type fieldForInner struct {
	Port  int    `koanf:"port" desc:"listen port"`
	Token string `koanf:"token"`
}

type FieldForSquashed struct {
	Region string `koanf:"region"`
}

type fieldForCfg struct {
	FieldForSquashed `koanf:",squash"`
	DB               fieldForInner            `koanf:"db"`
	Items            []fieldForInner          `koanf:"items"`
	Named            map[string]fieldForInner `koanf:"named"`
	Limits           map[string]int           `koanf:"limits"`
	Fixed            [2]int                   `koanf:"fixed"`
	Value            any                      `koanf:"value"`
	Started          time.Time                `koanf:"started"`
	Secret           fieldForInner            `koanf:"secret" sensitive:"true"`
}

func TestFieldFor(t *testing.T) {
	f, err := FieldFor[fieldForCfg]("db.port")
	require.NoError(t, err)
	assert.Equal(t, "db.port", f.Path)
	assert.Equal(t, "listen port", f.Tag.Get("desc"))
	assert.Equal(t, []int{1, 0}, f.Index)

	f, err = FieldFor[fieldForCfg]("region")
	require.NoError(t, err)
	assert.Equal(t, []int{0, 0}, f.Index, "a squashed struct's field keeps its index through the squash")

	f, err = FieldFor[fieldForCfg]("items[3].port")
	require.NoError(t, err)
	assert.Equal(t, "items[*].port", f.Path)
	assert.Equal(t, reflect.TypeFor[int](), f.Type)
	assert.Nil(t, f.Index)

	f, err = FieldFor[fieldForCfg](`named["password"].port`)
	require.NoError(t, err)
	assert.Equal(t, "named[*].port", f.Path)
	assert.True(t, f.Sensitive, "a credential-named map key covers what is beneath it")

	f, err = FieldFor[fieldForCfg](`limits["/upload"]`)
	require.NoError(t, err)
	assert.Equal(t, "limits[*]", f.Path)

	f, err = FieldFor[fieldForCfg](`value["x"][0]`)
	require.NoError(t, err)
	assert.Equal(t, "value", f.Path, "the interface's field owns what its value holds")

	f, err = FieldFor[fieldForCfg]("secret.port")
	require.NoError(t, err)
	assert.True(t, f.Sensitive)

	f, err = FieldFor[fieldForCfg]("db.token")
	require.NoError(t, err)
	assert.True(t, f.Sensitive)

	for _, bad := range []string{"db.nope", "db[0]", `items["a"]`, "fixed[2]", "db.port.x", "named[0]"} {
		_, err := FieldFor[fieldForCfg](bad)
		assert.ErrorIs(t, err, ErrKey, bad)
	}
}

func TestSchema_RejectsKeysThatCannotBeToldApart(t *testing.T) {
	type dotted struct {
		A int `koanf:"a.b"`
	}
	type twice struct {
		A int `koanf:"x"`
		B int `koanf:"x"`
	}
	_, err := FieldFor[dotted]("a")
	require.ErrorIs(t, err, ErrSchema)
	_, err = Diff[twice](nil, nil)
	require.ErrorIs(t, err, ErrSchema)
	_, err = Diff[int](nil, nil)
	require.ErrorIs(t, err, ErrSchema)
}

func changeKeys(changes []Change) []string {
	keys := make([]string, len(changes))
	for i, c := range changes {
		keys[i] = c.Key
	}
	return keys
}

// Values that render to the same text are still different values.
func TestDiff_ComparesValuesNotText(t *testing.T) {
	type cfg struct {
		Tags  []string `koanf:"tags"`
		Value any      `koanf:"value"`
	}
	a := &cfg{Tags: []string{"a,b"}, Value: 1}
	b := &cfg{Tags: []string{"a", "b"}, Value: "1"}
	changes, err := Diff(a, b)
	require.NoError(t, err)
	assert.Equal(t, []string{"tags[0]", "tags[1]", "value"}, changeKeys(changes))
	assert.Equal(t, "int", changes[2].Old.Type)
	assert.Equal(t, "string", changes[2].New.Type)

	changes, err = Diff(a, a)
	require.NoError(t, err)
	assert.Empty(t, changes)
}

func TestDiff_Collections(t *testing.T) {
	type item struct {
		Port int `koanf:"port"`
	}
	type cfg struct {
		Items  []item          `koanf:"items"`
		Named  map[string]item `koanf:"named"`
		Limits map[string]int  `koanf:"limits"`
		Opt    *item           `koanf:"opt"`
	}
	a := &cfg{
		Items:  []item{{1}, {2}, {3}},
		Named:  map[string]item{"x": {1}, "a.b": {2}},
		Limits: nil,
	}
	b := &cfg{
		Items:  []item{{1}, {3}},
		Named:  map[string]item{"x": {9}},
		Limits: map[string]int{},
		Opt:    &item{Port: 5},
	}
	changes, err := Diff(a, b)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"items[1].port",
		"items[2]", "items[2].port",
		`named["a.b"]`, `named["a.b"].port`,
		`named["x"].port`,
		"limits",
		"opt", "opt.port",
	}, changeKeys(changes))

	byKey := map[string]Change{}
	for _, c := range changes {
		byKey[c.Key] = c
	}
	removed := byKey["items[2]"]
	assert.True(t, removed.Structural)
	assert.True(t, removed.Old.Present)
	assert.False(t, removed.New.Present)
	assert.Equal(t, "items[*]", removed.Field.Path)

	assert.Equal(t, Value{Present: true, Type: "int", Text: "3"}, byKey["items[2].port"].Old)
	assert.Equal(t, Value{}, byKey["items[2].port"].New)

	limits := byKey["limits"]
	assert.True(t, limits.Structural, "nil and empty differ")
	assert.True(t, limits.Old.Nil)
	assert.Equal(t, "{}", limits.New.Text)

	opt := byKey["opt"]
	assert.True(t, opt.Structural)
	assert.True(t, opt.Old.Nil)
	assert.Equal(t, "opt.port", byKey["opt.port"].Field.Path)
	assert.Equal(t, []int{3, 0}, byKey["opt.port"].Field.Index)
}

func TestDiff_NilConfigs(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port"`
	}
	changes, err := Diff[cfg](nil, nil)
	require.NoError(t, err)
	assert.Empty(t, changes)

	changes, err = Diff(nil, &cfg{Port: 1})
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, "port", changes[0].Key)
	assert.False(t, changes[0].Old.Present)
}

// A secret that changes is a change, with nothing of it shown; a sensitive
// map is compared whole so its keys, which may be secrets, are not listed.
func TestDiff_Sensitive(t *testing.T) {
	type creds struct {
		User string `koanf:"user"`
		Pass string `koanf:"pass"`
	}
	type cfg struct {
		Password string            `koanf:"password"`
		DB       creds             `koanf:"db" sensitive:"true"`
		Tokens   map[string]string `koanf:"tokens"`
		Settings map[string]any    `koanf:"settings"`
	}
	a := &cfg{Password: "SECRET_A", DB: creds{User: "u", Pass: "SECRET_B"}, Tokens: map[string]string{"SECRET_KEY_A": "x"}, Settings: map[string]any{"api_key": "SECRET_C", "region": "us"}}
	b := &cfg{Password: "SECRET_D", DB: creds{User: "u", Pass: "SECRET_E"}, Tokens: map[string]string{"SECRET_KEY_B": "x"}, Settings: map[string]any{"api_key": "SECRET_F", "region": "eu"}}
	changes, err := Diff(a, b)
	require.NoError(t, err)
	assert.Equal(t, []string{"password", "db.pass", "tokens", `settings["api_key"]`, `settings["region"]`}, changeKeys(changes))
	for _, c := range changes[:4] {
		assert.True(t, c.Old.Redacted && c.New.Redacted, c.Key)
	}
	assert.Equal(t, "eu", changes[4].New.Text)
	text := ""
	for _, c := range changes {
		text += c.Key + c.Old.Text + c.New.Text
	}
	assert.NotContains(t, text, "SECRET_")
}

type diffNode struct {
	Name string    `koanf:"name"`
	Next *diffNode `koanf:"next"`
}

func TestDiff_CyclesAndUncomparableValues(t *testing.T) {
	type cfg struct {
		Root *diffNode `koanf:"root"`
		Hook any       `koanf:"hook"`
	}
	a := &diffNode{Name: "a"}
	a.Next = a
	b := &diffNode{Name: "a"}
	b.Next = b
	changes, err := Diff(&cfg{Root: a}, &cfg{Root: b})
	require.NoError(t, err)
	assert.Empty(t, changes)

	c := &diffNode{Name: "a"}
	c.Next = &diffNode{Name: "c", Next: c}
	changes, err = Diff(&cfg{Root: a}, &cfg{Root: c})
	require.NoError(t, err)
	assert.NotEmpty(t, changes)

	_, err = Diff(&cfg{Hook: func() {}}, &cfg{Hook: func() {}})
	require.ErrorIs(t, err, ErrSchema)

	// A cycle on one side only ends where it leads back.
	changes, err = Diff(nil, &cfg{Root: a})
	require.NoError(t, err)
	assert.Equal(t, []string{"root", "root.name", "root.next", "hook"}, changeKeys(changes))
	assert.Equal(t, cycleValue, changes[2].New.Text)
}

// An interface whose value changes type is one change at its key, the keys
// either value holds left unlisted; one that keeps its type lists them.
func TestDiff_InterfaceValues(t *testing.T) {
	type inner struct {
		Port int `koanf:"port"`
	}
	type cfg struct {
		Plugin any `koanf:"plugin"`
	}
	changes, err := Diff(&cfg{Plugin: inner{Port: 1}}, &cfg{Plugin: map[string]any{"port": 1}})
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, "plugin", changes[0].Key)
	assert.True(t, changes[0].Structural)
	assert.Equal(t, "config.inner", changes[0].Old.Type)

	changes, err = Diff(&cfg{Plugin: inner{Port: 1}}, &cfg{Plugin: inner{Port: 2}})
	require.NoError(t, err)
	assert.Equal(t, []string{"plugin.port"}, changeKeys(changes))

	changes, err = Diff(&cfg{Plugin: 1}, &cfg{Plugin: "1"})
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.False(t, changes[0].Structural)
	assert.Equal(t, "1", changes[0].New.Text)
}

func TestDiff_DoesNotChangeItsInputs(t *testing.T) {
	type cfg struct {
		Named map[string][]int `koanf:"named"`
	}
	a := &cfg{Named: map[string][]int{"x": {1}}}
	b := &cfg{Named: map[string][]int{"x": {2}, "y": nil}}
	_, err := Diff(a, b)
	require.NoError(t, err)
	assert.Equal(t, &cfg{Named: map[string][]int{"x": {1}}}, a)
	assert.Equal(t, &cfg{Named: map[string][]int{"x": {2}, "y": nil}}, b)
}

// A cycle reached through an interface ends where it leads back, on either
// side, as one through a pointer does.
func TestDiff_CyclesThroughInterfaces(t *testing.T) {
	type cfg struct {
		Data any `koanf:"data"`
	}
	m := map[string]any{}
	m["next"] = m
	changes, err := Diff(&cfg{Data: m}, &cfg{Data: m})
	require.NoError(t, err)
	assert.Empty(t, changes)

	other := map[string]any{"x": 1}
	other["next"] = other
	changes, err = Diff(&cfg{Data: m}, &cfg{Data: other})
	require.NoError(t, err)
	assert.Equal(t, []string{`data["next"]`, `data["x"]`}, changeKeys(changes))
	assert.Equal(t, cycleValue, changes[0].Old.Text)
	assert.Equal(t, cycleValue, changes[0].New.Text)

	// The cycle on one side meets a finite chain on the other.
	chain := map[string]any{"next": map[string]any{"next": map[string]any{}}}
	changes, err = Diff(&cfg{Data: m}, &cfg{Data: chain})
	require.NoError(t, err)
	assert.Equal(t, []string{`data["next"]`}, changeKeys(changes))
}

func TestDiff_NilInterfacesAndCycleShapes(t *testing.T) {
	type cfg struct {
		Data any `koanf:"data"`
	}
	c := &cfg{Data: map[string]string(nil)}
	changes, err := Diff(c, c)
	require.NoError(t, err)
	assert.Empty(t, changes, "equal typed nils are equal")

	changes, err = Diff(&cfg{Data: map[string]string(nil)}, &cfg{Data: map[string]string{"a": "1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"data", `data["a"]`}, changeKeys(changes), "one type both sides compares key by key")
	assert.True(t, changes[0].Structural)

	a := &diffNode{Name: "a"}
	a.Next = a
	changes, err = Diff(&struct {
		Root *diffNode `koanf:"root"`
	}{Root: a}, &struct {
		Root *diffNode `koanf:"root"`
	}{Root: &diffNode{Name: "a"}})
	require.NoError(t, err)
	require.Equal(t, []string{"root.next"}, changeKeys(changes))
	assert.True(t, changes[0].Structural, "a cycle turning nil is structural")
	assert.Equal(t, cycleValue, changes[0].Old.Text)
	assert.True(t, changes[0].New.Nil)
}

// A func, channel or unsafe pointer inside a value compared whole is refused
// as anywhere else.
func TestDiff_RefusesUncomparableValuesComparedWhole(t *testing.T) {
	type cfg struct {
		Sensitive map[string]any `koanf:"sensitive" sensitive:"true"`
		Opaque    map[int]any    `koanf:"opaque"`
	}
	handler := func() {}
	_, err := Diff(&cfg{Sensitive: map[string]any{"handler": handler}}, &cfg{Sensitive: map[string]any{"handler": handler}})
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "sensitive")
	assert.Contains(t, err.Error(), "func()")

	_, err = Diff(&cfg{}, &cfg{Opaque: map[int]any{1: make(chan int)}})
	require.ErrorIs(t, err, ErrSchema)

	// A cycle in a value compared whole ends the search too.
	m := map[string]any{"ok": 1}
	m["self"] = m
	_, err = Diff(&cfg{Sensitive: m}, &cfg{Sensitive: m})
	require.NoError(t, err)
}
