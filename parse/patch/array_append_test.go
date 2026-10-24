//go:build unit

package patch

import (
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

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
			p := &ParsedPatch{
				Operations: []*PatchOperation{
					{
						Op:    PatchOpArrayAppend,
						Path:  "/photo_blob_keys",
						Value: "tenants/x/work_orders/y/photos/abc.jpg",
						// the raw-path identifier fallback is gone;
						// schemas always carry the SQL column explicitly.
						Schema: &PathSchema{SQLColumn: "photo_blob_keys"},
					},
				},
			}
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
	p := &ParsedPatch{
		Operations: []*PatchOperation{
			{Op: PatchOpArrayAppend, Path: "/x", Value: "v", Schema: &PathSchema{SQLColumn: "x"}},
		},
	}
	_, err := BuildPatchSQL(p, unknownDialect{})
	if err == nil {
		t.Fatal("expected error for unknown dialect, got nil")
	}
	if !strings.Contains(err.Error(), "array_append not supported") {
		t.Errorf("expected 'array_append not supported' in %q", err.Error())
	}
}

// TestValidPatchOps_IncludesArrayAppend locks in that the wire-level
// validator recognises the new op. Without this entry the parser would
// reject `{"op":"array_append",...}` at validation time, defeating the
// helper's purpose.
func TestValidPatchOps_IncludesArrayAppend(t *testing.T) {
	t.Parallel()
	if !ValidPatchOps[PatchOpArrayAppend] {
		t.Fatal("PatchOpArrayAppend missing from ValidPatchOps")
	}
}

// unknownDialect implements SQLDialect with a name that's neither
// "postgres" nor "sqlite", to verify the array_append branch fails loud.
type unknownDialect struct{}

func (unknownDialect) MarshalValue(v any, _ core.TypeInfo) (any, error) { return v, nil }

func (unknownDialect) Name() string                  { return "duckdb" }
func (unknownDialect) Placeholder(n int) string      { return "?" }
func (unknownDialect) QuoteIdent(name string) string { return `"` + name + `"` }
