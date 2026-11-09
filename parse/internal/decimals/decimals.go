// Package decimals reads values as parse's exact decimals where a document
// member or a SQLite value holds one, and encodes a decimal as a key whose
// bytes order as the decimals do. The filter package reads document members
// through Member; parse's SQLite functions (parsesqlite) are Value and Key.
package decimals

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// The SQLite functions parse's dialect calls and parsesqlite registers:
// Value and Key of one argument.
const (
	SQLiteValueFunc = "parse_decimal"
	SQLiteKeyFunc   = "parse_decimal_key"
)

// PlainRegex is a decimal in plain notation, the text a decimal member's
// string holds (shopspring's JSON form): no exponent, so no cast of a member
// can overflow.
const PlainRegex = `^-?(0|[1-9][0-9]*)([.][0-9]+)?$`

// MaxMemberLength bounds a decimal member string's length; within it, its
// digits are within core.MaxDecimalDigits.
const MaxMemberLength = core.MaxDecimalDigits

var plainShape = regexp.MustCompile(PlainRegex)

// Member is the canonical text (core.DecimalText) of a decoded JSON document
// member read as a decimal: a JSON number (json.Number, or a Go number
// encoding/json decoded) within core.MaxDecimalDigits digits, or a string in
// plain notation of at most MaxMemberLength characters. ok is false for any
// other member.
func Member(v any) (string, bool) {
	switch m := v.(type) {
	case string:
		if len(m) > MaxMemberLength || !plainShape.MatchString(m) {
			return "", false
		}
	case json.Number:
	default:
		if !core.IsNumber(v) {
			return "", false
		}
	}
	text, err := core.DecimalText(v)
	if err != nil { // more digits than a decimal holds, or not finite
		return "", false
	}
	return text, true
}

// Value is the canonical text of a SQLite value read as a decimal, ok false
// when it holds none:
//
//   - INTEGER (int64): its digits;
//   - REAL (float64): its shortest decimal form, as core.DecimalText reads a
//     float64;
//   - TEXT (string or []byte): a JSON number literal, the text a decimal
//     column holds exactly; or a JSON string literal, a document member as
//     the `->` operator returns it, read as Member reads a string.
//
// A JSON literal of another kind (null, true, an object) is none.
func Value(v any) (string, bool) {
	var s string
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		text, err := core.DecimalText(x)
		return text, err == nil
	case string:
		s = x
	case []byte:
		s = string(x)
	default:
		return "", false
	}
	if strings.HasPrefix(s, `"`) {
		var member string
		if err := json.Unmarshal([]byte(s), &member); err != nil {
			return "", false
		}
		return Member(member)
	}
	text, err := core.DecimalText(json.Number(s))
	return text, err == nil
}

// Key signs of a decimal key's first byte: negatives, zero and positives
// order by it before any digit.
const (
	keyNegative byte = 0x01
	keyZero     byte = 0x02
	keyPositive byte = 0x03
)

// keyTerminator ends a negative key, above every complemented digit, so a
// negative whose digits extend another's (-0.51 after -0.5) orders lower.
const keyTerminator byte = 0xff

// exponentBias makes a magnitude's exponent, within ±core.MaxDecimalDigits,
// an unsigned 16-bit integer.
const exponentBias = 1 << 15

// Key encodes canonical decimal text (core.DecimalText) as bytes whose
// bytewise order is the decimals' numeric order; equal decimals have equal
// keys. A nonzero magnitude is 0.d₁d₂…dₙ × 10^e with d₁ and dₙ nonzero: its
// key is the sign byte, e biased to two big-endian bytes, then the digits.
// For a negative the exponent and digits are complemented and a terminator
// follows, so a greater magnitude orders lower. text must be canonical.
func Key(text string) []byte {
	if text == "0" {
		return []byte{keyZero}
	}
	neg := strings.HasPrefix(text, "-")
	if neg {
		text = text[1:]
	}
	intPart, frac, _ := strings.Cut(text, ".")
	var exp int
	var digits string
	if intPart == "0" {
		zeros := len(frac) - len(strings.TrimLeft(frac, "0"))
		exp, digits = -zeros, frac[zeros:]
	} else {
		exp, digits = len(intPart), strings.TrimRight(intPart+frac, "0")
	}
	biased := uint16(exp + exponentBias)
	key := make([]byte, 0, len(digits)+4)
	if !neg {
		key = append(key, keyPositive, byte(biased>>8), byte(biased))
		return append(key, digits...)
	}
	biased = ^biased
	key = append(key, keyNegative, byte(biased>>8), byte(biased))
	for i := range len(digits) {
		key = append(key, '0'+'9'-digits[i])
	}
	return append(key, keyTerminator)
}
