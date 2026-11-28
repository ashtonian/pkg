//go:build integration

package corpus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/internal/patchtest"
	"github.com/ashtonian/pkg/parse/patch"
)

// The cases holding a composed patch applied to a row's stored values
// (patch.ComposedPatch.Apply), as a store that holds rows itself applies
// it, to what the same update's SQL does on the database: the values it
// writes, whether it changes the row, and what it refuses.

// storedPatchRow is a row of every kind of column a patch writes: text on
// case-insensitive columns (a NOCASE collation on SQLite; a
// nondeterministic collation and citext on PostgreSQL) and an enum there,
// integers, floats, decimals (one padding to a scale, one a REAL on
// SQLite, one TEXT there), a boolean, timestamps, a date, the value types,
// a UUID, nullable wrappers, native arrays of every element type, a slice
// of decimals (a document), untyped documents (raw declared json on
// PostgreSQL, body a text column there), a document parse reads as its
// text (exact, a text column on PostgreSQL), so its numbers keep every
// digit, a map, a struct document, a typed map and the server's counter
// and stamp.
type storedPatchRow struct {
	ID        int64               `json:"id" db:"id"`
	Name      string              `json:"name" db:"name" parse:"patch"`
	Email     string              `json:"email" db:"email" parse:"patch"`
	Mood      string              `json:"mood" db:"mood" parse:"patch"`
	Note      *string             `json:"note" db:"note" parse:"patch" patch_ops:"replace,add,remove,test,move,copy"`
	Rank      int64               `json:"rank" db:"rank" parse:"patch"`
	Ratio     float64             `json:"ratio" db:"ratio" parse:"patch"`
	Ratio32   float32             `json:"ratio32" db:"ratio32" parse:"patch"`
	Flag      bool                `json:"flag" db:"flag" parse:"patch"`
	Price     decimal.Decimal     `json:"price" db:"price" parse:"patch"`
	Amount    decimal.Decimal     `json:"amount" db:"amount" parse:"patch"`
	Cost      decimal.Decimal     `json:"cost" db:"cost" parse:"patch"`
	At        time.Time           `json:"at" db:"at" parse:"patch"`
	Day       *core.Date          `json:"day" db:"day" parse:"patch" patch_ops:"replace,remove"`
	Clock     *core.LocalTime     `json:"clock" db:"clock" parse:"patch" patch_ops:"replace,remove"`
	Span      *core.Interval      `json:"span" db:"span" parse:"patch" patch_ops:"replace,remove"`
	Addr      *core.Inet          `json:"addr" db:"addr" parse:"patch" patch_ops:"replace,remove"`
	Subnet    *core.CIDR          `json:"subnet" db:"subnet" parse:"patch" patch_ops:"replace,remove"`
	Mac       *core.MACAddr       `json:"mac" db:"mac" parse:"patch" patch_ops:"replace,remove"`
	Ref       uuid.UUID           `json:"ref" db:"ref" parse:"patch"`
	Score     sql.NullInt64       `json:"score" db:"score" parse:"patch" patch_ops:"replace,remove"`
	SeenAt    sql.NullTime        `json:"seen_at" db:"seen_at" parse:"patch" patch_ops:"replace,remove"`
	Tags      []string            `json:"tags" db:"tags" parse:"patch"`
	Counts    []int64             `json:"counts" db:"counts" parse:"patch"`
	Times     []time.Time         `json:"times" db:"times" parse:"patch"`
	Flags     []bool              `json:"flags" db:"flags" parse:"patch"`
	Ratios    []float64           `json:"ratios" db:"ratios" parse:"patch"`
	Refs      []uuid.UUID         `json:"refs" db:"refs" parse:"patch"`
	Days      []core.Date         `json:"days" db:"days" parse:"patch"`
	Clocks    []core.LocalTime    `json:"clocks" db:"clocks" parse:"patch"`
	Spans     []core.Interval     `json:"spans" db:"spans" parse:"patch"`
	Addrs     []core.Inet         `json:"addrs" db:"addrs" parse:"patch"`
	Subnets   []core.CIDR         `json:"subnets" db:"subnets" parse:"patch"`
	Macs      []core.MACAddr      `json:"macs" db:"macs" parse:"patch"`
	Prices    []decimal.Decimal   `json:"prices" db:"prices" parse:"patch"`
	Doc       any                 `json:"doc" db:"doc" parse:"patch" patch_ops:"replace,add,remove,array_append"`
	Raw       any                 `json:"raw" db:"raw" parse:"patch"`
	Body      any                 `json:"body" db:"body" parse:"patch"`
	Exact     json.RawMessage     `json:"exact" db:"exact" parse:"patch"`
	Meta      map[string]any      `json:"meta" db:"meta" parse:"patch"`
	Dims      patchtest.ApplyDims `json:"dims" db:"dims" parse:"patch"`
	Labels    map[string]string   `json:"labels" db:"labels" parse:"patch"`
	Version   int64               `json:"version" db:"version" parse:"patch"`
	UpdatedAt *time.Time          `json:"updated_at" db:"updated_at" parse:"patch"`
}

// storedPatchSeed is the row every case starts from, before its own seed.
func storedPatchSeed() storedPatchRow {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	day, clock := core.Date("2026-01-02"), core.LocalTime("10:30:00")
	span := core.Interval{Days: 1}
	addr, err := core.ParseInet("192.0.2.1")
	if err != nil {
		panic(err)
	}
	subnet, err := core.ParseCIDR("10.0.0.0/8")
	if err != nil {
		panic(err)
	}
	mac, err := core.ParseMACAddr("08:00:2b:01:02:03")
	if err != nil {
		panic(err)
	}
	row := storedPatchRow{
		ID: 1, Name: "orig", Email: "a@x.io", Mood: "ok", Note: new("n"), Rank: 1, Ratio: 0.5, Ratio32: 0.1, Flag: true,
		Price: decimal.RequireFromString("1.5"), Amount: decimal.RequireFromString("2.25"), Cost: decimal.RequireFromString("1.5"), At: at,
		Day: &day, Clock: &clock, Span: &span, Addr: &addr, Subnet: &subnet, Mac: &mac,
		Ref:   uuid.MustParse("0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c"),
		Score: sql.NullInt64{Int64: 3, Valid: true}, SeenAt: sql.NullTime{Time: patchtest.RankedSeen(), Valid: true},
		Tags: []string{"zero"}, Counts: []int64{1}, Times: []time.Time{at},
		Flags: []bool{false}, Ratios: []float64{0.5}, Refs: []uuid.UUID{uuid.MustParse("0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c")},
		Days: []core.Date{day}, Clocks: []core.LocalTime{clock}, Spans: []core.Interval{span},
		Addrs: []core.Inet{addr}, Subnets: []core.CIDR{subnet}, Macs: []core.MACAddr{mac},
		Prices: []decimal.Decimal{decimal.RequireFromString("1.5")},
		Doc:    json.RawMessage(memberBase), Raw: json.RawMessage(memberBase), Body: json.RawMessage(memberBase),
		Exact:   json.RawMessage(`{"a":1}`),
		Meta:    map[string]any{"k": 1.0, "nested": map[string]any{"x": 1.0, "y": 2.0}},
		Dims:    patchtest.ApplyDims{W: 2, H: 7},
		Labels:  map[string]string{"old": "v"},
		Version: 1,
	}
	return row
}

