//go:build unit

package core

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nameX1 and nameX2 each give X its Go name; a json tag repeated across
// embeds is a vet error.
type nameX1 struct{ X int }

type nameX2 struct{ X int }

type nameLeaf struct {
	A int `json:"a"`
	B int
}

type NameExported struct {
	C int `json:"c"`
}

type nameCount int

type nameTwiceA struct{ nameLeaf }

type nameTwiceB struct{ nameLeaf }

type (
	// Two promoted fields claim "X" at one depth: neither takes it.
	nameTie struct {
		nameX1
		nameX2
		Y int `json:"y"`
	}
	// A shallower field takes a name from a promoted one.
	nameShadow struct {
		nameLeaf
		A int `json:"a"`
	}
	// At one depth, a tagged field takes a name from an untagged one.
	nameTaggedWins struct {
		nameLeaf
		NameExported
		Other int `json:"B"`
	}
	nameHidden struct {
		nameLeaf `json:"-"`
		D        int `json:"d"`
	}
	// An unexported embedded struct named by its tag is a member.
	nameUnexportedNamed struct {
		nameLeaf `json:"leaf"`
		D        int `json:"d"`
	}
	nameEmbeds struct {
		*NameExported
		nameCount
		//lint:ignore U1000 encoding/json skips an unexported field; so must the walk
		unexported int
		//lint:ignore SA5008 "-," names a field "-"
		Dash int `json:"-,"`
		//lint:ignore SA5008 a name is cut at a reserved character
		Bad    int `json:"a\"b"`
		Omit   int `json:",omitempty"`
		Quoted int `json:"q,string"`
	}
	// One struct type embedded twice at one depth gives each of its fields
	// two claims.
	nameTwice struct {
		nameTwiceA
		nameTwiceB
		E int `json:"e"`
	}
	// A struct embedding itself contributes its fields once.
	nameSelf struct {
		*nameSelf
		F int `json:"f"`
	}
	// Two structs embedding each other, exported so encoding/json can allocate
	// the pointers it decodes through: NameMutualB.G is shadowed.
	NameMutualA struct {
		*NameMutualB
		G int `json:"g"`
	}
	NameMutualB struct {
		*NameMutualA
		G int `json:"g"`
		H int `json:"h"`
	}
)

// JSONFieldsOf names exactly the members encoding/json writes, and each
// decodes into the field its route reaches.
func TestJSONFieldsOf_NamesWhatEncodingJSONWrites(t *testing.T) {
	for _, value := range []any{
		nameTie{}, nameShadow{}, nameTaggedWins{}, nameHidden{}, nameUnexportedNamed{},
		nameEmbeds{NameExported: &NameExported{}, nameCount: 1, Omit: 1}, nameTwice{},
		nameSelf{nameSelf: &nameSelf{}}, NameMutualA{NameMutualB: &NameMutualB{}},
	} {
		typ := reflect.TypeOf(value)
		t.Run(typ.Name(), func(t *testing.T) {
			raw, err := json.Marshal(value)
			require.NoError(t, err)
			var written map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &written))

			walk, err := JSONFieldsOf(typ)
			require.NoError(t, err)
			var named []string
			for f := range walk.Fields() {
				named = append(named, f.External())
			}
			assert.ElementsMatch(t, slices.Collect(maps.Keys(written)), named)

			for f := range walk.Fields() {
				if f.Type().Kind() != reflect.Int {
					continue
				}
				var seven any = 7
				if f.Quoted() {
					seven = "7"
				}
				member, err := json.Marshal(map[string]any{f.External(): seven})
				require.NoError(t, err)
				fresh := reflect.New(typ)
				require.NoError(t, json.Unmarshal(member, fresh.Interface()))
				got, err := f.Value(fresh.Elem())
				require.NoError(t, err)
				assert.Equal(t, int64(7), got.Int(), "member %q", f.External())
			}
		})
	}
}

