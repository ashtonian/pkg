//go:build unit

package filter_test

import (
	"math"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ParseDistance ---

func TestParseDistance(t *testing.T) {
	tests := []struct {
		input   string
		meters  float64
		wantErr bool
	}{
		{"500m", 500, false},
		{"5km", 5000, false},
		{"3mi", 4828.032, false},
		{"100ft", 30.48, false},
		{"50yd", 45.72, false},
		{"1.5km", 1500, false},
		{"0.5mi", 804.672, false},
		// Errors.
		{"", 0, true},
		{"km", 0, true},
		{"5", 0, true},
		{"-5km", 0, true},
		{"0km", 0, true},
		{"5parsecs", 0, true},
		{"50000000km", 0, true}, // exceeds Earth circumference
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := filter.ParseDistance(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tt.meters, got, 0.01)
		})
	}
}

// --- ValidateGeoPoint ---

func TestValidateGeoPoint(t *testing.T) {
	assert.NoError(t, filter.ValidateGeoPoint(0, 0))
	assert.NoError(t, filter.ValidateGeoPoint(90, 180))
	assert.NoError(t, filter.ValidateGeoPoint(-90, -180))
	assert.NoError(t, filter.ValidateGeoPoint(40.7128, -74.0060))

	assert.Error(t, filter.ValidateGeoPoint(91, 0))
	assert.Error(t, filter.ValidateGeoPoint(-91, 0))
	assert.Error(t, filter.ValidateGeoPoint(0, 181))
	assert.Error(t, filter.ValidateGeoPoint(0, -181))
	assert.Error(t, filter.ValidateGeoPoint(math.NaN(), 0))
	assert.Error(t, filter.ValidateGeoPoint(0, math.Inf(1)))
}

// --- ValidateGeoBoundingBox ---

func TestValidateGeoBoundingBox(t *testing.T) {
	valid := &filter.GeoBoundingBox{
		TopLeft:     filter.GeoPoint{Lat: 40.80, Lon: -74.10},
		BottomRight: filter.GeoPoint{Lat: 40.65, Lon: -73.70},
	}
	assert.NoError(t, filter.ValidateGeoBoundingBox(valid))

	inverted := &filter.GeoBoundingBox{
		TopLeft:     filter.GeoPoint{Lat: 40.65, Lon: -74.10},
		BottomRight: filter.GeoPoint{Lat: 40.80, Lon: -73.70},
	}
	assert.Error(t, filter.ValidateGeoBoundingBox(inverted))

	badCoord := &filter.GeoBoundingBox{
		TopLeft:     filter.GeoPoint{Lat: 91, Lon: 0},
		BottomRight: filter.GeoPoint{Lat: 40, Lon: 0},
	}
	assert.Error(t, filter.ValidateGeoBoundingBox(badCoord))
}

// --- ValidateGeoPolygon ---

func TestValidateGeoPolygon(t *testing.T) {
	valid := &filter.GeoPolygon{Points: []filter.GeoPoint{
		{Lat: 40.77, Lon: -73.98},
		{Lat: 40.73, Lon: -74.02},
		{Lat: 40.71, Lon: -73.95},
	}}
	assert.NoError(t, filter.ValidateGeoPolygon(valid))

	tooFew := &filter.GeoPolygon{Points: []filter.GeoPoint{
		{Lat: 40.77, Lon: -73.98},
		{Lat: 40.73, Lon: -74.02},
	}}
	assert.Error(t, filter.ValidateGeoPolygon(tooFew))

	tooMany := &filter.GeoPolygon{Points: make([]filter.GeoPoint, 101)}
	assert.Error(t, filter.ValidateGeoPolygon(tooMany))
}

// --- Geo Schema ---

func geoTestSchema() *filter.ResourceSchema {
	return &filter.ResourceSchema{
		Name: "devices",
		Fields: map[string]*filter.FieldSchema{
			"location": {
				ExternalName:     "location",
				SQLExpr:          "devices.location",
				DataType:         core.FieldTypeGeo,
				AllowedOperators: []filter.FilterOperator{filter.OpNear, filter.OpWithin},
				GeoConfig: &filter.GeoFieldConfig{
					StorageType: filter.GeoStorageJSONB,
					SRID:        4326,
					LatKey:      "lat",
					LonKey:      "lon",
				},
			},
		},
	}
}

