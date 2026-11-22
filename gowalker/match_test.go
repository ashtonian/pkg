package gowalker_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ashtonian/pkg/gowalker"
)

type matchUpperFirst struct {
	Upper int `json:"K"`
	Lower int `json:"k"`
}

type matchLowerFirst struct {
	Lower int `json:"k"`
	Upper int `json:"K"`
}

// matchOrbits names fields by runes whose simple-fold orbits hold more than
// two runes, or none beside the rune itself.
type matchOrbits struct {
	S      int `json:"s"`
	Sigma  int `json:"σ"`
	Dz     int `json:"ǆ"`
	Dotted int `json:"İ"`
	Sharp  int `json:"ß"`
}

type matchInner struct {
	Alpha int `json:"alpha"`
	Beta  int `json:"beta"`
	Delta int `json:"delta"`
	Zeta  int `json:"zeta"`
}

type MatchPointerInner struct {
	Gamma int `json:"gamma"`
	Zeta  int `json:"ZETA"`
}

// matchPromoted has promoted names that fold like shallower fields' declared
// after them, a promoted field its own field shadows, and two promoted names
// at one depth that fold alike.
type matchPromoted struct {
	matchInner
	*MatchPointerInner
	ALPHA int `json:"ALPHA"`
	Beta  int `json:"beta"`
	GAMMA int `json:"GAMMA"`
}

type matchPromotedLast struct {
	ALPHA int `json:"ALPHA"`
	matchInner
}

type matchTieA struct{ ID int }

type matchTieB struct{ ID int }

// matchTied drops the tied ID fields, so a member spelled ID folds to id.
type matchTied struct {
	matchTieA
	matchTieB
	Lower int `json:"id"`
}

type matchStrict struct {
	Name  int `json:"name,case:strict"`
	Upper int `json:"NAME"`
	Other int `json:"other,case:ignore"`
	Last  int `json:"last,omitempty,case:strict"`
}

// decodedField is the field of shape, typ's, that encoding/json decodes a
// member named name into, or nil.
func decodedField(t testing.TB, shape *gowalker.Shape, typ reflect.Type, name string) *gowalker.Field {
	t.Helper()
	doc, err := json.Marshal(map[string]int{name: 1})
	if err != nil {
		t.Fatal(err)
	}
	v := reflect.New(typ).Elem()
	if err := json.Unmarshal(doc, v.Addr().Interface()); err != nil {
		t.Fatalf("%s: %v", doc, err)
	}
	for _, f := range shape.Fields() {
		if fv, err := v.FieldByIndexErr(f.Index()); err == nil && fv.Int() == 1 {
			return f
		}
	}
	return nil
}

// checkMatch compares shape's Match of name, and MatchBytes, with the field
// encoding/json decodes the member into.
func checkMatch(t testing.TB, shape *gowalker.Shape, typ reflect.Type, name string) {
	t.Helper()
	want, wantMatch := decodedField(t, shape, typ, name), gowalker.NoMatch
	switch {
	case want != nil && want.Name() == name:
		wantMatch = gowalker.ExactMatch
	case want != nil:
		wantMatch = gowalker.FoldedMatch
	}
	if got, match := shape.Match(name); got != want || match != wantMatch {
		t.Errorf("%v: Match(%q) = %s %s, want %s %s", typ, name, goName(got), match, goName(want), wantMatch)
	}
	if got, match := shape.MatchBytes([]byte(name)); got != want || match != wantMatch {
		t.Errorf("%v: MatchBytes(%q) = %s %s, want %s %s", typ, name, goName(got), match, goName(want), wantMatch)
	}
}

// goName is the route of f's Go field names, or "" for nil.
func goName(f *gowalker.Field) string {
	if f == nil {
		return ""
	}
	var route []string
	for _, parent := range f.InlineParents() {
		route = append(route, parent.Name)
	}
	return strings.Join(append(route, f.GoField().Name), ".")
}

var matchTypes = []reflect.Type{
	reflect.TypeFor[matchUpperFirst](),
	reflect.TypeFor[matchLowerFirst](),
	reflect.TypeFor[matchOrbits](),
	reflect.TypeFor[matchPromoted](),
	reflect.TypeFor[matchPromotedLast](),
	reflect.TypeFor[matchTied](),
	reflect.TypeFor[matchStrict](),
}

