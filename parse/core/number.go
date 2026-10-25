package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
)

// maxExactFloatInt is 2^53: every integer of magnitude ≤ this is exactly
// representable as a float64; above it, adjacent integers collapse.
const maxExactFloatInt = 1 << 53

// maxLiteralLen / maxLiteralExp bound the literals NormalizeInt hands to
// math/big. A short literal such as "1e-1000000000" would otherwise make
// big.Rat materialise a billion-digit denominator.
const (
	maxLiteralLen = 64
	maxLiteralExp = 64
)

// NumericKind reports the numeric reflect.Kind a Go type decodes into
// (after pointer indirection), or reflect.Invalid for non-numeric types.
// Schemas record it so request values are range-checked against the real
// destination instead of a float64 approximation.
func NumericKind(t reflect.Type) reflect.Kind {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return reflect.Invalid
	}
	switch k := t.Kind(); k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return k
	}
	return reflect.Invalid
}

// exactInt is an integer known exactly: i when it fits int64, otherwise u
// (only for values above math.MaxInt64).
type exactInt struct {
	i      int64
	u      uint64
	isUint bool
}

func (x exactInt) String() string {
	if x.isUint {
		return strconv.FormatUint(x.u, 10)
	}
	return strconv.FormatInt(x.i, 10)
}

// NormalizeInt converts a decoded request value into an exact int64 in the
// domain of the signed kind (reflect.Invalid is the int64 domain). It accepts
// json.Number, every Go integer kind, and floats that are integral and within
// ±2^53 — a float beyond that has already lost precision upstream, so it is
// rejected rather than guessed. Fractional values, NaN/Inf, strings and
// out-of-range values are errors, as is an unsigned kind (NormalizeUint).
func NormalizeInt(v any, kind reflect.Kind) (int64, error) {
	if isUnsignedKind(kind) {
		return 0, fmt.Errorf("NormalizeInt: %s is unsigned; use NormalizeUint", kind)
	}
	x, err := toExactInt(v)
	if err != nil {
		return 0, err
	}
	lo, hi := intRange(kind)
	if x.isUint || x.i < lo || x.i > hi {
		return 0, fmt.Errorf("%s overflows %s", x, domainName(kind, "int64"))
	}
	return x.i, nil
}

// NormalizeUint is NormalizeInt for an unsigned kind (reflect.Invalid is the
// uint64 domain): a negative value is an error.
func NormalizeUint(v any, kind reflect.Kind) (uint64, error) {
	if kind != reflect.Invalid && !isUnsignedKind(kind) {
		return 0, fmt.Errorf("NormalizeUint: %s is signed; use NormalizeInt", kind)
	}
	x, err := toExactInt(v)
	if err != nil {
		return 0, err
	}
	if !x.isUint && x.i < 0 {
		return 0, fmt.Errorf("%s is negative; %s is unsigned", x, domainName(kind, "uint64"))
	}
	u := x.u
	if !x.isUint {
		u = uint64(x.i)
	}
	if u > maxUint(kind) {
		return 0, fmt.Errorf("%s overflows %s", x, domainName(kind, "uint64"))
	}
	return u, nil
}

// NormalizeFloat converts a decoded request value into a finite float64
// with the precision of kind (reflect.Float32, otherwise float64). Integers
// are accepted; literals are parsed directly at the destination precision
// so SQL parameters and typed Go values round identically.
func NormalizeFloat(v any, kind reflect.Kind) (float64, error) {
	var f float64
	switch n := v.(type) {
	case json.Number:
		bits := 64
		if kind == reflect.Float32 {
			bits = 32
		}
		parsed, err := strconv.ParseFloat(string(n), bits)
		if err != nil {
			return 0, fmt.Errorf("%s is not a representable number", n)
		}
		f = parsed
	case float64:
		f = n
	case float32:
		f = float64(n)
	default:
		if rv := reflect.ValueOf(v); rv.IsValid() && (rv.Kind() == reflect.Float32 || rv.Kind() == reflect.Float64) {
			f = rv.Float()
			break
		}
		x, err := toExactInt(v)
		if err != nil {
			return 0, err
		}
		if x.isUint {
			f = float64(x.u)
			if kind == reflect.Float32 {
				f = float64(float32(x.u))
			}
		} else {
			f = float64(x.i)
			if kind == reflect.Float32 {
				f = float64(float32(x.i))
			}
		}
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("number must be finite")
	}
	if kind == reflect.Float32 {
		rounded := float64(float32(f))
		if math.IsInf(rounded, 0) {
			return 0, fmt.Errorf("%v overflows float32", f)
		}
		f = rounded
	}
	return f, nil
}

