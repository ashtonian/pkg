//go:build unit

package core_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

type walkBase struct {
	B1 int `json:"b1"`
	B2 int
}

type walkDeep struct {
	walkBase
	D int `json:"d"`
}

type walkLower struct {
	L int `json:"l"`
}

type walkStatus string

type walkNames struct {
	Plain   int
	Named   int      `json:"named,omitempty"`
	Quoted  int      `json:"'quoted,name'"`
	Spaced  int      `json:"with space"`
	Unicode int      `json:"ünï"`
	Punct   int      `json:"a.b-c"`
	Empty   int      `json:""`
	OptOnly int      `json:",omitempty"`
	Inline  walkBase `json:",inline"`
	walkBase
	*walkLower
	walkStatus
	Nested  walkBase `json:"nested"`
	Skipped int      `json:"-"`
	private int
}

type walkTwice struct {
	*walkDeep
	Own int `json:"own"`
}

// The walk names fields exactly as encoding/json writes them: every name it
// gives a visible field is a key json.Marshal writes, and the reverse.
func TestStructFields_NameFieldsAsEncodingJSONDoes(t *testing.T) {
	for _, v := range []any{
		walkNames{Named: 1, OptOnly: 1, walkLower: &walkLower{}, walkStatus: "hidden", private: 1},
		walkTwice{walkDeep: &walkDeep{}},
		reflect.New(reflect.StructOf([]reflect.StructField{
			{Name: "Dash", Type: reflect.TypeFor[int](), Tag: reflect.StructTag(`json:"-,"`)},
			{Name: "Malformed", Type: reflect.TypeFor[int](), Tag: reflect.StructTag(`json:"has\"quote"`)},
		})).Elem().Interface(),
	} {
		t.Run(reflect.TypeOf(v).Name(), func(t *testing.T) {
			fields, err := core.StructFieldsOf(reflect.TypeOf(v), "json", "db")
			require.NoError(t, err)
			var names []string
			for _, f := range fields.Fields {
				if !f.Hidden() {
					names = append(names, f.External)
				}
			}
			assert.ElementsMatch(t, jsonKeys(t, v), names)
		})
	}
}

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

type walkWire struct {
	ID   string `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter,patch"`
}

type walkFlat struct {
	ID     string `json:"id"   db:"id"   parse:"filter,sort"`
	Name   string `json:"name" db:"name" parse:"filter,patch"`
	Secret string `json:"-"    db:"secret"`
}

type walkEmbedded struct {
	walkWire
	Secret string `json:"-" db:"secret"`
}

type walkPointerEmbedded struct {
	*walkWire
	Secret string `json:"-" db:"secret"`
}

type walkMiddle struct{ walkWire }

type walkTwoDeep struct {
	walkMiddle
	Secret string `json:"-" db:"secret"`
}

// A type that embeds its wire type walks as the same type written flat: the
// same names, columns and tags, routes reaching the same values.
func TestStructFields_EmbeddingWalksAsTheFlatType(t *testing.T) {
	want := describe(t, walkFlat{ID: "i", Name: "n", Secret: "s"})
	for name, v := range map[string]any{
		"depth 1":       walkEmbedded{walkWire: walkWire{ID: "i", Name: "n"}, Secret: "s"},
		"pointer embed": walkPointerEmbedded{walkWire: &walkWire{ID: "i", Name: "n"}, Secret: "s"},
		"depth 2":       walkTwoDeep{walkMiddle: walkMiddle{walkWire{ID: "i", Name: "n"}}, Secret: "s"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, want, describe(t, v))
		})
	}
}

type describedField struct {
	External, Column string
	Caps             core.Capabilities
	Value            any
}

func describe(t *testing.T, v any) []describedField {
	t.Helper()
	g := core.NewSchemaGenerator()
	fields, err := g.StructFields(reflect.TypeOf(v))
	require.NoError(t, err)
	rv := reflect.ValueOf(v)
	var out []describedField
	for _, f := range fields.Fields {
		col, ok, err := g.FieldColumn(f)
		require.NoError(t, err)
		require.True(t, ok)
		fv, err := rv.FieldByIndexErr(f.Route)
		require.NoError(t, err)
		out = append(out, describedField{External: f.External, Column: col, Caps: f.Tags.Parse, Value: fv.Interface()})
	}
	return out
}

