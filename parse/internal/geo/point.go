package geo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ReadPoint reads a stored JSONB point, decoded from JSON with numbers as
// json.Number, as PostgreSQL reads (doc->>latKey)::float8 and
// (doc->>lonKey)::float8. ok is false, the point NULL, when doc is not an
// object or either member is not a Coordinate.
func ReadPoint(doc any, latKey, lonKey string) (Point, bool) {
	obj, isObject := doc.(map[string]any)
	if !isObject {
		return Point{}, false
	}
	lat, latOK := Coordinate(obj[latKey])
	lon, lonOK := Coordinate(obj[lonKey])
	return Point{Lat: lat, Lon: lon}, latOK && lonOK
}

// ReadPointJSON is ReadPoint of the point's JSON text. A duplicated member
// reads its last value, as jsonb keeps it.
func ReadPointJSON(text []byte, latKey, lonKey string) (Point, bool, error) {
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return Point{}, false, fmt.Errorf("geo: decode point: %w", err)
	}
	p, ok := ReadPoint(doc, latKey, lonKey)
	return p, ok, nil
}

// Coordinate reads a point member, decoded from JSON with numbers as
// json.Number, as PostgreSQL's float8 input reads the member's ->> text: a
// JSON number, or a JSON string holding a number as float8 accepts it. ok
// is false for a missing or null member and wherever PostgreSQL refuses the
// query instead: a boolean, an object or array, text float8 rejects, a value
// beyond float8's range. NaN and the infinities are coordinates; no point
// holding one is near, in or on anything. A float64 is no member: every
// reader of a stored point decodes it with UseNumber, so the number's text
// is what float8 reads.
func Coordinate(member any) (float64, bool) {
	switch m := member.(type) {
	case json.Number:
		return float8(string(m))
	case string:
		return float8(m)
	}
	return 0, false
}

// float8 is PostgreSQL's float8 input (float8in): whitespace around the
// value; a decimal number, or a hexadecimal one as strtod reads it, whose
// binary exponent is optional; or NaN (with an optional payload), Inf or
// Infinity, in any case, the last two signed. ok is false for anything else
// and for a value float8 cannot hold: one that overflows, or a nonzero one
// that underflows to zero.
func float8(s string) (float64, bool) {
	s = strings.Trim(s, " \t\n\v\f\r")
	sign := ""
	if s != "" && (s[0] == '+' || s[0] == '-') {
		sign, s = s[:1], s[1:]
	}
	switch lower := strings.ToLower(s); {
	case lower == "inf" || lower == "infinity":
		if sign == "-" {
			return math.Inf(-1), true
		}
		return math.Inf(1), true
	case lower == "nan" || nanPayload(lower):
		return math.NaN(), true
	}
	if s == "" || s[0] == '+' || s[0] == '-' || strings.ContainsRune(s, '_') {
		return 0, false
	}
	mantissa := s
	if hex := len(s) > 1 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X'); hex {
		if i := strings.IndexAny(s, "pP"); i >= 0 {
			mantissa = s[2:i]
		} else {
			mantissa, s = s[2:], s+"p0"
		}
	} else if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa = s[:i]
	}
	f, err := strconv.ParseFloat(sign+s, 64)
	if err != nil {
		return 0, false
	}
	// strconv rounds a nonzero value below float8's range to zero; float8
	// refuses it.
	if f == 0 && strings.ContainsAny(mantissa, "123456789abcdefABCDEF") {
		return 0, false
	}
	return f, true
}

// nanPayload reports whether s, lower-cased, is strtod's nan(chars).
func nanPayload(s string) bool {
	inner, ok := strings.CutPrefix(s, "nan(")
	if !ok {
		return false
	}
	if inner, ok = strings.CutSuffix(inner, ")"); !ok {
		return false
	}
	for _, c := range inner {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c == '_') {
			return false
		}
	}
	return true
}