// storedPatchCase is one patch applied to the seeded row by the database
// and by ComposedPatch.Apply over the row's stored values.
type storedPatchCase struct {
	name string
	// only is the one kind of engine the case runs on, for a stored form
	// only that engine holds.
	only Kind
	// seed changes the seeded row; seedSQL then writes on each engine what
	// parse would not, such as a document's reordered keys.
	seed    func(*storedPatchRow)
	seedSQL map[Kind]string
	// body is a JSON Patch when it is an array, a merge patch otherwise.
	body string
	// changed is whether the patch changes the row; on overrides it for an
	// engine whose stored row differs, for SQL and Apply alike.
	changed bool
	on      map[Kind]bool
	// refused is the code of the error the update fails with, "" for none;
	// a store's Apply fails it with the same error.
	refused string
	// increments are the update's counters beyond the version.
	increments []string
}

// storedSeedSQL writes text into the stored row's column on every engine.
func storedSeedSQL(column, text string) map[Kind]string {
	stmt := "UPDATE patch_rows SET " + column + " = " + text
	seeds := map[Kind]string{SQLite: stmt, Postgres: stmt}
	return seeds
}

// storedDocumentSeed seeds column with the document text start, "" for
// NULL.
func storedDocumentSeed(column, start string) func(*storedPatchRow) {
	return func(r *storedPatchRow) {
		var doc any
		if start != "" {
			doc = json.RawMessage(start)
		}
		switch column {
		case "doc":
			r.Doc = doc
		case "raw":
			r.Raw = doc
		case "body":
			r.Body = doc
		default:
			panic("no document column " + column)
		}
	}
}

