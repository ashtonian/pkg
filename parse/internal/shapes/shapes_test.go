//go:build unit

package shapes

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selfA and selfB each write themselves; collide embeds both, so it
// inherits neither MarshalJSON and encoding/json writes it by its fields.
type selfA struct{ X int }

func (selfA) MarshalJSON() ([]byte, error) { return []byte(`"a"`), nil }

type selfB struct {
	Y int `json:"y"`
}

func (selfB) MarshalJSON() ([]byte, error) { return []byte(`"b"`), nil }

type collide struct {
	selfA
	selfB
	Z int `json:"z"`
}

type document struct {
	Own   selfA           `json:"own"`
	Both  collide         `json:"both"`
	Raw   json.RawMessage `json:"raw"`
	Sizes map[uint8]int   `json:"sizes"`
	Items []*collide      `json:"items"`
}

// A document's shape stops at a type that codes itself, and nowhere else:
// a struct whose embedded types' methods collide holds the members
// encoding/json writes for it, and a map's key shape reads its names.
func TestDocument_StopsWhereATypeCodesItself(t *testing.T) {
	root, err := Document(reflect.TypeFor[document]())
	require.NoError(t, err)
	for _, name := range []string{"own", "raw"} {
		f, _ := root.Lookup(name)
		require.NotNil(t, f, name)
		assert.True(t, f.Shape().Atomic(), name)
	}
	both, _ := root.Lookup("both")
	require.NotNil(t, both)
	assert.False(t, both.Shape().Atomic())
	raw, err := json.Marshal(collide{})
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

	again, err := Document(reflect.TypeFor[document]())
	require.NoError(t, err)
	assert.Same(t, root, again, "compiled once")
}

type tied struct {
	tiedA
	tiedB
	N int `json:"n"`
}

type tiedA struct{ ID int }

type tiedB struct{ ID int }

// A name two fields tie for at one depth is dropped, as encoding/json drops
// it, and a tag reflect cannot read whole is refused with its type named.
// The second type is built at run time, as vet refuses such a tag in
// source.
func TestDocument_DropsTiesAndRefusesUnreadableTags(t *testing.T) {
	root, err := Document(reflect.TypeFor[tied]())
	require.NoError(t, err)
	var names []string
	for _, f := range root.Fields() {
		names = append(names, f.Name())
	}
	assert.Equal(t, []string{"n"}, names)
	raw, err := json.Marshal(tied{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"n":0}`, string(raw))

	unreadable := reflect.StructOf([]reflect.StructField{{Name: "Doc", Type: reflect.TypeFor[int](), Tag: `json:"doc" bson: "doc"`}})
	_, err = Document(unreadable)
	require.ErrorContains(t, err, "struct { Doc int }: ")
	require.ErrorContains(t, err, "field Doc")
}
