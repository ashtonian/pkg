package filter

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// --- Geo Types ---

// GeoPoint represents a geographic coordinate.
type GeoPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// GeoDistance specifies a center point and radius for $near queries.
type GeoDistance struct {
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Distance string  `json:"distance"` // "10km", "5mi", "500m"
}

// GeoBoundingBox specifies a rectangular area for $within queries.
type GeoBoundingBox struct {
	TopLeft     GeoPoint `json:"top_left"`
	BottomRight GeoPoint `json:"bottom_right"`
}

// GeoPolygon specifies a closed polygon area for $within queries.
type GeoPolygon struct {
	Points []GeoPoint `json:"points"` // min 3, auto-closed
}

// --- Storage Configuration ---

// GeoStorageType describes how geo data is stored in the database column.
type GeoStorageType string

const (
	GeoStorageJSONB     GeoStorageType = "jsonb"
	GeoStorageGeography GeoStorageType = "geography"
	GeoStorageGeometry  GeoStorageType = "geometry"
)

// GeoFieldConfig holds geo-specific metadata parsed from struct tags.
// Filter coordinates are always WGS 84 longitude/latitude (SRID 4326).
type GeoFieldConfig struct {
	StorageType GeoStorageType // jsonb, geography, geometry
	// SRID of a native geometry column; shapes are transformed into it.
	// JSONB points and geography are 4326.
	SRID   int
	LatKey string // JSONB member holding latitude (default "lat")
	LonKey string // JSONB member holding longitude (default "lon")
}

// wgs84 is the SRID of every filter coordinate.
const wgs84 = 4326

// --- Distance Parsing ---

// distanceUnits maps unit suffixes to their meter conversion factors.
var distanceUnits = map[string]float64{
	"m":  1,
	"km": 1000,
	"mi": 1609.344,
	"ft": 0.3048,
	"yd": 0.9144,
}

// maxDistanceMeters is the Earth's circumference — the upper bound for any distance.
const maxDistanceMeters = 40_075_000

// ParseDistance parses a distance string (e.g., "5km", "500m", "3mi") into meters.
func ParseDistance(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty distance string")
	}

	// Find where the numeric part ends and the unit begins.
	unitStart := len(s)
	for i := range s {
		c := s[i]
		if c != '.' && (c < '0' || c > '9') {
			unitStart = i
			break
		}
	}

	numStr := s[:unitStart]
	unit := strings.ToLower(s[unitStart:])

	if numStr == "" {
		return 0, fmt.Errorf("no numeric value in distance %q", s)
	}

	value, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric value in distance %q: %w", s, err)
	}

	if value <= 0 {
		return 0, fmt.Errorf("distance must be positive, got %v", value)
	}

	factor, ok := distanceUnits[unit]
	if !ok {
		return 0, fmt.Errorf("unknown distance unit %q (supported: m, km, mi, ft, yd)", unit)
	}

	meters := value * factor
	if meters > maxDistanceMeters {
		return 0, fmt.Errorf("distance %.0fm exceeds Earth circumference (%.0fm)", meters, float64(maxDistanceMeters))
	}

	return meters, nil
}

// --- Validation ---

// ValidateGeoPoint checks that lat/lon are within valid ranges.
func ValidateGeoPoint(lat, lon float64) error {
	if lat < -90 || lat > 90 {
		return fmt.Errorf("latitude %v out of range [-90, 90]", lat)
	}
	if lon < -180 || lon > 180 {
		return fmt.Errorf("longitude %v out of range [-180, 180]", lon)
	}
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
		return fmt.Errorf("latitude and longitude must be finite numbers")
	}
	return nil
}

// ValidateGeoBoundingBox checks that the bounding box is valid.
func ValidateGeoBoundingBox(bb *GeoBoundingBox) error {
	if err := ValidateGeoPoint(bb.TopLeft.Lat, bb.TopLeft.Lon); err != nil {
		return fmt.Errorf("top_left: %w", err)
	}
	if err := ValidateGeoPoint(bb.BottomRight.Lat, bb.BottomRight.Lon); err != nil {
		return fmt.Errorf("bottom_right: %w", err)
	}
	if bb.TopLeft.Lat <= bb.BottomRight.Lat {
		return fmt.Errorf("top_left.lat (%v) must be greater than bottom_right.lat (%v)", bb.TopLeft.Lat, bb.BottomRight.Lat)
	}
	// Reordering the longitudes would turn a box meant to cross the
	// antimeridian into its complement; such boxes are not supported.
	if bb.TopLeft.Lon >= bb.BottomRight.Lon {
		return fmt.Errorf("top_left.lon (%v) must be less than bottom_right.lon (%v); boxes crossing the antimeridian are not supported", bb.TopLeft.Lon, bb.BottomRight.Lon)
	}
	return nil
}

