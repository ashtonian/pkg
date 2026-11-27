package gowalker_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

func TestMisspeltTagKey(t *testing.T) {
	known := []string{"default", "validate", "env", "envref", "filter_ops", "minitems", "maxitems"}
	for _, tc := range []struct {
		written string
		known   []string
		meant   string
	}{
		{"defualt", known, "default"},  // swapped
		{"defaul", known, "default"},   // deleted
		{"defaults", known, "default"}, // inserted
		{"dafault", known, "default"},  // replaced
		{"vaildate", known, "validate"},
		{"Default", known, "default"},
		{"DEFAULT", known, "default"},
		{"env_ref", known, "envref"},
		{"env-Ref", known, "envref"},
		{"filterOps", known, "filter_ops"},
		{"filter-ops", known, "filter_ops"},
		{"filter_op", known, "filter_ops"},
		{"minItems", known, "minitems"},
		// Equal under folding beats one edit, wherever the keys are listed.
		{"ABC", []string{"abd", "abc_"}, "abc_"},
		{"default", known, ""},
		{"filter_ops", known, ""},
		{"json", known, ""},
		{"yaml", known, ""},
		{"dfeualt", known, ""}, // two edits
		{"cba", []string{"abc"}, ""},
		{"badc", []string{"abcd"}, ""},
		{"a", []string{"abc"}, ""},
		{"x", nil, ""},
	} {
		meant, ok := gowalker.MisspeltTagKey(tc.written, tc.known)
		if meant != tc.meant || ok != (tc.meant != "") {
			t.Errorf("MisspeltTagKey(%q, %q) = %q, %v; want %q", tc.written, tc.known, meant, ok, tc.meant)
		}
	}
}

type spellEmbedded struct {
	Port int `defualt:"80"`
}

type spellRow struct {
	spellEmbedded
	Name  string `json:"name" defualt:"x" validte:"required"`
	Notes string `json:"-" defaults:"y"`
	//lint:ignore U1000 reflection fixture for a private field
	secret string `defualt:"z"`
	Clean  string `default:"ok" bson:"ok"`
}

type spellClean struct {
	Name string `json:"name" default:"x" validate:"required"`
}

// The sweep holds every declaration to the known keys, inlined, skipped and
// private ones included, and leaves choosing among them to the caller.
func TestShapeMisspeltTagKeys(t *testing.T) {
	known := []string{"default", "validate"}
	e := engine(t, gowalker.WithJSONFields("json"))
	type found struct {
		field     string
		state     gowalker.DeclarationState
		declaring reflect.Type
		written   string
		meant     string
	}
	var got []found
	for _, m := range schemaFor[spellRow](t, e).Root().MisspeltTagKeys(known) {
		got = append(got, found{m.GoField.Name, m.State, m.DeclaringType, m.Written, m.Meant})
	}
	embedded, row := reflect.TypeFor[spellEmbedded](), reflect.TypeFor[spellRow]()
	assert.Equal(t, []found{
		{"Port", gowalker.Named, embedded, "defualt", "default"},
		{"Name", gowalker.Named, row, "defualt", "default"},
		{"Name", gowalker.Named, row, "validte", "validate"},
		{"Notes", gowalker.Skipped, row, "defaults", "default"},
		{"secret", gowalker.Skipped, row, "defualt", "default"},
	}, got)

	assert.Equal(t, []gowalker.TagMisspelling(nil), schemaFor[spellClean](t, e).Root().MisspeltTagKeys(known))
}

// A returned declaration is the caller's copy, as Declarations returns it.
func TestShapeMisspeltTagKeysCopies(t *testing.T) {
	s := schemaFor[spellRow](t, engine(t, gowalker.WithJSONFields("json")))
	first := s.Root().MisspeltTagKeys([]string{"default"})
	first[0].Index[0] = 99
	first[0].InlineParents[0].Name = "changed"
	again := s.Root().MisspeltTagKeys([]string{"default"})
	assert.Equal(t, []int{0, 0}, again[0].Index)
	assert.Equal(t, "spellEmbedded", again[0].InlineParents[0].Name)
	assert.Equal(t, []string{"defualt"}, slices.Collect(again[0].TagKeys()))
}

// One edit is symmetric, at either end and in the middle.
func TestMisspeltTagKey_OneEdit(t *testing.T) {
	for _, pair := range [][2]string{
		{"abc", "abd"}, {"abc", "xbc"}, {"abc", "ab"}, {"abc", "bc"}, {"abc", "abcd"},
		{"abc", "bac"}, {"abc", "acb"}, {"ab", "ba"}, {"", "a"},
	} {
		for _, p := range [][2]string{pair, {pair[1], pair[0]}} {
			if meant, ok := gowalker.MisspeltTagKey(p[0], []string{p[1]}); !ok || meant != p[1] {
				t.Errorf("%q is not one edit from %q", p[0], p[1])
			}
		}
	}
}
