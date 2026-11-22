//go:build unit

package patch

import (
	"slices"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// parseAppend parses an array_append of value to path, the column col.
func parseAppend(t *testing.T, path, col, value string) *ParsedPatch {
	t.Helper()
	schema, err := NewPatchSchema("items", map[string]*PathSchema{
		path: {AllowedOps: []PatchOp{PatchOpArrayAppend}, DataType: core.FieldTypeArray, SQLColumn: col},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewParser(schema, nil).Parse([]byte(`[{"op":"array_append","path":"` + path + `","value":` + value + `}]`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestBuildPatchSQL_ArrayAppend asserts the dialect-specific SET fragment
// for the PatchOpArrayAppend extension. The fragment must
// COALESCE NULL into the empty container so first-append against a
// freshly-inserted NULL column produces a one-element array.
func TestBuildPatchSQL_ArrayAppend(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		dialect     SQLDialect
		wantClause  string
		wantContain []string
	}{
		{
			name:    "postgres",
			dialect: filter.PostgresDialect{},
			wantContain: []string{
				"photo_blob_keys = array_append",
				"COALESCE(photo_blob_keys, '{}')",
				"$1",
			},
		},
		{
			name:    "sqlite",
			dialect: filter.SQLiteDialect{},
			wantContain: []string{
				"photo_blob_keys = json_insert",
				"COALESCE(photo_blob_keys, '[]')",
				"'$[#]'",
				"?1",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := parseAppend(t, "/photo_blob_keys", "photo_blob_keys", `"tenants/x/work_orders/y/photos/abc.jpg"`)
			got, err := BuildPatchSQL(p, tc.dialect)
			if err != nil {
				t.Fatalf("BuildPatchSQL: %v", err)
			}
			if got.Empty {
				t.Fatal("expected non-empty patch result")
			}
			if len(got.Args) != 1 {
				t.Fatalf("expected 1 arg, got %d", len(got.Args))
			}
			for _, want := range tc.wantContain {
				if !strings.Contains(got.SetClause, want) {
					t.Errorf("set clause %q missing %q", got.SetClause, want)
				}
			}
		})
	}
}

// TestBuildPatchSQL_ArrayAppend_UnknownDialect asserts the SET builder
// rejects array_append against a dialect that has no rendering, instead
// of silently dropping the op or generating broken SQL.
func TestBuildPatchSQL_ArrayAppend_UnknownDialect(t *testing.T) {
	t.Parallel()
	p := parseAppend(t, "/x", "x", `"v"`)
	_, err := BuildPatchSQL(p, unknownDialect{})
	if err == nil {
		t.Fatal("expected error for unknown dialect, got nil")
	}
	if !strings.Contains(err.Error(), "array_append not supported") {
		t.Errorf("expected 'array_append not supported' in %q", err.Error())
	}
}

// TestValidOps_IncludesArrayAppend locks in that the wire-level
// validator recognises the new op. Without this entry the parser would
// reject `{"op":"array_append",...}` at validation time, defeating the
// helper's purpose.
func TestValidOps_IncludesArrayAppend(t *testing.T) {
	t.Parallel()
	if !IsValidOp(PatchOpArrayAppend) || !slices.Contains(Ops(), PatchOpArrayAppend) {
		t.Fatal("PatchOpArrayAppend is not a recognized operation")
	}
}

// unknownDialect implements SQLDialect with a name that's neither
// "postgres" nor "sqlite", to verify the array_append branch fails loud.
type unknownDialect struct{}

func (unknownDialect) MarshalValue(v any, _ core.TypeInfo) (any, error) { return v, nil }

func (unknownDialect) Name() string                  { return "duckdb" }
func (unknownDialect) Placeholder(n int) string      { return "?" }
func (unknownDialect) QuoteIdent(name string) string { return `"` + name + `"` }
