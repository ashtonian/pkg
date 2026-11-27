// Package geo is the geometry parse evaluates geo filters with where
// PostGIS does not: the WGS 84 geodesic distance PostGIS measures geography
// with, and the planar point-in-polygon test it applies to geometry in
// SRID 4326. filter.Evaluate calls it directly; parsesql registers it as
// SQLite functions under the names below, which filter.SQLiteDialect calls.
package geo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// The SQLite functions' names. Each takes the stored point as its JSON
// text and its member names, reads it with ReadPointJSON, and is NULL when
// the point is, else 1 or 0.
const (
	// SQLiteNearFunc(point, latKey, lonKey, lat, lon, meters) is Near.
	SQLiteNearFunc = "parse_geo_near"
	// SQLiteInBoxFunc(point, latKey, lonKey, south, north, west, east) is
	// Box.Covers.
	SQLiteInBoxFunc = "parse_geo_in_box"
	// SQLiteCoveredByFunc(point, latKey, lonKey, polygon) is CoveredBy, the
	// polygon a WKB blob as AppendPolygonWKB writes it.
	SQLiteCoveredByFunc = "parse_geo_covered_by"
)

// Point is a WGS 84 coordinate in degrees.
type Point struct{ Lat, Lon float64 }

// Near reports whether the stored point p, made Geodetic, lies within
// meters of center: ST_DWithin on geography. A point with a coordinate that
// is not finite is near nothing.
func Near(p, center Point, meters float64) bool {
	return Distance(Geodetic(p), center) <= meters
}

// Geodetic is p as PostGIS coerces a point cast to geography: a latitude
// beyond a pole reflects back across it (91 is 89, 200 is -20) and its
// longitude is kept, as Distance wraps longitudes itself.
func Geodetic(p Point) Point {
	if p.Lat < -90 || p.Lat > 90 {
		p.Lat = normalizeLatitude(p.Lat)
	}
	return p
}

// normalizeLatitude is liblwgeom's latitude_degrees_normalize.
func normalizeLatitude(lat float64) float64 {
	if lat > 360 {
		lat = math.Remainder(lat, 360)
	}
	if lat < -360 {
		lat = math.Remainder(lat, -360)
	}
	if lat > 180 {
		lat = 180 - lat
	}
	if lat < -180 {
		lat = -180 - lat
	}
	if lat > 90 {
		lat = 180 - lat
	}
	if lat < -90 {
		lat = -180 - lat
	}
	return lat
}

// Box is the axis-aligned box ST_MakeEnvelope(West, South, East, North,
// 4326) in degrees.
type Box struct{ South, North, West, East float64 }

// Covers reports whether p lies in b or on its boundary: ST_CoveredBy, and
// ST_Intersects, of a point by the box.
func (b Box) Covers(p Point) bool {
	return b.South <= p.Lat && p.Lat <= b.North && b.West <= p.Lon && p.Lon <= b.East
}

// CoveredBy reports whether p lies inside the polygon ring or on its
// boundary, with longitude as x and latitude as y: PostGIS's ST_CoveredBy
// of a point by a polygon in SRID 4326. The ring closes from its last point
// to its first; a repeated closing point is ignored. The test counts the
// ring's winding number around p, so it holds for either orientation, and a
// point on an edge is covered.
func CoveredBy(p Point, ring []Point) bool {
	wn := 0
	for i := range ring {
		a, b := ring[i], ring[(i+1)%len(ring)]
		if a == b {
			continue
		}
		if p.Lat > max(a.Lat, b.Lat) || p.Lat < min(a.Lat, b.Lat) {
			continue
		}
		// The sign of the cross product (b - a) × (p - a), as PostGIS's
		// lw_segment_side computes it: negative when p is left of a→b.
		// The explicit conversions keep each product rounded, never fused.
		side := float64((p.Lon-a.Lon)*(b.Lat-a.Lat)) - float64((b.Lon-a.Lon)*(p.Lat-a.Lat))
		if side == 0 && onSegment(p, a, b) {
			return true
		}
		switch {
		case side < 0 && a.Lat <= p.Lat && p.Lat < b.Lat:
			wn++
		case side > 0 && b.Lat <= p.Lat && p.Lat < a.Lat:
			wn--
		}
	}
	return wn != 0
}

