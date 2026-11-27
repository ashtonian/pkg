//go:build unit

package core_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/providertest"
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
			for f := range fields.Fields() {
				if !f.Hidden() {
					names = append(names, f.External())
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
	for f := range fields.Fields() {
		col, ok, err := g.FieldColumn(f)
		require.NoError(t, err)
		require.True(t, ok)
		fv, err := rv.FieldByIndexErr(f.Route())
		require.NoError(t, err)
		out = append(out, describedField{External: f.External(), Column: col, Caps: f.Tags().Parse, Value: fv.Interface()})
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

type walkWithNamedSection struct {
	ID                string `json:"id"`
	walkHiddenSection `json:"section"`
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
	shadows = map[string]reflect.Type{
		"a shallower field shadows a promoted one": reflect.StructOf([]reflect.StructField{
			embed(base), {Name: "B1", Type: reflect.TypeFor[string](), Tag: `json:"b1"`},
		}),
		"a renamed field shadows a promoted one": reflect.StructOf([]reflect.StructField{
			embed(base), {Name: "Label", Type: reflect.TypeFor[string](), Tag: `json:"b1"`},
		}),
	}
	tie = reflect.StructOf([]reflect.StructField{
		embed(base), {Name: "Other", Type: reflect.PointerTo(other), Anonymous: true},
	})
	return shadows, tie
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
			assert.Equal(t, reflect.TypeFor[string](), f.Type(), "the outer field, not ShadowBase.B1")
			assert.Len(t, f.Route(), 1)
		})
	}
}

type settledA struct {
	ID string `db:"a_id"`
}

type settledB struct {
	ID string `db:"b_id"`
}

// settledInner's two ID fields tie: encoding/json writes neither.
type settledInner struct {
	settledA
	settledB
}

// settledRow's own ID settles the name settledInner's fields tie over.
type settledRow struct {
	settledInner
	ID   string `db:"id"`
	Name string `json:"name" db:"name"`
}

