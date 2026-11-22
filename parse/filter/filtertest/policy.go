package filtertest

import (
	"errors"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// PolicyCase is a policy over Records, ANDed with a request filter, and the
// IDs the list admits, in ID order.
type PolicyCase struct {
	Name string
	// Filter is the request filter the policy is ANDed with; "" is none.
	Filter string
	Policy *filter.PolicyConstraints
	// Search is a free-text search over name (SearchSchema) ANDed with the
	// rest; "" searches nothing. Only SQL searches, so a case with a search
	// is SQLOnly.
	Search string
	Want   []string
	// Err is the code the policy is refused with; Want is unused.
	Err core.Code
	// SQLOnly marks a case a memory backend cannot evaluate: raw SQL or a
	// search. Memory backends skip it.
	SQLOnly bool
}

// PolicyQuery is one policy case as a backend receives it.
type PolicyQuery struct {
	// Filter is the parsed request filter; nil filters nothing.
	Filter *filter.FilterAST
	Policy *filter.PolicyConstraints
	Search string
}

// PolicyBackend evaluates a policy query over Records (memberships over
// LookupTables) and returns the matching IDs, or the error compiling the
// policy returned.
type PolicyBackend func(t *testing.T, q PolicyQuery) ([]string, error)

// SearchSchema is the free-text search a policy case's Search runs: name.
func SearchSchema() *filter.SearchSchema {
	return &filter.SearchSchema{Fields: []filter.SearchField{{Name: "name", Table: "records", Column: "name"}}, Param: filter.ParamSearch}
}

func constraint(field string, op filter.FilterOperator, value any) *filter.PolicyConstraints {
	return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: field, Operator: op, Value: value}}}
}

// rawCountOrBeta is a raw fragment whose top level is an OR: count above
// two (c, e, h) or the name beta (b).
func rawCountOrBeta() *filter.PolicyConstraints {
	return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{
		RawSQL: "records.count > $1 OR records.name = $2", RawArgs: []any{int64(2), "beta"},
	}}}
}

