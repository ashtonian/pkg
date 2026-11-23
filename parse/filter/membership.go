package filter

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// Lookup is the set a membership constraint tests a row's field against: the
// Project column (or ProjectKey columns) of the rows of Table that Where
// selects. SQL backends compile it to
// `field IN (SELECT project FROM table WHERE …)`; a
// backend that holds rows in memory resolves it once per request with
// ResolveMemberships. Identifiers are quoted, so they match names as created:
// Table is a table or view, optionally schema-qualified; Project and every
// condition column are plain column names.
type Lookup struct {
	Table   string
	Project string
	// ProjectKey, in place of Project, projects a key of several columns:
	// each value the lookup answers is a core.Key of a value per column, in
	// ProjectKey's order, a NULL column's value nil. A membership tests
	// one field, so only WhereKeyMember tests such a lookup in SQL;
	// ResolveMemberships asks one of a store to resolve a relation that
	// correlates keys of several columns.
	ProjectKey []string
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
	// LookupOpKeyIn selects rows whose Columns, together, equal one of Keys,
	// each a value per column in Columns' order; no keys select no row.
	LookupOpKeyIn LookupOp = "key_in"
	// LookupOpKeyMember selects rows whose Columns, together, are among the
	// keys Lookup projects (Lookup.ProjectKey). ResolveMemberships answers
	// the inner lookup first, so a store sees LookupOpKeyIn in its place.
	LookupOpKeyMember LookupOp = "key_member"
)

// LookupCond is a condition over one lookup row: a leaf comparing Column,
// or the key of several Columns, or a group of conditions all of which
// (And) or one of which (Or) hold. It has no negation, so a comparison with
// a NULL column selects nothing, exactly as in SQL. Build one with WhereEq,
// WhereIn, WhereNull, WhereNotNull, WhereMember, WhereKeyIn,
// WhereKeyMember, WhereAll and WhereAny.
type LookupCond struct {
	Column  string
	Columns []string // LookupOpKeyIn, LookupOpKeyMember
	Op      LookupOp
	Value   any        // LookupOpEq
	Values  []any      // LookupOpIn
	Keys    []core.Key // LookupOpKeyIn
	Lookup  *Lookup    // LookupOpMember, LookupOpKeyMember

	// Types, when set, types a comparison's column, or each of Columns in
	// order, as scalars: the column and its values compare as a filter
	// compares the type, a decimal by value and a timestamp at its
	// instant, whatever Go types hold them, and a member's column and the
	// inner lookup's projection likewise. Unset, a comparison takes its
	// type from its values' Go types and a member compares as stored. A
	// relation resolved outside SQL types every condition by its columns'
	// types (core.RelationSchema), so pairs stored under another spelling
	// relate as in SQL.
	Types []core.TypeInfo

	And []*LookupCond
	Or  []*LookupCond
}

