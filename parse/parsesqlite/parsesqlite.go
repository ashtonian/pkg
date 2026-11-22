// Package parsesqlite registers the SQL functions parse's SQLite dialect
// calls, with the modernc.org/sqlite driver. SQLite has no exact decimal
// type: a NUMERIC column or a json_extract result holds a double, so
// filter.SQLiteDialect compares, orders and facets decimals through
// deterministic functions instead. It has no geometry either, so geo
// filters on a JSONB point call functions that decide them as PostGIS does.
//
//   - parse_decimal(x) is the canonical decimal text (core.DecimalText) of
//     x, or NULL: equality, sets and facet keys compare it.
//   - parse_decimal_key(x) is a BLOB that orders bytewise as the decimal x,
//     or NULL: ranges, ORDER BY and keyset seeks compare it.
//   - parse_decimal_float32(x) is the canonical decimal text of x rounded
//     to float32, or NULL: the facet key of a float32 field's REAL column.
//
// x is a decimal column's value (an INTEGER, a REAL, or TEXT holding a JSON
// number literal) or a document member as SQLite's `->` operator returns
// it: a JSON number literal as written, or a JSON string literal holding a
// decimal in plain notation, as filter.PathValue reads a decimal member.
// Anything else is NULL, so a filter treats it as missing.
//
//   - parse_geo_near(point, latKey, lonKey, lat, lon, meters) is 1 when the
//     stored point lies within meters of (lat, lon) by the WGS 84 geodesic
//     distance, as PostGIS measures geography, else 0: $near.
//   - parse_geo_in_box(point, latKey, lonKey, south, north, west, east) is
//     1 when the stored point lies in the box or on its edge, else 0:
//     $within and $intersects a box.
//   - parse_geo_covered_by(point, latKey, lonKey, polygon) is 1 when the
//     stored point lies in the polygon (a WKB blob) or on its boundary, else
//     0: $within and $intersects a polygon.
//
// point is the stored point's JSON text; its latKey and lonKey members are
// read as PostgreSQL reads (doc->>'lat')::float8, a duplicated member by its
// last value. Each function is NULL when the point is: NULL, not an object,
// or a member missing, null or no number.
//
// Call Register before the database opens its first connection, or
// RegisterOn for a sqlite.Driver of your own. A statement on a connection
// without the functions fails with SQLite's "no such function:
// parse_decimal" rather than comparing a rounded double. Another SQLite
// driver registers the table Functions returns.
package parsesqlite

import (
	"database/sql/driver"
	"fmt"
	"slices"
	"sync"

	"modernc.org/sqlite"

	"github.com/ashtonian/pkg/parse/internal/decimals"
	"github.com/ashtonian/pkg/parse/internal/geo"
)

// The functions' SQL names, as filter.SQLiteDialect calls them.
const (
	DecimalFunc        = decimals.SQLiteValueFunc
	DecimalKeyFunc     = decimals.SQLiteKeyFunc
	DecimalFloat32Func = decimals.SQLiteFloat32Func
	GeoNearFunc        = geo.SQLiteNearFunc
	GeoInBoxFunc       = geo.SQLiteInBoxFunc
	GeoCoveredByFunc   = geo.SQLiteCoveredByFunc
)

// Function is one of the SQL functions filter.SQLiteDialect calls: the name
// it calls, the number of arguments it passes, and the implementation over
// the argument values a database/sql driver hands a scalar function. Each is
// deterministic, and a driver must register it as such for an index on it to
// be usable.
type Function struct {
	Name  string
	NArgs int
	Call  func(args []driver.Value) (driver.Value, error)
}

