//go:build unit

package client

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

func TestFilter_EncodesEachOperator(t *testing.T) {
	name, count := NewText("name"), NewOrdered[int]("count")
	meta := NewDocument[map[string]any]("meta")
	parts, owner := NewRelation[string]("parts"), NewReference[string]("owner")
	loc := NewGeo("loc")
	tests := []struct {
		name string
		f    Filter
		want string
	}{
		{"empty", Filter{}, `{}`},
		{"eq", name.Eq("a"), `{"name":{"$eq":"a"}}`},
		{"ne", count.Ne(1), `{"count":{"$ne":1}}`},
		{"in", name.In("a", "b"), `{"name":{"$in":["a","b"]}}`},
		{"in of nothing", name.In(), `{"name":{"$in":[]}}`},
		{"nin", count.Nin(1), `{"count":{"$nin":[1]}}`},
		{"gt", count.Gt(1), `{"count":{"$gt":1}}`},
		{"gte", count.Gte(1), `{"count":{"$gte":1}}`},
		{"lt", count.Lt(1), `{"count":{"$lt":1}}`},
		{"lte", count.Lte(1), `{"count":{"$lte":1}}`},
		{"like", name.Like("a%"), `{"name":{"$like":"a%"}}`},
		{"ilike", name.ILike("A%"), `{"name":{"$ilike":"A%"}}`},
		{"contains", name.Contains("a"), `{"name":{"$contains":"a"}}`},
		{"null", name.Null(), `{"name":{"$null":true}}`},
		{"not null", name.NotNull(), `{"name":{"$not_null":true}}`},
		{"document eq", meta.Eq(map[string]any{"$gt": 1}), `{"meta":{"$eq":{"$gt":1}}}`},
		{"document contains", meta.Contains(map[string]any{"env": "prod"}), `{"meta":{"$contains":{"env":"prod"}}}`},
		{"has key", meta.HasKey("env"), `{"meta":{"$has_key":"env"}}`},
		{"some", parts.Some(name.Eq("a")), `{"parts":{"$some":{"name":{"$eq":"a"}}}}`},
		{"none of any", parts.None(Filter{}), `{"parts":{"$none":{}}}`},
		{"is", owner.Is(name.Eq("a")), `{"owner":{"$is":{"name":{"$eq":"a"}}}}`},
		{"is not", owner.IsNot(name.Eq("a")), `{"owner":{"$is_not":{"name":{"$eq":"a"}}}}`},
		{"near", loc.Near(Point{Lat: 1, Lon: 2}, 1500.5), `{"loc":{"$near":{"lat":1,"lon":2,"distance":"1500.5m"}}}`},
		{"within box", loc.Within(Box(Point{Lat: 2}, Point{Lon: 1})), `{"loc":{"$within":{"top_left":{"lat":2,"lon":0},"bottom_right":{"lat":0,"lon":1}}}}`},
		{"intersects polygon", loc.Intersects(Polygon(Point{}, Point{Lat: 1}, Point{Lon: 1})), `{"loc":{"$intersects":{"points":[{"lat":0,"lon":0},{"lat":1,"lon":0},{"lat":0,"lon":1}]}}}`},
		{"not", Not(name.Eq("a")), `{"$not":{"name":{"$eq":"a"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkJSON(t, tt.want, encode(t, tt.f))
		})
	}
}

func TestFilter_AndOrFlattenAndDropTheEmptyFilter(t *testing.T) {
	a, b, c := NewField[int]("a").Eq(1), NewField[int]("b").Eq(2), NewField[int]("c").Eq(3)
	tests := []struct {
		name string
		f    Filter
		want string
	}{
		{"and of nothing matches everything", And(), `{}`},
		{"and drops empty filters", And(Filter{}, a, Filter{}), `{"a":{"$eq":1}}`},
		{"nested ands flatten", And(And(a, b), c), `{"$and":[{"a":{"$eq":1}},{"b":{"$eq":2}},{"c":{"$eq":3}}]}`},
		{"an and built in a loop stays flat", loopAnd(a, b, c), `{"$and":[{"a":{"$eq":1}},{"b":{"$eq":2}},{"c":{"$eq":3}}]}`},
		{"or of one is that filter", Or(a), `{"a":{"$eq":1}}`},
		{"nested ors flatten", Or(a, Or(b, c)), `{"$or":[{"a":{"$eq":1}},{"b":{"$eq":2}},{"c":{"$eq":3}}]}`},
		{"an or holding the empty filter matches everything", Or(a, Filter{}), `{}`},
		{"and inside or keeps its grouping", Or(And(a, b), c), `{"$or":[{"$and":[{"a":{"$eq":1}},{"b":{"$eq":2}}]},{"c":{"$eq":3}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkJSON(t, tt.want, encode(t, tt.f))
		})
	}
}

func loopAnd(filters ...Filter) Filter {
	var f Filter
	for _, next := range filters {
		f = And(f, next)
	}
	return f
}

func TestFilter_CarriesWhatTheGrammarCannotSay(t *testing.T) {
	a := NewField[int]("a").Eq(1)
	tests := []struct {
		name string
		f    Filter
		want error
	}{
		{"or of nothing", Or(), ErrEmptyOr},
		{"not of everything", Not(Filter{}), ErrNegatedEmpty},
		{"inside and", And(a, Or()), ErrEmptyOr},
		{"inside or beside the empty filter", Or(Filter{}, Not(Filter{})), ErrNegatedEmpty},
		{"inside not", Not(Or()), ErrEmptyOr},
		{"inside some", NewRelation[string]("parts").Some(Or()), ErrEmptyOr},
		{"inside elements", NewElements[[]int]("lines").None(Not(Filter{})), ErrNegatedEmpty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkErrorIs(t, tt.f.Err(), tt.want)
			check(t, false, tt.f.IsZero())
			_, err := json.Marshal(tt.f)
			checkErrorIs(t, err, tt.want)
		})
	}
}

func TestArray_EncodesElementConditions(t *testing.T) {
	tags, sizes, flags := NewTextArray("tags"), NewOrderedArray[int]("sizes"), NewArray[bool]("flags")
	tag, size := tags.Element(), sizes.Element()
	tests := []struct {
		name string
		f    Filter
		want string
	}{
		{"some", tags.Some(tag.Eq("a")), `{"tags":{"$some":{"$eq":"a"}}}`},
		{"one element in a range", sizes.Some(And(size.Gte(1), size.Lt(5))), `{"sizes":{"$some":{"$and":[{"$gte":1},{"$lt":5}]}}}`},
		{"either", tags.Some(Or(tag.Like("a%"), tag.Eq("b"))), `{"tags":{"$some":{"$or":[{"$like":"a%"},{"$eq":"b"}]}}}`},
		{"none outside a set", tags.None(Not(tag.In("a", "b"))), `{"tags":{"$none":{"$not":{"$in":["a","b"]}}}}`},
		{"any element", tags.Some(Filter{}), `{"tags":{"$some":{}}}`},
		{"no element", flags.None(Filter{}), `{"flags":{"$none":{}}}`},
		{"boolean element", flags.Some(flags.Element().Eq(true)), `{"flags":{"$some":{"$eq":true}}}`},
		{"contains", tags.Contains("a", "b"), `{"tags":{"$contains":["a","b"]}}`},
		{"contains nothing", tags.Contains(), `{"tags":{"$contains":[]}}`},
		{"null", sizes.Null(), `{"sizes":{"$null":true}}`},
		{"beside a field", And(NewText("name").Eq("x"), tags.Some(tag.Eq("a"))), `{"$and":[{"name":{"$eq":"x"}},{"tags":{"$some":{"$eq":"a"}}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkJSON(t, tt.want, encode(t, tt.f))
		})
	}
}

func TestArray_ElementConditionsStayInTheirArray(t *testing.T) {
	tags := NewTextArray("tags")
	tag, name := tags.Element(), NewText("name")
	tests := []struct {
		name    string
		f       Filter
		carried bool // the filter carries the error; otherwise only encoding it fails
	}{
		{"a field's condition in Some", tags.Some(name.Eq("a")), true},
		{"a field's condition beside the element's", tags.None(And(tag.Eq("a"), name.Eq("b"))), true},
		{"an element's condition in a relation", NewRelation[string]("parts").Some(tag.Eq("a")), true},
		{"an element's condition in a document array", NewElements[[]int]("lines").Some(Not(tag.Eq("a"))), true},
		{"an element's condition alone", tag.Eq("a"), false},
		{"an element's condition beside a field's", Or(name.Eq("a"), tag.Eq("a")), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.carried {
				checkErrorIs(t, tt.f.Err(), ErrElementCondition)
			}
			_, err := json.Marshal(tt.f)
			checkErrorIs(t, err, ErrElementCondition)
		})
	}
}

func TestFilter_DoesNotShareTheCallersSlice(t *testing.T) {
	keys := []string{"a", "b"}
	f := NewField[string]("k").In(keys...)
	keys[0] = "changed"
	checkJSON(t, `{"k":{"$in":["a","b"]}}`, encode(t, f))
}

func TestMoment_EncodesAbsoluteAndServerRelativeTimes(t *testing.T) {
	tests := []struct {
		name string
		m    Moment
		want string
	}{
		{"absolute", At(time.Date(2026, 10, 22, 8, 30, 0, 5, time.UTC)), `"2026-10-22T08:30:00.000000005Z"`},
		{"now", Now(), `"now"`},
		{"zero offset", Ago(0), `"now"`},
		{"weeks", Ago(14 * 24 * time.Hour), `"now-2w"`},
		{"days", Ago(8 * 24 * time.Hour), `"now-8d"`},
		{"hours", FromNow(36 * time.Hour), `"now+36h"`},
		{"minutes", Ago(90 * time.Minute), `"now-90m"`},
		{"seconds", FromNow(61 * time.Second), `"now+61s"`},
		{"negative ago is ahead", Ago(-time.Hour), `"now+1h"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check(t, tt.want, encode(t, tt.m))
		})
	}
}

