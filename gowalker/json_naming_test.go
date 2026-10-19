package gowalker_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

// jsonNaming is encoding/json's field naming: Go names for untagged fields,
// promoted embedded structs, "-," as a name, collisions settled by depth.
func jsonNaming(t testing.TB) *gowalker.Engine {
	t.Helper()
	return engine(t, gowalker.WithTaggedFields("json",
		gowalker.UntaggedName(func(goName string) string { return goName }),
		gowalker.PromoteEmbedded(),
		gowalker.DashCommaName(),
		gowalker.ResolveByDepth(),
	))
}

type jsonBase struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// jsonShadow's Name is shallower than jsonBase.Name.
type jsonShadow struct {
	jsonBase
	Name string `json:"name"`
}

// jsonRenamed's Label takes "name" from a different Go field.
type jsonRenamed struct {
	jsonBase
	Label string `json:"name"`
}

type jsonMiddle struct{ jsonBase }

// jsonDeep shadows a field promoted from depth 2.
type jsonDeep struct {
	jsonMiddle
	Enabled bool `json:"enabled"`
}

type jsonPointer struct {
	*jsonBase
	Name string `json:"name"`
}

type jsonInner struct {
	ID   int `json:"id"`
	Note string
}

// jsonUnexported promotes an unexported struct type's fields.
type jsonUnexported struct {
	jsonInner
	Note string
}

type jsonUntagged struct{ Kind string }

type jsonTaggedKind struct {
	Other string `json:"Kind"`
}

// jsonTaggedWins: at equal depth the tagged "Kind" beats the untagged one.
type jsonTaggedWins struct {
	jsonUntagged
	jsonTaggedKind
}

type jsonDash struct {
	//lint:ignore SA5008 the "-" name is what this test is about
	Comma     int `json:"-,"`
	Excluded  int `json:"-"`
	Untouched int
}

type jsonDashOptions struct {
	//lint:ignore SA5008 the "-" name is what this test is about
	Options int `json:"-,omitempty"`
}

// The fields are the members json.Marshal writes, and each reads the value
// written under its name.
func TestJSONNaming_TheMembersJSONWrites(t *testing.T) {
	e := jsonNaming(t)
	for _, value := range []any{
		jsonShadow{jsonBase{"inner", true}, "outer"},
		jsonRenamed{jsonBase{"inner", true}, "label"},
		jsonDeep{jsonMiddle{jsonBase{"deep", true}}, false},
		jsonPointer{&jsonBase{"inner", true}, "outer"},
		jsonUnexported{jsonInner{1, "inner"}, "outer"},
		jsonTaggedWins{jsonUntagged{"untagged"}, jsonTaggedKind{"tagged"}},
		jsonDash{Comma: 1, Excluded: 3, Untouched: 4},
		jsonDashOptions{Options: 2},
	} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var members map[string]json.RawMessage
			if err := json.Unmarshal(data, &members); err != nil {
				t.Fatal(err)
			}
			schema, err := e.CompileType(reflect.TypeOf(value))
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, f := range schema.Root().Fields() {
				names = append(names, f.Name())
				accessor, err := schema.AccessName(f.Name())
				if err != nil {
					t.Fatal(err)
				}
				got, err := accessor.Get(value)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(got.Interface())
				if err != nil {
					t.Fatal(err)
				}
				if string(encoded) != string(members[f.Name()]) {
					t.Errorf("%s reads %s; encoding/json writes %s", f.Name(), encoded, members[f.Name()])
				}
			}
			slices.Sort(names)
			if want := slices.Sorted(maps.Keys(members)); !slices.Equal(names, want) {
				t.Errorf("fields %v; encoding/json writes %v", names, want)
			}
		})
	}
}

// TieA and TieB tie at depth 1 in tieType, which reflect builds: go vet
// refuses a struct declaring two fields one json name.
type TieA struct {
	X int `json:"x"`
}

type TieB struct {
	X int `json:"x"`
}

var tieType = reflect.StructOf([]reflect.StructField{
	{Name: "TieA", Type: reflect.TypeFor[TieA](), Anonymous: true},
	{Name: "TieB", Type: reflect.TypeFor[TieB](), Anonymous: true},
})

