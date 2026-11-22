//go:build integration

package geo_test

import (
	"database/sql"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Distance is what PostGIS measures between geography points, and ST_DWithin
// decides a radius as Distance <= radius does, a hair either side of it.
func TestDistanceMatchesPostGIS(t *testing.T) {
	db := dbtest.PostGIS(t)
	rng := rand.New(rand.NewPCG(211, 219))
	var a, b []geo.Point
	add := func(p, q geo.Point) { a, b = append(a, p), append(b, q) }
	for range 2000 {
		add(geo.Point{Lat: rng.Float64()*180 - 90, Lon: rng.Float64()*360 - 180},
			geo.Point{Lat: rng.Float64()*180 - 90, Lon: rng.Float64()*360 - 180})
	}
	for range 200 { // nearly antipodal: the astroid start and Newton's method
		p := geo.Point{Lat: rng.Float64()*180 - 90, Lon: rng.Float64()*360 - 180}
		add(p, geo.Point{Lat: -p.Lat + rng.NormFloat64()*0.01, Lon: math.Remainder(p.Lon+180+rng.NormFloat64()*0.5, 360)})
	}
	for range 200 { // short lines, a few meters to a few kilometers
		p := geo.Point{Lat: rng.Float64()*178 - 89, Lon: rng.Float64()*360 - 180}
		add(p, geo.Point{Lat: p.Lat + rng.NormFloat64()*0.01, Lon: p.Lon + rng.NormFloat64()*0.01})
	}
	for _, pq := range [][2]geo.Point{
		{{Lat: 0, Lon: 0}, {Lat: 0, Lon: 0}},
		{{Lat: 0, Lon: 0}, {Lat: 1, Lon: 0}},
		{{Lat: 0, Lon: 0}, {Lat: 0, Lon: 1}},
		{{Lat: 0, Lon: 0}, {Lat: 0, Lon: 180}},
		{{Lat: 0, Lon: 0}, {Lat: 0, Lon: 179.5}},
		{{Lat: 90, Lon: 0}, {Lat: -90, Lon: 0}},
		{{Lat: 90, Lon: 0}, {Lat: 89, Lon: 123}},
		{{Lat: -10, Lon: 170}, {Lat: -10, Lon: -175}},
		{{Lat: 10, Lon: 10}, {Lat: 10.0000001, Lon: 10}},
	} {
		add(pq[0], pq[1])
	}

	want := column[float64](t, db, `
		SELECT ST_Distance(ST_SetSRID(ST_MakePoint(lon1, lat1), 4326)::geography,
		                   ST_SetSRID(ST_MakePoint(lon2, lat2), 4326)::geography)
		FROM unnest($1::float8[], $2::float8[], $3::float8[], $4::float8[]) WITH ORDINALITY AS t(lat1, lon1, lat2, lon2, i)
		ORDER BY i`, lats(a), lons(a), lats(b), lons(b))
	require.Len(t, want, len(a))

	// Each pair twice, with a radius just inside and just outside its
	// distance.
	var pa, pb []geo.Point
	var radius []float64
	for i := range a {
		got := geo.Distance(a[i], b[i])
		assert.InDelta(t, want[i], got, 1e-6, "%v → %v", a[i], b[i])
		pa, pb = append(pa, a[i], a[i]), append(pb, b[i], b[i])
		radius = append(radius, got*(1-1e-9)-1e-9, got*(1+1e-9)+1e-9)
	}
	within := column[bool](t, db, `
		SELECT ST_DWithin(ST_SetSRID(ST_MakePoint(lon1, lat1), 4326)::geography,
		                  ST_SetSRID(ST_MakePoint(lon2, lat2), 4326)::geography, r)
		FROM unnest($1::float8[], $2::float8[], $3::float8[], $4::float8[], $5::float8[]) WITH ORDINALITY AS t(lat1, lon1, lat2, lon2, r, i)
		ORDER BY i`, lats(pa), lons(pa), lats(pb), lons(pb), radius)
	require.Len(t, within, len(pa))
	for i := range pa {
		assert.Equal(t, geo.Distance(pa[i], pb[i]) <= radius[i], within[i], "%v → %v within %v", pa[i], pb[i], radius[i])
	}
}

// CoveredBy is ST_CoveredBy, and ST_Intersects, of a point by a polygon:
// inside, outside, at vertices and at the midpoints of edges.
func TestCoveredByMatchesPostGIS(t *testing.T) {
	db := dbtest.PostGIS(t)
	rng := rand.New(rand.NewPCG(220, 211))
	var wkbs [][]byte
	var rings [][]geo.Point
	var points []geo.Point
	var polygon []int
	for i := range 300 {
		ring := randomRing(rng, 3+rng.IntN(8), i%2 == 0)
		rings = append(rings, ring)
		wkbs = append(wkbs, geo.AppendPolygonWKB(nil, ring))
		probe := func(p geo.Point) { points, polygon = append(points, p), append(polygon, i+1) }
		for j, v := range ring {
			w := ring[(j+1)%len(ring)]
			probe(v)
			probe(geo.Point{Lat: (v.Lat + w.Lat) / 2, Lon: (v.Lon + w.Lon) / 2})
		}
		for range 10 {
			probe(geo.Point{Lat: rng.Float64()*24 - 12, Lon: rng.Float64()*24 - 12})
		}
	}

	type answer struct{ coveredBy, intersects bool }
	rows, err := db.Query(`
		SELECT ST_CoveredBy(pt, poly), ST_Intersects(pt, poly)
		FROM unnest($1::float8[], $2::float8[], $3::int[]) WITH ORDINALITY AS t(lat, lon, p, i),
		     LATERAL (SELECT ST_SetSRID(ST_MakePoint(lon, lat), 4326) AS pt,
		                     ST_SetSRID(ST_GeomFromWKB(($4::bytea[])[p]), 4326) AS poly) g
		ORDER BY i`, lats(points), lons(points), polygon, wkbs)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var want []answer
	for rows.Next() {
		var a answer
		require.NoError(t, rows.Scan(&a.coveredBy, &a.intersects))
		want = append(want, a)
	}
	require.NoError(t, rows.Err())
	require.Len(t, want, len(points))
	for i, p := range points {
		ring := rings[polygon[i]-1]
		got := geo.CoveredBy(p, ring)
		assert.Equal(t, want[i].coveredBy, got, "ST_CoveredBy(%v, %v)", p, ring)
		assert.Equal(t, want[i].intersects, got, "ST_Intersects(%v, %v)", p, ring)
	}
}

// The same, in the shape a production plan has: one polygon bound once for
// every row of the statement, where PostGIS may decide the points through
// its cached, prepared polygon rather than one built per row.
func TestCoveredByMatchesPostGISConstantPolygon(t *testing.T) {
	db := dbtest.PostGIS(t)
	rng := rand.New(rand.NewPCG(276, 10))
	for i := range 40 {
		ring := randomRing(rng, 3+rng.IntN(8), i%2 == 0)
		var points []geo.Point
		for j, v := range ring {
			w := ring[(j+1)%len(ring)]
			points = append(points, v, geo.Point{Lat: (v.Lat + w.Lat) / 2, Lon: (v.Lon + w.Lon) / 2})
		}
		for range 200 {
			points = append(points, geo.Point{Lat: rng.Float64()*24 - 12, Lon: rng.Float64()*24 - 12})
		}
		coveredBy := column[bool](t, db, `
			SELECT ST_CoveredBy(ST_SetSRID(ST_MakePoint(lon, lat), 4326), ST_SetSRID(ST_GeomFromWKB($3::bytea), 4326))
			FROM unnest($1::float8[], $2::float8[]) WITH ORDINALITY AS t(lat, lon, i)
			ORDER BY i`, lats(points), lons(points), geo.AppendPolygonWKB(nil, ring))
		intersects := column[bool](t, db, `
			SELECT ST_Intersects(ST_SetSRID(ST_MakePoint(lon, lat), 4326), ST_SetSRID(ST_GeomFromWKB($3::bytea), 4326))
			FROM unnest($1::float8[], $2::float8[]) WITH ORDINALITY AS t(lat, lon, i)
			ORDER BY i`, lats(points), lons(points), geo.AppendPolygonWKB(nil, ring))
		require.Len(t, coveredBy, len(points))
		require.Len(t, intersects, len(points))
		for k, p := range points {
			got := geo.CoveredBy(p, ring)
			assert.Equal(t, coveredBy[k], got, "ST_CoveredBy(%v, %v)", p, ring)
			assert.Equal(t, intersects[k], got, "ST_Intersects(%v, %v)", p, ring)
		}
	}
}

// randomRing is a simple polygon of n vertices around the origin, in angle
// order, on a grid of eighths when grid is set; others are concave.
func randomRing(rng *rand.Rand, n int, grid bool) []geo.Point {
	ring := make([]geo.Point, n)
	for i := range n {
		angle := 2 * math.Pi * (float64(i) + rng.Float64()*0.8) / float64(n)
		r := 2 + rng.Float64()*8
		p := geo.Point{Lat: r * math.Sin(angle), Lon: r * math.Cos(angle)}
		if grid {
			p = geo.Point{Lat: math.Round(p.Lat*8) / 8, Lon: math.Round(p.Lon*8) / 8}
		}
		ring[i] = p
	}
	return ring
}

func lats(ps []geo.Point) []float64 {
	out := make([]float64, len(ps))
	for i, p := range ps {
		out[i] = p.Lat
	}
	return out
}

func lons(ps []geo.Point) []float64 {
	out := make([]float64, len(ps))
	for i, p := range ps {
		out[i] = p.Lon
	}
	return out
}

// column runs q and returns its one column.
func column[T any](t *testing.T, db *sql.DB, q string, args ...any) []T {
	t.Helper()
	rows, err := db.Query(q, args...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []T
	for rows.Next() {
		var v T
		require.NoError(t, rows.Scan(&v))
		out = append(out, v)
	}
	require.NoError(t, rows.Err())
	return out
}
