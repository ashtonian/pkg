package gowalker_test

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

// jsonMembers is the member names json.Marshal writes for v, sorted.
func jsonMembers(t testing.TB, v any) []string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	return slices.Sorted(maps.Keys(members))
}

// fieldNames is the names e gives typ's fields, sorted.
func fieldNames(t testing.TB, e *gowalker.Engine, typ reflect.Type) []string {
	t.Helper()
	schema, err := e.CompileType(typ)
	if err != nil {
		t.Fatalf("%v: %v", typ, err)
	}
	var names []string
	for _, f := range schema.Root().Fields() {
		names = append(names, f.Name())
	}
	slices.Sort(names)
	return names
}

// oneField is a struct with one int field V under tag.
func oneField(tag reflect.StructTag) reflect.Type {
	return reflect.StructOf([]reflect.StructField{{Name: "V", Type: reflect.TypeFor[int](), Tag: tag}})
}

// oneValue is a oneField value with V set, so omitempty and omitzero keep
// the member.
func oneValue(typ reflect.Type) any {
	v := reflect.New(typ).Elem()
	v.Field(0).SetInt(1)
	return v.Interface()
}

// The name a json tag gives is the one json.Marshal writes, for every tag
// value up to four characters over the characters that end a name, start
// an identifier or are neither.
func TestWithJSONFields_NamesAsJSONWrites(t *testing.T) {
	e := engine(t, gowalker.WithJSONFields("json"))
	alphabet := []string{"a", "_", "1", " ", ",", "\\", "'", "\"", "`", "-", "é", "\xff"}
	values, level := []string{""}, []string{""}
	for range 4 {
		var next []string
		for _, prefix := range level {
			for _, c := range alphabet {
				next = append(next, prefix+c)
			}
		}
		values, level = append(values, next...), next
	}
	for _, value := range values {
		typ := oneField(reflect.StructTag("json:" + strconv.Quote(value)))
		if value == "-" {
			if got := fieldNames(t, e, typ); len(got) != 0 {
				t.Errorf("json:%q names %q; encoding/json skips the field", value, got)
			}
			continue
		}
		want := jsonMembers(t, oneValue(typ))
		if got := fieldNames(t, e, typ); !slices.Equal(got, want) {
			t.Errorf("json:%q names %q; encoding/json writes %q", value, got, want)
		}
	}
}

// The tags where cutting at the first comma, as WithTaggedFields does, names
// a member encoding/json does not write.
func TestWithJSONFields_ReservedCharacters(t *testing.T) {
	e := engine(t, gowalker.WithJSONFields("json"))
	for value, want := range map[string]string{
		`a"b`:       "a",
		`a\b`:       "a",
		`a'b`:       "a",
		`'a b'`:     "V",
		`"x"`:       "V",
		"a b":       "a b",
		"é":         "é",
		"1x":        "1x",
		"-,":        "-",
		" a":        " a",
		",omitzero": "V",
		"a-b":       "a-b",
		"$ref":      "$ref",
		"a!b":       "a!b",
		// Options never change which members exist.
		"a,omitempty":   "a",
		"a,string":      "a",
		"a,inline":      "a",
		"a,unknown":     "a",
		"a,format:x":    "a",
		"a,case:ignore": "a",
		",inline":       "V",
	} {
		got := fieldNames(t, e, oneField(reflect.StructTag("json:"+strconv.Quote(value))))
		if !slices.Equal(got, []string{want}) {
			t.Errorf("json:%q names %q, want %q", value, got, want)
		}
	}
}

// Any tag value names the member json.Marshal writes. The seeds are the
// shapes where the grammar matters and the options after a name.
func FuzzWithJSONFields(f *testing.F) {
	for _, seed := range []string{"a", `a"b`, `a\b`, "a'b", "'a b'", `"x"`, "a b", "-,", ",omitempty", "a,string", "a,inline", "a,format:RFC3339", "a,case:ignore", "\xff", "é,unknown"} {
		f.Add(seed)
	}
	e := engine(f, gowalker.WithJSONFields("json"))
	f.Fuzz(func(t *testing.T, value string) {
		typ := oneField(reflect.StructTag("json:" + strconv.Quote(value)))
		if _, err := json.Marshal(oneValue(typ)); err != nil {
			t.Skipf("encoding/json refuses json:%q: %v", value, err)
		}
		want := jsonMembers(t, oneValue(typ))
		if got := fieldNames(t, e, typ); !slices.Equal(got, want) {
			t.Errorf("json:%q names %q; encoding/json writes %q", value, got, want)
		}
	})
}

