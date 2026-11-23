package filter

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
)

// Lookup is the set a membership constraint tests a row's field against: the
// Project column of the rows of Table that Where selects. SQL
// backends compile it to `field IN (SELECT project FROM table WHERE …)`; a
// backend that holds rows in memory resolves it once per request with
// ResolveMemberships. Identifiers are quoted, so they match names as created:
// Table is a table or view, optionally schema-qualified; Project and every
// condition column are plain column names.
type Lookup struct {
	Table   string
	Project string
	// Where selects the lookup rows; nil selects every row.
	Where *LookupCond
	// Filter further selects the lookup rows by a filter over the lookup
	// table's own schema: a relation's target filter, its tenancy, soft
	// deletion and policy composed in. SQL renders it in the
	// subquery's WHERE; ResolveMemberships sends a lookup with a Filter
	// only to a RelationLookup, so a store that cannot evaluate it refuses
	// the request instead of ignoring it.
	Filter *ParsedFilter
}

// LookupOp is the comparison a LookupCond leaf makes.
type LookupOp string

const (
	// LookupOpEq selects rows whose column equals Value.
	LookupOpEq LookupOp = "eq"
	// LookupOpIn selects rows whose column is one of Values; an empty list
	// selects no row.
	LookupOpIn LookupOp = "in"
	// LookupOpNull selects rows whose column is NULL.
	LookupOpNull LookupOp = "null"
	// LookupOpNotNull selects rows whose column is not NULL.
	LookupOpNotNull LookupOp = "not_null"
	// LookupOpMember selects rows whose column is among the values Lookup
	// projects. ResolveMemberships answers the inner lookup first, so a
	// store sees LookupOpIn in its place.
	LookupOpMember LookupOp = "member"
)

// LookupCond is a condition over one lookup row: a leaf comparing Column, or
// a group of conditions all of which (And) or one of which (Or) hold. It has
// no negation, so a comparison with a NULL column selects nothing, exactly
// as in SQL. Build one with WhereEq, WhereIn, WhereNull, WhereAll and
// WhereAny.
type LookupCond struct {
	Column string
	Op     LookupOp
	Value  any     // LookupOpEq
	Values []any   // LookupOpIn
	Lookup *Lookup // LookupOpMember

	And []*LookupCond
	Or  []*LookupCond
}

// WhereEq selects rows whose column equals v.
func WhereEq(column string, v any) *LookupCond {
	return &LookupCond{Column: column, Op: LookupOpEq, Value: v}
}

// WhereIn selects rows whose column is one of vs; no values select no row.
func WhereIn(column string, vs ...any) *LookupCond {
	return &LookupCond{Column: column, Op: LookupOpIn, Values: vs}
}

// WhereNull selects rows whose column is NULL.
func WhereNull(column string) *LookupCond {
	return &LookupCond{Column: column, Op: LookupOpNull}
}

// WhereNotNull selects rows whose column is not NULL.
func WhereNotNull(column string) *LookupCond {
	return &LookupCond{Column: column, Op: LookupOpNotNull}
}

// WhereMember selects rows whose column is among the values l projects.
func WhereMember(column string, l *Lookup) *LookupCond {
	return &LookupCond{Column: column, Op: LookupOpMember, Lookup: l}
}

// WhereAll selects rows every condition selects.
func WhereAll(conds ...*LookupCond) *LookupCond { return &LookupCond{And: conds} }

// WhereAny selects rows at least one condition selects.
func WhereAny(conds ...*LookupCond) *LookupCond { return &LookupCond{Or: conds} }

var (
	lookupIdent      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	lookupQualifiedT = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
)

func (l *Lookup) validate() error {
	if l == nil {
		return invalidLookup("a membership constraint needs a lookup")
	}
	if !lookupQualifiedT.MatchString(l.Table) {
		return invalidLookup(fmt.Sprintf("lookup table %q is not an identifier", l.Table))
	}
	if !lookupIdent.MatchString(l.Project) {
		return invalidLookup(fmt.Sprintf("lookup column %q is not an identifier", l.Project))
	}
	if l.Where != nil {
		if err := l.Where.validate(); err != nil {
			return err
		}
	}
	if l.Filter != nil && l.Filter.ast != nil && hasRelation(l.Filter.ast.Root) {
		return invalidLookup(fmt.Sprintf("lookup on %s filters by a relation; resolve it first", l.Table))
	}
	return nil
}

func (c *LookupCond) validate() error {
	if c == nil {
		return invalidLookup("lookup condition is nil")
	}
	forms := 0
	if c.Column != "" || c.Op != "" {
		forms++
	}
	if len(c.And) > 0 {
		forms++
	}
	if len(c.Or) > 0 {
		forms++
	}
	if forms != 1 {
		return invalidLookup("a lookup condition is exactly one of a comparison, And or Or")
	}
	for _, g := range [][]*LookupCond{c.And, c.Or} {
		for _, child := range g {
			if err := child.validate(); err != nil {
				return err
			}
		}
	}
	if len(c.And) > 0 || len(c.Or) > 0 {
		return nil
	}
	if !lookupIdent.MatchString(c.Column) {
		return invalidLookup(fmt.Sprintf("lookup column %q is not an identifier", c.Column))
	}
	switch c.Op {
	case LookupOpEq:
		if isNullValue(c.Value) {
			return invalidLookup(fmt.Sprintf("lookup column %q compares with NULL; use WhereNull", c.Column))
		}
	case LookupOpIn:
		if slices.ContainsFunc(c.Values, isNullValue) {
			return invalidLookup(fmt.Sprintf("lookup column %q lists NULL; use WhereNull", c.Column))
		}
	case LookupOpNull, LookupOpNotNull:
	case LookupOpMember:
		if c.Lookup == nil {
			return invalidLookup(fmt.Sprintf("lookup column %q is a member of no lookup", c.Column))
		}
		return c.Lookup.validate()
	default:
		return invalidLookup(fmt.Sprintf("lookup column %q has unknown operator %q", c.Column, c.Op))
	}
	return nil
}

// invalidLookup is a membership lookup that cannot be built or answered.
// Only a policy declares one, so it is the server's fault.
func invalidLookup(msg string) error {
	return fmt.Errorf("%w: %s", ErrPolicyInvalid, msg)
}

// memberNode is the AST node of a membership constraint on field.
func memberNode(field *FieldSchema, l *Lookup) (*FilterNode, error) {
	if err := l.validate(); err != nil {
		return nil, err
	}
	if !core.IsKeyType(field.DataType) {
		return nil, invalidLookup(fmt.Sprintf("membership on %s field '%s'; a membership tests a string, UUID or integer field",
			field.DataType, field.ExternalName))
	}
	return &FilterNode{Field: field, Op: OpMember, Member: l}, nil
}