// functions is the table Register and RegisterOn install, in that order.
var functions = [...]Function{
	{DecimalFunc, 1, func(a []driver.Value) (driver.Value, error) { return Decimal(a[0]) }},
	{DecimalKeyFunc, 1, func(a []driver.Value) (driver.Value, error) { return DecimalKey(a[0]) }},
	{DecimalFloat32Func, 1, func(a []driver.Value) (driver.Value, error) { return DecimalFloat32(a[0]) }},
	{GeoNearFunc, 6, func(a []driver.Value) (driver.Value, error) { return GeoNear(a[0], a[1], a[2], a[3], a[4], a[5]) }},
	{GeoInBoxFunc, 7, func(a []driver.Value) (driver.Value, error) {
		return GeoInBox(a[0], a[1], a[2], a[3], a[4], a[5], a[6])
	}},
	{GeoCoveredByFunc, 4, func(a []driver.Value) (driver.Value, error) { return GeoCoveredBy(a[0], a[1], a[2], a[3]) }},
}

// Functions returns the table of functions Register installs, for a SQLite
// driver other than modernc.org/sqlite to register under the same names.
// Each call returns a new slice.
func Functions() []Function { return slices.Clone(functions[:]) }

// Register registers parse_decimal, parse_decimal_key,
// parse_decimal_float32, parse_geo_near, parse_geo_in_box and
// parse_geo_covered_by with modernc.org/sqlite, for every connection the
// "sqlite" driver opens afterwards; connections already open lack them. It
// registers once per process: later calls, from any goroutine or package,
// return the first call's result. When a name is already taken, the
// functions before it in [Functions] are registered and the rest are not.
func Register() error { return register() }

var register = sync.OnceValue(func() error {
	return registerAll(sqlite.RegisterDeterministicScalarFunction)
})

// RegisterOn registers the functions on d alone, so they reach only the
// connections d opens: a driver the caller constructs and registers under a
// name of its own with sql.Register, or opens through a connector. Call it
// once per driver, before d opens its first connection. When a name is
// already taken on d, the functions before it in [Functions] are registered
// and the rest are not.
func RegisterOn(d *sqlite.Driver) error {
	return registerAll(d.RegisterDeterministicScalarFunction)
}

type registerFunc func(name string, nArgs int32, call func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error)) error

func registerAll(register registerFunc) error {
	for _, fn := range functions {
		err := register(fn.Name, int32(fn.NArgs), func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			return fn.Call(args)
		})
		if err != nil {
			return fmt.Errorf("parsesqlite: register %s: %w", fn.Name, err)
		}
	}
	return nil
}

// Decimal is parse_decimal for one SQLite value: its canonical decimal
// text, or nil (SQL NULL) when it holds no decimal. It is exported for a
// SQLite driver other than modernc.org/sqlite to register under
// DecimalFunc.
func Decimal(v driver.Value) (driver.Value, error) {
	text, ok := decimals.Value(v)
	if !ok {
		return nil, nil
	}
	return text, nil
}

// DecimalKey is parse_decimal_key for one SQLite value: a BLOB whose bytes
// order as the decimal does, equal for equal decimals, or nil (SQL NULL)
// when it holds no decimal. It is exported for a SQLite driver other than
// modernc.org/sqlite to register under DecimalKeyFunc.
func DecimalKey(v driver.Value) (driver.Value, error) {
	text, ok := decimals.Value(v)
	if !ok {
		return nil, nil
	}
	return decimals.Key(text), nil
}

// DecimalFloat32 is parse_decimal_float32 for one SQLite value: the
// canonical decimal text of the float32 it holds (an INTEGER, a REAL or TEXT
// holding a JSON number literal, rounded to float32), or nil (SQL NULL). It
// is exported for a SQLite driver other than modernc.org/sqlite to register
// under DecimalFloat32Func.
func DecimalFloat32(v driver.Value) (driver.Value, error) {
	text, ok := decimals.Float32Value(v)
	if !ok {
		return nil, nil
	}
	return text, nil
}

