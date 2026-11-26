package patch

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// RenderedRelationStatement is one DELETE or INSERT emitted for a
// relation patch op. The repository decorator runs each statement on
// the open transaction in slice order; statements within one rendered
// op are atomic with the owner UPDATE that produced the owner keys.
type RenderedRelationStatement struct {
	SQL  string
	Args []any
}

// RenderRelationOpOptions configures RenderRelationOp.
type RenderRelationOpOptions struct {
	// TenantValue is bound into every junction statement as the value of
	// RelationSchema.TenantColumn. Required when the schema declares a
	// TenantColumn: rendering fails without it unless Unscoped is set.
	// Ignored for unscoped junctions.
	TenantValue any
	// Unscoped deliberately renders a tenant-scoped junction without a
	// tenant predicate. Never set it on a request path.
	Unscoped bool
	// Owner names the owner table, key and tenant column. A many-to-many
	// with a target (RelationSchema.TargetTable) needs it: its inserts
	// select the owner rows and the visible target rows instead of binding
	// raw values, so no pair names a target the request cannot see.
	Owner RelationOwner
	// TargetVisible renders the predicate (starting " AND ", or "") that
	// holds the target rows under alias to those the request may relate,
	// binding values with bind (filter.VisibleRelated). Required with a
	// TargetTable: inserts pair only visible targets, and deletes remove
	// only pairs to them.
	TargetVisible func(alias string, bind func(any) string) (string, error)
}

// RelationOwner is the table a relation's owners are rows of: Key are its
// key columns, paired by position with the relation's source columns.
type RelationOwner struct {
	Table  string
	Key    []string
	Tenant string
}

// relationTarget is the alias the guarded statements read target rows by.
const relationTarget = "target__"

