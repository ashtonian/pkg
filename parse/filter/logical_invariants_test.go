//go:build unit

package filter_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// TestEmptyLogicalOperatorsRejected covers /P-2: empty $not /
// $and / $or bodies must be rejected at parse time. Previously an empty object
// yielded a nil AST child (SIGSEGV in SQLBuilder.buildNode) and an empty array
// yielded a zero-child logical node (`WHERE ()` — invalid SQL).
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
// builder (only constructible programmatically after the P-1 parser fix)
// errors instead of emitting `WHERE ()`.
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

// TestEmptyNestedPolicyGroupSkipped: empty nested policy groups must not
// inject nil children into the AST (same panic class as P-1 via the policy
// path), and a policy that resolves to nothing leaves the user AST unchanged.
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

// FuzzParseThenBuild is the P-1 class guard: any input the parser accepts must
// build SQL without panicking, and repeated builds must be deterministic
// on both dialects. SQL generation may error for unsupported combinations.
func FuzzParseThenBuild(f *testing.F) {
	seeds := []string{
		`{"$not": {}}`,
		`{"$or": []}`,
		`{"$and": [{}]}`,
		`{"$not": {"$or": []}}`,
		`{"status": "active"}`,
		`{"$or": [{"status": "active"}, {"count": {"$gt": 5}}]}`,
		`{"name": {"$like": "%x%"}}`,
		`{"count": {"$in": [1, 2, 3]}}`,
		`{"$and": [{"$not": {"status": "inactive"}}]}`,
		`{"count":9007199254740993.0}`,
		`{"count":1e-1000000000}`,
		`{"name":{"$ilike":"a%_\\\\b"}}`,
		`{"created_at":{"$gte":"2026-01-01T01:00:00+01:00"}}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	schema := testSchema()
	f.Fuzz(func(t *testing.T, input string) {
		p := filter.NewFilterParser(schema, nil)
		ast, err := p.Parse(input)
		if err != nil {
			return // rejected input is fine; panics are not
		}
		for _, dialect := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
			builder := filter.NewSQLBuilder(schema, dialect)
			first, err := builder.Build(ast)
			if err != nil {
				continue // Unsupported combinations may fail without panicking.
			}
			second, err := builder.Build(ast)
			if err != nil || !reflect.DeepEqual(first, second) {
				t.Fatalf("non-deterministic SQL build for %T: first=%#v second=%#v error=%v", dialect, first, second, err)
			}
			if first.ParamCount != len(first.Args) {
				t.Fatalf("parameter count %d differs from argument count %d", first.ParamCount, len(first.Args))
			}
		}
	})
}
