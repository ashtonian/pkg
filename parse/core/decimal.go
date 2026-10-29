package core

import (
	"cmp"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

// Decimal is an exact decimal number, Coefficient × 10^Exponent: the method
// set of github.com/shopspring/decimal.Decimal. A Go type with it is a
// decimal field (FieldTypeDecimal), recognized by shape so this package
// depends on no decimal library; a decimal type with another method set
// declares its column with `storage:"scalar:decimal"`.
type Decimal interface {
	Coefficient() *big.Int
	Exponent() int32
}

var decimalInterface = reflect.TypeFor[Decimal]()

// IsDecimalType reports a decimal type: a named type whose value or pointer
// has the Decimal method set.
func IsDecimalType(t reflect.Type) bool {
	if t.Name() == "" {
		return false
	}
	if known, ok := decimalTypes.Load(t); ok {
		is, _ := known.(bool) // only bools are stored
		return is
	}
	is := t.Implements(decimalInterface) || reflect.PointerTo(t).Implements(decimalInterface)
	decimalTypes.Store(t, is)
	return is
}

// decimalTypes caches IsDecimalType's method-set check per named type.
var decimalTypes sync.Map // reflect.Type -> bool

// MaxDecimalDigits bounds the digits of a decimal's plain notation, the
// "0" before the point of a fraction included: PostgreSQL's limit on a
// declared numeric precision. A short exponent literal ("1e999999") would
// otherwise render, bind and compare a million digits.
const MaxDecimalDigits = 1000

// DecimalText is v's canonical decimal text: plain notation, no exponent,
// no leading zeros, no trailing fractional zeros, and "0" for a zero of
// either sign. One value has one text, so equal decimals have equal text
// ("1.50", "1.5e0" and 1.5 are all "1.5").
//
// It accepts a JSON number or a string in the JSON number grammar (the wire
// form of shopspring's decimal is a string), any Go integer, a finite float
// (its shortest decimal form, as strconv formats it), a Decimal, and a type
// whose text (encoding.TextMarshaler) is a decimal. Hexadecimal, fractions,
// NaN, infinities, a leading "+" or "." and anything with more than
// MaxDecimalDigits digits in plain notation are errors.
func DecimalText(v any) (string, error) {
	v = deref(v)
	switch d := v.(type) {
	case string:
		return parseDecimal(d)
	case json.Number:
		return parseDecimal(string(d))
	case Decimal:
		return partsDecimal(d.Coefficient(), d.Exponent())
	case float64:
		return floatDecimal(d, 64)
	case float32:
		return floatDecimal(float64(d), 32)
	case nil:
		return "", errors.New("expected a decimal, got null")
	}
	rv := reflect.ValueOf(v)
	if IsDecimalType(rv.Type()) {
		// IsDecimalType admits a type whose pointer has the method set.
		p := reflect.New(rv.Type())
		p.Elem().Set(rv)
		if d, ok := p.Interface().(Decimal); ok {
			return partsDecimal(d.Coefficient(), d.Exponent())
		}
	}
	if tm, ok := v.(encoding.TextMarshaler); ok {
		b, err := tm.MarshalText()
		if err != nil {
			return "", fmt.Errorf("marshal %T: %w", v, err)
		}
		return parseDecimal(string(b))
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return floatDecimal(rv.Float(), rv.Type().Bits())
	case reflect.String:
		return parseDecimal(rv.String())
	}
	return "", fmt.Errorf("expected a decimal number or numeric string, got %T", v)
}

// CompareDecimals compares two decimals exactly, returning -1, 0 or +1.
// Each operand is anything DecimalText accepts, pointers dereferenced; ok is
// false when either is not a decimal.
func CompareDecimals(a, b any) (cmp int, ok bool) {
	x, err := DecimalText(a)
	if err != nil {
		return 0, false
	}
	y, err := DecimalText(b)
	if err != nil {
		return 0, false
	}
	return compareDecimalText(x, y), true
}

// compareDecimalText orders two canonical decimal texts: by sign, then by
// magnitude, inverted for negatives.
func compareDecimalText(x, y string) int {
	xneg, yneg := strings.HasPrefix(x, "-"), strings.HasPrefix(y, "-")
	switch {
	case xneg && !yneg:
		return -1
	case !xneg && yneg:
		return 1
	case xneg:
		return compareMagnitude(y[1:], x[1:])
	}
	return compareMagnitude(x, y)
}

// compareMagnitude orders two unsigned canonical texts. Their integer parts
// carry no leading zeros, so the longer is the greater; equal lengths
// compare digit by digit, then the fractions, whose last digit is never
// zero, so a fraction that is a prefix of the other is the smaller.
func compareMagnitude(x, y string) int {
	xi, xf, _ := strings.Cut(x, ".")
	yi, yf, _ := strings.Cut(y, ".")
	if c := cmp.Compare(len(xi), len(yi)); c != 0 {
		return c
	}
	if c := strings.Compare(xi, yi); c != 0 {
		return c
	}
	return strings.Compare(xf, yf)
}

// parseDecimal reads s in the JSON number grammar,
// -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?, into canonical text.
func parseDecimal(s string) (string, error) {
	invalid := func() (string, error) { return "", fmt.Errorf("%q is not a decimal number", s) }
	i := 0
	neg := i < len(s) && s[i] == '-'
	if neg {
		i++
	}
	intStart := i
	if i < len(s) && s[i] == '0' {
		i++
	} else {
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	intPart := s[intStart:i]
	if intPart == "" {
		return invalid()
	}
	var frac string
	if i < len(s) && s[i] == '.' {
		i++
		start := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if frac = s[start:i]; frac == "" {
			return invalid()
		}
	}
	var exp int64
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		start := i
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		digits := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if i == digits {
			return invalid()
		}
		e, err := strconv.ParseInt(s[start:i], 10, 32)
		if err != nil {
			return "", fmt.Errorf("%q has more than %d digits", s, MaxDecimalDigits)
		}
		exp = e
	}
	if i != len(s) {
		return invalid()
	}
	if exp == 0 && !strings.HasSuffix(frac, "0") && !(neg && intPart == "0" && frac == "") {
		// Already canonical: the grammar admits no leading zeros.
		if len(intPart)+len(frac) > MaxDecimalDigits {
			return "", fmt.Errorf("%q has more than %d digits", s, MaxDecimalDigits)
		}
		return s, nil
	}
	text, ok := renderDecimal(neg, intPart, frac, exp)
	if !ok {
		return "", fmt.Errorf("%q has more than %d digits", s, MaxDecimalDigits)
	}
	return text, nil
}

// partsDecimal renders coefficient × 10^exponent.
func partsDecimal(coefficient *big.Int, exponent int32) (string, error) {
	digits := coefficient.String()
	neg := strings.HasPrefix(digits, "-")
	if neg {
		digits = digits[1:]
	}
	text, ok := renderDecimal(neg, digits, "", int64(exponent))
	if !ok {
		return "", fmt.Errorf("%se%d has more than %d digits", coefficient, exponent, MaxDecimalDigits)
	}
	return text, nil
}

// floatDecimal renders a finite float's shortest decimal at its precision
// (bits 32 or 64).
func floatDecimal(f float64, bits int) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", errors.New("decimal must be finite")
	}
	return parseDecimal(strconv.FormatFloat(f, 'g', -1, bits))
}