// RenderRelationOp renders the statements that apply a membership change to
// the pair table for the owners ownerKeys, for the caller to run in order in
// one transaction with the owner statement. Owners and values each bind as
// one parameter, so a statement's parameter count is independent of both
// counts; pairs written are owners times values.
//
//	replace with values:  DELETE FROM j WHERE <source in owners> [AND tenant = t] AND <target not in values> <visible>
//	                      INSERT INTO j (source, target [, tenant]) SELECT … owners × values … ON CONFLICT DO NOTHING
//	replace with none:    DELETE FROM j WHERE <source in owners> [AND tenant = t] <visible>
//	add:                  INSERT … ON CONFLICT DO NOTHING
//	remove:               DELETE FROM j WHERE <source in owners> [AND tenant = t] AND <target in values> <visible>
//
// A set of keys is the row value of its columns IN / NOT IN the keys,
// bound as one parameter (RelationDialect.KeyIn): each column and each
// key's value compare as a filter compares the column's type
// (RelationSchema.SourceTypes, TargetTypes), so a decimal stored as "1.50"
// is the pair of 1.5. Each op value is a target key: a scalar for a
// one-column target key, a core.Key of a value per target column for a
// longer one. Every key is written in its type's canonical form
// (core.NormalizeScalar), the form the pair table's unique key compares.
// With a target table (RelationSchema.TargetTable) the insert selects the
// owner rows and the visible target rows, and <visible> holds a delete to
// pairs whose target the request may see, so pairs it cannot see stay.
// Without one, the insert reads the owners and the values as the rows the
// dialect renders for them (RelationDialect.RelationRows), each key typed as
// its column.
//
// No owners need no statements. An operation it cannot render is an error: a
// relation whose columns do not pair or are not typed
// (core.RelationSchema.Check), a tenant-scoped pair table without
// RenderRelationOpOptions.TenantValue (or Unscoped), an add or remove with
// no values, a null value, a key of another length than its columns, or a
// value its column's type cannot hold.
func RenderRelationOp(op RelationPatchOp, d RelationDialect, ownerKeys []core.Key, opts RenderRelationOpOptions) ([]RenderedRelationStatement, error) {
	if op.Schema == nil {
		return nil, errors.New("patch: relation patch op has nil schema")
	}
	if len(ownerKeys) == 0 {
		return nil, nil
	}
	rel := op.Schema
	err := rel.Check()
	if err != nil {
		return nil, fmt.Errorf("patch: relation %s: %w", op.Field, err)
	}
	switch op.Verb {
	case RelationVerbReplace:
	case RelationVerbAdd, RelationVerbRemove:
		if len(op.Values) == 0 {
			return nil, fmt.Errorf("patch: relation %s: %s needs at least one value", op.Field, op.Verb)
		}
	default:
		return nil, fmt.Errorf("patch: relation %s: unknown relation verb %q", op.Field, op.Verb)
	}
	if rel.TenantColumn != "" && opts.TenantValue == nil && !opts.Unscoped {
		return nil, fmt.Errorf("relation junction %s is tenant-scoped (%s): set TenantValue or Unscoped", rel.JoinTable, rel.TenantColumn)
	}
	if rel.TargetTable != "" {
		if opts.Owner.Table == "" || len(opts.Owner.Key) == 0 || opts.TargetVisible == nil {
			return nil, fmt.Errorf("relation %s: a membership change through a target needs the owner table and the target's visibility", op.Field)
		}
		if len(opts.Owner.Key) != len(rel.SourceColumns) {
			return nil, fmt.Errorf("relation %s: the owner key (%s) does not pair with the source columns (%s)",
				op.Field, strings.Join(opts.Owner.Key, ", "), strings.Join(rel.SourceColumns, ", "))
		}
	}
	owners, err := relationKeys(ownerKeys, rel.SourceColumns, rel.SourceTypes)
	if err != nil {
		return nil, fmt.Errorf("patch: relation %s: owner keys: %w", op.Field, err)
	}
	values, err := relationValueKeys(op.Values, rel.TargetColumns, rel.TargetTypes)
	if err != nil {
		return nil, fmt.Errorf("patch: relation %s: values: %w", op.Field, err)
	}
	r := relationRenderer{
		rel: rel, d: d, opts: opts, owners: owners, values: values,
		hasTenant: rel.TenantColumn != "" && !opts.Unscoped,
	}

	var stmts []RenderedRelationStatement
	if op.Verb != RelationVerbAdd {
		st, err := r.delete(op.Verb == RelationVerbReplace)
		if err != nil {
			return nil, fmt.Errorf("relation %s: %w", op.Field, err)
		}
		stmts = append(stmts, st)
	}
	if op.Verb != RelationVerbRemove && len(values) > 0 {
		st, err := r.insert()
		if err != nil {
			return nil, fmt.Errorf("relation %s: %w", op.Field, err)
		}
		stmts = append(stmts, st)
	}
	return stmts, nil
}

// relationValueKeys are a membership op's values as target keys of the
// columns: a one-column key's value is the scalar itself, a longer key's a
// core.Key (or []any) of a value per column.
func relationValueKeys(values []any, columns []string, types []core.TypeInfo) ([]core.Key, error) {
	keys := make([]core.Key, len(values))
	for i, v := range values {
		switch k := v.(type) {
		case core.Key:
			keys[i] = k
		case []any:
			keys[i] = k
		default:
			if len(columns) != 1 {
				return nil, fmt.Errorf("value %d is %s; a target key of %d columns is a core.Key", i, gowalker.TypeText(reflect.TypeOf(v)), len(columns))
			}
			keys[i] = core.Key{v}
		}
	}
	return relationKeys(keys, columns, types)
}

// relationKeys are keys as one parameter binds them: a value per column,
// each in its column's type's canonical form (core.NormalizeScalar), a
// driver.Valuer read as the value it stands for, none null.
func relationKeys(keys []core.Key, columns []string, types []core.TypeInfo) ([]core.Key, error) {
	out := make([]core.Key, len(keys))
	for i, k := range keys {
		if len(k) != len(columns) {
			return nil, fmt.Errorf("key %d has %d values for %d columns", i, len(k), len(columns))
		}
		out[i] = make(core.Key, len(k))
		for j, part := range k {
			v, err := driverScalar(part)
			if err == nil {
				v, err = core.NormalizeScalar(types[j].Type, types[j].NumberKind, v)
			}
			if err != nil {
				if len(columns) == 1 {
					return nil, fmt.Errorf("key %d: %w", i, err)
				}
				return nil, fmt.Errorf("key %d: column %s: %w", i, columns[j], err)
			}
			out[i][j] = v
		}
	}
	return out, nil
}

