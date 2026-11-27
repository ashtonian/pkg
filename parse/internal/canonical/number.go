package canonical

import (
	"encoding/json"
	"strconv"
	"strings"
)

// numberForm is literal, a JSON number as written, in the one spelling
// every literal of its value and display scale shares, which is where
// jsonb's text equality holds two numbers equal: numeric writes a number
// as its value padded to its display scale, the literal's fractional
// digits less its exponent, none below zero (1e2, 100 and 10e1 are 100;
// 1.50e1 is 15.0; 1.0 is not 1; -0 is 0).
//
// The form is built from the literal's parts and never pads: its length
// is linear in the literal's, so 1e100000 costs what its text costs and an
// exponent of any size compares exactly. A number with no display scale is
// its coefficient without leading or trailing zeros, the trailing zeros
// folded into the exponent (100 is 1e2). One with a display scale S is its
// coefficient without leading zeros at exponent -S (15.0 is 150e-1): a JSON
// number spells its scale by its digits, so the zeros the scale counts
// stay. Zero is 0, or 0e-S; it has no sign.
func numberForm(literal json.Number) json.Number {
	text := string(literal)
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	mantissa, exp := text, ""
	e := strings.IndexAny(text, "eE")
	if e >= 0 {
		mantissa, exp = text[:e], text[e+1:]
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	coefficient := strings.TrimLeft(whole+fraction, "0")
	exponent := parseInteger(exp)
	sign := ""
	if negative && coefficient != "" {
		sign = "-"
	}

	scale := exponent.add(-len(fraction)).negate()
	if scale.positive() {
		if coefficient == "" {
			return json.Number("0e-" + scale.digits)
		}
		return json.Number(sign + coefficient + "e-" + scale.digits)
	}
	if coefficient == "" {
		return "0"
	}
	digits := strings.TrimRight(coefficient, "0")
	// With no display scale the exponent is at least the fraction's
	// length, so the value's exponent is not negative.
	power := exponent.add(len(coefficient) - len(digits) - len(fraction))
	if power.digits == "" {
		return json.Number(sign + digits)
	}
	return json.Number(sign + digits + "e" + power.digits)
}

// integer is an integer of any size: its sign and its decimal digits
// without leading zeros, none for zero. A JSON number's exponent is one,
// and its arithmetic here is linear in the exponent's digits.
type integer struct {
	negative bool
	digits   string
}

// parseInteger is the integer text spells: an optional sign, then decimal
// digits, as a JSON number's exponent is written; "" is zero.
func parseInteger(text string) integer {
	digits := strings.TrimLeft(strings.TrimLeft(text, "+-"), "0")
	n := integer{negative: strings.HasPrefix(text, "-") && digits != "", digits: digits}
	return n
}

// smallDigits is the most digits an integer's int64 arithmetic takes: an
// integer of fewer digits plus a literal's length stays within int64.
const smallDigits = 18

// add is n + k, where k is at most a literal's length.
func (n integer) add(k int) integer {
	var sum integer
	if len(n.digits) < smallDigits {
		v := int64(0)
		if n.digits != "" {
			v, _ = strconv.ParseInt(n.digits, 10, 64) // at most 17 decimal digits
		}
		if n.negative {
			v = -v
		}
		v += int64(k)
		sum.negative = v < 0
		if sum.negative {
			v = -v
		}
		if v != 0 {
			sum.digits = strconv.FormatInt(v, 10)
		}
		return sum
	}
	// n is at least 10^17, which no literal's length reaches, so the sum
	// keeps n's sign.
	magnitude := uint64(k)
	if k < 0 {
		magnitude = uint64(-k)
	}
	sum.negative = n.negative
	if (k < 0) == n.negative {
		sum.digits = addDigits(n.digits, magnitude)
	} else {
		sum.digits = subtractDigits(n.digits, magnitude)
	}
	return sum
}

func (n integer) negate() integer {
	negated := n
	negated.negative = !n.negative && n.digits != ""
	return negated
}

func (n integer) positive() bool { return !n.negative && n.digits != "" }

// addDigits is the decimal digits of digits + k.
func addDigits(digits string, k uint64) string {
	b := []byte(digits)
	carry := k
	for i := len(b) - 1; i >= 0 && carry > 0; i-- {
		v := uint64(b[i]-'0') + carry
		b[i] = byte('0' + v%10)
		carry = v / 10
	}
	if carry > 0 {
		return strconv.FormatUint(carry, 10) + string(b)
	}
	return string(b)
}

// subtractDigits is the decimal digits of digits - k, where digits is
// greater than k.
func subtractDigits(digits string, k uint64) string {
	b := []byte(digits)
	borrow := uint64(0)
	for i := len(b) - 1; i >= 0 && (k > 0 || borrow > 0); i-- {
		take := k%10 + borrow
		k /= 10
		d := uint64(b[i] - '0')
		borrow = 0
		if d < take {
			d += 10
			borrow = 1
		}
		b[i] = byte('0' + d - take)
	}
	return strings.TrimLeft(string(b), "0")
}