// matchProbes are names whose folds relate them to the fixtures' names in
// ways lowercasing does not: the Kelvin sign, long s, final sigma, a
// titlecase digraph, dotted and dotless i, capital sharp s.
var matchProbes = []string{
	"K", "k", "\u212a", "s", "S", "\u017f", "Σ", "σ", "ς", "Ǆ", "ǅ", "ǆ",
	"İ", "i", "I", "ı", "ß", "ẞ", "SS", "alpha", "Alpha", "ALPHA", "aLPHA",
	"beta", "BETA", "gamma", "Gamma", "GAMMA", "Delta", "Zeta", "id", "Id", "ID",
	"Name", "NAME", "OTHER", "LAST", "unknown", "",
}

func TestShapeMatch_AgreesWithEncodingJSON(t *testing.T) {
	e := engine(t, gowalker.WithJSONFields("json"), gowalker.DropTies())
	for _, typ := range matchTypes {
		schema, err := e.CompileType(typ)
		if err != nil {
			t.Fatalf("%v: %v", typ, err)
		}
		names := append([]string(nil), matchProbes...)
		for _, f := range schema.Root().Fields() {
			names = append(names, f.Name(), strings.ToUpper(f.Name()), strings.ToLower(f.Name()))
		}
		for _, name := range names {
			checkMatch(t, schema.Root(), typ, name)
		}
	}
}

func TestShapeMatch_SpellingsOfOneName(t *testing.T) {
	for _, tt := range []struct {
		name  string
		typ   reflect.Type
		probe string
		field string
		match gowalker.NameMatch
	}{
		{"upper exact", reflect.TypeFor[matchUpperFirst](), "K", "Upper", gowalker.ExactMatch},
		{"lower exact", reflect.TypeFor[matchUpperFirst](), "k", "Lower", gowalker.ExactMatch},
		{"kelvin folds to the first declared", reflect.TypeFor[matchUpperFirst](), "\u212a", "Upper", gowalker.FoldedMatch},
		{"kelvin folds to the first declared, lower first", reflect.TypeFor[matchLowerFirst](), "\u212a", "Lower", gowalker.FoldedMatch},
		{"long s", reflect.TypeFor[matchOrbits](), "\u017f", "S", gowalker.FoldedMatch},
		{"final sigma", reflect.TypeFor[matchOrbits](), "ς", "Sigma", gowalker.FoldedMatch},
		{"titlecase digraph", reflect.TypeFor[matchOrbits](), "ǅ", "Dz", gowalker.FoldedMatch},
		{"capital sharp s", reflect.TypeFor[matchOrbits](), "ẞ", "Sharp", gowalker.FoldedMatch},
		{"sharp s is not ss", reflect.TypeFor[matchOrbits](), "SS", "", gowalker.NoMatch},
		{"dotted capital i is not i", reflect.TypeFor[matchOrbits](), "i", "", gowalker.NoMatch},
		{"a shallower field declared after a promoted one", reflect.TypeFor[matchPromoted](), "Alpha", "ALPHA", gowalker.FoldedMatch},
		{"a shallower field declared first", reflect.TypeFor[matchPromotedLast](), "Alpha", "ALPHA", gowalker.FoldedMatch},
		{"through a pointer embed, below a shallower field", reflect.TypeFor[matchPromoted](), "Gamma", "GAMMA", gowalker.FoldedMatch},
		{"a promoted field nothing shallower folds like", reflect.TypeFor[matchPromoted](), "DELTA", "matchInner.Delta", gowalker.FoldedMatch},
		{"promoted at one depth, the first declared", reflect.TypeFor[matchPromoted](), "Zeta", "matchInner.Zeta", gowalker.FoldedMatch},
		{"shadowing field", reflect.TypeFor[matchPromoted](), "BETA", "Beta", gowalker.FoldedMatch},
		{"tied fields fold nothing", reflect.TypeFor[matchTied](), "ID", "Lower", gowalker.FoldedMatch},
		{"case:strict exact", reflect.TypeFor[matchStrict](), "name", "Name", gowalker.ExactMatch},
		{"case:strict passes its fold on", reflect.TypeFor[matchStrict](), "Name", "Upper", gowalker.FoldedMatch},
		{"case:strict alone folds nothing", reflect.TypeFor[matchStrict](), "LAST", "", gowalker.NoMatch},
		{"case:ignore folds", reflect.TypeFor[matchStrict](), "OTHER", "Other", gowalker.FoldedMatch},
		{"unknown", reflect.TypeFor[matchPromoted](), "epsilon", "", gowalker.NoMatch},
		{"longer than any fold", reflect.TypeFor[matchPromoted](), strings.Repeat("alpha", 200), "", gowalker.NoMatch},
	} {
		t.Run(tt.name, func(t *testing.T) {
			schema, err := engine(t, gowalker.WithJSONFields("json"), gowalker.DropTies()).CompileType(tt.typ)
			if err != nil {
				t.Fatal(err)
			}
			f, match := schema.Root().Match(tt.probe)
			if got := goName(f); got != tt.field || match != tt.match {
				t.Errorf("Match(%q) = %q %s, want %q %s", tt.probe, got, match, tt.field, tt.match)
			}
		})
	}
}

