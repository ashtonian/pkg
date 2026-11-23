//go:build unit

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

// A row scan fills a fresh row, where an unexported embedded pointer is nil
// and reflection cannot allocate it. A read column whose field is promoted
// through one, or a stored document holding a member promoted so, is refused
// when the resource is built.

type hiddenColumns struct {
	Extra string `json:"extra" db:"extra"`
}

type hiddenColumnRow struct {
	ID string `json:"id" db:"id"`
	*hiddenColumns
}

// HiddenColumnHolder is exported, and so settable, but holds the unexported
// pointer the column's field is promoted through.
type HiddenColumnHolder struct {
	*hiddenColumns
}

type deepHiddenColumnRow struct {
	ID string `json:"id" db:"id"`
	*HiddenColumnHolder
}

type hiddenMembers struct {
	X string `json:"x"`
}

// hiddenDoc is a document encoding/json writes x into while the pointer is
// set, and cannot decode x out of into a fresh value.
type hiddenDoc struct {
	*hiddenMembers
	Y string `json:"y"`
}

type hiddenDocRow struct {
	ID  string    `json:"id"  db:"id"`
	Doc hiddenDoc `json:"doc" db:"doc"`
}

type hiddenDocsRow struct {
	ID   string                 `json:"id"   db:"id"`
	Docs map[string][]hiddenDoc `json:"docs" db:"docs"`
}

type patchedHiddenDocRow struct {
	ID  string    `json:"id"  db:"id"`
	Doc hiddenDoc `json:"doc" db:"doc" parse:"patch"`
}

// scannedHiddenDoc decodes the driver's value itself, so its column is a
// stored document only when ScanJSON names it.
type scannedHiddenDoc struct {
	*hiddenMembers
}

func (d *scannedHiddenDoc) Scan(any) error { return nil }

type scannedHiddenDocRow struct {
	ID  string           `json:"id"  db:"id"`
	Doc scannedHiddenDoc `json:"doc" db:"doc"`
}

func TestNewResource_RefusesAColumnNoRowScanCanFill(t *testing.T) {
	const fix = "export the type or embed it by value"
	for _, tt := range []struct {
		name  string
		build func() error
		// want are the column, the field and the embedded type the error
		// names, each as it reads there.
		want []string
	}{
		{
			name: "value row",
			build: func() error {
				_, err := parse.NewResource[hiddenColumnRow]("rows")
				return err
			},
			want: []string{"column extra: field Extra:", "unexported embedded *parse_test.hiddenColumns"},
		},
		{
			name: "pointer row",
			build: func() error {
				_, err := parse.NewResource[*hiddenColumnRow]("rows")
				return err
			},
			want: []string{"column extra: field Extra:", "unexported embedded *parse_test.hiddenColumns"},
		},
		{
			name: "behind an exported embedded pointer",
			build: func() error {
				_, err := parse.NewResource[deepHiddenColumnRow]("rows")
				return err
			},
			want: []string{"column extra: field Extra:", "unexported embedded *parse_test.hiddenColumns"},
		},
		{
			name: "computed column",
			build: func() error {
				_, err := parse.NewResource[hiddenColumnRow]("rows", parse.ReadExpr("extra", parse.SQL("'computed'")))
				return err
			},
			want: []string{"column extra: field Extra:", "unexported embedded *parse_test.hiddenColumns"},
		},
		{
			name: "stored document",
			build: func() error {
				_, err := parse.NewResource[hiddenDocRow]("rows")
				return err
			},
			want: []string{"column doc: parse_test.hiddenDoc: field X:", "unexported embedded *parse_test.hiddenMembers"},
		},
		{
			name: "declared JSON document",
			build: func() error {
				_, err := parse.NewResource[hiddenDocRow]("rows", parse.ColumnType("doc", core.SQLJSONB))
				return err
			},
			want: []string{"column doc: parse_test.hiddenDoc: field X:", "unexported embedded *parse_test.hiddenMembers"},
		},
		{
			name: "document held in a map of slices",
			build: func() error {
				_, err := parse.NewResource[hiddenDocsRow]("rows")
				return err
			},
			want: []string{"column docs: parse_test.hiddenDoc: field X:", "unexported embedded *parse_test.hiddenMembers"},
		},
		{
			name: "patched document, resource",
			build: func() error {
				_, err := parse.NewResource[patchedHiddenDocRow]("rows")
				return err
			},
			want: []string{"field Doc: parse_test.hiddenDoc: field X:", "unexported embedded *parse_test.hiddenMembers"},
		},
		{
			name: "patched document, standalone patch schema",
			build: func() error {
				_, err := patch.GeneratePatchSchema[patchedHiddenDocRow](core.NewSchemaGenerator(core.WithSQLTableName("rows")))
				return err
			},
			want: []string{"field Doc: parse_test.hiddenDoc: field X:", "unexported embedded *parse_test.hiddenMembers"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.build()
			require.Error(t, err)
			for _, want := range tt.want {
				require.ErrorContains(t, err, want)
			}
			require.ErrorContains(t, err, fix)
		})
	}
}