func TestMoment_RefusesSubSecondRelativeTimes(t *testing.T) {
	for _, m := range []Moment{Ago(1500 * time.Millisecond), FromNow(time.Nanosecond), Ago(time.Duration(-1 << 63))} {
		f := NewTime("t").Gt(m)
		checkErrorIs(t, f.Err(), ErrSubSecond, "the filter carries it where it is built")
		checkErrorIs(t, f.Err(), ErrOperand)
		_, err := json.Marshal(f)
		checkErrorIs(t, err, ErrSubSecond)
		_, err = json.Marshal(m)
		checkErrorIs(t, err, ErrSubSecond, "a Moment alone refuses to encode too")
	}
}

// Every operand the grammar has no spelling for is refused where its filter
// is built, so Filter.Err, ListParams.Values and ListParams.Body all see it.
func TestFilter_RefusesOperandsWithoutASpelling(t *testing.T) {
	ratio, quoted := NewOrdered[float64]("ratio"), NewOrdered[Quoted[float64]]("q")
	loc, meta := NewGeo("loc"), NewDocument[map[string]any]("meta")
	tests := []struct {
		name string
		f    Filter
	}{
		{"NaN", ratio.Eq(math.NaN())},
		{"+Inf", ratio.Gt(math.Inf(1))},
		{"-Inf in a set", ratio.In(1, math.Inf(-1))},
		{"quoted NaN", quoted.Lt(Quote(math.NaN()))},
		{"NaN in a document", meta.Contains(map[string]any{"x": math.NaN()})},
		{"NaN in a point", loc.Near(Point{Lat: math.NaN()}, 10)},
		{"zero distance", loc.Near(Point{}, 0)},
		{"negative distance", loc.Near(Point{}, -5)},
		{"infinite distance", loc.Near(Point{}, math.Inf(1))},
		{"NaN distance", loc.Near(Point{}, math.NaN())},
		{"nested", NewRelation[string]("parts").Some(ratio.Eq(math.NaN()))},
		{"inside or", Or(ratio.Eq(1), ratio.Eq(math.Inf(1)))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkErrorIs(t, tt.f.Err(), ErrOperand)
			_, err := ListParams{Filter: tt.f}.Values()
			checkErrorIs(t, err, ErrOperand)
			_, err = ListParams{Filter: tt.f}.Body()
			checkErrorIs(t, err, ErrOperand)
		})
	}
	checkJSON(t, `{"loc":{"$near":{"lat":0,"lon":0,"distance":"0.001m"}}}`, encode(t, loc.Near(Point{}, 0.001)), "any positive distance has a spelling")
}