// --- FilterParser: Geo $near ---

func TestFilterParser_GeoNear(t *testing.T) {
	fp := filter.NewFilterParser(geoTestSchema(), nil)
	ast, err := fp.Parse(`{"location":{"$near":{"lat":40.7128,"lon":-74.006,"distance":"5km"}}}`)
	require.NoError(t, err)
	require.NotNil(t, ast)
	require.NotNil(t, ast.Root)
	assert.Equal(t, filter.OpNear, ast.Root.Op)

	gd, ok := ast.Root.Value.(*filter.GeoDistance)
	require.True(t, ok, "expected *GeoDistance, got %T", ast.Root.Value)
	assert.InDelta(t, 40.7128, gd.Lat, 0.0001)
	assert.InDelta(t, -74.006, gd.Lon, 0.0001)
	assert.Equal(t, "5km", gd.Distance)
}

// --- FilterParser: Geo $within bbox ---

func TestFilterParser_GeoWithinBBox(t *testing.T) {
	fp := filter.NewFilterParser(geoTestSchema(), nil)
	input := `{"location":{"$within":{"top_left":{"lat":40.80,"lon":-74.10},"bottom_right":{"lat":40.65,"lon":-73.70}}}}`
	ast, err := fp.Parse(input)
	require.NoError(t, err)
	require.NotNil(t, ast.Root)
	assert.Equal(t, filter.OpWithin, ast.Root.Op)

	bb, ok := ast.Root.Value.(*filter.GeoBoundingBox)
	require.True(t, ok, "expected *GeoBoundingBox, got %T", ast.Root.Value)
	assert.InDelta(t, 40.80, bb.TopLeft.Lat, 0.01)
	assert.InDelta(t, -74.10, bb.TopLeft.Lon, 0.01)
}

// --- FilterParser: Geo $within polygon ---

func TestFilterParser_GeoWithinPolygon(t *testing.T) {
	fp := filter.NewFilterParser(geoTestSchema(), nil)
	input := `{"location":{"$within":{"points":[{"lat":40.77,"lon":-73.98},{"lat":40.73,"lon":-74.02},{"lat":40.71,"lon":-73.95}]}}}`
	ast, err := fp.Parse(input)
	require.NoError(t, err)
	require.NotNil(t, ast.Root)

	poly, ok := ast.Root.Value.(*filter.GeoPolygon)
	require.True(t, ok, "expected *GeoPolygon, got %T", ast.Root.Value)
	assert.Len(t, poly.Points, 3)
}

// --- FilterParser: Geo validation errors ---

func TestFilterParser_GeoNear_InvalidCoords(t *testing.T) {
	fp := filter.NewFilterParser(geoTestSchema(), nil)
	_, err := fp.Parse(`{"location":{"$near":{"lat":999,"lon":-74.006,"distance":"5km"}}}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "E2112")
}

func TestFilterParser_GeoNear_InvalidDistance(t *testing.T) {
	fp := filter.NewFilterParser(geoTestSchema(), nil)
	_, err := fp.Parse(`{"location":{"$near":{"lat":40.71,"lon":-74.00,"distance":"5parsecs"}}}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "E2113")
}

func TestFilterParser_GeoNear_MissingDistance(t *testing.T) {
	fp := filter.NewFilterParser(geoTestSchema(), nil)
	_, err := fp.Parse(`{"location":{"$near":{"lat":40.71,"lon":-74.00}}}`)
	assert.Error(t, err)
}

// --- SQL: Postgres $near ---

