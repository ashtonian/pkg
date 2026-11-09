package filtertest

import (
	"encoding/json"
	"math"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// NumberKeyRecord is the numeric facet key fixture: number members written
// several ways, float64, float32 and int columns, and a float32 array
// column. Its table is NumberKeySchema's.
type NumberKeyRecord struct {
	ID     string          `json:"id"     db:"id"     parse:"filter"`
	Attrs  json.RawMessage `json:"attrs"  db:"attrs"  parse:"filter" paths:"n:int:facet;r:float:facet;ns:[]float:facet"`
	F64    float64         `json:"f64"    db:"f64"    parse:"filter,facet"`
	F32    float32         `json:"f32"    db:"f32"    parse:"filter,facet"`
	I      int64           `json:"i"      db:"i"      parse:"filter,facet"`
	Ratios []float32       `json:"ratios" db:"ratios" parse:"filter,facet"`
}

// NumberKeySchema returns NumberKeyRecord's generated filter schema.
func NumberKeySchema() (*filter.ResourceSchema, error) {
	return filter.GenerateFilterSchema[NumberKeyRecord](core.NewSchemaGenerator())
}

// NumberKeyRecords is the fixture data. One value is written as 7, 7.0 and
// 7e0, another as 1.5, 1.50 and 15e-1, and zero as -0 and -0.0. f's members
// are a string and a boolean, d's ns a string, so they hold no number; g's
// document is NULL. The float64 column holds a double with 17 significant
// digits, 1e21, 1e-7 and both zeros; the float32 column 0.1, 1.5e-7 and
// 16777217 (16777216 as a float32), which a REAL holds as the double of the
// float32.
func NumberKeyRecords() []NumberKeyRecord {
	return []NumberKeyRecord{
		{ID: "a", Attrs: json.RawMessage(`{"n":7,"r":1.5,"ns":[7,7.0,1]}`), F64: 0.30000000000000004, F32: 0.1, I: 7, Ratios: []float32{0.1, 0.1, 2}},
		{ID: "b", Attrs: json.RawMessage(`{"n":7.0,"r":1.50,"ns":[7e0]}`), F64: 1e21, F32: 2, I: 0, Ratios: []float32{2}},
		{ID: "c", Attrs: json.RawMessage(`{"n":7e0,"r":15e-1,"ns":[]}`), F64: math.Copysign(0, -1), F32: 1.5e-7, I: 9007199254740993, Ratios: []float32{}},
		{ID: "d", Attrs: json.RawMessage(`{"n":3.5,"r":2,"ns":"7"}`), F64: 0, F32: 2, I: 7, Ratios: []float32{16777217}},
		{ID: "e", Attrs: json.RawMessage(`{"n":-0,"r":2.0,"ns":[-0.0,"7",true]}`), F64: 1e-7, F32: 16777217, I: -1},
		{ID: "f", Attrs: json.RawMessage(`{"n":"7","r":true}`), F64: 2.5, F32: 0.5, I: 1},
		{ID: "g"},
	}
}

// NumberKeyFacets are the buckets every facet of NumberKeyRecords counts,
// keyed by canonical decimal text: one bucket per value however it is
// written, a list's element once per row.
var NumberKeyFacets = map[string]map[string]int64{
	"attrs.n":  {"7": 3, "3.5": 1, "0": 1},
	"attrs.r":  {"1.5": 3, "2": 2},
	"attrs.ns": {"7": 2, "1": 1, "0": 1},
	"f64":      {"0.30000000000000004": 1, "1000000000000000000000": 1, "0": 3, "0.0000001": 1, "2.5": 1},
	"f32":      {"0.1": 1, "2": 2, "0.00000015": 1, "16777216": 1, "0.5": 1, "0": 1},
	"i":        {"7": 2, "0": 2, "9007199254740993": 1, "-1": 1, "1": 1},
	"ratios":   {"0.1": 1, "2": 2, "16777216": 1},
}