func TestFilter_DoesNotShareTheCallersDocument(t *testing.T) {
	doc := map[string]any{"env": "prod"}
	f := NewDocument[map[string]any]("meta").Contains(doc)
	doc["env"] = "changed"
	checkJSON(t, `{"meta":{"$contains":{"env":"prod"}}}`, encode(t, f))
}

// stateful encodes whatever its counter holds when it is encoded.
type stateful struct{ n *int }

func (s stateful) MarshalJSON() ([]byte, error) { return json.Marshal(*s.n) }

// encodings is every wire form of f: its document, a list's query string
// and a list body.
func encodings(t *testing.T, f Filter) []string {
	t.Helper()
	q, err := ListParams{Filter: f}.Values()
	must(t, err)
	body, err := ListParams{Filter: f}.Body()
	must(t, err)
	forms := []string{encode(t, f), q.Encode(), string(body)}
	return forms
}

// A built filter's wire value is fixed: changing any operand the caller
// still holds — a document, a slice, a pointer's target, raw JSON, a list
// element, the state a marshaler reads — changes no later encoding, in a
// filter alone or combined. Numbers keep the text they were encoded with.
func TestFilter_EncodingIsFixedWhenBuilt(t *testing.T) {
	tests := []struct {
		name  string
		build func() (Filter, func())
		want  string
	}{
		{"a document combined with And", func() (Filter, func()) {
			doc := map[string]any{"label": "a"}
			f := And(NewText("name").Eq("pump"), NewDocument[map[string]any]("props").Contains(doc))
			return f, func() { doc["label"] = "b" }
		}, `{"$and":[{"name":{"$eq":"pump"}},{"props":{"$contains":{"label":"a"}}}]}`},
		{"a nested document member", func() (Filter, func()) {
			inner := map[string]any{"rpm": 10}
			f := NewDocument[map[string]any]("props").Eq(map[string]any{"motor": inner})
			return f, func() { inner["rpm"] = 20 }
		}, `{"props":{"$eq":{"motor":{"rpm":10}}}}`},
		{"a slice document", func() (Filter, func()) {
			tags := []string{"a", "b"}
			f := NewDocument[[]string]("tags").Eq(tags)
			return f, func() { tags[0] = "z" }
		}, `{"tags":{"$eq":["a","b"]}}`},
		{"a pointer's target", func() (Filter, func()) {
			note := "draft"
			f := NewField[*string]("note").Eq(&note)
			return f, func() { note = "final" }
		}, `{"note":{"$eq":"draft"}}`},
		{"raw JSON", func() (Filter, func()) {
			raw := json.RawMessage(`{"k":1}`)
			f := NewDocument[json.RawMessage]("props").Contains(raw)
			return f, func() { raw[5] = '2' }
		}, `{"props":{"$contains":{"k":1}}}`},
		{"list elements", func() (Filter, func()) {
			pair := []string{"a", "b"}
			f := NewField[[]string]("pair").In(pair, []string{"c"})
			return f, func() { pair[1] = "z" }
		}, `{"pair":{"$in":[["a","b"],["c"]]}}`},
		{"array contents", func() (Filter, func()) {
			sizes := []int{1, 2}
			f := NewOrderedArray[int]("sizes").Contains(sizes...)
			return f, func() { sizes[0] = 9 }
		}, `{"sizes":{"$contains":[1,2]}}`},
		{"a nested filter's document", func() (Filter, func()) {
			doc := map[string]any{"sku": "a"}
			f := NewElements[[]map[string]any]("lines").Some(NewDocument[map[string]any]("meta").Contains(doc))
			return f, func() { doc["sku"] = "b" }
		}, `{"lines":{"$some":{"meta":{"$contains":{"sku":"a"}}}}}`},
		{"a marshaler's state", func() (Filter, func()) {
			n := 1
			f := NewField[stateful]("n").Eq(stateful{&n})
			return f, func() { n = 2 }
		}, `{"n":{"$eq":1}}`},
		{"a number's text", func() (Filter, func()) {
			return NewOrdered[json.Number]("big").Gt("12345678901234567890.000"), func() {}
		}, `{"big":{"$gt":12345678901234567890.000}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, mutate := tt.build()
			must(t, f.Err())
			before := encodings(t, f)
			checkJSON(t, tt.want, before[0])
			mutate()
			checkDeep(t, before, encodings(t, f), "encoded after the operand changed")
			checkDeep(t, before, encodings(t, f), "encoded again")
		})
	}
}

// A leaf shared by two expressions is one fixed value: changing its operand
// or encoding one expression leaves both as they were built.
func TestFilter_SharedLeafStaysFixed(t *testing.T) {
	doc := map[string]any{"label": "a"}
	leaf := NewDocument[map[string]any]("props").Contains(doc)
	name := NewText("name")
	both := And(name.Eq("pump"), leaf)
	either := Or(leaf, Not(name.Like("x%")))
	wantBoth, wantEither := encodings(t, both), encodings(t, either)

	doc["label"] = "b"
	delete(doc, "label")
	checkDeep(t, wantBoth, encodings(t, both))
	checkDeep(t, wantEither, encodings(t, either))
	checkJSON(t, `{"$or":[{"props":{"$contains":{"label":"a"}}},{"$not":{"name":{"$like":"x%"}}}]}`, encode(t, either))
}

// A marshaler that fails is the filter's error from the moment it is built,
// before any encoding.
func TestFilter_MarshalerErrorIsVisibleWhenBuilt(t *testing.T) {
	f := NewDocument[json.RawMessage]("props").Eq(json.RawMessage(`{"broken"`))
	checkErrorIs(t, f.Err(), ErrOperand)
	_, err := ListParams{Filter: And(NewText("name").Eq("a"), f)}.Values()
	checkErrorIs(t, err, ErrOperand)
}

func TestSort_SpellsTheSortParameter(t *testing.T) {
	name, rank := NewSortableText("name"), NewSortableOrdered[int]("rank")
	created := NewSortableTime("created")
	check(t, "-rank,name,-created,id", Sort{rank.Desc(), name.Asc(), created.Desc(), NewSortKey("id").Asc()}.String())
	check(t, "", Sort{}.String())
	check(t, `{"name":{"$like":"a%"}}`, encode(t, name.Like("a%")), "a sortable field keeps its filter operators")
}

func TestNullable_IsAMergePatchMember(t *testing.T) {
	type patch struct {
		Name  Nullable[string] `json:"name,omitzero"`
		Note  Nullable[string] `json:"note,omitzero"`
		Count Nullable[int]    `json:"count,omitzero"`
	}
	check(t, `{"name":"pump","note":null}`, encode(t, patch{Name: Set("pump"), Note: Null[string]()}))
	check(t, `{}`, encode(t, patch{}))

	var got patch
	must(t, json.Unmarshal([]byte(`{"name":"pump","note":null}`), &got))
	v, ok := got.Name.Get()
	check(t, true, ok)
	check(t, "pump", v)
	check(t, true, got.Note.IsNull())
	check(t, true, got.Count.IsZero())

	err := json.Unmarshal([]byte(`{"count":"x"}`), &got)
	_, isTypeErr := errors.AsType[*json.UnmarshalTypeError](err)
	check(t, true, isTypeErr, "a value of the wrong type is an error, got", err)
}

// A member that is not tagged omitzero reaches MarshalJSON when absent;
// encoding it as null would clear a column the caller never touched.
func TestNullable_AbsentMemberWithoutOmitzeroDoesNotEncode(t *testing.T) {
	type omitEmpty struct {
		Name Nullable[string] `json:"name,omitzero"`
		Note Nullable[string] `json:"note,omitempty"`
	}
	type untagged struct {
		Name  Nullable[string] `json:"name,omitzero"`
		Alias Nullable[string] `json:"alias"`
	}
	_, err := json.Marshal(omitEmpty{Name: Set("pump")})
	checkErrorIs(t, err, ErrAbsentMember)
	_, err = json.Marshal(untagged{Name: Set("pump")})
	checkErrorIs(t, err, ErrAbsentMember)

	check(t, `{"name":"pump","note":"n"}`, encode(t, omitEmpty{Name: Set("pump"), Note: Set("n")}), "a present member encodes under any tag")
}

func TestNullable_SetOfNilIsWhatItEncodes(t *testing.T) {
	type patch struct {
		Tags   Nullable[[]string]       `json:"tags,omitzero"`
		Labels Nullable[map[string]int] `json:"labels,omitzero"`
		Nick   Nullable[*string]        `json:"nick,omitzero"`
		Any    Nullable[any]            `json:"any,omitzero"`
	}
	p := patch{Tags: Set[[]string](nil), Labels: Set[map[string]int](nil), Nick: Set[*string](nil), Any: Set[any](nil)}
	check(t, `{"tags":[],"labels":{},"nick":null,"any":null}`, encode(t, p))
	tags, ok := p.Tags.Get()
	check(t, true, ok)
	if tags == nil {
		t.Error("Set of a nil slice holds the empty slice")
	}
	check(t, true, p.Nick.IsNull(), "a nil pointer clears the field, so it is Null")
	check(t, true, p.Any.IsNull())
}

func TestOperation_EncodesTheMembersItsOpTakes(t *testing.T) {
	tags := NewRelation[string]("tags")
	tests := []struct {
		name string
		op   Operation
		want string
	}{
		{"add", Add("/a", 1), `{"op":"add","path":"/a","value":1}`},
		{"replace with null", Replace("/a", nil), `{"op":"replace","path":"/a","value":null}`},
		{"test", Test("/a", "x"), `{"op":"test","path":"/a","value":"x"}`},
		{"remove", Remove("/a"), `{"op":"remove","path":"/a"}`},
		{"move", Move("/a", "/b"), `{"op":"move","path":"/b","from":"/a"}`},
		{"copy", Copy("/a", "/b"), `{"op":"copy","path":"/b","from":"/a"}`},
		{"append", Append("/scores", 3), `{"op":"array_append","path":"/scores","value":3}`},
		{"append null", Append("/scores", nil), `{"op":"array_append","path":"/scores","value":null}`},
		{"array append", NewOrderedArray[int]("scores").Append(4), `{"op":"array_append","path":"/scores","value":4}`},
		{"relation add", tags.Add("t1", "t2"), `{"op":"add","path":"/tags","value":["t1","t2"]}`},
		{"relation remove carries its keys", tags.Remove("t1"), `{"op":"remove","path":"/tags","value":["t1"]}`},
		{"relation replace with none", tags.Replace(), `{"op":"replace","path":"/tags","value":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkJSON(t, tt.want, encode(t, tt.op))
		})
	}
}

// An operation's value is encoded when it is built, as a filter's operand
// is: changing what it was built from changes nothing it sends.
func TestOperation_EncodingIsFixedWhenBuilt(t *testing.T) {
	doc := map[string]any{"color": "red"}
	tags := []string{"a", "b"}
	ops := []Operation{Add("/attrs", doc), Replace("/tags", tags), Test("/attrs", doc), Append("/log", doc)}
	before := encode(t, ops)
	doc["color"] = "blue"
	tags[0] = "z"
	checkJSON(t, before, encode(t, ops))
	checkJSON(t, `[{"op":"add","path":"/attrs","value":{"color":"red"}},{"op":"replace","path":"/tags","value":["a","b"]},`+
		`{"op":"test","path":"/attrs","value":{"color":"red"}},{"op":"array_append","path":"/log","value":{"color":"red"}}]`, before)
}

// A value encoding/json refuses is ErrOperand where the operation is built,
// as an operand is in a filter, and marshaling a document holding it fails.
func TestOperation_RefusedValueIsErrOperandWhenBuilt(t *testing.T) {
	for name, op := range map[string]Operation{
		"NaN":             Replace("/score", math.NaN()),
		"infinity":        Append("/scores", math.Inf(-1)),
		"channel":         Add("/x", make(chan int)),
		"marshaler error": Test("/at", failingJSON{}),
	} {
		t.Run(name, func(t *testing.T) {
			checkErrorIs(t, op.Err(), ErrOperand)
			_, err := json.Marshal([]Operation{Remove("/a"), op})
			checkErrorIs(t, err, ErrOperand)
		})
	}
	check[error](t, nil, Add("/a", 1).Err())
}

// failingJSON is a value whose MarshalJSON fails.
type failingJSON struct{}

func (failingJSON) MarshalJSON() ([]byte, error) { return nil, errors.New("no JSON") }

func TestPointer_EscapesMemberNames(t *testing.T) {
	check(t, "/unit~1si~0v", Pointer("unit/si~v"))
	check(t, "/labels/env", Pointer("labels", "env"))
	check(t, "/", Pointer(""))
	check(t, "/~01", Pointer("~1"), "~ escapes before / so ~1 stays literal")
	check(t, "", Pointer(), "the empty pointer is the whole document")
}

func TestListParams_LeavesZeroMembersOut(t *testing.T) {
	q, err := ListParams{Sort: Sort{NewSortKey("a").Desc()}, PageSize: 5}.Values()
	must(t, err)
	check(t, "page_size=5&sort=-a", q.Encode())

	_, err = ListParams{Filter: Or()}.Values()
	checkErrorIs(t, err, ErrEmptyOr)
}