// renderDecimal renders the digits intPart‖frac with the point after
// len(intPart)+exp of them, in canonical plain notation; ok is false when
// that takes more than MaxDecimalDigits digits.
func renderDecimal(neg bool, intPart, frac string, exp int64) (text string, ok bool) {
	n := len(intPart) + len(frac)
	digit := func(k int) byte {
		if k < len(intPart) {
			return intPart[k]
		}
		return frac[k-len(intPart)]
	}
	lead := 0
	for lead < n && digit(lead) == '0' {
		lead++
	}
	if lead == n {
		return "0", true
	}
	trail := n
	for digit(trail-1) == '0' {
		trail--
	}
	sig := trail - lead
	// point is the point's place among the significant digits: before the
	// first when 0, after the last when sig.
	point := int64(len(intPart)) + exp - int64(lead)
	var width int64
	switch {
	case point <= 0:
		width = 1 - point + int64(sig) // "0." and the zeros before the digits
	case point >= int64(sig):
		width = point // the digits and trailing zeros
	default:
		width = int64(sig)
	}
	if width > MaxDecimalDigits {
		return "", false
	}
	var b strings.Builder
	b.Grow(int(width) + 2)
	if neg {
		b.WriteByte('-')
	}
	writeDigits := func(from, to int) {
		for k := from; k < to; k++ {
			b.WriteByte(digit(k))
		}
	}
	switch {
	case point <= 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", int(-point)))
		writeDigits(lead, trail)
	case point >= int64(sig):
		writeDigits(lead, trail)
		b.WriteString(strings.Repeat("0", int(point)-sig))
	default:
		writeDigits(lead, lead+int(point))
		b.WriteByte('.')
		writeDigits(lead+int(point), trail)
	}
	return b.String(), true
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }
