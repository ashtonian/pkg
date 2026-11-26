//go:build unit

package filter_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// A NULL operand or set member, whether nil, a nil pointer or a NULL
// Valuer, compares as SQL compares NULL: a comparison with it is Unknown,
// and `IN` a set holding it is True on an equal member and otherwise
// Unknown, so `NOT IN` is False or Unknown. filter.Evaluate answers each
// as SQLite does.
func TestEvaluate_NullLiteralIsUnknown(t *testing.T) {
	db := dbtest.SQLite(t)
	_, err := db.ExecContext(t.Context(), `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO t (id, v) VALUES (1, 'a')`)
	require.NoError(t, err)
	field := &filter.FieldSchema{ExternalName: "v", Table: "t", Column: "v", DataType: core.FieldTypeString}
	schema := &filter.ResourceSchema{Name: "t", Fields: map[string]*filter.FieldSchema{"v": field}}
	row := filter.ColumnRow(func(column string) (any, bool) { return "a", column == "t.v" })

	type node = filter.FilterNode
	var cases []struct {
		n    *node
		want filter.Truth
	}
	add := func(n *node, want filter.Truth) {
		cases = append(cases, struct {
			n    *node
			want filter.Truth
		}{n, want})
	}
	for _, null := range []any{nil, (*string)(nil), sql.NullString{}} {
		for _, op := range []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpLt, filter.OpLte, filter.OpGt, filter.OpGte} {
			add(&node{Field: field, Op: op, Value: null}, filter.Unknown)
		}
		add(&node{Field: field, Op: filter.OpIn, Value: []any{null}}, filter.Unknown)
		add(&node{Field: field, Op: filter.OpIn, Value: []any{"b", null}}, filter.Unknown)
		add(&node{Field: field, Op: filter.OpIn, Value: []any{null, "a"}}, filter.True)
		add(&node{Field: field, Op: filter.OpNin, Value: []any{null}}, filter.Unknown)
		add(&node{Field: field, Op: filter.OpNin, Value: []any{"b", null}}, filter.Unknown)
		add(&node{Field: field, Op: filter.OpNin, Value: []any{null, "a"}}, filter.False)
	}
	for _, tc := range cases {
		n := tc.n
		res, err := filter.NewSQLBuilder(schema, filter.SQLiteDialect{}).Build(&filter.FilterAST{Root: n})
		require.NoError(t, err, "$%s %#v", n.Op, n.Value)
		var answer string
		err = db.QueryRowContext(t.Context(), "SELECT CASE WHEN ("+res.Where+") THEN 'true' WHEN NOT ("+res.Where+") THEN 'false' ELSE 'unknown' END FROM t", res.Args...).Scan(&answer)
		require.NoError(t, err, "$%s %#v: %s", n.Op, n.Value, res.Where)

		assert.Equal(t, tc.want.String(), answer, "SQLite: $%s %#v", n.Op, n.Value)

		got, err := filter.Evaluate(n, row)
		require.NoError(t, err, "$%s %#v", n.Op, n.Value)
		assert.Equal(t, tc.want, got, "Evaluate: $%s %#v", n.Op, n.Value)
	}
}