type jsonUntaggedA struct{ Y int }
type jsonUntaggedB struct{ Y int }

type jsonUntaggedTie struct {
	jsonUntaggedA
	jsonUntaggedB
}

// A tie encoding/json resolves by dropping both fields fails compilation,
// naming both.
func TestJSONNaming_TiesFail(t *testing.T) {
	e := jsonNaming(t)
	for _, tc := range []struct {
		typ           reflect.Type
		name          string
		first, second string
	}{
		{tieType, "x", "TieA.X", "TieB.X"},
		{reflect.TypeFor[jsonUntaggedTie](), "Y", "jsonUntaggedA.Y", "jsonUntaggedB.Y"},
	} {
		_, err := e.CompileType(tc.typ)
		if !errors.Is(err, gowalker.ErrSchema) {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		conflict, ok := errors.AsType[*gowalker.NameConflict](err)
		if !ok || conflict.Name != tc.name {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		want := fmt.Sprintf("fields %s and %s of %s both use the name %q", tc.first, tc.second, tc.typ, tc.name)
		if conflict.Error() != want {
			t.Errorf("%s: %q, want %q", tc.typ, conflict.Error(), want)
		}
	}
}

// A shadowed field stays in its declaring struct's declared view.
func TestJSONNaming_ShadowedFieldStaysDeclared(t *testing.T) {
	schema := schemaFor[jsonShadow](t, jsonNaming(t))
	name, _ := schema.Root().Lookup("name")
	if name == nil || name.GoField().Name != "Name" || len(name.Index()) != 1 {
		t.Fatalf("name is %v", name)
	}
	var inner bool
	for _, f := range schema.Root().DeclaredView() {
		if f.Inline() {
			for _, g := range f.Shape().DeclaredView() {
				inner = inner || g.Name() == "name"
			}
		}
	}
	if !inner {
		t.Error("jsonBase.Name left the declared view")
	}
}

// Without the options, the default convention is unchanged: "-," excludes
// and a promoted collision fails.
func TestJSONNaming_OptionsAreOptIn(t *testing.T) {
	plain := engine(t, gowalker.WithTaggedFields("json",
		gowalker.UntaggedName(func(goName string) string { return goName }),
		gowalker.PromoteEmbedded(),
	))
	dash := schemaFor[jsonDash](t, plain)
	if got := len(dash.Root().Fields()); got != 1 {
		t.Errorf("%d fields; only Untouched is selected", got)
	}
	if _, err := plain.CompileType(reflect.TypeFor[jsonShadow]()); !errors.Is(err, gowalker.ErrSchema) {
		t.Errorf("a promoted collision compiled: %v", err)
	}
}

func TestJSONNaming_InvalidOptions(t *testing.T) {
	for name, options := range map[string][]gowalker.TagOption{
		"by depth without promotion": {gowalker.ResolveByDepth()},
		"nil name check":             {gowalker.ValidName(nil)},
	} {
		if _, err := gowalker.New(gowalker.WithTaggedFields("json", options...)); !errors.Is(err, gowalker.ErrSchema) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

type jsonQuoted struct {
	Name string `json:"'name'"`
}

// ValidName refuses a tag name before the field is selected.
func TestValidName_RefusesATagName(t *testing.T) {
	errQuoted := errors.New("quoted")
	e := engine(t, gowalker.WithTaggedFields("json",
		gowalker.UntaggedName(func(goName string) string { return goName }),
		gowalker.ValidName(func(name string) error {
			if strings.ContainsAny(name, `\'"`+"`") {
				return errQuoted
			}
			return nil
		}),
	))
	_, err := e.CompileType(reflect.TypeFor[jsonQuoted]())
	if !errors.Is(err, gowalker.ErrSchema) || !errors.Is(err, errQuoted) {
		t.Fatalf("compiled with a refused name: %v", err)
	}
	if _, err := e.CompileType(reflect.TypeFor[jsonBase]()); err != nil {
		t.Fatal(err)
	}
}
