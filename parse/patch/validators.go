package patch

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ashtonian/pkg/parse/core"
)

// eachValue runs check on a value, or on each element of an array value,
// as the filter side reads value tags on array fields. Null, and a null
// element, pass: whether the path or its elements may be null is the
// nullability check's to decide, and a merge-patch null clears the field.
func eachValue(check func(any) error) core.Validator {
	var walk core.Validator
	walk = func(value any) error {
		if value == nil {
			return nil
		}
		if items, ok := core.AnySlice(value); ok {
			for _, item := range items {
				if err := walk(item); err != nil {
					return err
				}
			}
			return nil
		}
		return check(value)
	}
	return walk
}

// enumValidator admits a value in the enum set.
func enumValidator(values []string) core.Validator {
	allowed := make(map[string]struct{}, len(values))
	names := make([]string, len(values))
	for i, v := range values {
		names[i] = strings.TrimSpace(v)
		allowed[names[i]] = struct{}{}
	}
	return eachValue(func(value any) error {
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected string for enum validation, got %T", value)
		}
		if _, found := allowed[s]; !found {
			return fmt.Errorf("value %q is not one of: %s", s, strings.Join(names, ", "))
		}
		return nil
	})
}

// boundsValidator admits a value within the field's bounds. It reads the
// values normalization produces: a string, an int64 or uint64, a float64,
// or a decimal's canonical text.
func boundsValidator(b core.Bounds, info core.TypeInfo) core.Validator {
	elem := info.Type
	if elem == core.FieldTypeArray {
		elem = info.Elem
	}
	switch elem {
	case core.FieldTypeString:
		return eachValue(func(value any) error {
			s, ok := value.(string)
			if !ok {
				return fmt.Errorf("expected string for length validation, got %T", value)
			}
			n := utf8.RuneCountInString(s)
			if b.MinLen != nil && n < *b.MinLen {
				return fmt.Errorf("%d characters is fewer than the minimum length %d", n, *b.MinLen)
			}
			if b.MaxLen != nil && n > *b.MaxLen {
				return fmt.Errorf("%d characters is more than the maximum length %d", n, *b.MaxLen)
			}
			return nil
		})
	case core.FieldTypeFloat:
		// A float field holds its value at its kind's precision, and its
		// bounds are read at that precision too, so max:"0.1" admits a
		// float32 field's 0.1.
		bits := 64
		if info.NumberKind == reflect.Float32 {
			bits = 32
		}
		lo, hi := floatBound(b.Min, bits, math.Inf(-1)), floatBound(b.Max, bits, math.Inf(1))
		return eachValue(func(value any) error {
			f, ok := value.(float64)
			if !ok {
				return fmt.Errorf("expected float64 for range validation, got %T", value)
			}
			if f < lo {
				return fmt.Errorf("%s is less than the minimum %s", strconv.FormatFloat(f, 'g', -1, bits), b.Min)
			}
			if f > hi {
				return fmt.Errorf("%s is greater than the maximum %s", strconv.FormatFloat(f, 'g', -1, bits), b.Max)
			}
			return nil
		})
	}
	// Integers and decimals compare exactly.
	return eachValue(func(value any) error {
		if b.Min != "" {
			c, ok := core.CompareDecimals(value, b.Min)
			if !ok {
				return fmt.Errorf("expected a number for range validation, got %T", value)
			}
			if c < 0 {
				return fmt.Errorf("%v is less than the minimum %s", value, b.Min)
			}
		}
		if b.Max != "" {
			c, ok := core.CompareDecimals(value, b.Max)
			if !ok {
				return fmt.Errorf("expected a number for range validation, got %T", value)
			}
			if c > 0 {
				return fmt.Errorf("%v is greater than the maximum %s", value, b.Max)
			}
		}
		return nil
	})
}

// floatBound is a numeric bound at a float kind's precision; unbounded when
// absent. A bound beyond the kind's range is the infinity it rounds to,
// which bounds every finite value as written.
func floatBound(text string, bits int, unbounded float64) float64 {
	if text == "" {
		return unbounded
	}
	f, _ := strconv.ParseFloat(text, bits) // canonical decimal text: only ErrRange, with ±Inf
	return f
}