// A row whose own field settles a tie inside an embedded struct is the row
// encoding/json writes; the embedded struct walked alone is ambiguous.
func TestStructFields_ARowSettlesAnEmbeddedTie(t *testing.T) {
	s, err := core.StructFieldsOf(reflect.TypeFor[settledRow](), "json", "db")
	require.NoError(t, err)
	var names []string
	for f := range s.Fields() {
		names = append(names, f.External())
	}
	data, err := json.Marshal(settledRow{ID: "outer"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"ID":"outer","name":""}`, string(data))
	assert.Equal(t, []string{"ID", "name"}, names)
	id, ok := s.External("ID")
	require.True(t, ok)
	assert.Len(t, id.Route(), 1, "the row's own ID")

	_, err = core.StructFieldsOf(reflect.TypeFor[settledInner](), "json", "db")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"ID"`)
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
// misspelt capability, an embedded struct hidden whole and an unexported one
// named whole fail the walk with an error naming the fields.
func TestStructFields_RefuseAmbiguousNamesAndUnknownCapabilities(t *testing.T) {
	cases := map[string]struct {
		typ  reflect.Type
		want string
	}{
		"a misspelt capability":           {reflect.TypeFor[walkMisspelt](), `unknown parse capability "fitler"`},
		"an embedded struct hidden whole": {reflect.TypeFor[walkWithHiddenSection](), `hide its fields one by one`},
		"an unexported embedded struct named whole": {
			reflect.TypeFor[walkWithNamedSection](),
			`which reflection cannot read; export the type or remove the name`,
		},
	}
	_, tie := shadowing()
	cases["two promoted fields at one depth"] = struct {
		typ  reflect.Type
		want string
	}{tie, `fields ShadowBase.B1 and Other.B1 are both named "b1" at one depth, so encoding/json writes neither; rename one`}
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
	_, ok := c.External("ID")
	assert.True(t, ok, "yaml names untagged fields by Go name")
}

// indexed and immutable are true, false or absent: anything else fails the
// walk naming the field and the tag, as a misspelt capability does.
func TestStructFields_ReadBooleanTagsStrictly(t *testing.T) {
	for _, tc := range []struct {
		typ                any
		indexed, immutable bool
		err                string
	}{
		{typ: struct {
			A int `indexed:"true" immutable:"true"`
		}{}, indexed: true, immutable: true},
		{typ: struct {
			A int `indexed:"false" immutable:"false"`
		}{}},
		{typ: struct{ A int }{}},
		{typ: struct {
			A int `indexed:"yes"`
		}{}, err: `field A: indexed: "yes" is neither true nor false`},
		{typ: struct {
			A int `indexed:"1"`
		}{}, err: `field A: indexed: "1" is neither true nor false`},
		{typ: struct {
			A int `immutable:"TRUE"`
		}{}, err: `field A: immutable: "TRUE" is neither true nor false`},
		{typ: struct {
			A int `immutable:""`
		}{}, err: `field A: immutable: "" is neither true nor false`},
	} {
		walk, err := core.StructFieldsOf(reflect.TypeOf(tc.typ), "json", "db")
		if tc.err != "" {
			assert.ErrorContains(t, err, tc.err)
			continue
		}
		require.NoError(t, err)
		tags := slices.Collect(walk.Fields())[0].Tags()
		assert.Equal(t, tc.indexed, tags.Indexed)
		assert.Equal(t, tc.immutable, tags.Immutable)
	}

	type misspelt struct {
		ID   string `json:"id" db:"id"`
		Slug string `json:"slug" db:"slug" parse:"filter" indexed:"yes"`
	}
	_, err := parse.NewResource[misspelt](providertest.Compile(filter.SQLiteDialect{}), "misspelts")
	assert.ErrorContains(t, err, `field Slug: indexed: "yes" is neither true nor false`)
}

// registryTypes builds n distinct struct types, as a large API declares
// resource, document and element types.
func registryTypes(n int) []reflect.Type {
	types := make([]reflect.Type, n)
	for i := range types {
		types[i] = reflect.StructOf([]reflect.StructField{
			{Name: "ID", Type: reflect.TypeFor[string](), Tag: `json:"id" db:"id"`},
			{Name: fmt.Sprintf("F%d", i), Type: reflect.TypeFor[int](), Tag: reflect.StructTag(fmt.Sprintf(`json:"f%d" parse:"filter"`, i))},
		})
	}
	return types
}

// Every walked type stays cached however many there are: a second walk of
// any type returns the walk of the first, whose fields maps are keyed by.
func TestStructFields_CacheEveryType(t *testing.T) {
	types := registryTypes(1100)
	walks := make([]*core.StructFields, len(types))
	for i, typ := range types {
		walk, err := core.StructFieldsOf(typ, "json", "registry")
		require.NoError(t, err)
		walks[i] = walk
	}
	for i, typ := range types {
		walk, err := core.StructFieldsOf(typ, "json", "registry")
		require.NoError(t, err)
		require.Same(t, walks[i], walk, "type %d", i)
	}
}

// BenchmarkStructFields walks the first and the last of more struct types
// than a small cache holds.
func BenchmarkStructFields(b *testing.B) {
	types := registryTypes(1100)
	for _, typ := range types {
		if _, err := core.StructFieldsOf(typ, "json", "bench"); err != nil {
			b.Fatal(err)
		}
	}
	for name, typ := range map[string]reflect.Type{"first type": types[0], "type 1100": types[len(types)-1]} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := core.StructFieldsOf(typ, "json", "bench"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type misspeltSection struct {
	Tags []string `json:"tags" minItems:"1"`
}

type misspeltRow struct {
	misspeltSection
	Name   string `json:"name" db:"name" filter_op:"eq" bson:"name" yaml:"name" validate:"required"`
	Secret string `json:"-" db:"secret" Patch_Ops:"replace"`
	Route  string `json:"route" path:"route"`
}

// Schema generation refuses each walked field's tag key that misspells one
// parse reads, through embedded structs and on hidden fields, naming the
// field, the key written and the key meant; the keys named as another
// library's, and the configured tags, are left alone. The walk itself
// refuses nothing.
func TestSchemaGenerator_RefusesMisspeltTagKeys(t *testing.T) {
	typ := reflect.TypeFor[misspeltRow]()
	_, err := core.NewSchemaGenerator().StructFields(typ)
	require.Error(t, err)
	for _, want := range []string{
		`field Name of core_test.misspeltRow has tag key "filter_op"; did you mean "filter_ops"?`,
		`field Secret of core_test.misspeltRow has tag key "Patch_Ops"; did you mean "patch_ops"?`,
		`field Tags of core_test.misspeltSection has tag key "minItems"; did you mean "minitems"?`,
		`field Route of core_test.misspeltRow has tag key "path"; did you mean "paths"?`,
	} {
		assert.Contains(t, err.Error(), want)
	}
	for _, foreign := range []string{`"bson"`, `"yaml"`, `"validate"`, `"db"`, `"json"`} {
		assert.NotContains(t, err.Error(), foreign)
	}

	_, err = core.NewSchemaGenerator(core.WithForeignTagKeys("filter_op"), core.WithForeignTagKeys("Patch_Ops", "minItems", "path")).StructFields(typ)
	require.NoError(t, err)
	_, err = core.NewSchemaGenerator(core.WithColumnTag("path"), core.WithForeignTagKeys("filter_op", "Patch_Ops", "minItems")).StructFields(typ)
	require.NoError(t, err, "path is the column tag")

	_, err = core.StructFieldsOf(typ, "json", "db")
	require.NoError(t, err)
}

// A walk's error names an unnamed row type without its tags, whose values,
// such as a description, are the declaration's, not the error's.
func TestStructFields_ErrorsLeaveTagValuesOut(t *testing.T) {
	const marker = "SECRET-MARKER"
	field := func(name, tag string) reflect.StructField {
		return reflect.StructField{Name: name, Type: reflect.TypeFor[string](), Tag: reflect.StructTag(tag)}
	}
	cases := map[string]struct {
		typ  reflect.Type
		want string
	}{
		"a repeated tag key": {
			reflect.StructOf([]reflect.StructField{field("A", `json:"a" description:"`+marker+`" db:"a" db:"b"`)}),
			`struct tag repeats the key "db"`,
		},
		"two fields named alike": {
			reflect.StructOf([]reflect.StructField{field("A", `json:"x" label:"`+marker+`"`), field("B", `json:"x"`)}),
			`are both named "x"`,
		},
		"a misspelt tag key": {
			reflect.StructOf([]reflect.StructField{field("A", `json:"a" description:"`+marker+`" filter_op:"eq"`)}),
			`has tag key "filter_op"; did you mean "filter_ops"?`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := core.NewSchemaGenerator().StructFields(tc.typ)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), marker)
		})
	}
}