type walkHiddenSection struct {
	Hash string `db:"hash"`
}

type walkWithHiddenSection struct {
	ID                string `json:"id"`
	walkHiddenSection `json:"-"`
}

// ShadowBase is exported for reflect.StructOf, which embeds only exported
// types.
type ShadowBase struct {
	B1 int `json:"b1"`
}

// shadowing builds shapes with a promoted name claimed twice at run time:
// declared, go vet refuses their repeated json tags.
func shadowing() (shadows map[string]reflect.Type, tie reflect.Type) {
	base := reflect.TypeFor[ShadowBase]()
	other := reflect.StructOf([]reflect.StructField{{Name: "B1", Type: reflect.TypeFor[int](), Tag: `json:"b1"`}})
	embed := func(t reflect.Type) reflect.StructField {
		return reflect.StructField{Name: t.Name(), Type: t, Anonymous: true}
	}
	return map[string]reflect.Type{
		"a shallower field shadows a promoted one": reflect.StructOf([]reflect.StructField{
			embed(base), {Name: "B1", Type: reflect.TypeFor[string](), Tag: `json:"b1"`},
		}),
		"a renamed field shadows a promoted one": reflect.StructOf([]reflect.StructField{
			embed(base), {Name: "Label", Type: reflect.TypeFor[string](), Tag: `json:"b1"`},
		}),
	}, reflect.StructOf([]reflect.StructField{
		embed(base), {Name: "Other", Type: reflect.PointerTo(other), Anonymous: true},
	})
}

// A field shadowing a promoted one is the field encoding/json writes under
// the name: the walk names it, and the shadowed field is gone.
func TestStructFields_TheShallowerFieldTakesAName(t *testing.T) {
	shadows, _ := shadowing()
	for name, typ := range shadows {
		t.Run(name, func(t *testing.T) {
			s, err := core.StructFieldsOf(typ, "json", "db")
			require.NoError(t, err)
			f, ok := s.External("b1")
			require.True(t, ok)
			assert.Equal(t, reflect.TypeFor[string](), f.Type, "the outer field, not ShadowBase.B1")
			assert.Len(t, f.Route, 1)
		})
	}
}

type walkMisspelt struct {
	Name string `json:"name" parse:"fitler"`
}

func TestStructFields_RefuseCapabilityAliases(t *testing.T) {
	for _, name := range []string{"facetable", "searchable", "includable"} {
		t.Run(name, func(t *testing.T) {
			typ := reflect.StructOf([]reflect.StructField{{
				Name: "Name", Type: reflect.TypeFor[string](),
				Tag: reflect.StructTag(`json:"name" parse:"filter,` + name + `"`),
			}})
			_, err := core.StructFieldsOf(typ, "json", "db")
			require.ErrorContains(t, err, `unknown parse capability "`+name+`"`)
		})
	}
}

// A name two fields claim at one depth, which encoding/json drops, a
// misspelt capability and an embedded struct hidden whole fail the walk
// with an error naming the fields.
func TestStructFields_RefuseAmbiguousNamesAndUnknownCapabilities(t *testing.T) {
	cases := map[string]struct {
		typ  reflect.Type
		want string
	}{
		"a misspelt capability":           {reflect.TypeFor[walkMisspelt](), `unknown parse capability "fitler"`},
		"an embedded struct hidden whole": {reflect.TypeFor[walkWithHiddenSection](), `hide its fields one by one`},
	}
	_, tie := shadowing()
	cases["two promoted fields at one depth"] = struct {
		typ  reflect.Type
		want string
	}{tie, `both named "b1"`}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := core.StructFieldsOf(tc.typ, "json", "db")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// The walk is computed once per type and tag pair.
func TestStructFields_AreSharedPerType(t *testing.T) {
	a, err := core.StructFieldsOf(reflect.TypeFor[walkFlat](), "json", "db")
	require.NoError(t, err)
	b, err := core.StructFieldsOf(reflect.TypeFor[*walkFlat](), "json", "db")
	require.NoError(t, err)
	assert.Same(t, a, b)
	c, err := core.StructFieldsOf(reflect.TypeFor[walkFlat](), "yaml", "db")
	require.NoError(t, err)
	assert.NotSame(t, a, c)
	assert.True(t, slices.ContainsFunc(c.Fields, func(f *core.Field) bool { return f.External == "ID" }), "yaml names untagged fields by Go name")
}
