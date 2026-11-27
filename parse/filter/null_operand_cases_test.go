//go:build unit || integration

package filter_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// nullColumn is a column the NULL operand cases compare: its field type,
// its SQL type on each database, the value its one row holds, and another
// value of its type.
type nullColumn struct {
	field            core.FieldType
	sqlite, postgres string
	held, other      any
}

// runNullOperands holds a database and filter.Evaluate to SQL's answer for
// a NULL operand or set member, whether nil, a nil pointer or a NULL
// Valuer: a comparison with it is Unknown, and `IN` a set holding it is
// True on an equal member and otherwise Unknown, so `NOT IN` is False or
// Unknown. Each case runs against a string and an integer column.
func runNullOperands(t *testing.T, db *sql.DB, d filter.Dialect) {
	columns := map[string]nullColumn{
		"string": {core.FieldTypeString, "TEXT", "text", "a", "b"},
		"int":    {core.FieldTypeInt, "INTEGER", "bigint", int64(7), int64(8)},
	}
	for name, c := range columns {
		t.Run(name, func(t *testing.T) {
			columnType := c.sqlite
			_, ok := d.(filter.PostgresDialect)
			if ok {
				columnType = c.postgres
			}
			table := "null_operands_" + name
			_, err := db.ExecContext(t.Context(), "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, v "+columnType+")")
			require.NoError(t, err)
			_, err = db.ExecContext(t.Context(), "INSERT INTO "+table+" (id, v) VALUES (1, "+d.Placeholder(1)+")", c.held)
			require.NoError(t, err)
			field := &filter.FieldSchema{ExternalName: "v", Table: table, Column: "v", DataType: c.field}
			schema := &filter.ResourceSchema{Name: table, Fields: map[string]*filter.FieldSchema{"v": field}}
			row := filter.ColumnRow(func(column string) (any, bool) { return c.held, column == table+".v" })

			for _, tc := range nullOperandCases(field, c) {
				n := tc.n
				res, err := filter.NewSQLBuilder(schema, d).Build(&filter.FilterAST{Root: n})
				require.NoError(t, err, "$%s %#v", n.Op, n.Value)
				var answer string
				err = db.QueryRowContext(t.Context(), "SELECT CASE WHEN ("+res.Where+") THEN 'true' WHEN NOT ("+res.Where+") THEN 'false' ELSE 'unknown' END FROM "+table, res.Args...).Scan(&answer)
				require.NoError(t, err, "$%s %#v: %s", n.Op, n.Value, res.Where)
				assert.Equal(t, tc.want.String(), answer, "the statement: $%s %#v", n.Op, n.Value)

				got, err := filter.Evaluate(n, row)
				require.NoError(t, err, "$%s %#v", n.Op, n.Value)
				assert.Equal(t, tc.want, got, "Evaluate: $%s %#v", n.Op, n.Value)
			}
		})
	}
}

type nullOperandCase struct {
	n    *filter.FilterNode
	want filter.Truth
}

// nullOperandCases are each NULL spelling of c's type as a comparison's
// operand and as a set member beside c's held and other values.
func nullOperandCases(field *filter.FieldSchema, c nullColumn) []nullOperandCase {
	nulls := []any{nil, (*string)(nil), sql.NullString{}}
	if c.field == core.FieldTypeInt {
		nulls = []any{nil, (*int64)(nil), sql.NullInt64{}}
	}
	var cases []nullOperandCase
	add := func(op filter.FilterOperator, value any, want filter.Truth) {
		cases = append(cases, nullOperandCase{&filter.FilterNode{Field: field, Op: op, Value: value}, want})
	}
	for _, null := range nulls {
		for _, op := range []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpLt, filter.OpLte, filter.OpGt, filter.OpGte} {
			add(op, null, filter.Unknown)
		}
		add(filter.OpIn, []any{null}, filter.Unknown)
		add(filter.OpIn, []any{c.other, null}, filter.Unknown)
		add(filter.OpIn, []any{null, c.held}, filter.True)
		add(filter.OpNin, []any{null}, filter.Unknown)
		add(filter.OpNin, []any{c.other, null}, filter.Unknown)
		add(filter.OpNin, []any{null, c.held}, filter.False)
	}
	return cases
}