func TestJSONFieldsOf_IsSharedAndReadsNoRowTags(t *testing.T) {
	type doc struct {
		A int `json:"a,omitempty" min:"abc" parse:"nonsense" indexed:"maybe"`
	}
	first, err := JSONFieldsOf(reflect.TypeFor[*doc]())
	require.NoError(t, err)
	second, err := JSONFieldsOf(reflect.TypeFor[doc]())
	require.NoError(t, err)
	assert.Same(t, first, second)
	require.Equal(t, 1, first.Len())
	assert.Equal(t, Tags{FieldNameOptions: "omitempty"}, slices.Collect(first.Fields())[0].Tags())

	_, err = JSONFieldsOf(reflect.TypeFor[map[string]int]())
	assert.EqualError(t, err, "expected struct type, got map")
}

// A tag reflect cannot read whole is refused, naming the field. The type is
// built at run time, as vet refuses such a tag in source.
func TestJSONFieldsOf_RefusesUnreadableTags(t *testing.T) {
	typ := reflect.StructOf([]reflect.StructField{{Name: "Doc", Type: reflect.TypeFor[int](), Tag: `json:"doc" bson: "doc"`}})
	_, err := JSONFieldsOf(typ)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field Doc")
}

// docSelfA and docSelfB each write themselves; docCollide embeds both, so
// it inherits neither MarshalJSON and encoding/json writes it by its fields.
type docSelfA struct{ X int }

func (docSelfA) MarshalJSON() ([]byte, error) { return []byte(`"a"`), nil }

type docSelfB struct {
	Y int `json:"y"`
}

func (docSelfB) MarshalJSON() ([]byte, error) { return []byte(`"b"`), nil }

type docCollide struct {
	docSelfA
	docSelfB
	Z int `json:"z"`
}

type docOf struct {
	Own   docSelfA        `json:"own"`
	Both  docCollide      `json:"both"`
	Raw   json.RawMessage `json:"raw"`
	Sizes map[uint8]int   `json:"sizes"`
	Items []*docCollide   `json:"items"`
}

// A document's shape stops at a type that codes itself, and nowhere else:
// a struct whose embedded types' methods collide holds the members
// encoding/json writes for it, and a map's key shape reads its names.
func TestJSONDocumentOf_StopsWhereATypeCodesItself(t *testing.T) {
	root, err := JSONDocumentOf(reflect.TypeFor[docOf]())
	require.NoError(t, err)
	for _, name := range []string{"own", "raw"} {
		f, _ := root.Lookup(name)
		require.NotNil(t, f, name)
		assert.True(t, f.Shape().Atomic(), name)
	}
	both, _ := root.Lookup("both")
	require.NotNil(t, both)
	assert.False(t, both.Shape().Atomic())
	raw, err := json.Marshal(docCollide{})
	require.NoError(t, err)
	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &members))
	var names []string
	for _, f := range both.Shape().Fields() {
		names = append(names, f.Name())
	}
	assert.ElementsMatch(t, slices.Collect(maps.Keys(members)), names)

	sizes, _ := root.Lookup("sizes")
	require.NotNil(t, sizes)
	assert.True(t, sizes.Shape().Key().IsCanonicalJSONKey([]byte("7")))
	assert.False(t, sizes.Shape().Key().IsCanonicalJSONKey([]byte("07")))
	items, _ := root.Lookup("items")
	require.NotNil(t, items)
	assert.Same(t, both.Shape(), items.Shape().Elem().Elem(), "one node per type")

	again, err := JSONDocumentOf(reflect.TypeFor[docOf]())
	require.NoError(t, err)
	assert.Same(t, root, again, "compiled once")

	// JSONFieldsOf still names a self-coding type's fields.
	fields, err := JSONFieldsOf(reflect.TypeFor[docSelfB]())
	require.NoError(t, err)
	_, ok := fields.External("y")
	assert.True(t, ok)
}
