//go:build unit

package filter

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

func geoField(tag reflect.StructTag) *core.Tags {
	return &core.Tags{Geo: tag.Get("geo"), GeoSRID: tag.Get("geo_srid"), GeoLat: tag.Get("geo_lat"), GeoLon: tag.Get("geo_lon")}
}

func TestParseGeoTag(t *testing.T) {
	cfg, err := parseGeoTag(geoField(`geo:"jsonb" geo_lat:"$.latitude" geo_lon:"$.longitude"`))
	require.NoError(t, err)
	assert.Equal(t, &GeoFieldConfig{StorageType: GeoStorageJSONB, SRID: 4326, LatKey: "latitude", LonKey: "longitude"}, cfg)

	cfg, err = parseGeoTag(geoField(`geo:"geometry" geo_srid:"3857"`))
	require.NoError(t, err)
	assert.Equal(t, 3857, cfg.SRID)
}

// Keys are interpolated into ->>'key' and '$.key': anything but an
// identifier fails generation rather than falling back or reaching SQL.
func TestParseGeoTag_Rejects(t *testing.T) {
	for name, tag := range map[string]reflect.StructTag{
		"unknown storage":          `geo:"point"`,
		"quote injection":          `geo:"jsonb" geo_lat:"$.lat')::float8,(evil--"`,
		"space":                    `geo:"jsonb" geo_lat:"$.lat lon"`,
		"nested path":              `geo:"jsonb" geo_lat:"$.location.lat"`,
		"missing $. prefix":        `geo:"jsonb" geo_lat:"lat"`,
		"leading digit":            `geo:"jsonb" geo_lon:"$.1lon"`,
		"srid not numeric":         `geo:"geometry" geo_srid:"web"`,
		"projected jsonb":          `geo:"jsonb" geo_srid:"3857"`,
		"projected geography":      `geo:"geography" geo_srid:"3857"`,
		"member keys on geography": `geo:"geography" geo_lat:"$.lat"`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseGeoTag(geoField(tag))
			assert.Error(t, err)
		})
	}
}

func TestPolygonWKT_ClosesRingWithoutAliasing(t *testing.T) {
	pts := make([]GeoPoint, 3, 8) // spare capacity: appending the closing point must not write into it
	pts[0], pts[1], pts[2] = GeoPoint{Lat: 0, Lon: 0}, GeoPoint{Lat: 1, Lon: 1.5}, GeoPoint{Lat: 0, Lon: 1}
	assert.Equal(t, "POLYGON((0 0, 1.5 1, 1 0, 0 0))", polygonWKT(&GeoPolygon{Points: pts}))
	assert.Len(t, pts, 3)
}
