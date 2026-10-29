package core

import (
	"fmt"
	"reflect"
	"strconv"
)

// Bounds are a field's inclusive value bounds, from its minlen, maxlen, min
// and max tags. A length bound counts a string's characters: Unicode code
// points, as a CHAR(n) or VARCHAR(n) column and PostgreSQL's char_length
// count them. A numeric bound is an exact decimal. On an array field each
// bound applies to every element, as enum does.
type Bounds struct {
	// MinLen and MaxLen are the fewest and most characters; nil is
	// unbounded.
	MinLen, MaxLen *int
	// Min and Max are the least and greatest number as canonical decimal
	// text (DecimalText); empty is unbounded.
	Min, Max string
}

// IsZero reports bounds that bound nothing.
func (b Bounds) IsZero() bool {
	return b.MinLen == nil && b.MaxLen == nil && b.Min == "" && b.Max == ""
}

// Check refuses bounds the field's type cannot hold: length bounds belong to
// a string or an array of strings, numeric bounds to an int, float or
// decimal or an array of them.
func (b Bounds) Check(info TypeInfo) error {
	elem := info.Type
	if elem == FieldTypeArray {
		elem = info.Elem
	}
	if (b.MinLen != nil || b.MaxLen != nil) && elem != FieldTypeString {
		return fmt.Errorf("minlen and maxlen bound strings and arrays of strings, not %s", describeType(info))
	}
	if (b.Min != "" || b.Max != "") && elem != FieldTypeInt && elem != FieldTypeFloat && elem != FieldTypeDecimal {
		return fmt.Errorf("min and max bound numbers and arrays of numbers, not %s", describeType(info))
	}
	return nil
}

func describeType(info TypeInfo) string {
	if info.Type == FieldTypeArray && info.Elem != "" {
		return fmt.Sprintf("an array of %s", info.Elem)
	}
	return string(info.Type)
}

func readBounds(tag reflect.StructTag) (Bounds, error) {
	var b Bounds
	var err error
	if b.MinLen, err = lengthTag(tag, "minlen"); err != nil {
		return Bounds{}, err
	}
	if b.MaxLen, err = lengthTag(tag, "maxlen"); err != nil {
		return Bounds{}, err
	}
	if b.MinLen != nil && b.MaxLen != nil && *b.MinLen > *b.MaxLen {
		return Bounds{}, fmt.Errorf("minlen %d exceeds maxlen %d", *b.MinLen, *b.MaxLen)
	}
	if b.Min, err = numberTag(tag, "min"); err != nil {
		return Bounds{}, err
	}
	if b.Max, err = numberTag(tag, "max"); err != nil {
		return Bounds{}, err
	}
	if b.Min != "" && b.Max != "" && compareDecimalText(b.Min, b.Max) > 0 {
		return Bounds{}, fmt.Errorf("min %s exceeds max %s", b.Min, b.Max)
	}
	return b, nil
}

func lengthTag(tag reflect.StructTag, name string) (*int, error) {
	text, ok := tag.Lookup(name)
	if !ok {
		return nil, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 0 {
		return nil, fmt.Errorf("%s %q is not a non-negative integer", name, text)
	}
	return &n, nil
}

func numberTag(tag reflect.StructTag, name string) (string, error) {
	text, ok := tag.Lookup(name)
	if !ok {
		return "", nil
	}
	canonical, err := parseDecimal(text)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return canonical, nil
}
