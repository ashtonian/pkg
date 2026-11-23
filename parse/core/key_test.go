//go:build unit

package core_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/ashtonian/pkg/parse/core"
)

// A key's text is what PostgreSQL's array_to_json(ARRAY[col::text, …])::text
// and SQLite's json_array(CAST(col AS TEXT), …) write for the same row, so
// includes and facets name a row alike in Go and in SQL.
func TestKeyText(t *testing.T) {
	id := uuid.MustParse("0190f3c2-0000-7000-8000-000000000011")
	for _, tt := range []struct {
		name string
		key  core.Key
		want string
	}{
		{name: "one string column", key: core.Key{"w1"}, want: "w1"},
		{name: "one integer column", key: core.Key{int64(42)}, want: "42"},
		{name: "one uuid column", key: core.Key{id}, want: "0190f3c2-0000-7000-8000-000000000011"},
		{name: "string and integer", key: core.Key{"w1", int64(-7)}, want: `["w1","-7"]`},
		{name: "uuid and string", key: core.Key{id, "a b"}, want: `["0190f3c2-0000-7000-8000-000000000011","a b"]`},
		{name: "quote and backslash", key: core.Key{`a"b`, `c\d`}, want: `["a\"b","c\\d"]`},
		{name: "named controls", key: core.Key{"\b\f\n\r\t", "x"}, want: `["\b\f\n\r\t","x"]`},
		{name: "other controls", key: core.Key{"\x00\x1f", "x"}, want: `["\u0000\u001f","x"]`},
		{name: "non-ASCII is not escaped", key: core.Key{"é/<", "x"}, want: `["é/<","x"]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, core.KeyText(tt.key))
		})
	}
}
