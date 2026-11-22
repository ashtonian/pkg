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

type jsonUnexportedNamedPointer struct {
	*jsonInner `json:"inner"`
}

// encoding/json writes an unexported embedded struct a tag names as a
// member, so it is a field. Reflection cannot read or write its value whole;
// its exported fields read and write through it.
func TestWithJSONFields_NamesAnUnexportedNamedEmbed(t *testing.T) {
	leaves := engine(t, gowalker.WithJSONFields("json"))
	got := fieldNames(t, leaves, reflect.TypeFor[jsonUnexportedNamed]())
	if want := jsonMembers(t, jsonUnexportedNamed{}); !slices.Equal(got, want) {
		t.Fatalf("names %q; encoding/json writes %q", got, want)
	}
	inner, _ := schemaFor[jsonUnexportedNamed](t, leaves).Root().Lookup("inner")
	if inner == nil || inner.GoField().IsExported() {
		t.Fatalf("inner is %v", inner)
	}

	schema := schemaFor[jsonUnexportedNamed](t, engine(t, gowalker.WithJSONFields("json", gowalker.NestedFields())))
	whole, err := schema.AccessName("inner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := whole.Get(jsonUnexportedNamed{}); !errors.Is(err, gowalker.ErrAccess) {
		t.Errorf("read the embed whole: %v", err)
	}
	if err := whole.Set(&jsonUnexportedNamed{}, jsonInner{}); !errors.Is(err, gowalker.ErrAccess) {
		t.Errorf("wrote the embed whole: %v", err)
	}
	err = schema.WalkValue(jsonUnexportedNamed{}, func(gowalker.ValueVisit) (gowalker.Control, error) { return gowalker.Continue, nil })
	if !errors.Is(err, gowalker.ErrAccess) {
		t.Errorf("walked into the embed: %v", err)
	}
	id, err := schema.ResolveText("inner.id")
	if err != nil {
		t.Fatal(err)
	}
	v := jsonUnexportedNamed{jsonInner: jsonInner{ID: 3}}
	if got, err := id.Get(v); err != nil || got.Interface() != 3 {
		t.Errorf("inner.id reads %v, %v", got.Interface(), err)
	}
	if err := id.Set(&v, 7); err != nil || v.ID != 7 {
		t.Errorf("inner.id wrote %d, %v", v.ID, err)
	}

	pointers := schemaFor[jsonUnexportedNamedPointer](t, engine(t, gowalker.WithJSONFields("json", gowalker.NestedFields())))
	id, err = pointers.ResolveText("inner.id")
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Set(&jsonUnexportedNamedPointer{}, 7, gowalker.WithPointerAllocation()); !errors.Is(err, gowalker.ErrAccess) {
		t.Errorf("allocated an unexported embedded pointer: %v", err)
	}
	p := jsonUnexportedNamedPointer{&jsonInner{}}
	if err := id.Set(&p, 7); err != nil || p.ID != 7 {
		t.Errorf("inner.id wrote %d through the pointer, %v", p.ID, err)
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

type jsonNamedEmbed struct {
	jsonInner `json:"bad"`
}

type jsonNamedField struct {
	Inner jsonInner `json:"bad"`
}

// ValidName sees every name a tag gives a selected field, an unexported
// embedded struct's included.
func TestWithJSONFields_ValidNameOnAnUnexportedNamedEmbed(t *testing.T) {
	errRefused := errors.New("refused")
	check := func(refuse bool) gowalker.TagOption {
		return gowalker.ValidName(func(name string) error {
			if refuse && name == "bad" {
				return errRefused
			}
			return nil
		})
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[jsonNamedEmbed](), reflect.TypeFor[jsonNamedField]()} {
		if _, err := engine(t, gowalker.WithJSONFields("json", check(true))).CompileType(typ); !errors.Is(err, errRefused) || !errors.Is(err, gowalker.ErrSchema) {
			t.Errorf("%v: the name was not refused: %v", typ, err)
		}
		if got := fieldNames(t, engine(t, gowalker.WithJSONFields("json", check(false))), typ); !slices.Equal(got, []string{"bad"}) {
			t.Errorf("%v: names %q", typ, got)
		}
	}
}

type TieOwnerA struct{ ID string }

type TieOwnerB struct{ ID string }

// TieOwner's two ID fields tie: encoding/json writes neither.
type TieOwner struct {
	TieOwnerA
	TieOwnerB
}

// TieOwnerRow's own ID settles the name TieOwner's fields tie over.
type TieOwnerRow struct {
	TieOwner
	ID string
}

type tieOwnerHolder struct {
	TieOwner
	ID string `json:"id"`
}

type tieOwnerWrapper struct {
	Config TieOwner
}

type tieBadTag struct {
	//lint:ignore SA5008 the repeated key is what compilation refuses
	X int `a:"1" a:"2"`
}

type tieOwnerOverBadTag struct {
	TieOwnerA
	TieOwnerB
	L tieBadTag
}

type tieOverBadTag struct {
	Config tieOwnerOverBadTag
}

// tieOverBadTagRow's Config shadows the conflicted struct holding a bad tag.
type tieOverBadTagRow struct {
	tieOverBadTag
	Config string
}

type okOverBadTag struct {
	Config struct{ L tieBadTag }
}

// okOverBadTagRow is tieOverBadTagRow without the conflict.
type okOverBadTagRow struct {
	okOverBadTag
	Config string
}

// tieOwnerShadowed's Config shadows tieOwnerWrapper's.
type tieOwnerShadowed struct {
	tieOwnerWrapper
	Config string
}

// A struct whose own names tie compiles inside a view that settles the name,
// as encoding/json writes that view; compiled alone, reached by a path, or
// asked for with At, it reports the tie.
func TestWithJSONFields_TieSettledByTheView(t *testing.T) {
	strict := engine(t, gowalker.WithJSONFields("json"))
	row := reflect.TypeFor[TieOwnerRow]()
	if got, want := fieldNames(t, strict, row), jsonMembers(t, TieOwnerRow{ID: "outer"}); !slices.Equal(got, want) {
		t.Fatalf("names %q; encoding/json writes %q", got, want)
	}
	schema := schemaFor[TieOwnerRow](t, strict)
	id, err := schema.AccessName("ID")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := id.Get(TieOwnerRow{ID: "outer"}); err != nil || got.Interface() != "outer" {
		t.Errorf("ID reads %v, %v", got.Interface(), err)
	}

	_, alone := strict.Compile[TieOwner]()
	if conflict, ok := errors.AsType[*gowalker.NameConflict](alone); !ok || conflict.Owner != reflect.TypeFor[TieOwner]() {
		t.Fatalf("TieOwner compiled alone: %v", alone)
	}
	var owner *gowalker.Shape
	for _, d := range schema.Root().Declarations() {
		if d.State == gowalker.InlineOwner && d.GoField.Name == "TieOwner" {
			owner = d.Field.Shape()
		}
	}
	_, err = schema.At(owner)
	if !errors.Is(err, gowalker.ErrSchema) || err.Error() != alone.Error() {
		t.Errorf("At(TieOwner) = %v, want %v", err, alone)
	}
	// Each call's error is the caller's to change.
	if e, ok := errors.AsType[*gowalker.Error](err); ok {
		e.Op = "changed"
	}
	if c, ok := errors.AsType[*gowalker.NameConflict](err); ok {
		c.Name = "changed"
	}
	if _, again := schema.At(owner); again.Error() != alone.Error() {
		t.Errorf("At(TieOwner) after a caller changed its error = %v", again)
	}
	if fields := owner.Fields(); len(fields) != 0 {
		t.Errorf("the conflicted view lists %d fields", len(fields))
	}

	// A named section of the type is on a path, even when a view reached it
	// inlined first. Sections are leaves unless NestedFields.
	nested := engine(t, gowalker.WithJSONFields("json", gowalker.NestedFields()))
	for name, typ := range map[string]reflect.Type{
		"section": reflect.StructOf([]reflect.StructField{
			{Name: "TieOwner", Type: reflect.TypeFor[TieOwner](), Anonymous: true},
			{Name: "ID", Type: reflect.TypeFor[string]()},
			{Name: "Section", Type: reflect.TypeFor[TieOwner](), Tag: `json:"section"`},
		}),
		"inlined, then a section": reflect.StructOf([]reflect.StructField{
			{Name: "A", Type: reflect.TypeFor[tieOwnerHolder](), Tag: `json:"a"`},
			{Name: "B", Type: reflect.TypeFor[*TieOwner](), Tag: `json:"b"`},
		}),
		"a section's element": reflect.StructOf([]reflect.StructField{
			{Name: "TieOwner", Type: reflect.TypeFor[TieOwner](), Anonymous: true},
			{Name: "ID", Type: reflect.TypeFor[string]()},
			{Name: "Items", Type: reflect.TypeFor[map[string][]TieOwner]()},
		}),
	} {
		if _, err := nested.CompileType(typ); !errors.Is(err, gowalker.ErrSchema) {
			t.Errorf("%s: compiled: %v", name, err)
		}
	}
	// The type of a field that lost its name is not on a path either.
	if got, want := fieldNames(t, nested, reflect.TypeFor[tieOwnerShadowed]()), jsonMembers(t, tieOwnerShadowed{}); !slices.Equal(got, want) {
		t.Errorf("names %q; encoding/json writes %q", got, want)
	}

	// A conflict leaves a struct's view out, not the checks below it: a
	// repeated tag key fails compilation with or without a conflict above it.
	for _, typ := range []reflect.Type{reflect.TypeFor[tieOverBadTagRow](), reflect.TypeFor[okOverBadTagRow]()} {
		if _, err := nested.CompileType(typ); !errors.Is(err, gowalker.ErrSchema) {
			t.Errorf("%v compiled: %v", typ, err)
		} else if _, ok := errors.AsType[*gowalker.TagError](err); !ok {
			t.Errorf("%v: %v", typ, err)
		}
	}

	// With DropTies nothing conflicts.
	dropping := schemaFor[TieOwnerRow](t, engine(t, gowalker.WithJSONFields("json"), gowalker.DropTies()))
	for _, d := range dropping.Root().Declarations() {
		if d.State != gowalker.InlineOwner || d.GoField.Name != "TieOwner" {
			continue
		}
		at, err := dropping.At(d.Field.Shape())
		if err != nil || len(at.Root().Fields()) != 0 {
			t.Errorf("At(TieOwner) with DropTies: %v", err)
		}
	}
}

type rankDerived struct{ Name int }

type rankExplicit struct {
	N int `json:"Name"`
}

type rankDeeper struct{ rankExplicit }

// Every claim to a name is ranked once: the shallowest wins, then a tag's.
// Claims at the top rank tie; strict compilation reports the first two in
// declaration order, of the name claimed first, and DropTies drops them all.
func TestResolveByDepth_RanksEachNameOnce(t *testing.T) {
	type ranked struct {
		rankDerived
		rankExplicit
		rankDeeper
		Other int
	}
	strict := engine(t, gowalker.WithJSONFields("json"))
	if got, want := fieldNames(t, strict, reflect.TypeFor[ranked]()), jsonMembers(t, ranked{}); !slices.Equal(got, want) {
		t.Errorf("names %q; encoding/json writes %q", got, want)
	}
	states := func(schema *gowalker.Schema) []string {
		var got []string
		for _, d := range schema.Root().Declarations() {
			if d.State != gowalker.InlineOwner {
				got = append(got, d.DeclaringType.Name()+"."+d.GoField.Name+":"+d.State.String())
			}
		}
		return got
	}
	want := []string{"rankDerived.Name:Shadowed", "rankExplicit.N:Named", "rankExplicit.N:Shadowed", "ranked.Other:Named"}
	if got := states(schemaFor[ranked](t, strict)); !slices.Equal(got, want) {
		t.Errorf("states %q, want %q", got, want)
	}

	field := func(name string, typ reflect.Type, tag reflect.StructTag) reflect.StructField {
		return reflect.StructField{Name: name, Type: typ, Tag: tag, Anonymous: name == typ.Name()}
	}
	contested := reflect.StructOf([]reflect.StructField{
		field("TieA", reflect.TypeFor[TieA](), ""),
		field("P1", reflect.TypeFor[int](), `json:"p"`),
		field("TieB", reflect.TypeFor[TieB](), ""),
		field("DeeperX", reflect.TypeFor[DeeperX](), ""),
		field("P2", reflect.TypeFor[int](), `json:"p"`),
		field("P3", reflect.TypeFor[int](), `json:"p"`),
		field("W", reflect.TypeFor[int](), ""),
	})
	_, err := strict.CompileType(contested)
	conflict, ok := errors.AsType[*gowalker.NameConflict](err)
	if !ok || conflict.Name != "x" || conflict.First.DeclaringType() != reflect.TypeFor[TieA]() || conflict.Second.DeclaringType() != reflect.TypeFor[TieB]() {
		t.Fatalf("first conflict: %v", err)
	}
	dropping := engine(t, gowalker.WithJSONFields("json"), gowalker.DropTies())
	if got, want := fieldNames(t, dropping, contested), jsonMembers(t, reflect.New(contested).Elem().Interface()); !slices.Equal(got, want) {
		t.Errorf("names %q; encoding/json writes %q", got, want)
	}
	schema, err := dropping.CompileType(contested)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"TieA.X:Tied", ".P1:Tied", "TieB.X:Tied", "DeepX.X:Shadowed", ".P2:Tied", ".P3:Tied", ".W:Named"}
	if got := states(schema); !slices.Equal(got, want) {
		t.Errorf("states %q, want %q", got, want)
	}
}
