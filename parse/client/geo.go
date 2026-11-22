package client

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// Point is a WGS 84 coordinate.
type Point struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// Shape is a region: a Box or a Polygon.
type Shape struct {
	topLeft, bottomRight Point
	points               []Point
}

// Box is the region between a top-left and a bottom-right corner.
func Box(topLeft, bottomRight Point) Shape {
	return Shape{topLeft: topLeft, bottomRight: bottomRight}
}

// Polygon is the region inside points, in order; the server takes 3 to 100.
func Polygon(points ...Point) Shape { return Shape{points: list(points)} }

// MarshalJSON encodes the shape as the server reads it.
func (s Shape) MarshalJSON() ([]byte, error) {
	if s.points != nil {
		return json.Marshal(struct {
			Points []Point `json:"points"`
		}{s.points})
	}
	return json.Marshal(struct {
		TopLeft     Point `json:"top_left"`
		BottomRight Point `json:"bottom_right"`
	}{s.topLeft, s.bottomRight})
}

// Geo is a geographic field.
type Geo struct{ name string }

// NewGeo returns the geographic field named name.
func NewGeo(name string) Geo { return Geo{name: name} }

// Name is the field's name in the grammar.
func (g Geo) Name() string { return g.name }

// Near matches rows within meters of p. A distance that is not a positive,
// finite length is ErrOperand: the grammar's distances are positive.
func (g Geo) Near(p Point, meters float64) Filter {
	if !(meters > 0) || math.IsInf(meters, 1) {
		return Filter{err: fmt.Errorf("%w: %s %s: distance %vm is not a positive length", ErrOperand, g.name, opNear, meters)}
	}
	return condition(g.name, opNear, struct {
		Point
		Distance string `json:"distance"`
	}{p, strconv.FormatFloat(meters, 'f', -1, 64) + "m"})
}

// Within matches rows inside s.
func (g Geo) Within(s Shape) Filter { return condition(g.name, opWithin, s) }

// Intersects matches rows whose geometry intersects s.
func (g Geo) Intersects(s Shape) Filter { return condition(g.name, opIntersects, s) }

// Null matches rows whose field is null.
func (g Geo) Null() Filter { return isNull(g.name) }

// NotNull matches rows whose field is not null.
func (g Geo) NotNull() Filter { return notNull(g.name) }
