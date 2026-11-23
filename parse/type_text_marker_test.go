//go:build unit

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// tagMarker is a tag value an error must not repeat: reflect writes an
// unnamed struct type with its fields' tags, values included.
const tagMarker = "tag-value-marker"

// markedRow is an unnamed struct type, as a row type may be.
type markedRow = struct {
	ID string `json:"id" db:"id" note:"tag-value-marker"`
}

type markedDocRow struct {
	ID  string `json:"id" db:"id"`
	Doc struct {
		Kind string `json:"kind" note:"tag-value-marker"`
	} `json:"doc" db:"doc" parse:"patch"`
}

type markedFilterRow struct {
	ID   string `json:"id" db:"id"`
	Meta struct {
		X int `json:"x" note:"tag-value-marker"`
	} `json:"meta" db:"meta" parse:"filter"`
}

// An error naming a row, document, patch-target or filter-field type names
// it as gowalker.TypeText writes it: without its fields' tags.
func TestTypeText_ErrorsLeaveOutTagValues(t *testing.T) {
	docs := func(t *testing.T) *parse.Resource[markedDocRow] {
		t.Helper()
		r, err := parse.NewResource[markedDocRow]("docs", parse.ColumnType("doc", core.SQLJSONB), parse.CustomizeSchemas(func(s parse.Schemas) error {
			return s.Patch.AddPath("/doc/*", &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeAny, SQLColumn: "doc"})
		}))
		require.NoError(t, err)
		return r
	}
	for _, tt := range []struct {
		name string
		err  func(t *testing.T) error
		want string
	}{
		{
			name: "row type",
			err: func(t *testing.T) error {
				_, err := parse.NewResource[markedRow]("rows", parse.TenantColumn("tenant_id"))
				return err
			},
			want: `tenant column "tenant_id" is not a column of struct { ID string }`,
		},
		{
			name: "stored document type",
			err: func(t *testing.T) error {
				scanner, err := docs(t).RowScanner(filter.PostgresDialect{})
				require.NoError(t, err)
				_, err = scanner.Scan(scanStrings(`{"Kind":"x"}`))
				return err
			},
			want: `JSON member "/Kind" is noncanonical for struct { Kind string }; use "kind"`,
		},
		{
			name: "patch target type",
			err: func(t *testing.T) error {
				_, err := docs(t).ParsePatch([]byte(`[{"op":"replace","path":"/doc/nope","value":1}]`))
				return err
			},
			want: `struct { Kind string } has no member "nope"`,
		},
		{
			name: "filter field type",
			err: func(t *testing.T) error {
				_, err := parse.NewResource[markedFilterRow]("filters", parse.ColumnType("meta", core.SQLDate))
				return err
			},
			want: `column "meta" declares a civil date; use core.Date, not struct { X int }`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.err(t)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), tagMarker)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}
