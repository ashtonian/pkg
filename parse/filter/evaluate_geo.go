package filter

import (
	"fmt"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/geo"
)

// evaluateGeo is a geo operator on a JSONB point, as PostGIS decides it:
// $near by the WGS 84 geodesic distance (ST_DWithin on geography), $within
// and $intersects a box or polygon planar in SRID 4326 (ST_CoveredBy and
// ST_Intersects), boundary included. A NULL point is Unknown. Native
// geometry storage is PostGIS's alone: E2115, as on SQLite.
func evaluateGeo(n *FilterNode, row Row) (Truth, error) {
	f := n.Field
	if err := validateGeoValue(n.Value); err != nil {
		return False, err
	}
	if cfg := f.GeoConfig; cfg != nil && cfg.StorageType != GeoStorageJSONB {
		return False, &core.ParseError{
			Kind: core.ParseErrorKindFilter, Code: ECodeGeoNotSupported.String(),
			Message: fmt.Sprintf("geo operator '%s' on native %s storage is not supported by the reference evaluator", n.Op, cfg.StorageType),
		}
	}
	v, present, err := row.Value(f)
	if err != nil {
		return False, err
	}
	if !present {
		return Unknown, nil
	}
	p, ok, err := storedPoint(f, v)
	if err != nil {
		return False, err
	}
	if !ok {
		return Unknown, nil
	}
	switch value := n.Value.(type) {
	case *GeoDistance:
		if n.Op != OpNear {
			break
		}
		meters, err := ParseDistance(value.Distance)
		if err != nil {
			return False, err
		}
		return truthOf(geo.Distance(geo.Geodetic(p), geo.Point{Lat: value.Lat, Lon: value.Lon}) <= meters), nil
	case *GeoBoundingBox:
		if n.Op == OpNear {
			break
		}
		return truthOf(geoBox(value).Covers(p)), nil
	case *GeoPolygon:
		if n.Op == OpNear {
			break
		}
		return truthOf(geo.CoveredBy(p, geoRing(value))), nil
	}
	return False, fmt.Errorf("geo operator %s: unexpected value %T", n.Op, n.Value)
}

// storedPoint reads a JSONB point: a document, or its JSON text, whose
// GeoConfig lat and lon members are coordinates (geo.Coordinate). ok is
// false, the point NULL, when the document is not an object or either
// member is missing, null or not a number.
func storedPoint(f *FieldSchema, v any) (geo.Point, bool, error) {
	doc, err := decodeDocument(v)
	if err != nil {
		return geo.Point{}, false, fmt.Errorf("field %s: %w", f.ExternalName, err)
	}
	obj, isObject := doc.(map[string]any)
	if !isObject {
		return geo.Point{}, false, nil
	}
	latKey, lonKey := "lat", "lon"
	if f.GeoConfig != nil {
		latKey, lonKey = f.GeoConfig.LatKey, f.GeoConfig.LonKey
	}
	lat, latOK := geo.Coordinate(obj[latKey])
	lon, lonOK := geo.Coordinate(obj[lonKey])
	return geo.Point{Lat: lat, Lon: lon}, latOK && lonOK, nil
}

// geoBox is the envelope PostgresDialect builds for bb.
func geoBox(bb *GeoBoundingBox) geo.Box {
	return geo.Box{South: bb.BottomRight.Lat, North: bb.TopLeft.Lat, West: bb.TopLeft.Lon, East: bb.BottomRight.Lon}
}

func geoRing(p *GeoPolygon) []geo.Point {
	ring := make([]geo.Point, len(p.Points))
	for i, pt := range p.Points {
		ring[i] = geo.Point{Lat: pt.Lat, Lon: pt.Lon}
	}
	return ring
}
