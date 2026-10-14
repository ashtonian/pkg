package config

import (
	"reflect"
	"regexp"
)

// Reflection prints an unnamed struct's tags, including sensitive defaults.
func typeText(t reflect.Type) string {
	if t == nil {
		return "<nil>"
	}
	return typeTags.ReplaceAllString(t.String(), "")
}

var typeTags = regexp.MustCompile(` "(?:[^"\\]|\\.)*"`)