func TestGeoSQL_Postgres_Near(t *testing.T) {
	schema := geoTestSchema()
	fp := filter.NewFilterParser(schema, nil)
	ast, err := fp.Parse(`{"location":{"$near":{"lat":40.7128,"lon":-74.006,"distance":"5km"}}}`)
	require.NoError(t, err)

	builder := filter.NewSQLBuilder(schema, filter.PostgresDialect{})
	result, err := builder.Build(ast)
	require.NoError(t, err)

	assert.Contains(t, result.Where, "ST_DWithin")
	assert.Contains(t, result.Where, "ST_MakePoint")
	assert.Len(t, result.Args, 3)
	// Args: lon, lat, distance_meters.
	assert.InDelta(t, -74.006, result.Args[0], 0.001)
	assert.InDelta(t, 40.7128, result.Args[1], 0.001)
	assert.InDelta(t, 5000.0, result.Args[2], 0.1)
}

// --- SQL: Postgres $within bbox ---

func TestGeoSQL_Postgres_WithinBBox(t *testing.T) {
	schema := geoTestSchema()
	fp := filter.NewFilterParser(schema, nil)
	input := `{"location":{"$within":{"top_left":{"lat":40.80,"lon":-74.10},"bottom_right":{"lat":40.65,"lon":-73.70}}}}`
	ast, err := fp.Parse(input)
	require.NoError(t, err)

	builder := filter.NewSQLBuilder(schema, filter.PostgresDialect{})
	result, err := builder.Build(ast)
	require.NoError(t, err)

	assert.Contains(t, result.Where, "ST_CoveredBy") // boundary-inclusive, like SQLite's BETWEEN
	assert.Contains(t, result.Where, "ST_MakeEnvelope")
	assert.Equal(t, []any{-74.10, 40.65, -73.70, 40.80}, result.Args) // xmin, ymin, xmax, ymax
}

// A polygon binds one WKT ring, not an envelope's four corners.
func TestGeoSQL_Postgres_WithinPolygon(t *testing.T) {
	schema := geoTestSchema()
	fp := filter.NewFilterParser(schema, nil)
	ast, err := fp.Parse(`{"location":{"$within":{"points":[{"lat":0,"lon":0},{"lat":1,"lon":1},{"lat":0,"lon":1}]}}}`)
	require.NoError(t, err)

	result, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(ast)
	require.NoError(t, err)
	assert.Contains(t, result.Where, "ST_CoveredBy(")
	assert.Contains(t, result.Where, "ST_GeomFromText($1, 4326)")
	assert.Equal(t, []any{"POLYGON((0 0, 1 1, 1 0, 0 0))"}, result.Args)
}

// --- SQL: SQLite $near (Haversine) ---

func TestGeoSQL_SQLite_Near(t *testing.T) {
	schema := geoTestSchema()
	fp := filter.NewFilterParser(schema, nil)
	ast, err := fp.Parse(`{"location":{"$near":{"lat":40.7128,"lon":-74.006,"distance":"5km"}}}`)
	require.NoError(t, err)

	builder := filter.NewSQLBuilder(schema, filter.SQLiteDialect{})
	result, err := builder.Build(ast)
	require.NoError(t, err)

	assert.Contains(t, result.Where, "asin(min(1.0,") // haversine, clamped into asin's domain
	assert.Contains(t, result.Where, "json_extract")
	assert.Equal(t, []any{40.7128, -74.006, 5000.0}, result.Args)
}

// --- SQL: SQLite $within bbox ---

func TestGeoSQL_SQLite_WithinBBox(t *testing.T) {
	schema := geoTestSchema()
	fp := filter.NewFilterParser(schema, nil)
	input := `{"location":{"$within":{"top_left":{"lat":40.80,"lon":-74.10},"bottom_right":{"lat":40.65,"lon":-73.70}}}}`
	ast, err := fp.Parse(input)
	require.NoError(t, err)

	builder := filter.NewSQLBuilder(schema, filter.SQLiteDialect{})
	result, err := builder.Build(ast)
	require.NoError(t, err)

	assert.Contains(t, result.Where, "BETWEEN")
	assert.Contains(t, result.Where, "json_extract")
	assert.Len(t, result.Args, 4)
}

// --- SQL: SQLite polygon not supported ---

