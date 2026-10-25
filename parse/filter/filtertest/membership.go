package filtertest

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// LookupTable is a table membership cases select from. A backend creates it
// beside "records" with a column per LookupColumn and loads Rows; a nil
// value is NULL.
type LookupTable struct {
	Name    string
	Columns []LookupColumn
	Rows    [][]any
}

// LookupColumn is one lookup column and the type its values have: string,
// uuid (uuid.UUID values) or int (int64 values).
type LookupColumn struct {
	Name string
	Type core.FieldType
}

// LookupTables are the shapes authorization resolves through: a reflexive
// closure over record ids, a pair relation with users, groups, tenants and
// ended pairs, grants of UUID references, and a nullable integer column.
func LookupTables() []LookupTable {
	text := func(name string) LookupColumn { return LookupColumn{Name: name, Type: core.FieldTypeString} }
	return []LookupTable{
		{
			Name:    "record_tree",
			Columns: []LookupColumn{text("root_id"), text("descendant_id")},
			Rows: [][]any{
				{"a", "a"}, {"a", "b"}, {"a", "c"}, {"b", "b"}, {"c", "c"}, {"e", "e"}, {"e", "f"},
			},
		},
		{
			Name:    "record_members",
			Columns: []LookupColumn{text("record_id"), text("tenant_id"), text("user_id"), text("group_id"), text("ended_at")},
			Rows: [][]any{
				{"a", "t1", "u1", nil, nil},
				{"b", "t1", nil, "g1", nil},
				{"c", "t1", "u1", nil, "2020-01-01T00:00:00Z"}, // ended
				{"e", "t2", "u1", nil, nil},                    // another tenant's pair
				{"f", "t1", "u2", "g2", nil},
			},
		},
		{
			Name:    "ref_grants",
			Columns: []LookupColumn{text("grantee"), {Name: "ref_id", Type: core.FieldTypeUUID}},
			Rows:    [][]any{{"t1", *ref(1)}, {"t1", *ref(5)}, {"t2", *ref(2)}},
		},
		{
			Name:    "count_marks",
			Columns: []LookupColumn{text("mark"), {Name: "value", Type: core.FieldTypeInt}},
			Rows:    [][]any{{"big", int64(9007199254740993)}, {"small", int64(1)}, {"none", nil}},
		},
	}
}

// MembershipCase applies Constraints to an empty filter over Records; Want
// is the matching IDs in ID order.
type MembershipCase struct {
	Name        string
	Constraints *filter.PolicyConstraints
	Want        []string
}

func member(field string, l *filter.Lookup, negate bool) *filter.PolicyConstraints {
	return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: field, Member: l, Negate: negate}}}
}

