package patch

import (
	"fmt"
	"strings"

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