// IsNumber reports whether v is a numeric value NormalizeInt / NormalizeFloat
// / CompareNumbers accept: json.Number or any Go integer or float kind.
func IsNumber(v any) bool {
	switch v.(type) {
	case json.Number, float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return true
	}
	return false
}

// CompareNumbers compares two numeric values exactly — no float64 widening
// of integers — returning -1, 0 or +1. ok is false when either operand is
// not a number (see IsNumber) or is NaN.
func CompareNumbers(a, b any) (cmp int, ok bool) {
	na, ok := toComparable(a)
	if !ok {
		return 0, false
	}
	nb, ok := toComparable(b)
	if !ok {
		return 0, false
	}
	switch {
	case !na.isFloat && !nb.isFloat:
		return compareExactInts(na.x, nb.x), true
	case na.isFloat && nb.isFloat:
		return cmpFloat(na.f, nb.f), true
	}
	return na.big().Cmp(nb.big()), true
}

// comparableNum is a number reduced for exact comparison: an exact integer,
// or a finite float64 compared as the exact binary value it holds.
type comparableNum struct {
	x       exactInt
	f       float64
	isFloat bool
}

func (c comparableNum) big() *big.Float {
	if c.isFloat {
		return new(big.Float).SetFloat64(c.f)
	}
	if c.x.isUint {
		return new(big.Float).SetUint64(c.x.u)
	}
	return new(big.Float).SetInt64(c.x.i)
}

func toComparable(v any) (comparableNum, bool) {
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) {
			return comparableNum{}, false
		}
		return comparableNum{f: n, isFloat: true}, true
	case float32:
		if math.IsNaN(float64(n)) {
			return comparableNum{}, false
		}
		return comparableNum{f: float64(n), isFloat: true}, true
	case json.Number:
		s := string(n)
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return comparableNum{x: exactInt{i: i}}, true
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return comparableNum{x: exactInt{u: u, isUint: true}}, true
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) {
			return comparableNum{}, false
		}
		return comparableNum{f: f, isFloat: true}, true
	}
	if rv := reflect.ValueOf(v); rv.IsValid() && (rv.Kind() == reflect.Float32 || rv.Kind() == reflect.Float64) {
		f := rv.Float()
		if math.IsNaN(f) {
			return comparableNum{}, false
		}
		return comparableNum{f: f, isFloat: true}, true
	}
	x, err := nativeInt(v)
	if err != nil {
		return comparableNum{}, false
	}
	return comparableNum{x: x}, true
}

func compareExactInts(a, b exactInt) int {
	switch {
	case a.isUint && b.isUint:
		return cmpOrdered(a.u, b.u)
	case a.isUint: // a > MaxInt64 ≥ b
		return 1
	case b.isUint:
		return -1
	}
	return cmpOrdered(a.i, b.i)
}

func cmpOrdered[T int64 | uint64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// toExactInt reduces v to an exact integer, accepting json.Number, Go
// integer kinds, and integral floats within ±2^53.
func toExactInt(v any) (exactInt, error) {
	switch n := v.(type) {
	case json.Number:
		return parseIntLiteral(string(n))
	case float64:
		return floatToExactInt(n)
	case float32:
		return floatToExactInt(float64(n))
	case nil:
		return exactInt{}, errors.New("expected a number, got null")
	}
	x, err := nativeInt(v)
	if err != nil {
		return exactInt{}, fmt.Errorf("expected a number, got %T", v)
	}
	return x, nil
}

// nativeInt handles every Go integer kind, including named types.
func nativeInt(v any) (exactInt, error) {
	switch n := v.(type) {
	case int:
		return exactInt{i: int64(n)}, nil
	case int8:
		return exactInt{i: int64(n)}, nil
	case int16:
		return exactInt{i: int64(n)}, nil
	case int32:
		return exactInt{i: int64(n)}, nil
	case int64:
		return exactInt{i: n}, nil
	case uint, uint8, uint16, uint32, uint64:
		return uintToExactInt(reflect.ValueOf(n).Uint()), nil
	}
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return exactInt{}, errors.New("not an integer")
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return exactInt{i: rv.Int()}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return uintToExactInt(rv.Uint()), nil
	}
	return exactInt{}, errors.New("not an integer")
}

