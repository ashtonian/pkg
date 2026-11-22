//go:build unit

package filtertest

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The resource declaration and the stored row differ only in Labels' Go
// element type: every field, tag and other type is the same.
func TestArrayResourceDeclaresArrayRecord(t *testing.T) {
	row, decl := reflect.TypeFor[ArrayRecord](), reflect.TypeFor[arrayResource]()
	if !assert.Equal(t, row.NumField(), decl.NumField()) {
		return
	}
	for i := range row.NumField() {
		r, d := row.Field(i), decl.Field(i)
		assert.Equal(t, r.Name, d.Name)
		assert.Equal(t, r.Tag, d.Tag, r.Name)
		if r.Name == "Labels" {
			assert.Equal(t, reflect.PointerTo(d.Type.Elem()), r.Type.Elem())
			continue
		}
		assert.Equal(t, r.Type, d.Type, r.Name)
	}
}
