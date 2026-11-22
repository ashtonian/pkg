//go:build unit || concurrency

package parsesqlite_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"

	"github.com/ashtonian/pkg/parse/internal/geo"
	"github.com/ashtonian/pkg/parse/parsesqlite"
)

func TestRegisterIsIdempotent(t *testing.T) {
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() { errs[i] = parsesqlite.Register() })
	}
	wg.Wait()
	for _, err := range errs {
		assert.NoError(t, err)
	}
	assert.NoError(t, parsesqlite.Register())
}

func open(t *testing.T) *sql.DB {
	t.Helper()
	require.NoError(t, parsesqlite.Register())
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// constructedDriver is a sqlite.Driver of the test's own with the functions
// registered on it alone. Package-level registrations never reach a
// constructed driver, so its connections have only what RegisterOn installed.
var constructedDriver = sync.OnceValues(func() (*sqlite.Driver, error) {
	d := &sqlite.Driver{}
	return d, parsesqlite.RegisterOn(d)
})

// connector opens a driver's connections without registering it by name.
type connector struct{ d driver.Driver }

func (c connector) Connect(context.Context) (driver.Conn, error) { return c.d.Open(":memory:") }
func (c connector) Driver() driver.Driver                        { return c.d }

func openOn(t *testing.T, d *sqlite.Driver) *sql.DB {
	t.Helper()
	db := sql.OpenDB(connector{d})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type database struct {
	name string
	db   *sql.DB
}

// databases opens one in-memory database per way of installing the
// functions: Register on the "sqlite" driver, RegisterOn on a constructed one.
func databases(t *testing.T) []database {
	t.Helper()
	d, err := constructedDriver()
	require.NoError(t, err)
	return []database{{"registered", open(t)}, {"constructed", openOn(t, d)}}
}

// Functions is the table both registrations install, under the names
// filter.SQLiteDialect calls.
func TestFunctions(t *testing.T) {
	var names []string
	for _, fn := range parsesqlite.Functions() {
		names = append(names, fn.Name)
	}
	assert.Equal(t, []string{
		parsesqlite.DecimalFunc, parsesqlite.DecimalKeyFunc, parsesqlite.DecimalFloat32Func,
		parsesqlite.GeoNearFunc, parsesqlite.GeoInBoxFunc, parsesqlite.GeoCoveredByFunc,
	}, names)

	fns := parsesqlite.Functions()
	fns[0].Name = "changed"
	assert.Equal(t, parsesqlite.DecimalFunc, parsesqlite.Functions()[0].Name, "Functions returns a copy")

	// A function called through the table answers as the exported function.
	got, err := parsesqlite.Functions()[0].Call([]driver.Value{"1.50"})
	require.NoError(t, err)
	assert.Equal(t, "1.5", got)
}

// A constructed driver without RegisterOn has none of the functions.
func TestConstructedDriverNeedsRegisterOn(t *testing.T) {
	db := openOn(t, &sqlite.Driver{})
	var got any
	err := db.QueryRow("SELECT parse_decimal('1.50')").Scan(&got)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such function: parse_decimal")
}

// When a name is already taken, RegisterOn names it and leaves the functions
// before it in the table registered and those after it not.
func TestRegisterOnNameTaken(t *testing.T) {
	d := &sqlite.Driver{}
	require.NoError(t, d.RegisterDeterministicScalarFunction(parsesqlite.DecimalFloat32Func, 1,
		func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) { return "taken", nil }))

	err := parsesqlite.RegisterOn(d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsesqlite: register "+parsesqlite.DecimalFloat32Func)

	db := openOn(t, d)
	var got any
	require.NoError(t, db.QueryRow("SELECT parse_decimal_key('1.5') IS NOT NULL").Scan(&got))
	assert.Equal(t, int64(1), got, "registered before the taken name")
	require.NoError(t, db.QueryRow("SELECT parse_decimal_float32(1)").Scan(&got))
	assert.Equal(t, "taken", got, "the taken name keeps its function")
	assert.Error(t, db.QueryRow("SELECT parse_geo_in_box(NULL, 'lat', 'lon', 0, 1, 0, 1)").Scan(&got), "after the taken name")
}

func TestDecimal(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want any
	}{
		{"an integer past double precision", "parse_decimal(9007199254740993)", "9007199254740993"},
		{"decimal text of 38 digits", "parse_decimal('12345678901234567890.123456789012345678')", "12345678901234567890.123456789012345678"},
		{"text with a scale and an exponent", "parse_decimal('1.0500e1')", "10.5"},
		{"a member's number as written", `parse_decimal('{"p":9007199254740993.5}' -> '$.p')`, "9007199254740993.5"},
		{"a member's string", `parse_decimal('{"p":"9.50"}' -> '$.p')`, "9.5"},
		{"a member's string with an exponent", `parse_decimal('{"p":"1e3"}' -> '$.p')`, nil},
		{"a member of another kind", `parse_decimal('{"p":true}' -> '$.p')`, nil},
		{"a missing member", `parse_decimal('{}' -> '$.p')`, nil},
		{"text that is no number", "parse_decimal('ten')", nil},
		{"a blob of no text", "parse_decimal(x'00')", nil},
		{"a blob of decimal text", "parse_decimal(CAST('10.50' AS BLOB))", nil},
		{"a blob literal of decimal text", "parse_decimal(x'31302E3530')", nil},
		{"the key of a blob of decimal text", "parse_decimal_key(CAST('10.50' AS BLOB))", nil},
		{"NULL", "parse_decimal(NULL)", nil},
	}
	for _, d := range databases(t) {
		for _, tt := range tests {
			t.Run(d.name+"/"+tt.name, func(t *testing.T) {
				var got any
				require.NoError(t, d.db.QueryRow("SELECT "+tt.expr).Scan(&got))
				assert.Equal(t, tt.want, got)
			})
		}
	}
}

// A float32 field's REAL holds the double of its float32; the function
// keys the float32.
func TestDecimalFloat32(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want any
	}{
		{"a REAL holding a float32", "(SELECT parse_decimal_float32(v) FROM f)", "0.1"},
		{"the same REAL read as a double", "(SELECT parse_decimal(v) FROM f)", "0.10000000149011612"},
		{"an integer", "parse_decimal_float32(2)", "2"},
		{"an integer past float32 precision", "parse_decimal_float32(16777217)", "16777216"},
		{"a REAL with an exponent", "parse_decimal_float32(1.5e-7)", "0.00000015"},
		{"negative zero", "parse_decimal_float32(-0.0)", "0"},
		{"a number literal", "parse_decimal_float32('2.50')", "2.5"},
		{"beyond float32's range", "parse_decimal_float32(1e39)", nil},
		{"a JSON string literal", `parse_decimal_float32('"2.5"')`, nil},
		{"text that is no number", "parse_decimal_float32('NaN')", nil},
		{"a blob of decimal text", "parse_decimal_float32(CAST('2.50' AS BLOB))", nil},
		{"NULL", "parse_decimal_float32(NULL)", nil},
	}
	for _, d := range databases(t) {
		_, err := d.db.Exec("CREATE TABLE f (v REAL)")
		require.NoError(t, err)
		_, err = d.db.Exec("INSERT INTO f VALUES (?)", float64(float32(0.1)))
		require.NoError(t, err)
		for _, tt := range tests {
			t.Run(d.name+"/"+tt.name, func(t *testing.T) {
				var got any
				require.NoError(t, d.db.QueryRow("SELECT "+tt.expr).Scan(&got))
				assert.Equal(t, tt.want, got)
			})
		}
	}
}

