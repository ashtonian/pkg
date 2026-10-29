// Package filtertest is the filter conformance fixture: one entity with a
// field of every filterable type, its rows, and the rows every backend must
// return for each filter. SQL dialects and in-memory evaluators run the
// same cases through Run, so an operator means one thing everywhere
// .
package filtertest

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/shopspring/decimal"
)

// Record declares every operator its field type admits (filter.OperatorsFor),
// so the cases cover the full operator × type matrix. Its table is
// "records".
type Record struct {
	ID     string             `json:"id"     db:"id"     parse:"filter" filter_ops:"eq,in"`
	Name   *string            `json:"name"   db:"name"   parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin,like,ilike,contains,null,not_null,exists"`
	Count  *int64             `json:"count"  db:"count"  parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin,null,not_null,exists"`
	Score  *float64           `json:"score"  db:"score"  parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin,null,not_null,exists"`
	Amount *decimal.Decimal   `json:"amount" db:"amount" parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin,null,not_null,exists"`
	Active *bool              `json:"active" db:"active" parse:"filter" filter_ops:"eq,ne,in,nin,null,not_null,exists"`
	At     *time.Time         `json:"at"     db:"at"     parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin,null,not_null,exists"`
	Ref    *uuid.UUID         `json:"ref"    db:"ref"    parse:"filter" filter_ops:"eq,ne,in,nin,null,not_null,exists"`
	Doc    json.RawMessage    `json:"doc"    db:"doc"    parse:"filter" filter_ops:"eq,ne,contains,null,not_null,exists"`
	Tags   []string           `json:"tags"   db:"tags"   parse:"filter" filter_ops:"contains,null,not_null,exists"`
	Loc    map[string]float64 `json:"loc"    db:"loc"    parse:"filter" filter_ops:"near,within,intersects,null,not_null,exists" geo:"jsonb"`
}

// Base is the instant timestamps are offset from; Now is the parser clock
// relative tokens resolve against.
var (
	Base = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	Now  = func() time.Time { return Base.Add(3 * time.Second) }
)

func at(sec int) *time.Time { return new(Base.Add(time.Duration(sec) * time.Second)) }

func ref(n int) *uuid.UUID {
	id := uuid.MustParse("0198a3c2-7a1e-7c00-8000-00000000000" + string(rune('0'+n)))
	return &id
}

func loc(lat, lon float64) map[string]float64 { return map[string]float64{"lat": lat, "lon": lon} }

func amount(s string) *decimal.Decimal { return new(decimal.RequireFromString(s)) }

// Records is the fixture data. Row d is NULL in every column but id; g's
// document stores its integer as 1.0 with keys in a different order than
// the cases name them. The amounts order differently as numbers than as
// text (9.5 < 10 < 10.25), and h's is stored with a scale (10.00).
func Records() []Record {
	return []Record{
		{ID: "a", Name: new("Alpha"), Count: new(int64(1)), Score: new(1.5), Amount: amount("10.25"), Active: new(true), At: at(1), Ref: ref(1),
			Doc: json.RawMessage(`{"env":"prod","n":1,"tags":["x","y"]}`), Tags: []string{"x", "y"}, Loc: loc(10, 10)},
		{ID: "b", Name: new("beta"), Count: new(int64(2)), Score: new(2.5), Amount: amount("9.5"), Active: new(false), At: at(2), Ref: ref(2),
			Doc: json.RawMessage(`{"env":"dev","n":2}`), Tags: []string{"y"}, Loc: loc(20, 20)},
		{ID: "c", Name: new("Gamma%"), Count: new(int64(3)), Score: new(-1.0), Amount: amount("-1"), Active: new(true), At: at(3), Ref: ref(3),
			Doc: json.RawMessage(`{"env":"prod","nested":{"a":[1,2]}}`), Tags: []string{"z", "x"}, Loc: loc(-10, 170)},
		{ID: "d"},
		{ID: "e", Name: new("alpha beta"), Count: new(int64(9007199254740993)), Score: new(0.0), Amount: amount("100"), Active: new(false), At: at(5), Ref: ref(5),
			Doc: json.RawMessage(`[1,2,3]`), Tags: []string{}, Loc: loc(0, 0)},
		{ID: "f", Name: new("del_ta"), Count: new(int64(0)), Score: new(0.001), Amount: amount("0.5"), Active: new(true), At: at(6), Ref: ref(6),
			Doc: json.RawMessage(`"text"`), Tags: []string{"x"}, Loc: loc(20.5, 19.5)},
		{ID: "g", Name: new("Ωmega"), Count: new(int64(-5)), Score: new(100.25), Amount: amount("2"), At: at(7), Ref: ref(7),
			Doc: json.RawMessage(`{"n":1.0,"env":"prod"}`), Tags: []string{"x", "x"}},
		{ID: "h", Name: new(`back\slash`), Count: new(int64(9007199254740992)), Score: new(2.5), Amount: amount("10.00"), Active: new(true),
			Doc: json.RawMessage(`{}`), Tags: []string{"y", "z"}, Loc: loc(25, 25)},
	}
}

// Case is one filter and the IDs it matches, in ID order.
type Case struct {
	Name   string
	Filter string
	Want   []string
	// ParseErr is the E-code the parser rejects Filter with; Want is unused.
	ParseErr string
	// Unsupported names the backends that cannot evaluate the filter and
	// must fail instead ("sqlite", "memory"); a coded failure carries
	// E2115.
	Unsupported []string
}

// Backend evaluates a parsed filter over Records and returns the matching
// IDs.
type Backend func(t *testing.T, f *filter.ParsedFilter) ([]string, error)

// Parser returns the fixture's filter parser, clock pinned to Now.
func Parser(t testing.TB) *filter.FilterParser {
	t.Helper()
	schema, err := Schema()
	if err != nil {
		t.Fatalf("filtertest: schema: %v", err)
	}
	cfg := core.DefaultConfig()
	cfg.Clock = Now
	return filter.NewFilterParser(schema, cfg)
}

// Schema returns Record's generated filter schema.
func Schema() (*filter.ResourceSchema, error) {
	return filter.GenerateFilterSchema[Record](core.NewSchemaGenerator())
}

// Run evaluates every case on backend, named as Case.Unsupported names it.
func Run(t *testing.T, name string, backend Backend) {
	parser := Parser(t)
	for _, c := range Cases() {
		t.Run(c.Name, func(t *testing.T) {
			ast, err := parser.Parse(c.Filter)
			if c.ParseErr != "" {
				var pe *core.ParseError
				if !errors.As(err, &pe) || pe.Code != c.ParseErr {
					t.Fatalf("parse %s: err = %v, want %s", c.Filter, err, c.ParseErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse %s: %v", c.Filter, err)
			}
			got, err := backend(t, filter.NewParsedFilter(ast))
			if slices.Contains(c.Unsupported, name) {
				var pe *core.ParseError
				if err == nil || errors.As(err, &pe) && pe.Code != filter.ECodeGeoNotSupported.String() {
					t.Fatalf("%s: want an unsupported-operation error, got ids %v err %v", c.Filter, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: %v", c.Filter, err)
			}
			slices.Sort(got)
			want := c.Want
			if want == nil {
				want = []string{}
			}
			if got == nil {
				got = []string{}
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s:\n got %v\nwant %v", c.Filter, got, want)
			}
		})
	}
}