// onSegment reports whether p, already on the line through a and b, lies
// between them: PostGIS's lw_pt_in_seg, which leaves b to the next segment.
func onSegment(p, a, b Point) bool {
	return a.Lon <= p.Lon && p.Lon < b.Lon || a.Lon >= p.Lon && p.Lon > b.Lon ||
		a.Lat <= p.Lat && p.Lat < b.Lat || a.Lat >= p.Lat && p.Lat > b.Lat
}

// Polygon WKB: little-endian, type 3 (Polygon), one ring of x/y points.
const (
	wkbLittleEndian = 1
	wkbPolygon      = 3
	wkbHeader       = 1 + 4 + 4 + 4 // byte order, type, ring count, point count
)

// MinRingPoints is the fewest points of a ring AppendPolygonWKB writes: a
// triangle, before the point that closes it.
const MinRingPoints = 3

// AppendPolygonWKB appends ring to b as the WKB of a one-ring polygon,
// closed by repeating its first point, in lon/lat (x/y) order. ring must
// hold at least MinRingPoints points, as filter's polygon validation
// requires; a shorter ring panics.
func AppendPolygonWKB(b []byte, ring []Point) []byte {
	if len(ring) < MinRingPoints {
		panic(fmt.Sprintf("geo: AppendPolygonWKB of a %d-point ring, want at least %d", len(ring), MinRingPoints))
	}
	b = append(b, wkbLittleEndian)
	b = binary.LittleEndian.AppendUint32(b, wkbPolygon)
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(ring)+1))
	for i := range len(ring) + 1 {
		pt := ring[i%len(ring)]
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(pt.Lon))
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(pt.Lat))
	}
	return b
}

// errPolygonWKB is a blob that is not a polygon AppendPolygonWKB wrote.
var errPolygonWKB = errors.New("geo: want a little-endian one-ring polygon WKB")

// ReadPolygonWKB appends the ring of the polygon WKB b to ring and returns
// it. b must be a polygon as AppendPolygonWKB writes it: little-endian, one
// ring of at least MinRingPoints points and the closing point, which repeats
// the first, its points finite.
func ReadPolygonWKB(ring []Point, b []byte) ([]Point, error) {
	if len(b) < wkbHeader || b[0] != wkbLittleEndian ||
		binary.LittleEndian.Uint32(b[1:]) != wkbPolygon || binary.LittleEndian.Uint32(b[5:]) != 1 {
		return nil, errPolygonWKB
	}
	n := binary.LittleEndian.Uint32(b[9:])
	if uint64(len(b)-wkbHeader) != 16*uint64(n) {
		return nil, fmt.Errorf("%w: %d points in %d bytes", errPolygonWKB, n, len(b)-wkbHeader)
	}
	if n < MinRingPoints+1 {
		return nil, fmt.Errorf("%w: a ring of %d points, want at least %d", errPolygonWKB, n, MinRingPoints+1)
	}
	start := len(ring)
	for i := range int(n) {
		off := wkbHeader + 16*i
		lon := math.Float64frombits(binary.LittleEndian.Uint64(b[off:]))
		lat := math.Float64frombits(binary.LittleEndian.Uint64(b[off+8:]))
		if math.IsNaN(lon) || math.IsInf(lon, 0) || math.IsNaN(lat) || math.IsInf(lat, 0) {
			return nil, fmt.Errorf("%w: point %d is not finite", errPolygonWKB, i)
		}
		ring = append(ring, Point{Lat: lat, Lon: lon})
	}
	if ring[start] != ring[len(ring)-1] {
		return nil, fmt.Errorf("%w: the ring is not closed", errPolygonWKB)
	}
	return ring, nil
}