// Match agrees with encoding/json on any member name; seeds run without
// -fuzz.
func FuzzShapeMatch(f *testing.F) {
	for _, probe := range matchProbes {
		f.Add(probe)
	}
	e := engine(f, gowalker.WithJSONFields("json"), gowalker.DropTies())
	shapes := make([]*gowalker.Shape, len(matchTypes))
	for i, typ := range matchTypes {
		schema, err := e.CompileType(typ)
		if err != nil {
			f.Fatal(err)
		}
		shapes[i] = schema.Root()
	}
	f.Fuzz(func(t *testing.T, name string) {
		// encoding/json replaces invalid UTF-8 before it matches, so it
		// would match another name than the one given.
		if !utf8.ValidString(name) {
			return
		}
		for i, typ := range matchTypes {
			checkMatch(t, shapes[i], typ, name)
		}
	})
}

type matchProbe struct {
	probe string
	field string
	match gowalker.NameMatch
}

func checkProbes(t *testing.T, shape *gowalker.Shape, probes []matchProbe) {
	t.Helper()
	for _, tt := range probes {
		f, match := shape.Match(tt.probe)
		if got := goName(f); got != tt.field || match != tt.match {
			t.Errorf("Match(%q) = %q %s, want %q %s", tt.probe, got, match, tt.field, tt.match)
		}
	}
}

type matchAliased struct {
	A int `db:"a"`
	B int `db:"k"`
}

// aliasK gives the field named a the alias K, which folds like k.
func aliasK(sf reflect.StructField) (gowalker.FieldRule, error) {
	rule := gowalker.FieldRule{Name: sf.Tag.Get("db")}
	if rule.Name == "a" {
		rule.Aliases = []string{"K"}
	}
	return rule, nil
}

func TestShapeMatch_Aliases(t *testing.T) {
	root := schemaFor[matchAliased](t, engine(t, gowalker.WithFieldResolver(aliasK))).Root()
	checkProbes(t, root, []matchProbe{
		{"K", "A", gowalker.ExactMatch},
		{"k", "B", gowalker.ExactMatch},
		{"A", "A", gowalker.FoldedMatch},
		// A is declared first, so its alias takes the fold B's name shares.
		{"\u212a", "A", gowalker.FoldedMatch},
		{"x", "", gowalker.NoMatch},
	})
}

type matchFolding struct {
	Port int `db:"port"`
	Host int `db:"Host"`
}

func TestShapeMatch_CaseFoldNames(t *testing.T) {
	resolver := func(sf reflect.StructField) (gowalker.FieldRule, error) {
		rule := gowalker.FieldRule{Name: sf.Tag.Get("db")}
		if rule.Name == "port" {
			rule.Aliases = []string{"listen_port"}
		}
		return rule, nil
	}
	root := schemaFor[matchFolding](t, engine(t, gowalker.WithFieldResolver(resolver), gowalker.WithCaseFoldNames())).Root()
	probes := []matchProbe{
		{"port", "Port", gowalker.ExactMatch},
		{"PORT", "Port", gowalker.FoldedMatch},
		{"listen_port", "Port", gowalker.ExactMatch},
		{"LISTEN_PORT", "Port", gowalker.FoldedMatch},
		{"Host", "Host", gowalker.ExactMatch},
		{"host", "Host", gowalker.FoldedMatch},
		{"user", "", gowalker.NoMatch},
		{strings.Repeat("port", 100), "", gowalker.NoMatch},
	}
	checkProbes(t, root, probes)
	for _, tt := range probes {
		f, _ := root.Match(tt.probe)
		if lookup, ok := root.Lookup(tt.probe); lookup != f || ok != (f != nil) {
			t.Errorf("Lookup(%q) = %q, want Match's %q", tt.probe, goName(lookup), goName(f))
		}
	}
}

