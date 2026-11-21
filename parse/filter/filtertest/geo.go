package filtertest

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// GeoRecord is the stored-point fixture: JSONB points written in the forms
// PostgreSQL reads with (doc->>'lat')::float8, which every backend must
// read alike. Pos declares its own member names.
type GeoRecord struct {
	ID  string          `json:"id"  db:"id"  parse:"filter" filter_ops:"eq,in"`
	Loc json.RawMessage `json:"loc" db:"loc" parse:"filter" filter_ops:"near,within,intersects,null" geo:"jsonb"`
	Pos json.RawMessage `json:"pos" db:"pos" parse:"filter" filter_ops:"near,within" geo:"jsonb" geo_lat:"$.y" geo_lon:"$.x"`
}

// GeoRecords is the fixture data. a, b, j and k's points are all (10, 10):
// numbers, strings holding numbers (spaces around b's longitude), and
// exponents. c's lacks a longitude, d's latitude is null and f's point is
// an array, so those three are NULL points; i has no point. g's latitude
// lies beyond the pole (100, which geography reads as 80) and h's longitude
// past the antimeridian (370, which wraps to 10).
func GeoRecords() []GeoRecord {
	return []GeoRecord{
		geoRecord("a", `{"lat":10,"lon":10}`, `{"y":10,"x":10}`),
		geoRecord("b", `{"lat":"10","lon":" 10 "}`, `{"y":"10","x":"10"}`),
		geoRecord("c", `{"lat":10}`, `{"y":10}`),
		geoRecord("d", `{"lat":null,"lon":10}`, `{"lat":10,"lon":10}`),
		geoRecord("f", `[10,10]`, `{"y":20,"x":20}`),
		geoRecord("g", `{"lat":100,"lon":10}`, `{"y":10,"x":10.5}`),
		geoRecord("h", `{"lat":10.5,"lon":370}`, `{"y":10.5,"x":370}`),
		{ID: "i"},
		geoRecord("j", `{"lat":1e1,"lon":1.0E1}`, `{"y":1e1,"x":10}`),
		geoRecord("k", `{"lat":"1e1","lon":"10.0"}`, `{"y":"1e1","x":"10.0"}`),
	}
}

func geoRecord(id, loc, pos string) GeoRecord {
	return GeoRecord{ID: id, Loc: json.RawMessage(loc), Pos: json.RawMessage(pos)}
}

// GeoCases are geo filters on GeoRecords and the rows each matches.
func GeoCases() []Case {
	return slices.Clone(geoCases)
}

var geoCases = []Case{
	{Name: "near reads numbers, numeric strings and exponents alike", Filter: `{"loc":{"$near":{"lat":10,"lon":10,"distance":"1km"}}}`, Want: ids("abjk")},
	{Name: "near reflects a latitude beyond the pole", Filter: `{"loc":{"$near":{"lat":80,"lon":10,"distance":"1km"}}}`, Want: ids("g")},
	{Name: "near wraps a longitude past the antimeridian", Filter: `{"loc":{"$near":{"lat":10.5,"lon":10,"distance":"1km"}}}`, Want: ids("h")},
	{Name: "not near excludes null points", Filter: `{"$not":{"loc":{"$near":{"lat":10,"lon":10,"distance":"1km"}}}}`, Want: ids("gh")},
	{Name: "within a box reads coordinates unwrapped", Filter: `{"loc":{"$within":{"top_left":{"lat":15,"lon":5},"bottom_right":{"lat":5,"lon":15}}}}`, Want: ids("abjk")},
	{Name: "not within a box excludes null points", Filter: `{"$not":{"loc":{"$within":{"top_left":{"lat":15,"lon":5},"bottom_right":{"lat":5,"lon":15}}}}}`, Want: ids("gh")},
	{Name: "within a polygon", Filter: `{"loc":{"$within":{"points":[{"lat":5,"lon":5},{"lat":15,"lon":5},{"lat":15,"lon":15},{"lat":5,"lon":15}]}}}`, Want: ids("abjk")},
	{Name: "not intersects a polygon excludes null points", Filter: `{"$not":{"loc":{"$intersects":{"points":[{"lat":5,"lon":5},{"lat":15,"lon":5},{"lat":15,"lon":15}]}}}}`, Want: ids("gh")},
	{Name: "null is a missing column only", Filter: `{"loc":{"$null":true}}`, Want: ids("i")},
	{Name: "declared members", Filter: `{"pos":{"$near":{"lat":10,"lon":10,"distance":"1km"}}}`, Want: ids("abjk")},
	{Name: "declared members wrap a longitude", Filter: `{"pos":{"$near":{"lat":10.5,"lon":10,"distance":"1km"}}}`, Want: ids("h")},
	{Name: "declared members within a box", Filter: `{"pos":{"$within":{"top_left":{"lat":25,"lon":5},"bottom_right":{"lat":5,"lon":25}}}}`, Want: ids("abfgjk")},
}

// GeoSchema returns GeoRecord's generated filter schema.
func GeoSchema() (*filter.ResourceSchema, error) {
	return filter.GenerateFilterSchema[GeoRecord](core.NewSchemaGenerator())
}

// RunGeo evaluates every GeoCases case on backend, which evaluates against
// GeoRecords.
func RunGeo(t *testing.T, backend Backend) {
	schema, err := GeoSchema()
	if err != nil {
		t.Fatalf("filtertest: geo schema: %v", err)
	}
	run(t, "", filter.NewFilterParser(schema, core.DefaultConfig()), GeoCases(), backend)
}
