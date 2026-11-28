//go:build unit

package filter_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// TestEmptyLogicalOperatorsRejected: an empty $not / $and / $or body is
// refused at parse time, since an empty object would leave the node no child
// and an empty array would compile to `WHERE ()`.
func TestEmptyLogicalOperatorsRejected(t *testing.T) {
	p := filter.NewFilterParser(testSchema(), nil)
	cases := []struct {
		name  string
		input string
	}{
		{"empty not object", `{"$not": {}}`},
		{"empty or array", `{"$or": []}`},
		{"or of empty objects", `{"$or": [{}]}`},
		{"and of empty objects", `{"$and": [{}]}`},
		{"empty and array", `{"$and": []}`},
		{"nested empty or under not", `{"$not": {"$or": []}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Parse(tc.input)
			if err == nil {
				t.Fatalf("Parse(%s) succeeded; want invalid-filter error", tc.input)
			}
			var pe *core.ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("Parse(%s) error = %T (%v); want *core.ParseError", tc.input, err, err)
			}
			if pe.Code != filter.ECodeInvalidJSON.String() {
				t.Errorf("Parse(%s) code = %s; want %s", tc.input, pe.Code, filter.ECodeInvalidJSON.String())
			}
		})
	}
}

// TestBuildNodeNilGuard covers the builder-side backstop: a nil AST node
// reaching the SQL builder returns an error instead of dereferencing nil.
func TestBuildNodeNilGuard(t *testing.T) {
	ast := &filter.FilterAST{Root: &filter.FilterNode{Op: filter.OpNot, Children: []*filter.FilterNode{nil}}}
	b := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{})
	if _, err := b.Build(ast); err == nil {
		t.Fatal("Build with nil child succeeded; want error")
	}
}

// TestEmptyLogicalNodeErrors: a zero-child logical node reaching the
// builder, which only a programmatic AST can hold since the parser refuses
// one, errors instead of emitting `WHERE ()`.
func TestEmptyLogicalNodeErrors(t *testing.T) {
	for _, op := range []filter.FilterOperator{filter.OpOr, filter.OpAnd} {
		ast := &filter.FilterAST{Root: &filter.FilterNode{Op: op}}
		b := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{})
		res, err := b.Build(ast)
		if err == nil {
			t.Fatalf("Build(empty %s) = %q; want error", op, res.Where)
		}
		if strings.Contains(err.Error(), "()") {
			t.Errorf("error mentions empty parens; want a descriptive message: %v", err)
		}
	}
}

// TestEmptyNestedPolicyGroupSkipped: empty nested policy groups add no nil
// children to the AST, and a policy that resolves to nothing leaves the
// request's AST unchanged.
func TestEmptyNestedPolicyGroupSkipped(t *testing.T) {
	schema := testSchema()

	t.Run("nested empty and group", func(t *testing.T) {
		pc := &filter.PolicyConstraints{
			Constraints: []*filter.PolicyConstraint{{Field: "status", Operator: filter.OpEq, Value: "active"}},
			And:         []*filter.PolicyConstraints{{}}, // empty nested group → nil node
		}
		ast, err := filter.ApplyPolicyConstraints(schema, nil, pc)
		if err != nil {
			t.Fatalf("ApplyPolicyConstraints: %v", err)
		}
		if _, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(ast); err != nil {
			t.Fatalf("Build after empty nested group: %v", err)
		}
	})

	t.Run("all groups empty leaves user AST unchanged", func(t *testing.T) {
		p := filter.NewFilterParser(schema, nil)
		userAST, err := p.Parse(`{"status": "active"}`)
		if err != nil {
			t.Fatal(err)
		}
		pc := &filter.PolicyConstraints{And: []*filter.PolicyConstraints{{}, {}}}
		ast, err := filter.ApplyPolicyConstraints(schema, userAST, pc)
		if err != nil {
			t.Fatalf("ApplyPolicyConstraints: %v", err)
		}
		res, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(ast)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if strings.Contains(res.Where, "()") || res.Where == "" {
			t.Errorf("degenerate WHERE %q from empty policy groups", res.Where)
		}
	})

	t.Run("or of empty groups omitted", func(t *testing.T) {
		pc := &filter.PolicyConstraints{
			Constraints: []*filter.PolicyConstraint{{Field: "status", Operator: filter.OpEq, Value: "active"}},
			Or:          []*filter.PolicyConstraints{{}, {}}, // every OR branch empty → OR node must be dropped
		}
		ast, err := filter.ApplyPolicyConstraints(schema, nil, pc)
		if err != nil {
			t.Fatalf("ApplyPolicyConstraints: %v", err)
		}
		res, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(ast)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if strings.Contains(res.Where, "()") {
			t.Errorf("empty parens in WHERE %q", res.Where)
		}
	})
}

// A filter the parser accepts builds the same SQL on every build, with one
// argument per parameter; one it refuses is a parse error with its code.
func TestParseThenBuild(t *testing.T) {
	type built struct {
		where string
		args  []any
	}
	tests := []struct {
		name     string
		input    string
		postgres built
		sqlite   built
		code     core.Code
	}{
		{
			name:     "or of an equality and a comparison",
			input:    `{"$or": [{"status": "active"}, {"count": {"$gt": 5}}]}`,
			postgres: built{`("items"."status" = $1 OR "items"."count" > $2)`, []any{"active", int64(5)}},
			sqlite:   built{`("items"."status" = ?1 OR "items"."count" > ?2)`, []any{"active", int64(5)}},
		},
		{
			name:     "like",
			input:    `{"name": {"$like": "%x%"}}`,
			postgres: built{`"items"."name" LIKE $1 ESCAPE '\'`, []any{"%x%"}},
			sqlite:   built{`"items"."name" GLOB ?1`, []any{"*x*"}},
		},
		{
			name:     "ilike of wildcards and an escaped backslash",
			input:    `{"name":{"$ilike":"a%_\\\\b"}}`,
			postgres: built{`"items"."name" ILIKE $1 ESCAPE '\'`, []any{`a%_\\b`}},
			sqlite:   built{`"items"."name" LIKE ?1 ESCAPE '\'`, []any{`a%_\\b`}},
		},
		{
			name:     "not under and",
			input:    `{"$and": [{"$not": {"status": "inactive"}}]}`,
			postgres: built{`NOT ("items"."status" = $1)`, []any{"inactive"}},
			sqlite:   built{`NOT ("items"."status" = ?1)`, []any{"inactive"}},
		},
		{
			name:     "an integer past 2^53 written with a fraction",
			input:    `{"count":9007199254740993.0}`,
			postgres: built{`"items"."count" = $1`, []any{int64(9007199254740993)}},
			sqlite:   built{`"items"."count" = ?1`, []any{int64(9007199254740993)}},
		},
		{
			name:     "a timestamp with an offset binds as UTC",
			input:    `{"created_at":{"$gte":"2026-01-01T01:00:00+01:00"}}`,
			postgres: built{`"items"."created_at" >= $1`, []any{"2026-01-01T00:00:00.000000000Z"}},
			sqlite:   built{`"items"."created_at" >= ?1`, []any{"2026-01-01T00:00:00.000000000Z"}},
		},
		{name: "an operator the field does not allow", input: `{"count": {"$in": [1, 2, 3]}}`, code: filter.ECodeInvalidOperator},
		{name: "a fraction too small for any integer", input: `{"count":1e-1000000000}`, code: filter.ECodeValueTypeMismatch},
	}
	schema := testSchema()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast, err := filter.NewFilterParser(schema, nil).Parse(tt.input)
			if tt.code != "" {
				var pe *core.ParseError
				require.ErrorAs(t, err, &pe)
				assert.Equal(t, tt.code.String(), pe.Code)
				return
			}
			require.NoError(t, err)
			dialects := []struct {
				dialect filter.Dialect
				want    built
			}{
				{filter.PostgresDialect{}, tt.postgres},
				{filter.SQLiteDialect{}, tt.sqlite},
			}
			for _, d := range dialects {
				builder := filter.NewSQLBuilder(schema, d.dialect)
				first, err := builder.Build(ast)
				require.NoError(t, err, "%T", d.dialect)
				assert.Equal(t, d.want, built{first.Where, first.Args}, "%T", d.dialect)
				assert.Equal(t, len(first.Args), first.ParamCount, "%T", d.dialect)

				second, err := builder.Build(ast)
				require.NoError(t, err, "%T", d.dialect)
				assert.Equal(t, first, second, "%T", d.dialect)
			}
		})
	}
}