// A key other than json follows encoding/json's rules under that key.
func TestWithJSONFields_OtherKey(t *testing.T) {
	type row struct {
		A int `api:"a\"b"`
		B int `json:"ignored"`
	}
	got := fieldNames(t, engine(t, gowalker.WithJSONFields("api")), reflect.TypeFor[row]())
	if want := []string{"B", "a"}; !slices.Equal(got, want) {
		t.Errorf("names %q, want %q", got, want)
	}
}

type jsonUnexportedNamed struct {
	jsonInner `json:"inner"`
	Note      string
}

// encoding/json writes an unexported embedded struct a tag names, which
// safe reflection cannot read: compilation refuses it rather than leave a
// member out.
func TestWithJSONFields_RefusesAnUnexportedNamedEmbed(t *testing.T) {
	_, err := engine(t, gowalker.WithJSONFields("json")).Compile[jsonUnexportedNamed]()
	if !errors.Is(err, gowalker.ErrSchema) || !strings.Contains(err.Error(), "jsonInner") {
		t.Fatalf("compiled: %v", err)
	}
	if got := jsonMembers(t, jsonUnexportedNamed{}); !slices.Contains(got, "inner") {
		t.Fatalf("encoding/json no longer writes the embed: %q", got)
	}
}

// The conventions the option fixes cannot be changed; the others apply.
func TestWithJSONFields_Options(t *testing.T) {
	for name, option := range map[string]gowalker.TagOption{
		"UntaggedName":    gowalker.UntaggedName(strings.ToLower),
		"PromoteEmbedded": gowalker.PromoteEmbedded(),
		"DashCommaName":   gowalker.DashCommaName(),
		"nil":             nil,
	} {
		if _, err := gowalker.New(gowalker.WithJSONFields("json", option)); !errors.Is(err, gowalker.ErrSchema) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := gowalker.New(gowalker.WithJSONFields("")); !errors.Is(err, gowalker.ErrSchema) {
		t.Errorf("empty key: %v", err)
	}
	errRefused := errors.New("refused")
	refuse := gowalker.ValidName(func(name string) error {
		if name == "name" {
			return errRefused
		}
		return nil
	})
	if _, err := engine(t, gowalker.WithJSONFields("json", refuse)).Compile[jsonBase](); !errors.Is(err, errRefused) {
		t.Errorf("ValidName did not apply: %v", err)
	}
	type nested struct {
		Base jsonBase `json:"base"`
	}
	schema := schemaFor[nested](t, engine(t, gowalker.WithJSONFields("json", gowalker.NestedFields())))
	if _, err := schema.ResolveText("base.name"); err != nil {
		t.Errorf("NestedFields did not apply: %v", err)
	}
}

// With DropTies a tie drops every field claiming the name, as encoding/json
// drops them; without it the tie fails compilation.
type DeepX struct {
	X int `json:"x"`
}

type DeeperX struct{ DeepX }

// TieA.X and TieB.X tie at depth 1, and DeeperX.DeepX.X, deeper, loses to
// the tie too. reflect builds the type: go vet refuses a struct declaring
// one json name twice.
var deepTieType = reflect.StructOf([]reflect.StructField{
	{Name: "TieA", Type: reflect.TypeFor[TieA](), Anonymous: true},
	{Name: "TieB", Type: reflect.TypeFor[TieB](), Anonymous: true},
	{Name: "DeeperX", Type: reflect.TypeFor[DeeperX](), Anonymous: true},
	{Name: "Y", Type: reflect.TypeFor[int]()},
})

func TestDropTies(t *testing.T) {
	tie := deepTieType
	dropping := engine(t, gowalker.WithJSONFields("json"), gowalker.DropTies())
	for _, typ := range []reflect.Type{tieType, tie} {
		want := jsonMembers(t, reflect.New(typ).Elem().Interface())
		if got := fieldNames(t, dropping, typ); !slices.Equal(got, want) {
			t.Errorf("%v: names %q; encoding/json writes %q", typ, got, want)
		}
	}
	if _, err := engine(t, gowalker.WithJSONFields("json")).CompileType(tie); !errors.Is(err, gowalker.ErrSchema) {
		t.Errorf("a tie compiled without DropTies: %v", err)
	}
	byDepth := engine(t, gowalker.WithTaggedFields("json", gowalker.UntaggedName(strings.ToLower), gowalker.PromoteEmbedded()), gowalker.ResolveByDepth(), gowalker.DropTies())
	if got := fieldNames(t, byDepth, tie); !slices.Equal(got, []string{"y"}) {
		t.Errorf("ResolveByDepth with DropTies names %q", got)
	}
	if _, err := gowalker.New(gowalker.DropTies()); !errors.Is(err, gowalker.ErrSchema) {
		t.Errorf("DropTies without name resolution: %v", err)
	}
}