// GeoNear is parse_geo_near: 1 when the stored point lies within meters of
// (lat, lon) by the WGS 84 geodesic distance, 0 when it does not, or nil
// (SQL NULL) when the point is NULL. A stored latitude beyond a pole is
// coerced as PostGIS coerces geography. It is exported for a SQLite driver
// other than modernc.org/sqlite to register under GeoNearFunc.
func GeoNear(point, latKey, lonKey, lat, lon, meters driver.Value) (driver.Value, error) {
	p, ok, err := storedPoint(GeoNearFunc, point, latKey, lonKey)
	if !ok || err != nil {
		return nil, err
	}
	operands, err := numbers(GeoNearFunc, lat, lon, meters)
	if err != nil {
		return nil, err
	}
	return truth(geo.Near(p, geo.Point{Lat: operands[0], Lon: operands[1]}, operands[2])), nil
}

// GeoInBox is parse_geo_in_box: 1 when the stored point lies in the box or
// on its edge, 0 when it does not, or nil (SQL NULL) when the point is
// NULL. It is exported for a SQLite driver other than modernc.org/sqlite to
// register under GeoInBoxFunc.
func GeoInBox(point, latKey, lonKey, south, north, west, east driver.Value) (driver.Value, error) {
	p, ok, err := storedPoint(GeoInBoxFunc, point, latKey, lonKey)
	if !ok || err != nil {
		return nil, err
	}
	sides, err := numbers(GeoInBoxFunc, south, north, west, east)
	if err != nil {
		return nil, err
	}
	return truth(geo.Box{South: sides[0], North: sides[1], West: sides[2], East: sides[3]}.Covers(p)), nil
}

// GeoCoveredBy is parse_geo_covered_by: 1 when the stored point lies in the
// polygon or on its boundary, 0 when it does not, or nil (SQL NULL) when the
// point is NULL. polygon is a little-endian one-ring polygon WKB. It is
// exported for a SQLite driver other than modernc.org/sqlite to register
// under GeoCoveredByFunc.
func GeoCoveredBy(point, latKey, lonKey, polygon driver.Value) (driver.Value, error) {
	p, ok, err := storedPoint(GeoCoveredByFunc, point, latKey, lonKey)
	if !ok || err != nil {
		return nil, err
	}
	wkb, isBlob := polygon.([]byte)
	if !isBlob {
		return nil, fmt.Errorf("%s: want the polygon as a WKB blob, got %T", GeoCoveredByFunc, polygon)
	}
	// A filter's polygon holds at most 100 points; one more closes it.
	var buf [101]geo.Point
	ring, err := geo.ReadPolygonWKB(buf[:0], wkb)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", GeoCoveredByFunc, err)
	}
	return truth(geo.CoveredBy(p, ring)), nil
}

// storedPoint reads the stored point: its JSON text and member names. ok is
// false when the point is NULL.
func storedPoint(fn string, point, latKey, lonKey driver.Value) (geo.Point, bool, error) {
	lat, latOK := latKey.(string)
	lon, lonOK := lonKey.(string)
	if !latOK || !lonOK {
		return geo.Point{}, false, fmt.Errorf("%s: want the point's member names as text, got %T and %T", fn, latKey, lonKey)
	}
	var text []byte
	switch v := point.(type) {
	case nil:
		return geo.Point{}, false, nil
	case string:
		text = []byte(v)
	case []byte:
		text = v
	default:
		return geo.Point{}, false, fmt.Errorf("%s: want the point as JSON text, got %T", fn, point)
	}
	p, ok, err := geo.ReadPointJSON(text, lat, lon)
	if err != nil {
		return geo.Point{}, false, fmt.Errorf("%s: %w", fn, err)
	}
	return p, ok, nil
}

// numbers reads the bound operands, INTEGERs or REALs.
func numbers(fn string, vs ...driver.Value) ([]float64, error) {
	out := make([]float64, len(vs))
	for i, v := range vs {
		switch n := v.(type) {
		case float64:
			out[i] = n
		case int64:
			out[i] = float64(n)
		default:
			return nil, fmt.Errorf("%s: want a number for operand %d, got %T", fn, i+1, v)
		}
	}
	return out, nil
}

func truth(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