func TestRowScanner_RefusesScanJSONOnADocumentItCannotDecode(t *testing.T) {
	r, err := parse.NewResource[scannedHiddenDocRow]("rows")
	require.NoError(t, err, "the driver hands the column to its sql.Scanner")
	for _, d := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		_, err = r.RowScanner(d)
		require.NoError(t, err)
	}
	_, err = r.RowScanner(filter.PostgresDialect{}, parse.ScanJSON("doc"))
	require.ErrorContains(t, err, `ScanJSON names "doc": parse_test.scannedHiddenDoc: field X:`)
	require.ErrorContains(t, err, "unexported embedded *parse_test.hiddenMembers")
}

// byValueDoc embeds an unexported struct by value: encoding/json decodes the
// members it promotes.
type byValueDoc struct {
	hiddenMembers
	Y string `json:"y"`
}

// byValueRow embeds an unexported struct by value, whose promoted fields
// reflection sets, as encoding/json does.
type byValueRow struct {
	ID string `json:"id" db:"id"`
	hiddenColumns
	Doc byValueDoc `json:"doc" db:"doc"`
}

func TestRowScanner_ScansFieldsAnUnexportedStructEmbeddedByValuePromotes(t *testing.T) {
	db := dbtest.SQLite(t)
	_, err := db.ExecContext(t.Context(), `CREATE TABLE "by_value" ("id" TEXT PRIMARY KEY, "extra" TEXT NOT NULL, "doc" TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO "by_value" VALUES ('a', 'promoted', '{"x":"member","y":"own"}')`)
	require.NoError(t, err)
	r, err := parse.NewResource[byValueRow]("by_value")
	require.NoError(t, err)
	columns, err := r.Columns(filter.SQLiteDialect{})
	require.NoError(t, err)
	rows := resourceRows(t, db, r, filter.SQLiteDialect{}, parse.Statement{SQL: `SELECT ` + joinColumns(columns) + ` FROM "by_value"`})
	want := byValueRow{ID: "a", hiddenColumns: hiddenColumns{Extra: "promoted"}, Doc: byValueDoc{hiddenMembers: hiddenMembers{X: "member"}, Y: "own"}}
	assert.Equal(t, []byValueRow{want}, rows)
}

func TestRowScanner_LeavesAnUnreadColumnBehindAnUnexportedEmbeddedPointer(t *testing.T) {
	db := dbtest.SQLite(t)
	_, err := db.ExecContext(t.Context(), `CREATE TABLE "rows" ("id" TEXT PRIMARY KEY, "extra" TEXT)`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO "rows" VALUES ('a', 'unread')`)
	require.NoError(t, err)
	r, err := parse.NewResource[hiddenColumnRow]("rows", parse.ReadColumns("id"))
	require.NoError(t, err, "a column the resource does not read is never scanned")
	columns, err := r.Columns(filter.SQLiteDialect{})
	require.NoError(t, err)
	rows := resourceRows(t, db, r, filter.SQLiteDialect{}, parse.Statement{SQL: `SELECT ` + joinColumns(columns) + ` FROM "rows"`})
	assert.Equal(t, []hiddenColumnRow{{ID: "a"}}, rows)
}