// ValidateGeoPolygon checks that the polygon has valid points.
func ValidateGeoPolygon(p *GeoPolygon) error {
	if len(p.Points) < 3 {
		return fmt.Errorf("polygon requires at least 3 points, got %d", len(p.Points))
	}
	if len(p.Points) > 100 {
		return fmt.Errorf("polygon exceeds maximum of 100 points, got %d", len(p.Points))
	}
	for i, pt := range p.Points {
		if err := ValidateGeoPoint(pt.Lat, pt.Lon); err != nil {
			return fmt.Errorf("point[%d]: %w", i, err)
		}
	}
	return nil
}

// validateGeoValue re-checks a geo operand at build time: programmatic ASTs
// bypass the parser, and a swapped corner or out-of-range point would
// otherwise compile to a predicate that silently means something else.
func validateGeoValue(value any) error {
	var code = ECodeInvalidGeoCoords
	var err error
	switch v := value.(type) {
	case *GeoDistance:
		if err = ValidateGeoPoint(v.Lat, v.Lon); err == nil {
			code = ECodeInvalidDistance
			_, err = ParseDistance(v.Distance)
		}
	case *GeoBoundingBox:
		err = ValidateGeoBoundingBox(v)
	case *GeoPolygon:
		code = ECodeInvalidGeoPolygon
		err = ValidateGeoPolygon(v)
	default:
		err = fmt.Errorf("unexpected geo value %T", value)
	}
	if err != nil {
		return &core.ParseError{Kind: core.ParseErrorKindFilter, Code: code.String(), Message: err.Error(), Cause: err}
	}
	return nil
}

// --- Value Parsing ---

// parseGeoValue decodes a raw JSON value into a typed geo struct and validates it.
// Returns the typed value (GeoDistance, GeoBoundingBox, or GeoPolygon) or an error.
func parseGeoValue(field *FieldSchema, op FilterOperator, value any) (any, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidGeoCoords.String(),
			Message: fmt.Sprintf("field '%s': geo operator %s requires an object value", field.ExternalName, op),
		}
	}

	switch op {
	case OpNear:
		return parseGeoDistanceValue(field.ExternalName, obj)
	case OpWithin:
		return parseGeoWithinValue(field.ExternalName, obj)
	case OpIntersects:
		return parseGeoWithinValue(field.ExternalName, obj)
	default:
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidOperator.String(),
			Message: fmt.Sprintf("field '%s': unknown geo operator %s", field.ExternalName, op),
		}
	}
}

func parseGeoDistanceValue(fieldName string, obj map[string]any) (*GeoDistance, error) {
	lat, ok := toFloat64(obj["lat"])
	if !ok {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidGeoCoords.String(),
			Message: fmt.Sprintf("field '%s': $near requires numeric 'lat'", fieldName),
		}
	}
	lon, ok := toFloat64(obj["lon"])
	if !ok {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidGeoCoords.String(),
			Message: fmt.Sprintf("field '%s': $near requires numeric 'lon'", fieldName),
		}
	}
	distStr, ok := obj["distance"].(string)
	if !ok || distStr == "" {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidDistance.String(),
			Message: fmt.Sprintf("field '%s': $near requires string 'distance' (e.g., \"5km\")", fieldName),
		}
	}

	if err := ValidateGeoPoint(lat, lon); err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidGeoCoords.String(),
			Message: fmt.Sprintf("field '%s': %v", fieldName, err),
		}
	}
	if _, err := ParseDistance(distStr); err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidDistance.String(),
			Message: fmt.Sprintf("field '%s': %v", fieldName, err),
		}
	}

	return &GeoDistance{Lat: lat, Lon: lon, Distance: distStr}, nil
}

func parseGeoWithinValue(fieldName string, obj map[string]any) (any, error) {
	// Detect shape: bbox has "top_left"/"bottom_right", polygon has "points".
	if _, hasTL := obj["top_left"]; hasTL {
		return parseGeoBBoxValue(fieldName, obj)
	}
	if _, hasPoints := obj["points"]; hasPoints {
		return parseGeoPolygonValue(fieldName, obj)
	}
	return nil, &core.ParseError{
		Kind:    core.ParseErrorKindFilter,
		Code:    ECodeInvalidGeoCoords.String(),
		Message: fmt.Sprintf("field '%s': $within requires 'top_left'/'bottom_right' (bbox) or 'points' (polygon)", fieldName),
	}
}

