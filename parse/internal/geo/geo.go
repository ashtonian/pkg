// Package geo is the geometry parse evaluates geo filters with where
// PostGIS does not: the WGS 84 geodesic distance PostGIS measures geography
// with, and the planar point-in-polygon test it applies to geometry in
// SRID 4326. filter.Evaluate calls it directly; parsesqlite registers it as
// SQLite functions under the names below, which filter.SQLiteDialect calls.
package geo

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The SQLite functions' names. Each reads a stored point as its lat and lon
// members as SQLite's -> returns them (JSON text, or NULL when missing),
// through CoordinateJSON, and is NULL when the point is.
const (
	// SQLiteDistanceFunc(lat, lon, lat2, lon2) is Distance in meters from
	// the stored point, made Geodetic, to (lat2, lon2).
	SQLiteDistanceFunc = "parse_geo_distance"
	// SQLiteInBoxFunc(lat, lon, south, north, west, east) is Box.Covers: 1
	// or 0.
	SQLiteInBoxFunc = "parse_geo_in_box"
	// SQLiteCoveredByFunc(lat, lon, polygon) is CoveredBy, the polygon a
	// WKB blob as AppendPolygonWKB writes it: 1 or 0.
	SQLiteCoveredByFunc = "parse_geo_covered_by"
)

// Point is a WGS 84 coordinate in degrees.
type Point struct{ Lat, Lon float64 }

// Coordinate reads a stored point's member, decoded from JSON, as
// PostgreSQL reads (doc->>'lat')::float8: a JSON number, or a JSON string
// holding one with spaces around it allowed. ok is false for a missing or
// null member, a coordinate that is not finite, and any other value, where
// PostgreSQL refuses the query instead.
func Coordinate(member any) (float64, bool) {
	switch m := member.(type) {
	case json.Number:
		return number(string(m))
	case float64:
		return m, !math.IsNaN(m) && !math.IsInf(m, 0)
	case string:
		return number(strings.Trim(m, " \t\n\r\v\f"))
	}
	return 0, false
}

// CoordinateJSON is Coordinate of a member held as JSON text, as SQLite's
// -> returns it.
func CoordinateJSON(text string) (float64, bool) {
	if !strings.HasPrefix(text, `"`) {
		return number(text)
	}
	var s string
	if json.Unmarshal([]byte(text), &s) != nil {
		return 0, false
	}
	return Coordinate(s)
}

func number(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
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

// AppendPolygonWKB appends ring to b as the WKB of a one-ring polygon,
// closed by repeating its first point, in lon/lat (x/y) order.
func AppendPolygonWKB(b []byte, ring []Point) []byte {
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
// ring, its points finite.
func ReadPolygonWKB(ring []Point, b []byte) ([]Point, error) {
	if len(b) < wkbHeader || b[0] != wkbLittleEndian ||
		binary.LittleEndian.Uint32(b[1:]) != wkbPolygon || binary.LittleEndian.Uint32(b[5:]) != 1 {
		return nil, errPolygonWKB
	}
	n := binary.LittleEndian.Uint32(b[9:])
	if uint64(len(b)-wkbHeader) != 16*uint64(n) {
		return nil, fmt.Errorf("%w: %d points in %d bytes", errPolygonWKB, n, len(b)-wkbHeader)
	}
	for i := range int(n) {
		off := wkbHeader + 16*i
		lon := math.Float64frombits(binary.LittleEndian.Uint64(b[off:]))
		lat := math.Float64frombits(binary.LittleEndian.Uint64(b[off+8:]))
		if math.IsNaN(lon) || math.IsInf(lon, 0) || math.IsNaN(lat) || math.IsInf(lat, 0) {
			return nil, fmt.Errorf("%w: point %d is not finite", errPolygonWKB, i)
		}
		ring = append(ring, Point{Lat: lat, Lon: lon})
	}
	return ring, nil
}