// driverScalar is k as the database binds it: a driver.Valuer's value,
// else k; null is no value.
func driverScalar(k any) (any, error) {
	valuer, ok := k.(driver.Valuer)
	if ok {
		v, err := valuer.Value()
		if err != nil {
			return nil, err
		}
		k = v
	}
	if k == nil {
		return nil, errors.New("null is not a key")
	}
	return k, nil
}

// tenantScalar is a tenant value as a JSON record binds it: a database
// scalar (a driver.Valuer's value, an integer as int64 or uint64, a float
// as float64, a string, a boolean).
func tenantScalar(k any) (any, error) {
	if valuer, ok := k.(driver.Valuer); ok {
		v, err := valuer.Value()
		if err != nil {
			return nil, err
		}
		k = v
	}
	rv := reflect.ValueOf(k)
	switch rv.Kind() {
	case reflect.Invalid:
		return nil, errors.New("null is not a key")
	case reflect.String:
		return rv.String(), nil
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint(), nil
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	}
	return nil, fmt.Errorf("%s is not a key: keys are strings, numbers or booleans", gowalker.TypeText(reflect.TypeOf(k)))
}

// RelationDialect is the dialect a relation patch's statements are written
// for, and what a SQL provider implements for them beside SQLDialect: key
// columns compared as filters compare their types, and the rows an insert
// without a target table pairs (filter.Dialect is one).
type RelationDialect interface {
	SQLDialect
	// KeyIn renders the key columns of table, qualified by alias and typed
	// by types, as a row IN keys (NOT IN when member is false), the keys
	// bound as one parameter.
	KeyIn(table, alias string, columns []string, types []core.TypeInfo, keys []core.Key, member bool, bind func(any) string) (string, error)
	// KeyOperand renders what a key column, read as expr and typed t,
	// compares by.
	KeyOperand(expr string, t core.TypeInfo) string
	// RelationRows renders the rows an insert into rel.JoinTable selects
	// when no target table holds the values: owners, keys of
	// rel.SourceColumns, crossed with values, keys of rel.TargetColumns,
	// each set bound as one parameter whatever its size, and each key read
	// back as its column's type. tenant, when not nil, is the tenant of
	// rel.TenantColumn every pair is written for. The result names what
	// the select list writes each column from, and the conditions the
	// statement's own are ANDed after.
	RelationRows(rel *core.RelationSchema, owners, values []core.Key, tenant *dialect.PairTenant, bind func(any) string) (dialect.PairRows, error)
}

// relationRenderer renders one membership change's statements.
type relationRenderer struct {
	rel            *core.RelationSchema
	d              RelationDialect
	opts           RenderRelationOpOptions
	owners, values []core.Key
	hasTenant      bool
}

// statement accumulates one statement's arguments, numbering placeholders
// as they are bound.
type statement struct {
	d    RelationDialect
	args []any
}

func (s *statement) bind(v any) string {
	s.args = append(s.args, v)
	return s.d.Placeholder(len(s.args))
}

// in is the membership of columns of table, each qualified by alias and
// typed by types, in keys, bound as one parameter (RelationDialect.KeyIn).
func (s *statement) in(table, alias string, columns []string, types []core.TypeInfo, keys []core.Key, member bool) (string, error) {
	return s.d.KeyIn(table, alias, columns, types, keys, member, s.bind)
}

// keyOperands are the columns, each qualified by column, as they compare
// by their types (RelationDialect.KeyOperand).
func keyOperands(d RelationDialect, column func(string) string, columns []string, types []core.TypeInfo) []string {
	operands := make([]string, len(columns))
	for i, c := range columns {
		operands[i] = d.KeyOperand(column(c), types[i])
	}
	return operands
}

// columnList is the columns, each qualified by column, comma-separated.
func columnList(column func(string) string, columns []string) string {
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = column(c)
	}
	return strings.Join(out, ", ")
}