type matchStrictFolding struct {
	Name int `json:"name,case:strict"`
	Port int `json:"port"`
}

// Under WithCaseFoldNames, Lookup folds every name; Match still matches a
// case:strict field only exactly, as encoding/json does.
func TestShapeMatch_CaseStrictUnderCaseFoldNames(t *testing.T) {
	root := schemaFor[matchStrictFolding](t, engine(t, gowalker.WithJSONFields("json"), gowalker.WithCaseFoldNames())).Root()
	checkProbes(t, root, []matchProbe{
		{"name", "Name", gowalker.ExactMatch},
		{"NAME", "", gowalker.NoMatch},
		{"PORT", "Port", gowalker.FoldedMatch},
	})
	if f, ok := root.Lookup("NAME"); !ok || goName(f) != "Name" {
		t.Errorf("Lookup(NAME) = %q, want Name", goName(f))
	}
}

type matchHolder struct {
	Items   []matchInner
	Started time.Time
}

func TestShapeMatch_NonStructShapesMatchNothing(t *testing.T) {
	isTime := func(t reflect.Type) bool { return t == reflect.TypeFor[time.Time]() }
	schema := schemaFor[*matchHolder](t, engine(t, gowalker.WithJSONFields("json", gowalker.NestedFields()), gowalker.WithAtomicType(isTime)))
	items, _ := schema.Root().Elem().Lookup("Items")
	started, _ := schema.Root().Elem().Lookup("Started")
	for _, shape := range []*gowalker.Shape{schema.Root(), items.Shape(), started.Shape()} {
		if f, match := shape.Match("Items"); f != nil || match != gowalker.NoMatch {
			t.Errorf("%v: Match = %q %s, want nothing", shape.Type(), goName(f), match)
		}
	}
	checkProbes(t, items.Shape().Elem(), []matchProbe{{"ALPHA", "Alpha", gowalker.FoldedMatch}})
}

func TestShapeMatch_AllocatesNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector's instrumentation allocates")
	}
	type longNamed struct {
		Long  int `json:"ǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆǆ"`
		Short int `json:"short"`
	}
	exact := schemaFor[longNamed](t, engine(t, gowalker.WithJSONFields("json"))).Root()
	folding := schemaFor[longNamed](t, engine(t, gowalker.WithJSONFields("json"), gowalker.WithCaseFoldNames())).Root()
	probes := []string{
		strings.Repeat("ǆ", 60), strings.Repeat("Ǆ", 60), strings.Repeat("x", 100),
		strings.Repeat("x", 10000), "short", "SHORT", "nope",
	}
	for _, shape := range []*gowalker.Shape{exact, folding} {
		for _, probe := range probes {
			bytes := []byte(probe)
			if allocs := testing.AllocsPerRun(20, func() { shape.Match(probe) }); allocs != 0 {
				t.Errorf("Match(%.20q) allocates %v", probe, allocs)
			}
			if allocs := testing.AllocsPerRun(20, func() { shape.MatchBytes(bytes) }); allocs != 0 {
				t.Errorf("MatchBytes(%.20q) allocates %v", probe, allocs)
			}
			if allocs := testing.AllocsPerRun(20, func() { shape.Lookup(probe) }); allocs != 0 {
				t.Errorf("Lookup(%.20q) allocates %v", probe, allocs)
			}
		}
	}
}

func TestNameMatch_String(t *testing.T) {
	for match, want := range map[gowalker.NameMatch]string{
		gowalker.NoMatch: "NoMatch", gowalker.ExactMatch: "ExactMatch", gowalker.FoldedMatch: "FoldedMatch", 9: "NameMatch(9)",
	} {
		if got := match.String(); got != want {
			t.Errorf("%d: %q, want %q", match, got, want)
		}
	}
}
