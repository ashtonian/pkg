package gowalker

import (
	"reflect"
	"strings"
)

// WithTaggedFields selects exported fields with an explicit name in tag, such
// as db:"id". It uses the text before the first comma, preserves whitespace,
// and skips absent tags, empty names and "-". Comma options are ignored.
// Each selected field is a leaf: its payload cannot contribute nested names.
// Embedded fields are not promoted. Duplicate names fail compilation.
//
// This replaces the field resolver. Use [WithFieldResolver] for other naming,
// fallback or inline rules. To read annotations without renaming fields, use
// [Field.Tags] with the default engine instead.
func WithTaggedFields(tag string) Option {
	return func(s *settings) error {
		if tag == "" {
			return ErrSchema
		}
		for _, c := range tag {
			if c <= ' ' || c == ':' || c == '"' || c == 0x7f {
				return ErrSchema
			}
		}
		s.resolver = func(sf reflect.StructField) (FieldRule, error) {
			name, _, _ := strings.Cut(sf.Tag.Get(tag), ",")
			return FieldRule{
				Name: name, Skip: !sf.IsExported() || name == "" || name == "-", Atomic: true,
			}, nil
		}
		return nil
	}
}