func storedPatchCases(t *testing.T) []storedPatchCase {
	// An interval of 24 hours in each engine's text: PostgreSQL's equality
	// holds it equal to one day, its text and parse's do not.
	inHours := map[Kind]string{
		SQLite:   `UPDATE patch_rows SET span = 'PT24H', spans = '["PT24H"]'`,
		Postgres: `UPDATE patch_rows SET span = '24 hours', spans = '{"24 hours"}'`,
	}
	cases := []storedPatchCase{
		{name: "the same integer", body: `{"rank":1}`},
		{name: "another integer", body: `{"rank":2}`, changed: true},
		{name: "NULL to NULL", seed: func(r *storedPatchRow) { r.Note = nil }, body: `{"note":null}`},
		{name: "NULL to a value", seed: func(r *storedPatchRow) { r.Note = nil }, body: `{"note":"a"}`, changed: true},
		{name: "a value to NULL", body: `{"note":null}`, changed: true},
		{name: "the same text", seed: func(r *storedPatchRow) { r.Name = "Name" }, body: `{"name":"Name"}`},
		{name: "case alone, on a case-insensitive column", seed: func(r *storedPatchRow) { r.Name = "Name" }, body: `{"name":"NAME"}`, changed: true},
		{name: "case alone, on citext", seed: func(r *storedPatchRow) { r.Email = "Bob@x.io" }, body: `{"email":"bob@x.io"}`, changed: true},
		{name: "the same enum label", body: `{"mood":"ok"}`},
		{name: "another enum label", body: `{"mood":"happy"}`, changed: true},
		{name: "a decimal the column pads", body: `{"price":"1.50"}`},
		{name: "a decimal in another scale", seed: func(r *storedPatchRow) { r.Amount = decimal.RequireFromString("1.5") }, body: `{"amount":"1.50"}`},
		{name: "a decimal stored in another scale", seedSQL: storedSeedSQL("amount", "1.50"), body: `{"amount":"1.5"}`},
		{name: "a decimal stored in another scale, in a text column", seedSQL: storedSeedSQL("cost", "'1.50'"), body: `{"cost":"1.5"}`},
		{name: "a decimal stored in an exponent, in a text column", seedSQL: storedSeedSQL("cost", "'0.15e1'"), body: `{"cost":"1.5"}`},
		{name: "a decimal in another scale, in a text column", body: `{"cost":"1.50"}`},
		{name: "a number stored as an integer or a real", seedSQL: storedSeedSQL("price", "1.0, amount = 1"), body: `{"price":"1","amount":"1"}`},
		{name: "another decimal", body: `{"price":"1.51"}`, changed: true},
		{name: "a float zero of the other sign", seedSQL: storedSeedSQL("ratio", "CAST('-0' AS REAL)"), body: `{"ratio":0}`,
			on: map[Kind]bool{Postgres: true}},
		{name: "the same float32", body: `{"ratio32":0.1}`},
		{name: "another float32", body: `{"ratio32":0.25}`, changed: true},
		{name: "the same boolean", body: `{"flag":true}`},
		{name: "another boolean", body: `{"flag":false}`, changed: true},
		{name: "the same interval", body: `{"span":"P1D"}`},
		{name: "an interval in other units", body: `{"span":"PT24H"}`, changed: true},
		{name: "an interval stored in hours", seedSQL: inHours, body: `{"span":"PT24H","spans":["PT24H"]}`},
		{name: "an interval stored in hours, written as a day", seedSQL: inHours, body: `{"span":"P1D"}`, changed: true},
		{name: "an interval array stored in hours, written as a day", seedSQL: inHours, body: `{"spans":["P1D"]}`, changed: true},
		{name: "an interval array in other units", body: `{"spans":["PT24H"]}`, changed: true},
		{name: "a timestamp's instant in another offset", body: `{"at":"2025-12-31T19:00:00-05:00"}`},
		{name: "a timestamp stored in another offset", seedSQL: storedSeedSQL("at", "'2025-12-31T19:00:00-05:00'"), body: `{"at":"2026-01-01T00:00:00Z"}`},
		{name: "another instant", body: `{"at":"2026-01-01T00:00:00.000001Z"}`, changed: true},
		{name: "the same date", body: `{"day":"2026-01-02"}`},
		{name: "another date", body: `{"day":"2028-02-29"}`, changed: true},
		{name: "a date removed", body: `[{"op":"remove","path":"/day"}]`, changed: true},
		{name: "the same UUID in upper case", body: `{"ref":"0190A5B4-7C1E-7D8A-9B2C-3D4E5F6A7B8C"}`},
		{name: "another UUID", body: `{"ref":"` + uuid.Nil.String() + `"}`, changed: true},
		{name: "a UUID stored in upper case", only: SQLite, seedSQL: map[Kind]string{SQLite: "UPDATE patch_rows SET ref = upper(ref)"},
			body: `{"ref":"0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c"}`},
		{name: "a UUID stored in braces", only: SQLite, seedSQL: map[Kind]string{SQLite: "UPDATE patch_rows SET ref = '{' || upper(ref) || '}'"},
			body: `{"ref":"0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c"}`},
		{name: "the value types, each in its canonical text", body: `{"clock":"10:30:00","span":"P1D","addr":"192.0.2.1","subnet":"10.0.0.0/8","mac":"08:00:2b:01:02:03"}`},
		{name: "the value types, each in another spelling", body: `{"clock":"10:30:00.000","addr":"192.0.2.1/32","mac":"08-00-2B-01-02-03"}`},
		{name: "the value types, each stored in another spelling",
			seedSQL: storedSeedSQL("clock", "'10:30:00.000', span = 'P1DT0H', addr = '192.0.2.1/32', subnet = '10.0.0.0/8', mac = '08-00-2B-01-02-03'"),
			body:    `{"clock":"10:30:00","span":"P1D","addr":"192.0.2.1","subnet":"10.0.0.0/8","mac":"08:00:2b:01:02:03"}`},
		{name: "the value types changed", body: `{"clock":"10:30:00.500","span":"PT90M","addr":"2001:0DB8::0001","subnet":"2001:DB8::/32","mac":"08-00-2B-01-02-04"}`, changed: true},
		{name: "the value-type arrays, each element in its canonical text", body: `{"days":["2026-01-02"],"clocks":["10:30:00"],"spans":["P1D"],"addrs":["192.0.2.1"],"subnets":["10.0.0.0/8"],"macs":["08:00:2b:01:02:03"]}`},
		{name: "the value-type arrays, each element in another spelling", body: `{"clocks":["10:30:00.000"],"spans":["P1DT0H"],"addrs":["192.0.2.1/32"],"macs":["08-00-2B-01-02-03"]}`},
		{name: "the value-type arrays, each element stored in another spelling", seedSQL: map[Kind]string{
			SQLite:   `UPDATE patch_rows SET clocks = '["10:30:00.000"]', spans = '[ "P1DT0H" ]', addrs = '["192.0.2.1/32"]', macs = '["08-00-2B-01-02-03"]'`,
			Postgres: `UPDATE patch_rows SET days = '{20260102}', clocks = '{10:30:00.000}', spans = '{"1 day"}', addrs = '{192.0.2.1/32}', subnets = '{10/8}', macs = '{08-00-2B-01-02-03}'`,
		}, body: `{"days":["2026-01-02"],"clocks":["10:30:00"],"spans":["P1D"],"addrs":["192.0.2.1"],"subnets":["10.0.0.0/8"],"macs":["08:00:2b:01:02:03"]}`},
		{name: "the value-type arrays changed", body: `{"days":["2028-02-29"],"clocks":["10:30:00.500"],"spans":["PT90M"],"addrs":["2001:0DB8::0001"],"subnets":["2001:DB8::/32"],"macs":["08-00-2B-01-02-04"]}`, changed: true},
		{name: "the value types removed", body: `[{"op":"remove","path":"/clock"},{"op":"remove","path":"/span"},{"op":"remove","path":"/addr"},{"op":"remove","path":"/subnet"},{"op":"remove","path":"/mac"}]`, changed: true},
		{name: "the same document", seed: storedDocumentSeed("doc", `{"a":1,"b":[2]}`), body: `[{"op":"replace","path":"/doc","value":{"b":[2],"a":1}}]`},
		{name: "a document stored in another key order", seedSQL: storedSeedSQL("doc", `'{"b":[2], "a":1}'`),
			body: `[{"op":"replace","path":"/doc","value":{"a":1,"b":[2]}}]`},
		{name: "a json column stored in another key order", seedSQL: storedSeedSQL("raw", `'{"b":[2], "a":1}'`),
			body: `[{"op":"replace","path":"/raw","value":{"a":1,"b":[2]}}]`},
		{name: "a member set to what a spaced document holds", seedSQL: storedSeedSQL("doc", `'{"a": 1}'`),
			body: `[{"op":"replace","path":"/doc/a","value":1}]`},
		{name: "a document number stored at another scale", seedSQL: storedSeedSQL("doc", `'{"a":1.0}'`),
			body: `[{"op":"replace","path":"/doc","value":{"a":1}}]`, changed: true},
		{name: "document numbers stored in another form", seedSQL: storedSeedSQL("doc", `'{"a":1e2,"b":-0,"c":1.50e1}'`),
			body: `[{"op":"replace","path":"/doc","value":{"a":100,"b":0,"c":15.0}}]`},
		{name: "json column numbers stored in another form", seedSQL: storedSeedSQL("raw", `'{"a":1e2,"b":-0,"c":1.50e1}'`),
			body: `[{"op":"replace","path":"/raw","value":{"a":100,"b":0,"c":15.0}}]`},
		{name: "a text column's document stored in another key order", seedSQL: storedSeedSQL("body", `'{"b":[2], "a":1}'`),
			body: `[{"op":"replace","path":"/body","value":{"a":1,"b":[2]}}]`},
		{name: "a text column's document numbers stored in another form", seedSQL: storedSeedSQL("body", `'{"a":1e2,"b":-0,"c":1.50e1}'`),
			body: `[{"op":"replace","path":"/body","value":{"a":100,"b":0,"c":15.0}}]`},
		{name: "a text column's document number stored at another scale", seedSQL: storedSeedSQL("body", `'{"a":1.0}'`),
			body: `[{"op":"replace","path":"/body","value":{"a":1}}]`, changed: true},
		{name: "the same slice of decimals, a document", body: `{"prices":["1.5"]}`},
		{name: "a slice of decimals stored in another scale, a document of strings", seedSQL: storedSeedSQL("prices", `'["1.50"]'`),
			body: `{"prices":["1.5"]}`, changed: true},
		{name: "another document", seed: storedDocumentSeed("doc", `{"a":1}`), body: `[{"op":"replace","path":"/doc","value":{"a":2}}]`, changed: true},
		{name: "a merge that sets what is there", seed: storedDocumentSeed("doc", `{"a":1,"b":{"c":2}}`), body: `{"doc":{"b":{"c":2}}}`},
		{name: "a merge deleting a missing member", seed: storedDocumentSeed("doc", `{"a":1}`), body: `{"doc":{"z":null}}`},
		{name: "a merge that changes a member", seed: storedDocumentSeed("doc", `{"a":1,"b":{"c":2}}`), body: `{"doc":{"b":{"c":3}}}`, changed: true},
		{name: "a merge into NULL", seed: storedDocumentSeed("doc", ""), body: `{"doc":{"z":null}}`, changed: true},
		{name: "a merge into a json column that sets what is there", seed: storedDocumentSeed("raw", `{"a":1,"b":{"c":2}}`), body: `{"raw":{"b":{"c":2}}}`},
		{name: "a merge into a json column that changes a member", seed: storedDocumentSeed("raw", `{"a":1}`), body: `{"raw":{"b":{"c":3},"a":null}}`, changed: true},
		{name: "the same array", seed: func(r *storedPatchRow) { r.Tags = []string{"x", "y"} }, body: `{"tags":["x","y"]}`},
		{name: "an array in another order", seed: func(r *storedPatchRow) { r.Tags = []string{"x", "y"} }, body: `{"tags":["y","x"]}`, changed: true},
		{name: "a cleared array that is empty", seed: func(r *storedPatchRow) { r.Tags = []string{} }, body: `{"tags":null}`},
		{name: "an append to a NULL array", seed: func(r *storedPatchRow) { r.Tags = nil }, body: `[{"op":"array_append","path":"/tags","value":"x"}]`, changed: true},
		{name: "an integer appended", body: `[{"op":"array_append","path":"/counts","value":9007199254740993}]`, changed: true},
		{name: "a timestamp appended", body: `[{"op":"array_append","path":"/times","value":"2026-10-01T02:00:00+02:00"}]`, changed: true},
		{name: "a boolean appended", body: `[{"op":"array_append","path":"/flags","value":true}]`, changed: true},
		{name: "a float appended", body: `[{"op":"array_append","path":"/ratios","value":1.25}]`, changed: true},
		{name: "a UUID appended", body: `[{"op":"array_append","path":"/refs","value":"` + uuid.Nil.String() + `"}]`, changed: true},
		{name: "an array's float zero of the other sign", seed: func(r *storedPatchRow) { r.Ratios = []float64{math.Copysign(0, -1)} }, body: `{"ratios":[0]}`, changed: true},
		{name: "an array's float in another form", seed: func(r *storedPatchRow) { r.Ratios = []float64{100} }, body: `{"ratios":[1e2]}`},
		{name: "a UUID array in upper case", body: `{"refs":["0190A5B4-7C1E-7D8A-9B2C-3D4E5F6A7B8C"]}`},
		{name: "timestamps replaced in another offset", body: `{"times":["2025-12-31T19:00:00-05:00"]}`},
		{name: "an array stored in other spacing", only: SQLite, seed: func(r *storedPatchRow) { r.Tags = []string{"x", "y"} },
			seedSQL: map[Kind]string{SQLite: `UPDATE patch_rows SET tags = '[ "x", "y" ]'`}, body: `{"tags":["x","y"]}`},
		{name: "an array's elements stored in another form", seedSQL: map[Kind]string{
			SQLite:   `UPDATE patch_rows SET refs = upper(refs), times = '["2025-12-31T19:00:00-05:00"]', ratios = '[5e-1]'`,
			Postgres: `UPDATE patch_rows SET ratios = '{5e-1}'`,
		}, body: `{"refs":["0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c"],"times":["2026-01-01T00:00:00Z"],"ratios":[0.5]}`},
		{name: "one column of two changing", body: `{"rank":1,"name":"b"}`, changed: true},
		{name: "the server's columns do not count", body: `{"rank":1,"version":7,"updated_at":"2001-01-01T00:00:00Z"}`},
		{name: "a nullable wrapper's value replaced", body: `[{"op":"replace","path":"/score","value":7}]`, changed: true},
		{name: "a nullable wrapper removed", body: `[{"op":"remove","path":"/score"}]`, changed: true},
		{name: "a nullable wrapper's time replaced", body: `[{"op":"replace","path":"/seen_at","value":"2026-05-06T07:08:09Z"}]`, changed: true},
		{name: "a nullable wrapper's time removed", body: `[{"op":"remove","path":"/seen_at"}]`, changed: true},
		{name: "a nullable wrapper merged, another merged null", body: `{"score":9,"seen_at":null}`, changed: true},
		{name: "a nullable wrapper set to what it holds", body: `{"score":3}`},
		{name: "an add of a whole field", body: `[{"op":"add","path":"/note","value":"x"}]`, changed: true},

		// The fold cases (BuildPatchSQL_FoldsOrderedOps).
		{name: "two appends keep both", body: `[{"op":"array_append","path":"/tags","value":"one"},{"op":"array_append","path":"/tags","value":"two"}]`, changed: true},
		{name: "replace then append", body: `[{"op":"replace","path":"/tags","value":["a"]},{"op":"array_append","path":"/tags","value":"b"}]`, changed: true},
		{name: "replace then appends", body: `[{"op":"replace","path":"/tags","value":["a"]},{"op":"array_append","path":"/tags","value":"b"},{"op":"array_append","path":"/tags","value":"c"}]`, changed: true},
		{name: "append then replace discards the append", body: `[{"op":"array_append","path":"/tags","value":"x"},{"op":"replace","path":"/tags","value":["a"]}]`, changed: true},
		{name: "remove then append starts empty", body: `[{"op":"remove","path":"/tags"},{"op":"array_append","path":"/tags","value":"b"}]`, changed: true},
		{name: "append then remove", body: `[{"op":"array_append","path":"/tags","value":"b"},{"op":"remove","path":"/tags"}]`, changed: true},
		{name: "last scalar replace wins", body: `[{"op":"replace","path":"/name","value":"x"},{"op":"replace","path":"/name","value":"y"}]`, changed: true},
		{name: "interleaved columns", body: `[{"op":"array_append","path":"/tags","value":"t1"},{"op":"replace","path":"/name","value":"n"},{"op":"array_append","path":"/tags","value":"t2"}]`, changed: true},
		{name: "a patched stamp column assigned once", body: `[{"op":"replace","path":"/updated_at","value":"2001-01-01T00:00:00Z"},{"op":"replace","path":"/name","value":"n"}]`, changed: true},
		{name: "a counter moves one past what it holds, once", body: `[{"op":"replace","path":"/version","value":99},{"op":"replace","path":"/name","value":"n"}]`, changed: true},
		{name: "a patch writing only server columns writes nothing", body: `[{"op":"replace","path":"/version","value":99},{"op":"replace","path":"/updated_at","value":"2001-01-01T00:00:00Z"}]`},

		// The applier cases (Applier_AgreesWithTheDatabase).
		{name: "merge scalars", body: `{"name":"new","rank":5}`, changed: true},
		{name: "merge into a map", body: `{"meta":{"nested":{"x":null,"z":3},"k2":"v","list":[1,2]}}`, changed: true},
		{name: "merge into a struct document", body: `{"dims":{"w":9}}`, changed: true},
		{name: "merge into a typed map", body: `{"labels":{"old":null,"new":"x"}}`, changed: true},
		{name: "replace an array", body: `[{"op":"replace","path":"/tags","value":["x","y"]}]`, changed: true},
		{name: "remove an array", body: `[{"op":"remove","path":"/tags"}]`, changed: true},
		{name: "remove a map", body: `[{"op":"remove","path":"/meta"}]`, changed: true},
		{name: "replace a map", body: `[{"op":"replace","path":"/meta","value":{"a":1}}]`, changed: true},
		{name: "replace a struct document", body: `[{"op":"replace","path":"/dims","value":{"w":1}}]`, changed: true},
		{name: "replace with null", body: `[{"op":"replace","path":"/note","value":null}]`, changed: true},
		{name: "replace a map member", body: `[{"op":"replace","path":"/meta/k","value":2}]`, changed: true},
		{name: "a map member set to what it holds", body: `[{"op":"replace","path":"/meta/k","value":1}]`},
		{name: "add and remove nested map members", body: `[{"op":"add","path":"/meta/nested/z","value":3},{"op":"remove","path":"/meta/nested/x"}]`, changed: true},
		{name: "replace a struct document member", body: `[{"op":"replace","path":"/dims/w","value":9}]`, changed: true},
		{name: "add and remove typed map members", body: `[{"op":"add","path":"/labels/new","value":"x"},{"op":"remove","path":"/labels/old"}]`, changed: true},
		{name: "a typed map member named as an index", body: `[{"op":"add","path":"/labels/0","value":"x"}]`, changed: true},
		{name: "members of a map the patch wrote whole", body: `[{"op":"replace","path":"/meta","value":{"a":1}},{"op":"add","path":"/meta/b","value":2}]`, changed: true},
		{name: "members of a removed map", body: `[{"op":"remove","path":"/meta"},{"op":"add","path":"/meta/x","value":1}]`, changed: true},
		{name: "members of a map cleared with null", body: `[{"op":"replace","path":"/meta","value":null},{"op":"add","path":"/meta/x","value":1}]`, changed: true},
		{name: "members of a removed typed map", body: `[{"op":"remove","path":"/labels"},{"op":"add","path":"/labels/a","value":"b"}]`, changed: true},
		{name: "a member, then the map cleared with null", body: `[{"op":"add","path":"/meta/x","value":1},{"op":"replace","path":"/meta","value":null}]`, changed: true},
		{name: "a member, then the map removed", body: `[{"op":"add","path":"/meta/x","value":1},{"op":"remove","path":"/meta"}]`, changed: true},
		{name: "remove struct members the type omits when zero", body: `[{"op":"add","path":"/dims/note","value":"x"},{"op":"add","path":"/dims/at","value":3},{"op":"remove","path":"/dims/note"},{"op":"remove","path":"/dims/at"}]`},
		{name: "the operation a row fails, after others", body: `[{"op":"replace","path":"/name","value":"x"},{"op":"replace","path":"/meta/k","value":2},{"op":"remove","path":"/labels/gone"},{"op":"remove","path":"/meta/zz"}]`,
			refused: core.CodePatchValueInvalid.String()},

		// The member updates (Update_Members), on the map column.
		{name: "members written in one statement", seed: func(r *storedPatchRow) {
			r.Meta = map[string]any{"color": "blue", "old": true, "n": map[string]any{"k": "v"}}
		}, body: `[{"op":"replace","path":"/meta/color","value":"red"},{"op":"add","path":"/meta/n/size","value":{"w":2}},{"op":"remove","path":"/meta/old"}]`, changed: true},
		{name: "an add below a member that is not an object", seed: func(r *storedPatchRow) { r.Meta = map[string]any{"color": "green"} },
			body: `[{"op":"add","path":"/meta/color/x","value":1}]`, refused: core.CodePatchValueInvalid.String()},

		// What a store refuses, every store alike.
		{name: "a test", body: `[{"op":"test","path":"/note","value":"n"}]`, refused: core.CodeTestOpSQL.String()},
		{name: "a copy", body: `[{"op":"copy","from":"/note","path":"/note"}]`, refused: core.CodeOpNotSQLCompilable.String()},
		{name: "a move", body: `[{"op":"move","from":"/note","path":"/note"}]`, refused: core.CodeOpNotSQLCompilable.String()},
		{name: "an array element of a document", body: `[{"op":"replace","path":"/doc/e/0","value":3}]`, refused: core.CodeOpNotSQLCompilable.String()},
		{name: "the end of a document's array", body: `[{"op":"add","path":"/doc/e/-","value":3}]`, refused: core.CodeOpNotSQLCompilable.String()},
		{name: "an append below a field", body: `[{"op":"array_append","path":"/meta/list","value":1}]`, refused: core.CodeOpNotSQLCompilable.String()},
		{name: "an append to a document", seed: storedDocumentSeed("doc", `[1]`), body: `[{"op":"array_append","path":"/doc","value":2}]`, refused: core.CodeOpNotSQLCompilable.String()},
		{name: "an identity column counted", body: `{"rank":2}`, increments: []string{"id"}, refused: "identity"},
	}
	// A document's numbers compare by value and display scale exactly,
	// however many digits they take: plain is 10^1001, past a float64's
	// range, which exact, read as its text, holds.
	plain := "1" + strings.Repeat("0", 1001)
	for _, n := range []struct {
		name, stored, written string
		changed               bool
	}{
		{"a document number past a thousand digits, stored in another exponent", "10e1000", "1e1001", false},
		{"a document number past a thousand digits, stored in plain notation", plain, "1e1001", false},
		{"a document number's fraction an exponent covers, past a thousand digits", "1.0e1001", "1e1001", false},
		{"a document number past a thousand digits, stored at another display scale", plain + ".0", "1e1001", true},
		{"another document number past a thousand digits", "1e1001", "1e1002", true},
		{"a document number's scale past a thousand digits, stored in another spelling", "0.1e-999", "1e-1000", false},
		{"a document number's scale past a thousand digits, written at another scale", "1e-1000", "1.0e-1000", true},
		{"a document's zero at a scale past a thousand digits, stored negative", "-0.0e-1000", "0e-1001", false},
	} {
		cases = append(cases, storedPatchCase{name: n.name, seedSQL: storedSeedSQL("exact", `'{"a":`+n.stored+`}'`),
			body: `[{"op":"replace","path":"/exact","value":{"a":` + n.written + `}}]`, changed: n.changed})
	}
	for _, column := range []string{"doc", "raw", "body"} {
		cases = append(cases, storedMemberCases(column)...)
		cases = append(cases, storedMergeCases(t, column)...)
		cases = append(cases, storedQuotedMemberCases(t, column)...)
	}
	return cases
}

