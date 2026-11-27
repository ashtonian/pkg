package filter

import (
	"fmt"
	"reflect"

	"github.com/ashtonian/pkg/gowalker"
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
	err := validateGeoValue(f, n.Value)
	if err != nil {
		return False, err
	}
	if cfg := f.GeoConfig; cfg != nil && cfg.StorageType != GeoStorageJSONB {
		return False, core.NewError(ECodeGeoNotSupported, fmt.Sprintf("geo operator '%s' on native %s storage is not supported by the reference evaluator", n.Op, cfg.StorageType),
			core.WithField(f.ExternalName))
	}
	holds, err := geoPredicate(n.Op, n.Value)
	if err != nil {
		return False, err
	}
	v, present, err := row.Value(f)
	if err != nil {
		return False, err
	}
	if !present {
		return Unknown, nil
	}
	doc, err := decodeDocument(v)
	if err != nil {
		return False, fmt.Errorf("field %s: %w", f.ExternalName, err)
	}
	latKey, lonKey := geoKeys(f)
	p, ok := geo.ReadPoint(doc, latKey, lonKey)
	if !ok {
		return Unknown, nil
	}
	return truthOf(holds(p)), nil
}

// geoPredicate is the test op makes of a stored point with value, its
// operand: a distance for $near, a box or polygon for $within and
// $intersects. Any other pairing is the error the dialects build.
func geoPredicate(op FilterOperator, value any) (func(geo.Point) bool, error) {
	switch v := value.(type) {
	case *GeoDistance:
		if op != OpNear {
			break
		}
		meters, err := ParseDistance(v.Distance)
		if err != nil {
			return nil, err
		}
		center := geo.Point{Lat: v.Lat, Lon: v.Lon}
		return func(p geo.Point) bool { return geo.Near(p, center, meters) }, nil
	case *GeoBoundingBox:
		if op != OpWithin && op != OpIntersects {
			break
		}
		return geoBox(v).Covers, nil
	case *GeoPolygon:
		if op != OpWithin && op != OpIntersects {
			break
		}
		ring := geoRing(v)
		return func(p geo.Point) bool { return geo.CoveredBy(p, ring) }, nil
	}
	return nil, fmt.Errorf("geo operator %s does not take %s", op, gowalker.TypeText(reflect.TypeOf(value)))
}

// geoKeys are the members a JSONB point holds its latitude and longitude
// under: GeoFieldConfig's, a point's by default.
func geoKeys(f *FieldSchema) (lat, lon string) {
	lat, lon = geoMemberLat, geoMemberLon
	if cfg := f.GeoConfig; cfg != nil {
		if cfg.LatKey != "" {
			lat = cfg.LatKey
		}
		if cfg.LonKey != "" {
			lon = cfg.LonKey
		}
	}
	return lat, lon
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