// delete removes the owners' pairs, the tenant's when the pair table holds
// one: those whose target is among the values (keep false, a remove), or
// those whose target is not (keep true, a replace; with no values, every
// pair). Through a target table it reaches only pairs to visible targets.
// Each column is qualified by the pair table: SQLite reads an unqualified
// quoted name that matches no column as a string, which would turn a
// misspelt target column's NOT IN into every pair.
func (r relationRenderer) delete(keep bool) (RenderedRelationStatement, error) {
	rel := r.rel
	s := &statement{d: r.d}
	pairs := r.d.QuoteIdent(rel.JoinTable)
	column := func(name string) string { return pairs + "." + r.d.QuoteIdent(name) }
	var sb strings.Builder
	owners, err := s.in(rel.JoinTable, rel.JoinTable, rel.SourceColumns, rel.SourceTypes, r.owners, true)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString("DELETE FROM " + pairs + " WHERE " + owners)
	if r.hasTenant {
		sb.WriteString(" AND " + column(rel.TenantColumn) + " = " + s.bind(r.opts.TenantValue))
	}
	if !keep || len(r.values) > 0 {
		targets, err := s.in(rel.JoinTable, rel.JoinTable, rel.TargetColumns, rel.TargetTypes, r.values, !keep)
		if err != nil {
			return RenderedRelationStatement{}, err
		}
		sb.WriteString(" AND " + targets)
	}
	if rel.TargetTable != "" {
		visible, err := r.opts.TargetVisible(relationTarget, s.bind)
		if err != nil {
			return RenderedRelationStatement{}, err
		}
		target := func(name string) string { return r.qualified(relationTarget, name) }
		pair := keyOperands(r.d, column, rel.TargetColumns, rel.TargetTypes)
		row := strings.Join(pair, ", ")
		if len(pair) > 1 {
			row = "(" + row + ")"
		}
		sb.WriteString(" AND " + row + " IN (SELECT " + strings.Join(keyOperands(r.d, target, rel.TargetKeys, rel.TargetTypes), ", ") +
			" FROM " + r.d.QuoteIdent(rel.TargetTable) + " " + relationTarget)
		if visible != "" {
			sb.WriteString(" WHERE " + strings.TrimPrefix(visible, " AND "))
		}
		sb.WriteByte(')')
	}
	return RenderedRelationStatement{SQL: sb.String(), Args: s.args}, nil
}

// relationOwner is the alias the guarded insert reads owner rows by.
const relationOwner = "owner__"

