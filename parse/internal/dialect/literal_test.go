//go:build unit

package dialect

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSQLiteJSONPath(t *testing.T) {
	for _, tt := range []struct {
		name   string
		labels []string
		want   string
	}{
		{"the document", nil, `$`},
		{"a name", []string{"a", "b-c"}, `$."a"."b-c"`},
		{"a double quote and a backslash", []string{`q"u\o`}, `$."q\"u\\o"`},
		{"what would start another step", []string{"a.b", "[0]", "$"}, `$."a.b"."[0]"."$"`},
		{"a single quote, which only a literal escapes", []string{"it's"}, `$."it's"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SQLiteJSONPath(tt.labels))
		})
	}
}

func TestQuoteLiteral(t *testing.T) {
	for _, tt := range []struct {
		name, s, postgres, sqlite string
	}{
		{"empty", "", `''`, `''`},
		{"plain", "a", `'a'`, `'a'`},
		{"single quotes", "it's ''", `'it''s '''''`, `'it''s '''''`},
		{"a double quote", `a"b`, `'a"b'`, `'a"b'`},
		{"a backslash and a single quote", `a\'b\\`, `E'a\\''b\\\\'`, `'a\''b\\'`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.postgres, Postgres{}.QuoteLiteral(tt.s))
			assert.Equal(t, tt.sqlite, SQLite{}.QuoteLiteral(tt.s))
		})
	}
}