// MembershipCases are the membership semantics every backend shares: SQL's
// `x IN (subquery)`, three-valued under NOT when x or a projected value is
// NULL.
func MembershipCases() []MembershipCase {
	closure := func(where *filter.LookupCond) *filter.Lookup {
		return &filter.Lookup{Table: "record_tree", Project: "descendant_id", Where: where}
	}
	members := &filter.Lookup{Table: "record_members", Project: "record_id", Where: filter.WhereAll(
		filter.WhereEq("tenant_id", "t1"),
		filter.WhereAny(filter.WhereEq("user_id", "u1"), filter.WhereIn("group_id", "g1", "g2")),
		filter.WhereNull("ended_at"),
	)}
	grants := &filter.Lookup{Table: "ref_grants", Project: "ref_id", Where: filter.WhereEq("grantee", "t1")}
	allMarks := &filter.Lookup{Table: "count_marks", Project: "value"}
	return []MembershipCase{
		{Name: "closure under one root", Constraints: member("id", closure(filter.WhereEq("root_id", "a")), false), Want: ids("abc")},
		{Name: "closure under several roots", Constraints: member("id", closure(filter.WhereIn("root_id", "b", "e")), false), Want: ids("bef")},
		{Name: "no roots selects nothing", Constraints: member("id", closure(filter.WhereIn("root_id")), false), Want: ids("")},
		{Name: "not in an empty set is every row", Constraints: member("id", closure(filter.WhereIn("root_id")), true), Want: ids("abcdefgh")},
		{Name: "every lookup row", Constraints: member("id", closure(nil), false), Want: ids("abcef")},
		{Name: "pair relation: user or group, live, in tenant", Constraints: member("id", members, false), Want: ids("abf")},
		{Name: "outside a pair relation", Constraints: member("id", members, true), Want: ids("cdegh")},
		{Name: "uuid field", Constraints: member("ref", grants, false), Want: ids("ae")},
		{Name: "outside uuid grants excludes NULL refs", Constraints: member("ref", grants, true), Want: ids("bcfg")},
		{Name: "integer exact beyond 2^53", Constraints: member("count", &filter.Lookup{Table: "count_marks", Project: "value", Where: filter.WhereEq("mark", "big")}, false), Want: ids("e")},
		{Name: "a NULL in the set selects only members", Constraints: member("count", allMarks, false), Want: ids("ae")},
		{Name: "a NULL in the set under NOT selects nothing", Constraints: member("count", allMarks, true), Want: ids("")},
		{Name: "OR with a field constraint", Constraints: &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{
			{Constraints: []*filter.PolicyConstraint{{Field: "name", Operator: filter.OpEq, Value: "beta"}}},
			member("id", closure(filter.WhereEq("root_id", "c")), false),
		}}, Want: ids("bc")},
		{Name: "AND of two memberships", Constraints: &filter.PolicyConstraints{And: []*filter.PolicyConstraints{
			member("id", closure(filter.WhereEq("root_id", "a")), false),
			member("id", members, false),
		}}, Want: ids("ab")},
	}
}

// Lookups resolves the cases' memberships over LookupTables with
// filter.MatchLookupRow: the in-memory answer to every lookup.
func Lookups() filter.MembershipLookup { return tableLookup(LookupTables()) }

type tableLookup []LookupTable

func (ts tableLookup) LookupValues(ctx context.Context, l *filter.Lookup) ([]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	i := slices.IndexFunc(ts, func(t LookupTable) bool { return t.Name == l.Table })
	if i < 0 {
		return nil, fmt.Errorf("filtertest: no lookup table %q", l.Table)
	}
	t := ts[i]
	project := slices.IndexFunc(t.Columns, func(c LookupColumn) bool { return c.Name == l.Project })
	if project < 0 {
		return nil, fmt.Errorf("filtertest: table %q has no column %q", l.Table, l.Project)
	}
	var out []any
	for _, row := range t.Rows {
		ok, err := filter.MatchLookupRow(l.Where, func(name string) (any, bool) {
			c := slices.IndexFunc(t.Columns, func(c LookupColumn) bool { return c.Name == name })
			if c < 0 {
				return nil, false
			}
			return row[c], true
		})
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, row[project])
		}
	}
	return out, nil
}

// RunMembership evaluates every membership case on backend. The filter it
// passes still holds membership nodes: a SQL backend compiles them, a
// memory backend resolves them (filter.ResolveMemberships) first.
func RunMembership(t *testing.T, backend Backend) {
	t.Helper()
	schema, err := Schema()
	if err != nil {
		t.Fatalf("filtertest: schema: %v", err)
	}
	for _, c := range MembershipCases() {
		t.Run(c.Name, func(t *testing.T) {
			ast, err := filter.ApplyPolicyConstraints(schema, nil, c.Constraints)
			if err != nil {
				t.Fatalf("apply constraints: %v", err)
			}
			got, err := backend(t, filter.NewParsedFilter(ast))
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			slices.Sort(got)
			if got == nil {
				got = []string{}
			}
			if !slices.Equal(got, c.Want) {
				t.Errorf("got %v, want %v", got, c.Want)
			}
		})
	}
}
