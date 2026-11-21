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
//   - parse_geo_distance(lat, lon, lat2, lon2) is the WGS 84 geodesic
//     distance in meters from the stored point to (lat2, lon2), as PostGIS
//     measures geography, or NULL: $near.
//   - parse_geo_in_box(lat, lon, south, north, west, east) is 1 when the
//     stored point lies in the box or on its edge, else 0, or NULL: $within
//     and $intersects a box.
//   - parse_geo_covered_by(lat, lon, polygon) is 1 when the stored point
//     lies in the polygon (a WKB blob) or on its boundary, else 0, or NULL:
//     $within and $intersects a polygon.
//
// lat and lon are the point's members as SQLite's -> returns them: a JSON
// number, or a JSON string holding one, as PostgreSQL casts ->> text to
// float8. A member that is missing, null or anything else makes the point
// NULL.
//
// Call Register before the database opens its first connection. A statement
// on a connection without the functions fails with SQLite's "no such
// function: parse_decimal" rather than comparing a rounded double.
package parsesqlite

import (
	"database/sql/driver"
	"fmt"
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
	GeoDistanceFunc    = geo.SQLiteDistanceFunc
	GeoInBoxFunc       = geo.SQLiteInBoxFunc
	GeoCoveredByFunc   = geo.SQLiteCoveredByFunc
)

// Register registers parse_decimal, parse_decimal_key,
// parse_decimal_float32, parse_geo_distance, parse_geo_in_box and
// parse_geo_covered_by with modernc.org/sqlite, for every connection the
// "sqlite" driver opens afterwards; connections already open lack them. It
// registers once per process: later calls, from any goroutine or package,
// return the first call's result.
func Register() error { return register() }

var register = sync.OnceValue(func() error {
	for _, fn := range []struct {
		name  string
		nArgs int32
		call  func(args []driver.Value) (driver.Value, error)
	}{
		{DecimalFunc, 1, func(a []driver.Value) (driver.Value, error) { return Decimal(a[0]) }},
		{DecimalKeyFunc, 1, func(a []driver.Value) (driver.Value, error) { return DecimalKey(a[0]) }},
		{DecimalFloat32Func, 1, func(a []driver.Value) (driver.Value, error) { return DecimalFloat32(a[0]) }},
		{GeoDistanceFunc, 4, func(a []driver.Value) (driver.Value, error) { return GeoDistance(a[0], a[1], a[2], a[3]) }},
		{GeoInBoxFunc, 6, func(a []driver.Value) (driver.Value, error) { return GeoInBox(a[0], a[1], a[2], a[3], a[4], a[5]) }},
		{GeoCoveredByFunc, 3, func(a []driver.Value) (driver.Value, error) { return GeoCoveredBy(a[0], a[1], a[2]) }},
	} {
		err := sqlite.RegisterDeterministicScalarFunction(fn.name, fn.nArgs, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			return fn.call(args)
		})
		if err != nil {
			return fmt.Errorf("parsesqlite: register %s: %w", fn.name, err)
		}
	}
	return nil
})

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

// GeoDistance is parse_geo_distance: the WGS 84 geodesic distance in meters
// from the stored point (lat, lon) to the point (lat2, lon2), or nil (SQL
// NULL) when the stored point is NULL. A stored latitude beyond a pole is
// coerced as PostGIS coerces geography. It is exported for a SQLite driver
// other than modernc.org/sqlite to register under GeoDistanceFunc.
func GeoDistance(lat, lon, lat2, lon2 driver.Value) (driver.Value, error) {
	p, ok := storedPoint(lat, lon)
	if !ok {
		return nil, nil
	}
	to, err := point(GeoDistanceFunc, lat2, lon2)
	if err != nil {
		return nil, err
	}
	return geo.Distance(geo.Geodetic(p), to), nil
}

// GeoInBox is parse_geo_in_box: 1 when the stored point (lat, lon) lies in
// the box or on its edge, 0 when it does not, or nil (SQL NULL) when the
// point is NULL. It is exported for a SQLite driver other than
// modernc.org/sqlite to register under GeoInBoxFunc.
func GeoInBox(lat, lon, south, north, west, east driver.Value) (driver.Value, error) {
	p, ok := storedPoint(lat, lon)
	if !ok {
		return nil, nil
	}
	var box geo.Box
	for _, side := range []struct {
		v   driver.Value
		dst *float64
	}{{south, &box.South}, {north, &box.North}, {west, &box.West}, {east, &box.East}} {
		f, ok := numeric(side.v)
		if !ok {
			return nil, fmt.Errorf("%s: want a number for each side of the box, got %T", GeoInBoxFunc, side.v)
		}
		*side.dst = f
	}
	return truth(box.Covers(p)), nil
}

// GeoCoveredBy is parse_geo_covered_by: 1 when the stored point (lat, lon)
// lies in the polygon or on its boundary, 0 when it does not, or nil (SQL
// NULL) when the point is NULL. polygon is a little-endian one-ring polygon
// WKB. It is exported for a SQLite driver other than modernc.org/sqlite to
// register under GeoCoveredByFunc.
func GeoCoveredBy(lat, lon, polygon driver.Value) (driver.Value, error) {
	p, ok := storedPoint(lat, lon)
	if !ok {
		return nil, nil
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

// storedPoint reads a JSONB point's members as -> returns them; ok is false
// when the point is NULL.
func storedPoint(lat, lon driver.Value) (geo.Point, bool) {
	la, latOK := member(lat)
	lo, lonOK := member(lon)
	return geo.Point{Lat: la, Lon: lo}, latOK && lonOK
}

// member is a point member as -> returns it: JSON text, or NULL when the
// member is missing.
func member(v driver.Value) (float64, bool) {
	switch m := v.(type) {
	case string:
		return geo.CoordinateJSON(m)
	case []byte:
		return geo.CoordinateJSON(string(m))
	}
	return numeric(v)
}

// point reads a bound point, which the dialect always binds.
func point(fn string, lat, lon driver.Value) (geo.Point, error) {
	la, latOK := numeric(lat)
	lo, lonOK := numeric(lon)
	if !latOK || !lonOK {
		return geo.Point{}, fmt.Errorf("%s: want a number for each coordinate, got %T and %T", fn, lat, lon)
	}
	return geo.Point{Lat: la, Lon: lo}, nil
}

// numeric is an INTEGER or REAL value as a finite float64.
func numeric(v driver.Value) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return geo.Coordinate(n)
	case int64:
		return float64(n), true
	}
	return 0, false
}

func truth(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
