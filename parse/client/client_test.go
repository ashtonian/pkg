//go:build unit

package client

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

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
			assert.JSONEq(t, tt.want, encode(t, tt.f))
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
			assert.JSONEq(t, tt.want, encode(t, tt.f))
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
			assert.ErrorIs(t, tt.f.Err(), tt.want)
			assert.False(t, tt.f.IsZero())
			_, err := json.Marshal(tt.f)
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

func TestFilter_DoesNotShareTheCallersSlice(t *testing.T) {
	keys := []string{"a", "b"}
	f := NewField[string]("k").In(keys...)
	keys[0] = "changed"
	assert.JSONEq(t, `{"k":{"$in":["a","b"]}}`, encode(t, f))
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
			assert.Equal(t, tt.want, encode(t, tt.m))
		})
	}
}

func TestMoment_RefusesSubSecondRelativeTimes(t *testing.T) {
	for _, m := range []Moment{Ago(1500 * time.Millisecond), FromNow(time.Nanosecond), Ago(time.Duration(-1 << 63))} {
		_, err := json.Marshal(NewTime("t").Gt(m))
		assert.ErrorIs(t, err, ErrSubSecond)
	}
}

func TestSort_SpellsTheSortParameter(t *testing.T) {
	name, rank := NewSortableText("name"), NewSortableOrdered[int]("rank")
	created := NewSortableTime("created")
	assert.Equal(t, "-rank,name,-created,id", Sort{rank.Desc(), name.Asc(), created.Desc(), NewSortKey("id").Asc()}.String())
	assert.Empty(t, Sort{}.String())
	assert.Equal(t, `{"name":{"$like":"a%"}}`, encode(t, name.Like("a%")), "a sortable field keeps its filter operators")
}

func TestNullable_IsAMergePatchMember(t *testing.T) {
	type patch struct {
		Name  Nullable[string] `json:"name,omitzero"`
		Note  Nullable[string] `json:"note,omitzero"`
		Count Nullable[int]    `json:"count,omitzero"`
	}
	assert.Equal(t, `{"name":"pump","note":null}`, encode(t, patch{Name: Set("pump"), Note: Null[string]()}))
	assert.Equal(t, `{}`, encode(t, patch{}))

	var got patch
	require.NoError(t, json.Unmarshal([]byte(`{"name":"pump","note":null}`), &got))
	v, ok := got.Name.Get()
	assert.True(t, ok)
	assert.Equal(t, "pump", v)
	assert.True(t, got.Note.IsNull())
	assert.True(t, got.Count.IsZero())

	err := json.Unmarshal([]byte(`{"count":"x"}`), &got)
	_, isTypeErr := errors.AsType[*json.UnmarshalTypeError](err)
	assert.True(t, isTypeErr, "a value of the wrong type is an error: %v", err)
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
		{"relation add", tags.Add("t1", "t2"), `{"op":"add","path":"/tags","value":["t1","t2"]}`},
		{"relation remove carries its keys", tags.Remove("t1"), `{"op":"remove","path":"/tags","value":["t1"]}`},
		{"relation replace with none", tags.Replace(), `{"op":"replace","path":"/tags","value":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.JSONEq(t, tt.want, encode(t, tt.op))
		})
	}
}

func TestPointer_EscapesMemberNames(t *testing.T) {
	assert.Equal(t, "/unit~1si~0v", Pointer("unit/si~v"))
	assert.Equal(t, "/labels/env", Pointer("labels", "env"))
	assert.Equal(t, "/", Pointer(""))
	assert.Equal(t, "/~01", Pointer("~1"), "~ escapes before / so ~1 stays literal")
	assert.Empty(t, Pointer(), "the empty pointer is the whole document")
}

func TestListParams_LeavesZeroMembersOut(t *testing.T) {
	q, err := ListParams{Sort: Sort{NewSortKey("a").Desc()}, PageSize: 5}.Values()
	require.NoError(t, err)
	assert.Equal(t, "page_size=5&sort=-a", q.Encode())

	_, err = ListParams{Filter: Or()}.Values()
	assert.ErrorIs(t, err, ErrEmptyOr)
}