// storedQuotedMemberCases are the member writes of names holding quotes
// and backslashes (BuildPatchSQL_SQLiteWritesAQuoteOrBackslashInAMember),
// and the labels of PathLabels_ReachTheMemberEncodingJSONNames, on the
// document column column.
func storedQuotedMemberCases(t *testing.T, column string) []storedPatchCase {
	t.Helper()
	var out []storedPatchCase
	for _, name := range []string{`x"y`, `x\y`, `"`, `\`, `\"`, `x".y`, `it's a.b[0]$<&>`, `q"u\o'te a.b[0]$<&>`} {
		start, err := json.Marshal(map[string]any{"ok": map[string]any{}, name: map[string]any{"z0": 0}})
		require.NoError(t, err)
		for _, op := range []struct {
			what string
			op   map[string]any
		}{
			{"added", map[string]any{"op": "add", "path": core.FormatPointer(column, name+"+"), "value": 1}},
			{"replaced", map[string]any{"op": "replace", "path": core.FormatPointer(column, name), "value": 1}},
			{"removed", map[string]any{"op": "remove", "path": core.FormatPointer(column, name)}},
			{"added below", map[string]any{"op": "add", "path": core.FormatPointer(column, "ok", name), "value": 1}},
			{"added inside", map[string]any{"op": "add", "path": core.FormatPointer(column, name, "z"), "value": 1}},
		} {
			body, err := json.Marshal([]map[string]any{op.op})
			require.NoError(t, err)
			c := storedPatchCase{name: column + ": " + op.what + " " + name, seed: storedDocumentSeed(column, string(start)), body: string(body), changed: true}
			out = append(out, c)
		}
	}
	return out
}

// storedMemberCases are the member cases (BuildPatchSQL_Members) on the
// document column column: their patches, and their merge patches where
// they have one.
func storedMemberCases(column string) []storedPatchCase {
	var out []storedPatchCase
	for _, tc := range memberCases() {
		c := storedPatchCase{name: column + ": " + tc.name, seed: storedDocumentSeed(column, tc.start), body: onColumn(tc.ops, column)}
		c.changed = tc.want != "" && !jsonTextEqual(tc.start, tc.want)
		if tc.want == "" {
			c.refused = core.CodePatchValueInvalid.String()
		}
		out = append(out, c)
		if tc.merge != "" {
			merged := c
			merged.name, merged.body = column+": "+tc.name+", as a merge", onColumn(tc.merge, column)
			out = append(out, merged)
		}
	}
	return out
}

// storedMergeCases are the merge cases (BuildPatchSQL_Merge) on the
// document column column.
func storedMergeCases(t *testing.T, column string) []storedPatchCase {
	var out []storedPatchCase
	for _, tc := range mergeCases(t) {
		c := storedPatchCase{name: column + ": merge: " + tc.name, seed: storedDocumentSeed(column, tc.start), body: onColumn(tc.body, column)}
		c.changed = !jsonTextEqual(tc.start, tc.want)
		out = append(out, c)
	}
	return out
}

// onColumn is body, a patch of the doc column, patching column instead.
func onColumn(body, column string) string {
	body = strings.ReplaceAll(body, `"/doc`, `"/`+column)
	return strings.Replace(body, `{"doc":`, `{"`+column+`":`, 1)
}

// jsonTextEqual reports whether two document texts, "" for NULL, hold one
// document, numbers as written.
func jsonTextEqual(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	var x, y any
	ok := json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil
	return ok && reflect.DeepEqual(x, y)
}

// storedPatchFixture is the patch_rows table on one database, the
// resource over it, and how a row there is read and stored.
type storedPatchFixture struct {
	b        sqlBackend
	db       *sql.DB
	r        *parse.Resource[storedPatchRow]
	table    sqlTable
	columns  map[string]core.TypeInfo
	identity *backend.Resource
	at       time.Time
}

// storedPatchTable is the patch_rows table: each column of its kind, the
// collations, citext and enum the change predicate cases compare under, a
// padded decimal and a REAL one, and on PostgreSQL a declared json column
// and documents in TEXT columns.
func storedPatchTable(columns map[string]core.TypeInfo, order []string) sqlTable {
	table := sqlTable{name: "patch_rows", key: []string{"id"}}
	for _, name := range order {
		table.columns = append(table.columns, sqlColumn{name: name, kind: columns[name]})
	}
	table = table.holding("name", SQLite, "TEXT COLLATE NOCASE").holding("name", Postgres, "TEXT COLLATE ci")
	table = table.holding("email", SQLite, "TEXT COLLATE NOCASE").holding("email", Postgres, "citext")
	table = table.holding("mood", Postgres, "mood")
	table = table.holding("price", SQLite, "NUMERIC").holding("price", Postgres, "NUMERIC(12,2)")
	table = table.holding("amount", SQLite, "REAL")
	// sql.NullTime scans a time.Time, which SQLite's driver returns for a
	// DATETIME column alone.
	table = table.holding("seen_at", SQLite, "DATETIME")
	return table.holding("raw", Postgres, "JSON").holding("body", Postgres, "TEXT").holding("exact", Postgres, "TEXT")
}

func newStoredPatchFixture(t *testing.T, b sqlBackend) *storedPatchFixture {
	t.Helper()
	provider, db := b.open(t)
	if b.engine == Postgres {
		for _, stmt := range []string{
			`CREATE EXTENSION IF NOT EXISTS citext SCHEMA public`,
			`CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy')`,
			`CREATE COLLATION ci (provider = icu, locale = 'und-u-ks-level2', deterministic = false)`,
		} {
			_, err := db.ExecContext(t.Context(), stmt)
			require.NoError(t, err, stmt)
		}
	}
	all := []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpArrayAppend, patch.PatchOpMove, patch.PatchOpCopy, patch.PatchOpTest}
	members := []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove}
	r, err := parse.NewResource[storedPatchRow](provider, "patch_rows", parse.ColumnType("raw", core.SQLJSON), parse.CustomizeSchemas(func(s parse.Schemas) error {
		for path, column := range map[string]string{"/doc/**": "doc", "/raw/**": "raw", "/body/**": "body", "/meta/**": "meta"} {
			err := s.Patch.AddPath(path, &patch.PathSchema{AllowedOps: all, DataType: core.FieldTypeAny, SQLColumn: column})
			if err != nil {
				return err
			}
		}
		for path, column := range map[string]string{"/dims/*": "dims", "/labels/*": "labels"} {
			err := s.Patch.AddPath(path, &patch.PathSchema{AllowedOps: members, DataType: core.FieldTypeAny, SQLColumn: column})
			if err != nil {
				return err
			}
		}
		return nil
	}))
	require.NoError(t, err)

	columns := map[string]core.TypeInfo{}
	var order []string
	rt := reflect.TypeFor[storedPatchRow]()
	for i := range rt.NumField() {
		f := rt.Field(i)
		name := f.Tag.Get("db")
		kind := core.TypeOf(f.Type)
		declared, ok := r.ColumnSQLType(name)
		if ok {
			kind.SQLType = declared
		}
		columns[name] = kind
		order = append(order, name)
	}
	f := &storedPatchFixture{
		b: b, db: db, r: r, table: storedPatchTable(columns, order), columns: columns,
		identity: &backend.Resource{Table: "patch_rows", Key: []string{"id"}},
		at:       time.Date(2026, 11, 27, 12, 0, 0, 123_456_000, time.UTC),
	}
	b.create(t, db, f.table)
	return f
}