// The comparisons CAST(… AS NUMERIC) gets wrong past double precision
// (pkg#144) order exactly through the key.
func TestDecimalKeyOrdersExactly(t *testing.T) {
	for _, expr := range []string{
		"parse_decimal_key('12345678901234567.1') < parse_decimal_key('12345678901234567.2')",
		"parse_decimal_key('99999999999999999999') < parse_decimal_key('100000000000000000000')",
		`parse_decimal_key('{"a":9007199254740993.5}' -> '$.a') < parse_decimal_key('9007199254740993.6')`,
		"parse_decimal_key('-0.5') > parse_decimal_key('-0.51')",
		"parse_decimal_key('10.50') = parse_decimal_key(10.5)",
		"parse_decimal_key('ten') IS NULL",
	} {
		for _, d := range databases(t) {
			var holds bool
			require.NoError(t, d.db.QueryRow("SELECT "+expr).Scan(&holds), d.name, expr)
			assert.True(t, holds, d.name, expr)
		}
	}
}

// The geo functions read the stored point from its JSON text, as
// PostgreSQL reads (doc->>'lat')::float8, and are NULL when it is NULL.
func TestGeoFunctions(t *testing.T) {
	square := "x'" + hex.EncodeToString(geo.AppendPolygonWKB(nil, []geo.Point{{Lat: 0, Lon: 0}, {Lat: 0, Lon: 30}, {Lat: 30, Lon: 30}, {Lat: 30, Lon: 0}})) + "'"
	near := func(point string) string { return "parse_geo_near(" + point + ", 'lat', 'lon', 10, 20, 1)" }
	tests := []struct {
		name string
		expr string
		want any
	}{
		{"near itself", near(`'{"lat":10,"lon":20}'`), int64(1)},
		{"near, members held as strings", near(`'{"lat":"10","lon":" 20 "}'`), int64(1)},
		{"near, a duplicated member read by its last value", near(`'{"lat":50,"lon":20,"lat":10}'`), int64(1)},
		{"near, a latitude beyond the pole", "parse_geo_near('{\"lat\":100,\"lon\":20}', 'lat', 'lon', 80, 20, 1)", int64(1)},
		{"not near", near(`'{"lat":10.01,"lon":20}'`), int64(0)},
		{"near on the ellipsoid along a meridian", "parse_geo_near('{\"lat\":0,\"lon\":0}', 'lat', 'lon', 1, 0, 110600)", int64(1)},
		{"not near on the ellipsoid along a meridian", "parse_geo_near('{\"lat\":0,\"lon\":0}', 'lat', 'lon', 1, 0, 110500)", int64(0)},
		{"near, a NaN member", near(`'{"lat":"NaN","lon":20}'`), int64(0)},
		{"near, declared members", "parse_geo_near('{\"y\":10,\"x\":20}', 'y', 'x', 10, 20, 1)", int64(1)},
		{"near, a missing member", near(`'{"lat":10}'`), nil},
		{"near, a null member", near(`'{"lat":null,"lon":20}'`), nil},
		{"near, a boolean member", near(`'{"lat":true,"lon":20}'`), nil},
		{"near, a member float8 refuses", near(`'{"lat":"1_0","lon":20}'`), nil},
		{"near, an array", near(`'[10,20]'`), nil},
		{"near, a NULL point", near("NULL"), nil},
		{"in a box", `parse_geo_in_box('{"lat":10,"lon":20}', 'lat', 'lon', 0, 10, 0, 20)`, int64(1)},
		{"outside a box", `parse_geo_in_box('{"lat":10,"lon":20}', 'lat', 'lon', 0, 9.5, 0, 20)`, int64(0)},
		{"in a box, a NULL point", "parse_geo_in_box(NULL, 'lat', 'lon', 0, 10, 0, 20)", nil},
		{"covered by a polygon", `parse_geo_covered_by('{"lat":10,"lon":20}', 'lat', 'lon', ` + square + ")", int64(1)},
		{"on a polygon's edge", `parse_geo_covered_by('{"lat":0,"lon":15}', 'lat', 'lon', ` + square + ")", int64(1)},
		{"outside a polygon", `parse_geo_covered_by('{"lat":31,"lon":15}', 'lat', 'lon', ` + square + ")", int64(0)},
		{"covered by a polygon, a NULL point", "parse_geo_covered_by(NULL, 'lat', 'lon', " + square + ")", nil},
	}
	dbs := databases(t)
	for _, d := range dbs {
		for _, tt := range tests {
			t.Run(d.name+"/"+tt.name, func(t *testing.T) {
				var got any
				require.NoError(t, d.db.QueryRow("SELECT "+tt.expr).Scan(&got))
				assert.Equal(t, tt.want, got)
			})
		}
	}
	for name, expr := range map[string]string{
		"a point that is not JSON":         "parse_geo_near('{', 'lat', 'lon', 1, 1, 1)",
		"a point that is a number":         "parse_geo_near(10, 'lat', 'lon', 1, 1, 1)",
		"member names that are not text":   "parse_geo_near('{}', 1, 2, 1, 1, 1)",
		"a center of text":                 "parse_geo_near('{\"lat\":1,\"lon\":1}', 'lat', 'lon', 'north', 1, 1)",
		"a box side of text":               "parse_geo_in_box('{\"lat\":1,\"lon\":1}', 'lat', 'lon', 0, 'ten', 0, 20)",
		"a polygon that is no blob":        "parse_geo_covered_by('{\"lat\":1,\"lon\":1}', 'lat', 'lon', 'square')",
		"a polygon blob of a point":        "parse_geo_covered_by('{\"lat\":1,\"lon\":1}', 'lat', 'lon', x'0101000000')",
		"a center of text, a NULL point":   "parse_geo_near(NULL, 'lat', 'lon', 'north', 1, 1)",
		"a box side of text, a NULL point": "parse_geo_in_box(NULL, 'lat', 'lon', 0, 'ten', 0, 20)",
		"a polygon of text, a NULL point":  "parse_geo_covered_by(NULL, 'lat', 'lon', 'square')",
		"a short polygon, a NULL point":    "parse_geo_covered_by(NULL, 'lat', 'lon', x'01030000000100000000000000')",
	} {
		for _, d := range dbs {
			t.Run(d.name+"/"+name, func(t *testing.T) {
				var got any
				assert.Error(t, d.db.QueryRow("SELECT "+expr).Scan(&got))
			})
		}
	}
}