// SQLite evaluates neither polygons nor $intersects; both fail with E2115
// instead of compiling a polygon's points into a bounding-box predicate.
func TestGeoSQL_SQLite_UnsupportedShapes(t *testing.T) {
	schema := geoTestSchema()
	schema.Fields["location"].AllowedOperators = []filter.FilterOperator{filter.OpNear, filter.OpWithin, filter.OpIntersects}
	for name, input := range map[string]string{
		"within polygon":  `{"location":{"$within":{"points":[{"lat":0,"lon":0},{"lat":1,"lon":1},{"lat":0,"lon":1}]}}}`,
		"intersects bbox": `{"location":{"$intersects":{"top_left":{"lat":40.80,"lon":-74.10},"bottom_right":{"lat":40.65,"lon":-73.70}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			ast, err := filter.NewFilterParser(schema, nil).Parse(input)
			require.NoError(t, err)
			_, err = filter.NewSQLBuilder(schema, filter.SQLiteDialect{}).Build(ast)
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, filter.ECodeGeoNotSupported.String(), pe.Code)
		})
	}
}

// --- Schema Generator: geo tag ---

func TestGenerateFilterSchema_GeoTag(t *testing.T) {
	type GeoDevice struct {
		ID       string         `json:"id" parse:"filter" filter_ops:"eq"`
		Location map[string]any `json:"location" parse:"filter" filter_ops:"near,within" geo:"jsonb" geo_srid:"4326"`
	}

	gen := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[GeoDevice](gen)
	require.NoError(t, err)

	locField, ok := schema.Fields["location"]
	require.True(t, ok, "location field should be in schema")
	assert.Equal(t, core.FieldTypeGeo, locField.DataType)
	require.NotNil(t, locField.GeoConfig)
	assert.Equal(t, filter.GeoStorageJSONB, locField.GeoConfig.StorageType)
	assert.Equal(t, 4326, locField.GeoConfig.SRID)
	assert.Contains(t, locField.AllowedOperators, filter.OpNear)
	assert.Contains(t, locField.AllowedOperators, filter.OpWithin)
}

// --- Cost Estimation ---

func TestGeoCostEstimation(t *testing.T) {
	schema := geoTestSchema()
	fp := filter.NewFilterParser(schema, nil)
	ast, err := fp.Parse(`{"location":{"$near":{"lat":40.71,"lon":-74.00,"distance":"5km"}}}`)
	require.NoError(t, err)

	pf := filter.NewParsedFilter(ast)
	// Unindexed geo field should have high cost (50x base).
	assert.True(t, pf.EstimatedCost >= 50, "expected geo cost >= 50, got %d", pf.EstimatedCost)
}

func TestGeoCostEstimation_Indexed(t *testing.T) {
	schema := geoTestSchema()
	schema.Fields["location"].Indexed = true
	fp := filter.NewFilterParser(schema, nil)
	ast, err := fp.Parse(`{"location":{"$near":{"lat":40.71,"lon":-74.00,"distance":"5km"}}}`)
	require.NoError(t, err)

	pf := filter.NewParsedFilter(ast)
	// Indexed geo field should have moderate cost (5x base).
	assert.True(t, pf.EstimatedCost >= 5 && pf.EstimatedCost < 50,
		"expected indexed geo cost in [5, 50), got %d", pf.EstimatedCost)
}

// --- SQL output format verification ---

func TestGeoSQL_Postgres_Near_Format(t *testing.T) {
	schema := geoTestSchema()
	fp := filter.NewFilterParser(schema, nil)
	ast, err := fp.Parse(`{"location":{"$near":{"lat":40.71,"lon":-74.00,"distance":"1km"}}}`)
	require.NoError(t, err)

	builder := filter.NewSQLBuilder(schema, filter.PostgresDialect{})
	result, err := builder.Build(ast)
	require.NoError(t, err)

	// Verify SQL uses parameterized placeholders, not interpolated values.
	assert.Contains(t, result.Where, "$1")
	assert.Contains(t, result.Where, "$2")
	assert.Contains(t, result.Where, "$3")
	assert.False(t, strings.Contains(result.Where, "40.71"), "SQL should not contain interpolated lat")
}
