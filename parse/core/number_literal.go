package core

import (
	"fmt"
	"math"
)

// numberLiteral is a JSON number literal split into its parts.
type numberLiteral struct {
	neg bool
	// intDigits are the digits before the point; fracDigits the digits after
	// it, empty without a point.
	intDigits, fracDigits string
	// exp is the exponent with its sign, empty without one.
	exp string
}

// scanNumber splits s when it is one JSON number literal (RFC 8259 §6),
// -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?, and nothing else. It is
// the package's one number grammar: strconv and math/big read Go's, which
// also admits hexadecimal, a leading "+" or ".", leading zeros, digit
// separators and the names of infinity and NaN, so every literal passes
// through here before either sees it.
func scanNumber(s string) (numberLiteral, bool) {
	var lit numberLiteral
	i := 0
	if i < len(s) && s[i] == '-' {
		lit.neg = true
		i++
	}
	start := i
	if i < len(s) && s[i] == '0' {
		i++
	} else {
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	if lit.intDigits = s[start:i]; lit.intDigits == "" {
		return numberLiteral{}, false
	}
	if i < len(s) && s[i] == '.' {
		i++
		start = i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if lit.fracDigits = s[start:i]; lit.fracDigits == "" {
			return numberLiteral{}, false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		start = i
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		digits := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if i == digits {
			return numberLiteral{}, false
		}
		lit.exp = s[start:i]
	}
	if i != len(s) {
		return numberLiteral{}, false
	}
	return lit, true
}

// errNotNumber is the error for text that is not a JSON number literal.
func errNotNumber(s string) error { return fmt.Errorf("%q is not a number", s) }

// integral reports a literal written without a fraction or an exponent.
func (l numberLiteral) integral() bool { return l.fracDigits == "" && l.exp == "" }

// maxExponent bounds the exponent arithmetic is done with. A literal whose
// value it changes needs about 2^40 digits, far more than memory holds.
const maxExponent = 1 << 40

// exponent is the literal's exponent, 0 without one, saturated at
// ±maxExponent.
func (l numberLiteral) exponent() int64 {
	if l.exp == "" {
		return 0
	}
	digits := l.exp
	neg := digits[0] == '-'
	if digits[0] == '-' || digits[0] == '+' {
		digits = digits[1:]
	}
	var e int64
	for i := 0; i < len(digits) && e < maxExponent; i++ {
		e = e*10 + int64(digits[i]-'0')
	}
	e = min(e, maxExponent)
	if neg {
		return -e
	}
	return e
}

// exponentFitsInt32 reports an exponent parse reads: a Decimal's exponent
// is an int32, and the SDK refuses a literal whose exponent is not one, a
// zero's included.
func (l numberLiteral) exponentFitsInt32() bool {
	e := l.exponent()
	return math.MinInt32 <= e && e <= math.MaxInt32
}

// errExponentBeyondInt32 is the error for a literal exponentFitsInt32
// refuses.
func errExponentBeyondInt32(s string) error { return fmt.Errorf("%q has an exponent beyond int32", s) }

// literalInteger is how a literal reads as a 64-bit integer.
type literalInteger uint8

const (
	// integerExact: an integer within int64 or, above it, uint64.
	integerExact literalInteger = iota
	// integerFraction: not an integer.
	integerFraction
	// integerOverflow: an integer below math.MinInt64 or above
	// math.MaxUint64.
	integerOverflow
)

// exactInteger reads the literal as an integer, exactly and however it is
// written ("7", "7.0", "0.7e1", "700e-2"). It takes time linear in the
// literal's length whatever its exponent, and does not allocate.
func (l numberLiteral) exactInteger() (exactInt, literalInteger) {
	digits := int64(len(l.intDigits) + len(l.fracDigits))
	digit := func(k int64) byte {
		if k < int64(len(l.intDigits)) {
			return l.intDigits[k]
		}
		return l.fracDigits[k-int64(len(l.intDigits))]
	}
	// point is the point's place among the digits: the value is the digits
	// before it, then point-digits zeros when it lies past the last one.
	point := int64(len(l.intDigits)) + l.exponent()
	for k := max(point, 0); k < digits; k++ {
		if digit(k) != '0' {
			return exactInt{}, integerFraction
		}
	}
	var u uint64
	for k := int64(0); k < point; k++ {
		var d uint64
		if k < digits {
			d = uint64(digit(k) - '0')
		} else if u == 0 {
			break // zeros times a power of ten
		}
		if u > (math.MaxUint64-d)/10 {
			return exactInt{}, integerOverflow
		}
		u = u*10 + d
	}
	if !l.neg {
		return uintToExactInt(u), integerExact
	}
	if u > 1<<63 {
		return exactInt{}, integerOverflow
	}
	return exactInt{i: -int64(u)}, integerExact // u == 1<<63 wraps to math.MinInt64
}