// PolicyCases are the policy semantics every backend shares: raw fragments
// as one operand, a lookup shared by two fields, negated groups, SQL's
// unknown and the NULL tests.
func PolicyCases() []PolicyCase {
	// labels selects beta and c: b's name and c's id.
	labels := func() *filter.Lookup {
		return &filter.Lookup{Table: "record_labels", Project: "label", Where: filter.WhereEq("kind", "x")}
	}
	shared := labels()
	sharedBy := func(negate bool) *filter.PolicyConstraints {
		return &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{
			{Constraints: []*filter.PolicyConstraint{{Field: "id", Member: shared, Negate: negate}}},
			{Constraints: []*filter.PolicyConstraint{{Field: "name", Member: shared, Negate: negate}}},
		}}
	}
	beta := constraint("name", filter.OpEq, "beta")
	return []PolicyCase{
		{Name: "raw OR fragment ANDed with a request filter", Filter: `{"active":true}`, Policy: rawCountOrBeta(), Want: ids("ch"), SQLOnly: true},
		{Name: "raw OR fragment under OR", Policy: &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{
			rawCountOrBeta(), constraint("name", filter.OpEq, "Alpha"),
		}}, Want: ids("abceh"), SQLOnly: true},
		{Name: "raw OR fragment under NOT", Policy: &filter.PolicyConstraints{Not: true, And: []*filter.PolicyConstraints{rawCountOrBeta()}}, Want: ids("afg"), SQLOnly: true},
		{Name: "raw OR fragment ANDed with a search", Policy: rawCountOrBeta(), Search: "alpha", Want: ids("e"), SQLOnly: true},
		{Name: "raw OR fragment beside a field constraint", Policy: &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{
			{Field: "active", Operator: filter.OpEq, Value: false},
			{RawSQL: "records.count > $1 OR records.name = $2", RawArgs: []any{int64(2), "beta"}},
		}}, Want: ids("be"), SQLOnly: true},

		{Name: "one lookup shared by two fields", Policy: sharedBy(false), Want: ids("bc")},
		{Name: "one lookup shared by two fields, denied", Policy: &filter.PolicyConstraints{Not: true, And: []*filter.PolicyConstraints{sharedBy(false)}}, Want: ids("aefgh")},
		{Name: "one lookup shared by two negated constraints", Policy: sharedBy(true), Want: ids("abcdefgh")},
		{Name: "one lookup shared by two fields beside a request filter", Filter: `{"active":false}`, Policy: sharedBy(false), Want: ids("b")},

		{Name: "negated group", Policy: &filter.PolicyConstraints{Not: true, Or: []*filter.PolicyConstraints{beta, constraint("count", filter.OpGt, int64(2))}}, Want: ids("afg")},
		{Name: "negated empty group", Policy: &filter.PolicyConstraints{Not: true}, Err: core.CodeInvalidOperator},
		{Name: "negated empty group under AND", Policy: &filter.PolicyConstraints{And: []*filter.PolicyConstraints{beta, {Not: true}}}, Err: core.CodeInvalidOperator},
		{Name: "negated group of empty groups", Policy: &filter.PolicyConstraints{Not: true, Or: []*filter.PolicyConstraints{{}}}, Err: core.CodeInvalidOperator},
		{Name: "empty group admits every row", Policy: &filter.PolicyConstraints{And: []*filter.PolicyConstraints{{}}}, Want: ids("abcdefgh")},

		{Name: "unknown admits no row", Policy: &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{filter.UnknownConstraint()}}, Want: ids("")},
		{Name: "negated unknown admits no row", Policy: &filter.PolicyConstraints{Not: true, Constraints: []*filter.PolicyConstraint{filter.UnknownConstraint()}}, Want: ids("")},
		{Name: "unknown under OR leaves the other branch", Policy: &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{
			{Constraints: []*filter.PolicyConstraint{filter.UnknownConstraint()}}, beta,
		}}, Want: ids("b")},
		{Name: "unknown in a negated group excludes what the group matches", Policy: &filter.PolicyConstraints{Not: true, Or: []*filter.PolicyConstraints{
			{Constraints: []*filter.PolicyConstraint{filter.UnknownConstraint()}}, beta,
		}}, Want: ids("")},
		{Name: "an invalid value compiled to unknown", Policy: &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{
			{Field: "count", Operator: filter.OpEq, Value: "many", UnknownIfInvalid: true},
		}}, Want: ids("")},
		{Name: "an invalid value refused", Policy: constraint("count", filter.OpEq, "many"), Err: core.CodeValueTypeMismatch},

		{Name: "$null without a value", Policy: constraint("name", filter.OpNull, nil), Want: ids("d")},
		{Name: "$null true", Policy: constraint("name", filter.OpNull, true), Want: ids("d")},
		{Name: "$null false", Policy: constraint("name", filter.OpNull, false), Want: ids("abcefgh")},
		{Name: "$not_null without a value", Policy: constraint("name", filter.OpNotNull, nil), Want: ids("abcefgh")},
		{Name: "$not_null true", Policy: constraint("name", filter.OpNotNull, true), Want: ids("abcefgh")},
		{Name: "$not_null false", Policy: constraint("name", filter.OpNotNull, false), Want: ids("d")},
		{Name: "$exists true", Policy: constraint("name", filter.OpExists, true), Want: ids("abcefgh")},
		{Name: "$exists false", Policy: constraint("name", filter.OpExists, false), Want: ids("d")},
		{Name: "$null with a value that is no boolean", Policy: constraint("name", filter.OpNull, "yes"), Err: core.CodeValueTypeMismatch},
		{Name: "$exists without a value", Policy: constraint("name", filter.OpExists, nil), Err: core.CodeValueTypeMismatch},
	}
}

// RunPolicy evaluates every policy case on backend. A memory backend (sql
// false) skips the cases only SQL evaluates.
func RunPolicy(t *testing.T, sql bool, backend PolicyBackend) {
	t.Helper()
	parser := Parser(t)
	for _, c := range PolicyCases() {
		t.Run(c.Name, func(t *testing.T) {
			if c.SQLOnly && !sql {
				t.Skip("only SQL evaluates this policy")
			}
			q := PolicyQuery{Policy: c.Policy, Search: c.Search}
			if c.Filter != "" {
				ast, err := parser.Parse(c.Filter)
				if err != nil {
					t.Fatalf("parse %s: %v", c.Filter, err)
				}
				q.Filter = ast
			}
			got, err := backend(t, q)
			if c.Err != "" {
				var pe *core.ParseError
				if !errors.As(err, &pe) || pe.Code != c.Err.String() {
					t.Fatalf("err = %v (ids %v), want %s", err, got, c.Err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := sameIDs(got, c.Want); err != nil {
				t.Fatal(err)
			}
		})
	}
}