// insert pairs every owner with every value, ON CONFLICT DO NOTHING (each
// pair needs a unique key). Through a target table the pairs are selected
// from the owner rows and the visible target rows:
//
//	INSERT INTO j (source, target [, tenant])
//	SELECT owner__.key, target__.key [, owner__.tenant | target__.tenant]
//	FROM owners owner__, targets target__
//	WHERE <owner__.key in owners> [AND owner__.tenant = t] AND <target__.key in values> <visible>
//
// so every key is compared with a column and binds as its type. Without one,
// the owners and values are rows of their own (RelationDialect.RelationRows).
func (r relationRenderer) insert() (RenderedRelationStatement, error) {
	rel := r.rel
	s := &statement{d: r.d}
	q := r.d.QuoteIdent
	cols := columnList(q, rel.SourceColumns) + ", " + columnList(q, rel.TargetColumns)
	if r.hasTenant {
		cols += ", " + q(rel.TenantColumn)
	}
	var sb strings.Builder
	sb.WriteString("INSERT INTO " + q(rel.JoinTable) + " (" + cols + ") SELECT ")
	if rel.TargetTable == "" {
		var tenant *dialect.PairTenant
		if r.hasTenant {
			value := r.opts.TenantValue
			tenant = &dialect.PairTenant{Value: value, Record: func() (any, error) { return tenantScalar(value) }}
		}
		rows, err := r.d.RelationRows(rel, r.owners, r.values, tenant, s.bind)
		if err != nil {
			return RenderedRelationStatement{}, err
		}
		pair := newPair{sources: rows.Sources, targets: rows.Targets, tenant: rows.Tenant}
		sb.WriteString(pair.selectList() + rows.From)
		where := rows.Where
		held := r.held(pair)
		if held != "" {
			where = append(slices.Clip(where), "NOT "+held)
		}
		if len(where) > 0 {
			sb.WriteString(" WHERE " + strings.Join(where, " AND "))
		}
		sb.WriteString(" ON CONFLICT DO NOTHING")
		return RenderedRelationStatement{SQL: sb.String(), Args: s.args}, nil
	}

	owner := func(name string) string { return r.qualified(relationOwner, name) }
	target := func(name string) string { return r.qualified(relationTarget, name) }
	pair := newPair{sources: make([]string, len(r.opts.Owner.Key)), targets: make([]string, len(rel.TargetKeys))}
	for i, c := range r.opts.Owner.Key {
		pair.sources[i] = owner(c)
	}
	for i, c := range rel.TargetKeys {
		pair.targets[i] = target(c)
	}
	if r.hasTenant {
		switch {
		case r.opts.Owner.Tenant != "":
			pair.tenant = owner(r.opts.Owner.Tenant)
		case rel.TargetTenant != "":
			pair.tenant = target(rel.TargetTenant)
		default:
			return RenderedRelationStatement{}, fmt.Errorf("relation junction %s is tenant-scoped, but neither its owners nor its targets hold a tenant", rel.JoinTable)
		}
	}
	sb.WriteString(pair.selectList())
	sb.WriteString(" FROM " + q(r.opts.Owner.Table) + " " + relationOwner + ", " + q(rel.TargetTable) + " " + relationTarget)
	owners, err := s.in(r.opts.Owner.Table, relationOwner, r.opts.Owner.Key, rel.SourceTypes, r.owners, true)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString(" WHERE " + owners)
	if r.hasTenant && r.opts.Owner.Tenant != "" {
		sb.WriteString(" AND " + owner(r.opts.Owner.Tenant) + " = " + s.bind(r.opts.TenantValue))
	}
	targets, err := s.in(rel.TargetTable, relationTarget, rel.TargetKeys, rel.TargetTypes, r.values, true)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString(" AND " + targets)
	visible, err := r.opts.TargetVisible(relationTarget, s.bind)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString(visible)
	held := r.held(pair)
	if held != "" {
		sb.WriteString(" AND NOT " + held)
	}
	sb.WriteString(" ON CONFLICT DO NOTHING")
	return RenderedRelationStatement{SQL: sb.String(), Args: s.args}, nil
}

// newPair is what an insert's select list writes a pair's columns from.
type newPair struct {
	sources, targets []string
	tenant           string
}

// selectList is the insert's select list: sources, targets, then the tenant
// when the pair has one.
func (p newPair) selectList() string {
	list := strings.Join(p.sources, ", ") + ", " + strings.Join(p.targets, ", ")
	if p.tenant != "" {
		list += ", " + p.tenant
	}
	return list
}

// held is the condition that the pair table holds pair's keys under
// another spelling, its columns compared as their types compare
// (RelationDialect.KeyOperand): a decimal stored as "1.50" in a text column
// is the key 1.5, which the table's unique key, comparing what is stored,
// would take as a second pair. Empty when every column compares as it is
// stored, where ON CONFLICT alone keeps a pair once.
func (r relationRenderer) held(pair newPair) string {
	rel := r.rel
	const alias = "held__"
	column := func(name string) string { return r.qualified(alias, name) }
	var conds []string
	spelled := false
	compare := func(columns, exprs []string, types []core.TypeInfo) {
		for i, c := range columns {
			stored := r.d.KeyOperand(column(c), types[i])
			if stored != column(c) {
				spelled = true
			}
			conds = append(conds, stored+" = "+r.d.KeyOperand(exprs[i], types[i]))
		}
	}
	compare(rel.SourceColumns, pair.sources, rel.SourceTypes)
	compare(rel.TargetColumns, pair.targets, rel.TargetTypes)
	if !spelled {
		return ""
	}
	if r.hasTenant {
		conds = append(conds, column(rel.TenantColumn)+" = "+pair.tenant)
	}
	return "EXISTS (SELECT 1 FROM " + r.d.QuoteIdent(rel.JoinTable) + " " + r.d.QuoteIdent(alias) + " WHERE " + strings.Join(conds, " AND ") + ")"
}

// qualified is column of alias, a FROM item of the statement, both quoted.
func (r relationRenderer) qualified(alias, column string) string {
	return r.d.QuoteIdent(alias) + "." + r.d.QuoteIdent(column)
}