// typed is c with its compared columns typed by types (LookupCond.Types).
func (c *LookupCond) typed(types []core.TypeInfo) *LookupCond {
	c.Types = types
	return c
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

// WhereKeyIn selects rows whose columns, together, equal one of keys, each a
// value per column in columns' order; no keys select no row. A key of one
// column is WhereIn's.
func WhereKeyIn(columns []string, keys ...core.Key) *LookupCond {
	return &LookupCond{Columns: columns, Op: LookupOpKeyIn, Keys: keys}
}

// WhereKeyMember selects rows whose columns, together, are among the keys l
// projects (Lookup.ProjectKey, of as many columns). A key of one column is
// WhereMember's.
func WhereKeyMember(columns []string, l *Lookup) *LookupCond {
	return &LookupCond{Columns: columns, Op: LookupOpKeyMember, Lookup: l}
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
	if l.ProjectKey == nil {
		if !lookupIdent.MatchString(l.Project) {
			return invalidLookup(fmt.Sprintf("lookup column %q is not an identifier", l.Project))
		}
	} else {
		if l.Project != "" {
			return invalidLookup(fmt.Sprintf("lookup on %s projects both Project and ProjectKey", l.Table))
		}
		if err := checkLookupKey(l.ProjectKey); err != nil {
			return err
		}
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

// validateProjecting is validate, with l projecting n columns: one for a
// membership, a key's for a key member.
func (l *Lookup) validateProjecting(n int) error {
	if err := l.validate(); err != nil {
		return err
	}
	projected := l.projected()
	if len(projected) == n {
		return nil
	}
	if n == 1 {
		return invalidLookup(fmt.Sprintf("lookup on %s projects the key (%s); a membership tests one field", l.Table, strings.Join(projected, ", ")))
	}
	return invalidLookup(fmt.Sprintf("lookup on %s projects %d columns (%s) for a key of %d", l.Table, len(projected), strings.Join(projected, ", "), n))
}

// projected are the columns l projects: Project alone, or ProjectKey.
func (l *Lookup) projected() []string {
	if l.ProjectKey != nil {
		return l.ProjectKey
	}
	return []string{l.Project}
}

// checkLookupKey refuses the columns of a key of several that are fewer
// than two, name a column twice or name one that is no identifier.
func checkLookupKey(columns []string) error {
	if len(columns) < 2 {
		return invalidLookup(fmt.Sprintf("lookup key (%s) has %d columns; a key of one column is the column itself", strings.Join(columns, ", "), len(columns)))
	}
	for i, c := range columns {
		if !lookupIdent.MatchString(c) {
			return invalidLookup(fmt.Sprintf("lookup column %q is not an identifier", c))
		}
		if slices.Contains(columns[:i], c) {
			return invalidLookup(fmt.Sprintf("lookup key (%s) names %q twice", strings.Join(columns, ", "), c))
		}
	}
	return nil
}

// validateTypes refuses Types on a NULL test, which compares nothing, and
// Types that do not type each compared column as a scalar, or whose type a
// compared value cannot take.
func (c *LookupCond) validateTypes() error {
	if c.Types == nil {
		return nil
	}
	compared := []string{c.Column}
	switch c.Op {
	case LookupOpKeyIn, LookupOpKeyMember:
		compared = c.Columns
	case LookupOpNull, LookupOpNotNull:
		return invalidLookup(fmt.Sprintf("lookup column %q: a NULL test has no type to compare by", c.Column))
	}
	if len(c.Types) != len(compared) {
		return invalidLookup(fmt.Sprintf("lookup condition on (%s) types %d of its %d columns", strings.Join(compared, ", "), len(c.Types), len(compared)))
	}
	for i, t := range c.Types {
		if !core.IsKeyType(t.Type) {
			return invalidLookup(fmt.Sprintf("lookup column %q is typed %s; a comparison compares scalars", compared[i], t.Type))
		}
	}
	var values [][]any
	switch c.Op {
	case LookupOpEq:
		values = [][]any{{c.Value}}
	case LookupOpIn:
		for _, v := range c.Values {
			values = append(values, []any{v})
		}
	case LookupOpKeyIn:
		for _, k := range c.Keys {
			values = append(values, k)
		}
	}
	for _, row := range values {
		for i, v := range row {
			if i >= len(c.Types) || isNullValue(v) {
				continue
			}
			if _, err := core.NormalizeScalar(c.Types[i].Type, c.Types[i].NumberKind, projectedValue(v)); err != nil {
				return invalidLookup(fmt.Sprintf("lookup column %q: %v", compared[i], err))
			}
		}
	}
	return nil
}

// validateKey checks a key condition: its Columns, and its Keys of a NULL-free
// value per column, or its Lookup projecting as many columns.
func (c *LookupCond) validateKey() error {
	if c.Column != "" {
		return invalidLookup(fmt.Sprintf("lookup key condition names column %q; a key's columns are its Columns", c.Column))
	}
	if err := checkLookupKey(c.Columns); err != nil {
		return err
	}
	key := strings.Join(c.Columns, ", ")
	if c.Op == LookupOpKeyMember {
		if c.Lookup == nil {
			return invalidLookup(fmt.Sprintf("lookup key (%s) is a member of no lookup", key))
		}
		return c.Lookup.validateProjecting(len(c.Columns))
	}
	for i, k := range c.Keys {
		if len(k) != len(c.Columns) {
			return invalidLookup(fmt.Sprintf("lookup key (%s): key %d has %d values", key, i, len(k)))
		}
		if slices.ContainsFunc(k, isNullValue) {
			return invalidLookup(fmt.Sprintf("lookup key (%s): key %d holds NULL, which equals no row", key, i))
		}
	}
	return nil
}

func (c *LookupCond) validate() error {
	if c == nil {
		return invalidLookup("lookup condition is nil")
	}
	forms := 0
	if c.Column != "" || len(c.Columns) > 0 || c.Op != "" {
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
	if err := c.validateTypes(); err != nil {
		return err
	}
	if c.Op == LookupOpKeyIn || c.Op == LookupOpKeyMember {
		return c.validateKey()
	}
	if c.Columns != nil {
		return invalidLookup(fmt.Sprintf("lookup condition on %q is %s, which compares one column; Columns are a key condition's", c.Column, c.Op))
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
		return c.Lookup.validateProjecting(1)
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
	if err := l.validateProjecting(1); err != nil {
		return nil, err
	}
	if !core.IsKeyType(field.DataType) {
		return nil, invalidLookup(fmt.Sprintf("membership on %s field '%s'; a membership tests a scalar field",
			field.DataType, field.ExternalName))
	}
	return &FilterNode{Field: field, Op: OpMember, Member: l}, nil
}