// reset stores the seeded row as tc seeds it.
func (f *storedPatchFixture) reset(t *testing.T, tc storedPatchCase) {
	t.Helper()
	_, err := f.db.ExecContext(t.Context(), "DELETE FROM patch_rows")
	require.NoError(t, err)
	row := storedPatchSeed()
	if tc.seed != nil {
		tc.seed(&row)
	}
	f.b.insert(t, f.db, f.table, recordRows(t, f.table, []storedPatchRow{row})...)
	stmt, ok := tc.seedSQL[f.b.engine]
	if ok {
		_, err := f.db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
}

// storedCell is a stored value in the domain filter.ColumnValue reads it
// in, and whether it is not NULL: a document's JSON null is a value.
type storedCell struct {
	Value   any
	Present bool
}

// readExpr is how row reads column on f's engine: as stored on SQLite; on
// PostgreSQL an array and a document as JSON text, a value type as parse
// reads it (filter.Dialect.ReadColumn), and any type a driver scans into no
// Go value of its own as text.
func (f *storedPatchFixture) readExpr(column string) string {
	quoted := f.b.dialect.QuoteIdent(column)
	if f.b.engine == SQLite {
		return quoted
	}
	kind := f.columns[column]
	switch kind.Type {
	case core.FieldTypeArray:
		switch kind.Elem {
		case core.FieldTypeInterval:
			quoted = f.b.dialect.ReadColumn(quoted, core.SQLIntervalArray)
		case core.FieldTypeInet:
			quoted = f.b.dialect.ReadColumn(quoted, core.SQLInetArray)
		case core.FieldTypeCIDR:
			quoted = f.b.dialect.ReadColumn(quoted, core.SQLCIDRArray)
		}
		return "array_to_json(" + quoted + ")::text"
	case core.FieldTypeJSON, core.FieldTypeObject:
		return quoted + "::text"
	case core.FieldTypeInt, core.FieldTypeFloat, core.FieldTypeBool, core.FieldTypeTimestamp:
		return quoted
	case core.FieldTypeInterval:
		return f.b.dialect.ReadColumn(quoted, core.SQLInterval)
	case core.FieldTypeInet:
		return f.b.dialect.ReadColumn(quoted, core.SQLInet)
	}
	return quoted + "::text"
}

// row reads the stored row, each column in filter.ColumnValue's domain.
func (f *storedPatchFixture) row(t *testing.T) map[string]storedCell {
	t.Helper()
	exprs := make([]string, len(f.table.columns))
	values := make([]any, len(f.table.columns))
	dest := make([]any, len(f.table.columns))
	for i, c := range f.table.columns {
		exprs[i] = f.readExpr(c.name)
		dest[i] = &values[i]
	}
	query := "SELECT " + strings.Join(exprs, ", ") + " FROM patch_rows WHERE id = 1"
	require.NoError(t, f.db.QueryRowContext(t.Context(), query).Scan(dest...), query)
	out := make(map[string]storedCell, len(values))
	for i, c := range f.table.columns {
		out[c.name] = f.cell(t, c.name, values[i])
	}
	return out
}

// cell is v, column's stored value or one Apply writes, in
// filter.ColumnValue's domain.
func (f *storedPatchFixture) cell(t *testing.T, column string, v any) storedCell {
	t.Helper()
	kind := f.columns[column]
	field := &filter.FieldSchema{ExternalName: column, DataType: kind.Type, Elem: kind.Elem, NumberKind: kind.NumberKind}
	value, present, err := filter.ColumnValue(field, v)
	require.NoError(t, err, "column %s: %#v", column, v)
	c := storedCell{Value: value, Present: present}
	return c
}

// stored is row as a store holds it: each value in the form a row stores
// (core.StorageValue), a document's JSON null as its text.
func (f *storedPatchFixture) stored(t *testing.T, row map[string]storedCell) map[string]any {
	t.Helper()
	out := make(map[string]any, len(row))
	for column, c := range row {
		kind := f.columns[column]
		switch {
		case !c.Present:
			out[column] = nil
		case c.Value == nil:
			out[column] = "null"
		default:
			v, err := core.StorageValue(kind, c.Value)
			require.NoError(t, err, "column %s", column)
			out[column] = v
		}
	}
	return out
}

// options are the update's: the stamp, the version and tc's counters.
func (f *storedPatchFixture) options(tc storedPatchCase) []parse.UpdateOption {
	opts := []parse.UpdateOption{parse.Stamp("updated_at", f.at), parse.Increment("version")}
	for _, column := range tc.increments {
		opts = append(opts, parse.Increment(column))
	}
	return opts
}

// apply is what a store that holds rows itself does with tc's update of
// the stored row before: compose it under the update's options, refuse an
// identity write, and apply it to the row's stored values.
func (f *storedPatchFixture) apply(t *testing.T, tc storedPatchCase, parsed *patch.ParsedPatch, before map[string]storedCell) (map[string]storedCell, bool, error) {
	t.Helper()
	update, write, err := f.applyStored(tc, parsed, f.stored(t, before))
	if err != nil {
		return nil, false, err
	}
	columns := update.Columns()
	require.Len(t, write.Values, len(columns))
	after := maps.Clone(before)
	for i, column := range columns {
		after[column] = f.cell(t, column, write.Values[i])
	}
	return after, write.Changed, nil
}

// applyStored composes tc's update and applies it to stored, the row's
// values as a store holds them.
func (f *storedPatchFixture) applyStored(tc storedPatchCase, parsed *patch.ParsedPatch, stored map[string]any) (*patch.ComposedPatch, patch.RowWrite, error) {
	options := backend.UpdateOptions{Stamp: &backend.Stamp{Column: "updated_at", At: f.at}, Increments: append([]string{"version"}, tc.increments...)}
	update, err := options.Compose(parsed)
	if err != nil {
		return nil, patch.RowWrite{}, err
	}
	err = f.identity.CheckUpdate(update)
	if err != nil {
		return nil, patch.RowWrite{}, err
	}
	write, err := update.Apply(func(column string) (any, bool) {
		v, ok := stored[column]
		return v, ok
	})
	if err != nil {
		return nil, patch.RowWrite{}, err
	}
	return update, write, nil
}

// guardedUpdate runs parsed's update of the stored row as BuildPatchSQL
// writes it with its change predicate, and reports whether it wrote the
// row: the predicate as the database evaluates it, before any read a
// resource makes.
func (f *storedPatchFixture) guardedUpdate(t *testing.T, parsed *patch.ParsedPatch) (bool, error) {
	t.Helper()
	d := patchDialectOf(t, f.b)
	res, err := patch.BuildPatchSQL(parsed, d, patch.WithChangePredicate())
	require.NoError(t, err)
	stmt := "UPDATE patch_rows SET " + res.SetClause + " WHERE id = " + d.Placeholder(res.ParamCount+1) + " AND " + res.ChangePredicate
	out, err := f.db.ExecContext(t.Context(), stmt, append(res.Args, 1)...)
	if err != nil {
		return false, err
	}
	n, err := out.RowsAffected()
	require.NoError(t, err)
	return n == 1, nil
}

// storedText is the stored row's column as the database holds it, as text.
func (f *storedPatchFixture) storedText(t *testing.T, column string) string {
	t.Helper()
	var text string
	query := "SELECT " + f.b.dialect.QuoteIdent(column) + " FROM patch_rows WHERE id = 1"
	require.NoError(t, f.db.QueryRowContext(t.Context(), query).Scan(&text), query)
	return text
}

func (f *storedPatchFixture) parse(t *testing.T, body string) *patch.ParsedPatch {
	t.Helper()
	var parsed *patch.ParsedPatch
	var err error
	if strings.HasPrefix(body, "[") {
		parsed, err = f.r.ParsePatch([]byte(body))
	} else {
		parsed, err = f.r.ParseMergePatch([]byte(body))
	}
	require.NoError(t, err, body)
	return parsed
}

// sameRefusal asserts that memory, Apply's error, is sql's, the update's:
// one ParseError, code, message, path and index alike, or one text.
func sameRefusal(t *testing.T, sqlErr, memory error, code string) {
	t.Helper()
	require.Error(t, sqlErr, "the update")
	require.Error(t, memory, "Apply")
	pe, isParse := errors.AsType[*core.ParseError](sqlErr)
	if !isParse {
		assert.Contains(t, sqlErr.Error(), code)
		assert.Equal(t, sqlErr.Error(), memory.Error())
		return
	}
	assert.Equal(t, code, pe.Code, "%v", sqlErr)
	mpe, ok := errors.AsType[*core.ParseError](memory)
	require.True(t, ok, "Apply: %v is no ParseError", memory)
	assert.Equal(t, pe.Code, mpe.Code)
	assert.Equal(t, pe.Message, mpe.Message)
	assert.Equal(t, pe.Path, mpe.Path)
	assert.Equal(t, pe.Index, mpe.Index)
}

// ComposedPatch_ApplyAgreesWithTheDatabase runs each case on the engine
// and through Apply over the row's stored values: the row the update
// leaves is the stored row with Apply's values written (stamp and counter
// included), compared in filter.ColumnValue's domain; the update that
// skips unchanged rows writes the row exactly when Apply reports it
// changed; and a patch the update refuses Apply refuses with the same
// error.
func composedPatchApplyAgreesWithTheDatabase(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		f := newStoredPatchFixture(t, b)
		names := map[string]bool{}
		for _, tc := range storedPatchCases(t) {
			require.False(t, names[tc.name], "case %q is named twice", tc.name)
			names[tc.name] = true
			if tc.only != "" && tc.only != b.engine {
				continue
			}
			t.Run(tc.name, func(t *testing.T) {
				ctx := t.Context()
				parsed := f.parse(t, tc.body)
				f.reset(t, tc)
				before := f.row(t)
				want, memoryChanged, memoryErr := f.apply(t, tc, parsed, before)

				_, err := f.r.Update(ctx, parsed, parse.ByID(int64(1)), parse.Scope{}, f.options(tc)...)
				if tc.refused != "" {
					sameRefusal(t, err, memoryErr, tc.refused)
					assert.Equal(t, before, f.row(t), "a refused update writes nothing")
					return
				}
				require.NoError(t, err)
				require.NoError(t, memoryErr, "Apply")
				assert.Equal(t, want, f.row(t), "the row the update leaves is the stored row with Apply's values")

				changed := tc.changed
				override, ok := tc.on[b.engine]
				if ok {
					changed = override
				}
				assert.Equal(t, changed, memoryChanged, "Apply's verdict")
				f.reset(t, tc)
				res, err := f.r.Update(ctx, parsed, parse.ByID(int64(1)), parse.Scope{}, append(f.options(tc), parse.SkipUnchanged())...)
				require.NoError(t, err)
				assert.Equal(t, changed, len(res.Changed) == 1, "the update skipping unchanged rows writes the row")
				if !changed {
					assert.Equal(t, before, f.row(t), "a row the update skips keeps its stamp and counter")
				}
			})
		}

		// A counter at its largest moves past it nowhere: PostgreSQL fails
		// the update (bigint out of range), and SQLite writes a REAL the
		// row's integer cannot scan, which fails it too. Apply refuses it.
		t.Run("a counter at its largest", func(t *testing.T) {
			tc := storedPatchCase{seed: func(r *storedPatchRow) { r.Version = math.MaxInt64 }, body: `{"rank":2}`}
			parsed := f.parse(t, tc.body)
			f.reset(t, tc)
			before := f.row(t)
			_, _, memoryErr := f.apply(t, tc, parsed, before)
			require.ErrorContains(t, memoryErr, "patch: counter column version holds 9223372036854775807, the largest integer it can")
			_, err := f.r.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{}, f.options(tc)...)
			require.Error(t, err)
			assert.Equal(t, before, f.row(t), "the failed update writes nothing")
		})

		// A document in a text column that holds no JSON has no value to
		// compare: PostgreSQL's cast to jsonb fails the change predicate,
		// SQLite's parse_canonical fails it, and Apply refuses the stored
		// text; the resource refuses to read it. A statement that compares
		// nothing replaces it.
		t.Run("a text column's document stored as text that is not JSON", func(t *testing.T) {
			tc := storedPatchCase{body: `[{"op":"replace","path":"/body","value":{"a":1}}]`}
			parsed := f.parse(t, tc.body)
			f.reset(t, tc)
			stored := f.stored(t, f.row(t))
			_, err := f.db.ExecContext(t.Context(), `UPDATE patch_rows SET body = 'not json'`)
			require.NoError(t, err)
			stored["body"] = "not json"

			_, _, memoryErr := f.applyStored(tc, parsed, stored)
			require.ErrorContains(t, memoryErr, "patch: comparing column body: decoding the stored document: invalid character")
			_, err = f.guardedUpdate(t, parsed)
			want := map[Kind]string{
				SQLite:   "parse_canonical: document: decoding the stored document: invalid character",
				Postgres: "invalid input syntax for type json",
			}
			require.ErrorContains(t, err, want[b.engine])
			_, err = f.r.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{}, append(f.options(tc), parse.SkipUnchanged())...)
			require.Error(t, err)
			assert.Equal(t, "not json", f.storedText(t, "body"), "the failed updates write nothing")

			_, err = f.r.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{}, f.options(tc)...)
			require.NoError(t, err)
			assert.JSONEq(t, `{"a":1}`, f.storedText(t, "body"))
		})

		// A document number outside PostgreSQL's numeric range, which only
		// a json or text column holds there, compares exactly on SQLite and
		// in Apply. PostgreSQL's cast to jsonb refuses it, stored or
		// written, so an update that compares it fails rather than
		// guessing; one that compares nothing writes it.
		t.Run("a document number outside PostgreSQL's numeric range", func(t *testing.T) {
			for _, n := range []struct {
				stored, written string
				changed         bool
			}{
				{`{"a":1e200000}`, `{"a":10e199999}`, false},
				{`{"a":1}`, `{"a":1e-200000}`, true},
			} {
				tc := storedPatchCase{body: `[{"op":"replace","path":"/exact","value":` + n.written + `}]`}
				parsed := f.parse(t, tc.body)
				seed := func() {
					f.reset(t, tc)
					_, err := f.db.ExecContext(t.Context(), "UPDATE patch_rows SET exact = "+f.b.dialect.Placeholder(1), n.stored)
					require.NoError(t, err)
				}
				seed()
				_, write, err := f.applyStored(tc, parsed, f.stored(t, f.row(t)))
				require.NoError(t, err, "Apply")
				assert.Equal(t, n.changed, write.Changed, "Apply's verdict on %s", n.stored)

				changed, err := f.guardedUpdate(t, parsed)
				if b.engine == Postgres {
					require.ErrorContains(t, err, "value overflows numeric format")
				} else {
					require.NoError(t, err)
					assert.Equal(t, n.changed, changed, "the predicate's verdict on %s", n.stored)
				}
				seed()
				res, err := f.r.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{}, append(f.options(tc), parse.SkipUnchanged())...)
				if b.engine == Postgres {
					require.ErrorContains(t, err, "value overflows numeric format")
					assert.Equal(t, n.stored, f.storedText(t, "exact"), "the failed updates write nothing")
				} else {
					require.NoError(t, err)
					assert.Equal(t, n.changed, len(res.Changed) == 1, "the update skipping unchanged rows writes the row")
				}

				seed()
				_, err = f.r.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{}, f.options(tc)...)
				require.NoError(t, err)
				assert.Equal(t, n.written, f.storedText(t, "exact"))
			}
		})

		// A scalar that holds no value of its column's type, which only
		// SQLite stores, compares as stored there and in Apply, as no form
		// of a value: the write that stores parse's text changes it. A
		// legacy midnight timestamp in a date column, which parse reads as
		// its date, is one; text that is no UUID or timestamp, which parse
		// refuses to read, is another.
		if b.engine == SQLite {
			t.Run("scalars stored as text of no value of their type", func(t *testing.T) {
				for _, held := range []map[string]string{
					{"day": "2026-01-02T00:00:00Z"},
					{"ref": "not a uuid", "at": "yesterday"},
				} {
					tc := storedPatchCase{body: `{"ref":"0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c","day":"2026-01-02","at":"2026-01-01T00:00:00Z"}`}
					parsed := f.parse(t, tc.body)
					f.reset(t, tc)
					stored := f.stored(t, f.row(t))
					for column, text := range held {
						_, err := f.db.ExecContext(t.Context(), "UPDATE patch_rows SET "+column+" = ?", text)
						require.NoError(t, err)
						stored[column] = text
					}

					_, write, err := f.applyStored(tc, parsed, stored)
					require.NoError(t, err, "Apply")
					assert.True(t, write.Changed, "Apply's verdict on %v", held)
					changed, err := f.guardedUpdate(t, parsed)
					require.NoError(t, err)
					assert.True(t, changed, "the predicate's verdict on %v", held)
					for column := range held {
						assert.NotEqual(t, held[column], f.storedText(t, column), "the write stores parse's text in %s", column)
					}
				}
				f.reset(t, storedPatchCase{})
				_, err := f.db.ExecContext(t.Context(), "UPDATE patch_rows SET day = '2026-01-02T00:00:00Z'")
				require.NoError(t, err)
				res, err := f.r.Update(t.Context(), f.parse(t, `{"day":"2026-01-02"}`), parse.ByID(int64(1)), parse.Scope{}, append(f.options(storedPatchCase{}), parse.SkipUnchanged())...)
				require.NoError(t, err)
				assert.Len(t, res.Changed, 1, "the update skipping unchanged rows writes the legacy date")
				assert.Equal(t, "2026-01-02", f.storedText(t, "day"))
			})
		}
	})
}