type foldedInner struct {
	Alpha int `json:"alpha"`
}

type foldedRow struct {
	foldedInner
	Upper  int `json:"ALPHA"`
	Strict int `json:"name,case:strict"`
	Hidden int `json:"-"`
}

// A name matches by case only where no field has it exactly, the shallower
// field taking it, and never a case:strict field: as encoding/json decodes,
// in a row walk and a document walk alike.
func TestStructFields_FoldedMatch(t *testing.T) {
	row, err := core.StructFieldsOf(reflect.TypeFor[foldedRow](), core.JSONTagKey, core.DefaultColumnTagKey)
	require.NoError(t, err)
	doc, err := core.JSONFieldsOf(reflect.TypeFor[foldedRow]())
	require.NoError(t, err)
	for _, walk := range []*core.StructFields{row, doc} {
		for name, want := range map[string]string{
			"Alpha":  "ALPHA",
			"alpha":  "",
			"ALPHA":  "",
			"NAME":   "",
			"Hidden": "",
			"other":  "",
		} {
			f, ok := walk.FoldedMatch([]byte(name))
			if want == "" {
				assert.False(t, ok, name)
				continue
			}
			require.True(t, ok, name)
			assert.Equal(t, want, f.External(), name)
		}
	}
}

// Field.Tags hands out the walk's own Tags by value, which shares nothing
// with the walk only while Tags holds no reference.
func TestTags_HoldsNoReference(t *testing.T) {
	var refs []string
	var visit func(typ reflect.Type, path string)
	visit = func(typ reflect.Type, path string) {
		switch typ.Kind() {
		case reflect.Struct:
			for i := range typ.NumField() {
				f := typ.Field(i)
				visit(f.Type, path+"."+f.Name)
			}
		case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		default:
			refs = append(refs, path+" is a "+typ.Kind().String())
		}
	}
	visit(reflect.TypeFor[core.Tags](), "Tags")
	assert.Empty(t, refs, "give it a Field accessor that copies, as Enum and Bounds have")
}
