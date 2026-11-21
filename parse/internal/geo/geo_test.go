//go:build unit

package geo

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GeographicLib's inverse test set (geodtest.c, testcases): s12 to 1e-8 m.
func TestDistanceMatchesGeographicLib(t *testing.T) {
	cases := [][5]float64{
		{35.60777, -139.44815, -11.17491, -69.95921, 8935244.5604818305},
		{55.52454, 106.05087, 77.03196, 197.18234, 4105086.1713924406},
		{-21.97856, 142.59065, 41.84138, 98.56635, 8394328.894657671},
		{-66.99028, 112.2363, -12.70631, 285.90344, 11150344.2312080241},
		{-17.42761, 173.34268, -15.84784, 5.93557, 16076603.1631180673},
		{32.84994, 48.28919, -56.28556, 202.29132, 16727068.9438164461},
		{6.96833, 52.74123, -7.39675, 206.17291, 17102477.2496958388},
		{-50.56724, -16.30485, -33.56571, -94.97412, 6455670.5118668696},
		{-58.93002, -8.90775, -8.91104, 133.13503, 11756066.0219864627},
		{-68.82867, -74.28391, -50.63005, -8.36685, 3956936.926063544},
		{-10.62672, -32.0898, 5.883, -134.31681, 11470869.3864563009},
		{-21.76221, 166.90563, 48.72884, 213.97627, 9098627.3986554915},
		{-19.79938, -174.47484, -11.99349, -154.35109, 2319004.8601169389},
		{-11.95887, -116.94513, 4.57352, 7.16501, 13834722.5801401374},
		{-87.85331, 85.66836, 66.48646, 16.09921, 17286615.3147144645},
		{1.74708, 128.32011, -11.16617, 11.87109, 12942901.1241347408},
		{-25.72959, -144.90758, -57.70581, -269.17879, 9413446.7452453107},
		{-41.22777, 122.32875, -7.57291, 130.37946, 3812686.035106021},
		{11.01307, 138.25278, 6.62726, 247.05981, 11911190.819018408},
		{-29.47124, 95.14681, -27.46601, -69.15955, 13487015.8381145492},
	}
	for _, c := range cases {
		a, b := Point{c[0], c[1]}, Point{c[2], c[3]}
		assert.InDelta(t, c[4], Distance(a, b), 1e-8, "%v → %v", a, b)
		assert.InDelta(t, c[4], Distance(b, a), 1e-8, "%v → %v", b, a)
	}
}

// GeographicLib's WGS 84 regression cases (geodtest.c, GeodSolve*): nearly
// antipodal points, which need the astroid start and Newton's method, and
// the equatorial and meridional special cases.
func TestDistanceRegressions(t *testing.T) {
	tests := []struct {
		a, b  Point
		s12   float64
		delta float64
	}{
		{Point{40.6, -73.8}, Point{49.01666667, 2.55}, 5853226, 0.5},
		{Point{36.493349428792, 0}, Point{36.49334942879201, .0000008}, 0.072, 0.5e-3},
		{Point{88.202499451857, 0}, Point{-88.202499451857, 179.981022032992859592}, 20003898.214, 0.5e-3},
		{Point{89.262080389218, 0}, Point{-89.262080389218, 179.992207982775375662}, 20003925.854, 0.5e-3},
		{Point{89.333123580033, 0}, Point{-89.333123580032997687, 179.99295812360148422}, 20003926.881, 0.5e-3},
		{Point{56.320923501171, 0}, Point{-56.320923501171, 179.664747671772880215}, 19993558.287, 0.5e-3},
		{Point{52.784459512564, 0}, Point{-52.784459512563990912, 179.634407464943777557}, 19991596.095, 0.5e-3},
		{Point{48.522876735459, 0}, Point{-48.52287673545898293, 179.599720456223079643}, 19989144.774, 0.5e-3},
		{Point{0, 0}, Point{0, 179}, 19926189, 0.5},
		{Point{0, 0}, Point{0, 179.5}, 19980862, 0.5},
		{Point{0, 0}, Point{0, 180}, 20003931, 0.5},
		{Point{0, 0}, Point{1, 180}, 19893357, 0.5},
	}
	for _, tt := range tests {
		assert.InDelta(t, tt.s12, Distance(tt.a, tt.b), tt.delta, "%v → %v", tt.a, tt.b)
	}
}