func uintToExactInt(u uint64) exactInt {
	if u <= math.MaxInt64 {
		return exactInt{i: int64(u)}
	}
	return exactInt{u: u, isUint: true}
}

func floatToExactInt(f float64) (exactInt, error) {
	switch {
	case math.IsNaN(f) || math.IsInf(f, 0):
		return exactInt{}, errors.New("number must be finite")
	case f != math.Trunc(f):
		return exactInt{}, fmt.Errorf("expected an integer, got %v", f)
	case math.Abs(f) > maxExactFloatInt:
		return exactInt{}, fmt.Errorf("integer %v arrived as a float beyond ±2^53 and has already lost precision; decode request bodies with a number-preserving codec", f)
	}
	return exactInt{i: int64(f)}, nil
}

// parseIntLiteral parses a JSON number literal exactly. Plain integers take
// the strconv fast path; fraction/exponent forms ("1e3", "2.0") are accepted
// when they denote an integer.
func parseIntLiteral(s string) (exactInt, error) {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return exactInt{i: i}, nil
	} else if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(s, "-") {
		if u, uerr := strconv.ParseUint(s, 10, 64); uerr == nil {
			return exactInt{u: u, isUint: true}, nil
		}
		return exactInt{}, fmt.Errorf("%s overflows 64-bit integers", s)
	} else if errors.Is(err, strconv.ErrRange) {
		return exactInt{}, fmt.Errorf("%s overflows int64", s)
	}
	if !strings.ContainsAny(s, ".eE") {
		return exactInt{}, fmt.Errorf("%q is not a number", s)
	}
	if len(s) > maxLiteralLen || literalExponent(s) > maxLiteralExp {
		return exactInt{}, fmt.Errorf("%s is not an exact 64-bit integer", s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return exactInt{}, fmt.Errorf("%q is not a number", s)
	}
	if !r.IsInt() {
		return exactInt{}, fmt.Errorf("expected an integer, got %s", s)
	}
	n := r.Num()
	if n.IsInt64() {
		return exactInt{i: n.Int64()}, nil
	}
	if n.IsUint64() {
		return exactInt{u: n.Uint64(), isUint: true}, nil
	}
	return exactInt{}, fmt.Errorf("%s overflows 64-bit integers", s)
}

// literalExponent returns |exponent| of a number literal, or 0 when absent
// or unparseable (the big.Rat parse then decides validity).
func literalExponent(s string) int {
	i := strings.IndexAny(s, "eE")
	if i < 0 {
		return 0
	}
	e, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return maxLiteralExp + 1
	}
	if e < 0 {
		return -e
	}
	return e
}

func isUnsignedKind(k reflect.Kind) bool {
	switch k {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return false
}

func maxUint(k reflect.Kind) uint64 {
	switch k {
	case reflect.Uint8:
		return math.MaxUint8
	case reflect.Uint16:
		return math.MaxUint16
	case reflect.Uint32:
		return math.MaxUint32
	case reflect.Uint:
		return math.MaxUint
	}
	return math.MaxUint64
}

func intRange(k reflect.Kind) (int64, int64) {
	switch k {
	case reflect.Int8:
		return math.MinInt8, math.MaxInt8
	case reflect.Int16:
		return math.MinInt16, math.MaxInt16
	case reflect.Int32:
		return math.MinInt32, math.MaxInt32
	case reflect.Int:
		return math.MinInt, math.MaxInt
	}
	return math.MinInt64, math.MaxInt64
}

func domainName(k reflect.Kind, fallback string) string {
	if k == reflect.Invalid {
		return fallback
	}
	return k.String()
}

// maxPortableLiteralLen bounds a decimal literal PortableDecimal inspects; a
// longer literal carries more digits than any float64 holds anyway.
const maxPortableLiteralLen = 128

// PortableDecimal reports whether a JSON number literal denotes exactly the
// decimal of its nearest float64's shortest form, so every backend compares
// the same value: PostgreSQL's numeric sees the literal, SQLite and Go see
// the float64. "0.1", "1.50" and "2e3" are portable; "0.10000000000000001"
// (which a float64 rounds to 0.1) and "1e400" (which overflows) are not.
func PortableDecimal(s string) bool {
	if len(s) > maxPortableLiteralLen {
		return false
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(strings.TrimPrefix(s[i+1:], "+"))
		if err != nil || exp > 400 || exp < -400 {
			return false
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) {
		return false
	}
	want, ok := new(big.Rat).SetString(s)
	if !ok {
		return false
	}
	got, _ := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
	return got != nil && want.Cmp(got) == 0
}