func parseGeoBBoxValue(fieldName string, obj map[string]any) (*GeoBoundingBox, error) {
	tl, err := extractGeoPoint(obj["top_left"])
	if err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidGeoCoords.String(),
			Message: fmt.Sprintf("field '%s': top_left: %v", fieldName, err),
		}
	}
	br, err := extractGeoPoint(obj["bottom_right"])
	if err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidGeoCoords.String(),
			Message: fmt.Sprintf("field '%s': bottom_right: %v", fieldName, err),
		}
	}

	bb := &GeoBoundingBox{TopLeft: *tl, BottomRight: *br}
	if err := ValidateGeoBoundingBox(bb); err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidGeoCoords.String(),
			Message: fmt.Sprintf("field '%s': %v", fieldName, err),
		}
	}
	return bb, nil
}

func parseGeoPolygonValue(fieldName string, obj map[string]any) (*GeoPolygon, error) {
	arr, ok := obj["points"].([]any)
	if !ok {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidGeoPolygon.String(),
			Message: fmt.Sprintf("field '%s': 'points' must be an array", fieldName),
		}
	}

	points := make([]GeoPoint, 0, len(arr))
	for i, item := range arr {
		pt, err := extractGeoPoint(item)
		if err != nil {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeInvalidGeoCoords.String(),
				Message: fmt.Sprintf("field '%s': points[%d]: %v", fieldName, i, err),
			}
		}
		points = append(points, *pt)
	}

	poly := &GeoPolygon{Points: points}
	if err := ValidateGeoPolygon(poly); err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeInvalidGeoPolygon.String(),
			Message: fmt.Sprintf("field '%s': %v", fieldName, err),
		}
	}
	return poly, nil
}

func extractGeoPoint(v any) (*GeoPoint, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object with lat/lon, got %T", v)
	}
	lat, ok := toFloat64(obj["lat"])
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'lat'")
	}
	lon, ok := toFloat64(obj["lon"])
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'lon'")
	}
	if err := ValidateGeoPoint(lat, lon); err != nil {
		return nil, err
	}
	return &GeoPoint{Lat: lat, Lon: lon}, nil
}

func toFloat64(v any) (float64, bool) {
	if !core.IsNumber(v) {
		return 0, false
	}
	f, err := core.NormalizeFloat(v, reflect.Float64)
	return f, err == nil
}

// --- Tag Parsing ---

// geoKeyRe is a JSONB member name: it is interpolated into `->>'key'` and
// `'$.key'` accessors, so only identifiers are accepted.
var geoKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseGeoTag reads the geo struct tags: geo (jsonb | geography | geometry),
// geo_srid (native geometry only) and geo_lat / geo_lon (`$.member` of a
// JSONB point). Invalid tags fail schema generation.
func parseGeoTag(tags *core.Tags) (*GeoFieldConfig, error) {
	cfg := &GeoFieldConfig{
		StorageType: GeoStorageType(tags.Geo),
		SRID:        wgs84,
		LatKey:      "lat",
		LonKey:      "lon",
	}
	switch cfg.StorageType {
	case GeoStorageJSONB, GeoStorageGeography, GeoStorageGeometry:
	default:
		return nil, fmt.Errorf("geo storage %q: want jsonb, geography or geometry", cfg.StorageType)
	}
	if tag := tags.GeoSRID; tag != "" {
		n, err := strconv.Atoi(tag)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("geo_srid %q: want a positive integer", tag)
		}
		if n != wgs84 && cfg.StorageType != GeoStorageGeometry {
			return nil, fmt.Errorf("geo_srid %d: %s storage holds WGS 84 (4326) coordinates", n, cfg.StorageType)
		}
		cfg.SRID = n
	}
	for _, k := range []struct {
		tag, value string
		dst        *string
	}{{"geo_lat", tags.GeoLat, &cfg.LatKey}, {"geo_lon", tags.GeoLon, &cfg.LonKey}} {
		tag := k.value
		if tag == "" {
			continue
		}
		if cfg.StorageType != GeoStorageJSONB {
			return nil, fmt.Errorf("%s applies to jsonb storage only", k.tag)
		}
		key := strings.TrimPrefix(tag, "$.")
		if key == tag || !geoKeyRe.MatchString(key) {
			return nil, fmt.Errorf("%s %q: want $.member with an identifier member name", k.tag, tag)
		}
		*k.dst = key
	}
	return cfg, nil
}

// polygonWKT renders a polygon as closed-ring WKT in lon/lat order.
func polygonWKT(p *GeoPolygon) string {
	var b strings.Builder
	b.WriteString("POLYGON((")
	for i := range len(p.Points) + 1 {
		pt := p.Points[i%len(p.Points)]
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.FormatFloat(pt.Lon, 'g', -1, 64))
		b.WriteByte(' ')
		b.WriteString(strconv.FormatFloat(pt.Lat, 'g', -1, 64))
	}
	b.WriteString("))")
	return b.String()
}

// isGeoOp returns true if the operator is a geo-spatial operator.
func isGeoOp(op FilterOperator) bool {
	return op == OpNear || op == OpWithin || op == OpIntersects
}