func TestDistanceEdges(t *testing.T) {
	assert.Zero(t, Distance(Point{20.001, 0}, Point{20.001, 0}), "coincident points")
	assert.Zero(t, Distance(Point{90, 0}, Point{90, 180}), "the pole, at two longitudes")
	assert.InDelta(t, Distance(Point{-10, 170}, Point{-10, -175}), Distance(Point{-10, 170}, Point{-10, 185}), 1e-9, "across the antimeridian")
	for _, p := range []Point{{91, 0}, {math.NaN(), 0}, {0, math.NaN()}, {0, math.Inf(1)}} {
		assert.True(t, math.IsNaN(Distance(p, Point{0, 0})), "%v", p)
		assert.True(t, math.IsNaN(Distance(Point{0, 0}, p)), "%v", p)
	}
}

func TestCoveredBy(t *testing.T) {
	triangle := []Point{{0, 0}, {40, 0}, {0, 40}} // lat, lon
	// A square with a notch cut into its lower-left corner (lon as x).
	notched := []Point{{0, 15}, {0, 30}, {30, 30}, {30, 0}, {15, 0}, {12, 12}}
	tests := []struct {
		name string
		ring []Point
		p    Point
		want bool
	}{
		{"inside", triangle, Point{10, 10}, true},
		{"a vertex", triangle, Point{0, 0}, true},
		{"the last vertex", triangle, Point{0, 40}, true},
		{"on the closing edge", triangle, Point{0, 20}, true},
		{"on a diagonal edge", triangle, Point{20, 20}, true},
		{"on a diagonal edge off the grid", triangle, Point{20.5, 19.5}, true},
		{"just outside a diagonal edge", triangle, Point{20, 20.000001}, false},
		{"beyond a vertex along its edge", triangle, Point{0, 41}, false},
		{"outside", triangle, Point{25, 25}, false},
		{"south of the base", triangle, Point{-1, 10}, false},
		{"in the notch", notched, Point{10, 10}, false},
		{"at the notch's corner", notched, Point{0, 0}, false},
		{"beside the notch", notched, Point{20, 20}, true},
		{"the notch's inner vertex", notched, Point{12, 12}, true},
		{"either orientation", []Point{{0, 0}, {0, 40}, {40, 0}}, Point{10, 10}, true},
		{"an explicitly closed ring", []Point{{0, 0}, {40, 0}, {0, 40}, {0, 0}}, Point{10, 10}, true},
		{"a repeated point", []Point{{0, 0}, {40, 0}, {40, 0}, {0, 40}}, Point{10, 10}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CoveredBy(tt.p, tt.ring))
		})
	}
}

func TestPolygonWKB(t *testing.T) {
	ring := []Point{{0, 0}, {40, 0.5}, {-0.25, 40}}
	b := AppendPolygonWKB(nil, ring)
	require.Len(t, b, wkbHeader+16*4)
	got, err := ReadPolygonWKB(nil, b)
	require.NoError(t, err)
	assert.Equal(t, append(ring, ring[0]), got, "closed by its first point")

	for name, bad := range map[string][]byte{
		"empty":       nil,
		"big-endian":  append([]byte{0}, b[1:]...),
		"a point":     append(append([]byte{1}, 1, 0, 0, 0), b[5:]...),
		"two rings":   append(append([]byte{}, b[:5]...), append([]byte{2, 0, 0, 0}, b[9:]...)...),
		"truncated":   b[:len(b)-1],
		"extra bytes": append(append([]byte{}, b...), 0),
		"NaN":         AppendPolygonWKB(nil, []Point{{math.NaN(), 0}, {1, 1}, {0, 1}}),
	} {
		_, err := ReadPolygonWKB(nil, bad)
		assert.ErrorIs(t, err, errPolygonWKB, name)
	}
}
