package core

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"

	"github.com/ashtonian/pkg/gowalker"
)

// maxExactFloatInt is 2^53: every integer of magnitude ≤ this is exactly
// representable as a float64; above it, adjacent integers collapse.
const maxExactFloatInt = 1 << 53

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
	if k := t.Kind(); isNumericKind(k) {
		return k
	}
	return reflect.Invalid
}

// isNumericKind reports a Go integer or float kind.
func isNumericKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
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

// value is x as the Go integer NormalizeJSON gives it: int64, or uint64
// above math.MaxInt64.
func (x exactInt) value() any {
	if x.isUint {
		return x.u
	}
	return x.i
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
		if _, ok := scanNumber(string(n)); !ok {
			return 0, errNotNumber(string(n))
		}
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

// IsNumber reports whether v is of a numeric type: json.Number, or a Go
// integer or float kind, named types included. They are the operands
// CompareNumbers reads; a json.Number's text is checked where it is read.
func IsNumber(v any) bool {
	switch v.(type) {
	case json.Number:
		return true
	case nil:
		return false
	}
	return isNumericKind(reflect.TypeOf(v).Kind())
}

// CompareNumbers compares two numbers by value, returning -1, 0 or +1. An
// integer compares exactly however it is written — 9007199254740993,
// "9007199254740993.0" and "9.007199254740993e15" are one value — and
// against a float as the exact binary value the float holds: nothing is
// widened to float64. A literal that is not an integer, or is an integer
// beyond 64 bits, is the float64 nearest it. ok is false when an operand is
// not a number (IsNumber), is NaN, or is a json.Number that is no JSON
// number literal or lies beyond float64's range. It does not allocate.
func CompareNumbers(a, b any) (cmp int, ok bool) {
	x, ok := numberOperand(a)
	if !ok {
		return 0, false
	}
	y, ok := numberOperand(b)
	if !ok {
		return 0, false
	}
	switch {
	case x.kind == operandFloat && y.kind == operandFloat:
		return compareFloats(x.float(), y.float()), true
	case x.kind == operandFloat:
		return -compareIntFloat(y, x.float()), true
	case y.kind == operandFloat:
		return compareIntFloat(x, y.float()), true
	}
	return compareInts(x, y), true
}

// operand is a number reduced for comparison: an int64, a uint64 above
// math.MaxInt64, or the bits of a float64 other than NaN, tagged by kind. At
// two words it is reduced and compared in registers.
type operand struct {
	bits uint64
	kind operandKind
}

type operandKind uint8

const (
	operandSigned operandKind = iota + 1
	operandUnsigned
	operandFloat
)

func signedOperand(i int64) operand { return operand{bits: uint64(i), kind: operandSigned} }

func exactOperand(x exactInt) operand {
	if x.isUint {
		return operand{bits: x.u, kind: operandUnsigned}
	}
	return signedOperand(x.i)
}

func floatOperand(f float64) (operand, bool) {
	if math.IsNaN(f) {
		return operand{}, false
	}
	return operand{bits: math.Float64bits(f), kind: operandFloat}, true
}

func (o operand) float() float64 { return math.Float64frombits(o.bits) }

// numberOperand reduces v as CompareNumbers reads it.
func numberOperand(v any) (operand, bool) {
	switch n := v.(type) {
	case float64:
		return floatOperand(n)
	case int64:
		return signedOperand(n), true
	case json.Number:
		return literalOperand(string(n))
	case string, bool, nil:
		return operand{}, false
	}
	if x, ok := nativeInt(v); ok {
		return exactOperand(x), true
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Float32 || rv.Kind() == reflect.Float64 {
		return floatOperand(rv.Float())
	}
	return operand{}, false
}

// literalOperand reads a literal through the integer reader NormalizeInt
// uses, so the two agree on every integer; only a literal that is not one
// is read as a float.
func literalOperand(s string) (operand, bool) {
	lit, ok := scanNumber(s)
	if !ok {
		return operand{}, false
	}
	if x, r := lit.exactInteger(); r == integerExact {
		return exactOperand(x), true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil { // beyond float64's range
		return operand{}, false
	}
	return floatOperand(f)
}

// compareFloats orders two float64s other than NaN.
func compareFloats(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// compareInts orders two integer operands.
func compareInts(a, b operand) int {
	switch {
	case a.kind == operandUnsigned && b.kind == operandUnsigned:
		return cmp.Compare(a.bits, b.bits)
	case a.kind == operandUnsigned: // a > MaxInt64 ≥ b
		return 1
	case b.kind == operandUnsigned:
		return -1
	}
	return cmp.Compare(int64(a.bits), int64(b.bits))
}

// two63 is 2^63, exactly a float64.
const two63 = float64(1 << 63)

// compareIntFloat orders an integer operand against the exact value of f, a
// float64 other than NaN. Within int64 or uint64 range the float's integer
// part converts exactly, so only its fraction is left to compare.
func compareIntFloat(x operand, f float64) int {
	if x.kind == operandUnsigned { // x ≥ 2^63
		switch {
		case f < two63:
			return 1
		case f >= 2*two63:
			return -1
		}
		return cmp.Compare(x.bits, uint64(f)) // f ≥ 2^53 is an integer
	}
	switch {
	case f >= two63:
		return -1
	case f < -two63:
		return 1
	}
	whole := math.Trunc(f)
	if c := cmp.Compare(int64(x.bits), int64(whole)); c != 0 {
		return c
	}
	return cmp.Compare(whole, f)
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
	x, ok := nativeInt(v)
	if !ok {
		return exactInt{}, fmt.Errorf("expected a number, got %s", gowalker.TypeText(reflect.TypeOf(v)))
	}
	return x, nil
}

// nativeInt reads every Go integer kind, named types included; ok is false
// for any other value.
func nativeInt(v any) (exactInt, bool) {
	switch n := v.(type) {
	case int:
		return exactInt{i: int64(n)}, true
	case int8:
		return exactInt{i: int64(n)}, true
	case int16:
		return exactInt{i: int64(n)}, true
	case int32:
		return exactInt{i: int64(n)}, true
	case int64:
		return exactInt{i: n}, true
	case uint:
		return uintToExactInt(uint64(n)), true
	case uint8:
		return uintToExactInt(uint64(n)), true
	case uint16:
		return uintToExactInt(uint64(n)), true
	case uint32:
		return uintToExactInt(uint64(n)), true
	case uint64:
		return uintToExactInt(n), true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return exactInt{i: rv.Int()}, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return uintToExactInt(rv.Uint()), true
	}
	return exactInt{}, false
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

// parseIntLiteral reads a JSON number literal as an exact integer: "1e3" and
// "2.0" are integers, "2.5" is not.
func parseIntLiteral(s string) (exactInt, error) {
	lit, ok := scanNumber(s)
	if !ok {
		return exactInt{}, errNotNumber(s)
	}
	if !lit.exponentFitsInt32() {
		return exactInt{}, errExponentBeyondInt32(s)
	}
	x, r := lit.exactInteger()
	switch {
	case r == integerFraction:
		return exactInt{}, fmt.Errorf("expected an integer, got %s", s)
	case r == integerOverflow && lit.neg:
		return exactInt{}, fmt.Errorf("%s overflows int64", s)
	case r == integerOverflow:
		return exactInt{}, fmt.Errorf("%s overflows 64-bit integers", s)
	}
	return x, nil
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

// maxPortableExp bounds the exponent PortableDecimal hands to math/big; a
// float64's exponent range is far inside it.
const maxPortableExp = 400

// PortableDecimal reports whether a JSON number literal denotes exactly the
// decimal of its nearest float64's shortest form, so every backend compares
// the same value: PostgreSQL's numeric sees the literal, SQLite and Go see
// the float64. "0.1", "1.50" and "2e3" are portable; "0.10000000000000001"
// (which a float64 rounds to 0.1) and "1e400" (which overflows) are not.
func PortableDecimal(s string) bool {
	if len(s) > maxPortableLiteralLen {
		return false
	}
	lit, ok := scanNumber(s)
	if e := lit.exponent(); !ok || e > maxPortableExp || e < -maxPortableExp {
		return false
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

// NonFinite reports v, a value a column of type t holds, is NaN or an
// infinity: a float's, or a decimal's as the text PostgreSQL's numeric
// writes it ("NaN", "Infinity", "-Infinity"). No request value is one, a
// stored value may be (NormalizeStored), and none has a canonical text
// (CanonicalText), so no facet or key names one.
func NonFinite(t FieldType, v any) bool {
	return nonFiniteRank(t, v) != 0
}

// nonFiniteRank is v's place among the values of a column of type t as
// PostgreSQL orders them: -1 for -Infinity, before every finite value
// (0), 1 for Infinity, 2 for NaN.
func nonFiniteRank(t FieldType, v any) int {
	rv := reflect.ValueOf(v)
	switch {
	case t == FieldTypeFloat && (rv.Kind() == reflect.Float32 || rv.Kind() == reflect.Float64):
		f := rv.Float()
		switch {
		case math.IsNaN(f):
			return 2
		case math.IsInf(f, 1):
			return 1
		case math.IsInf(f, -1):
			return -1
		}
	case t == FieldTypeDecimal && rv.Kind() == reflect.String:
		switch rv.String() {
		case "NaN":
			return 2
		case "Infinity":
			return 1
		case "-Infinity":
			return -1
		}
	}
	return 0
}
