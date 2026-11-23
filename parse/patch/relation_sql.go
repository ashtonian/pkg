package patch

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

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
// A set of one-column keys is `col = ANY($n)` / `col <> ALL($n)` on
// PostgreSQL and `col IN (SELECT value FROM json_each(?n))` / `NOT IN` on
// SQLite; a set of keys of several columns is the row value of the columns
// IN / NOT IN the keys (dialect.KeySet). Each op value is a target key: a
// scalar for a one-column target key, a core.Key of a value per target
// column for a longer one. With a target table (RelationSchema.TargetTable)
// the insert selects the owner rows and the visible target rows, and
// <visible> holds a delete to pairs whose target the request may see, so
// pairs it cannot see stay. Without one, PostgreSQL's insert reads its rows
// as the pair table's own row type (jsonb_populate_recordset), which types
// each key as its column.
//
// No owners need no statements. An operation it cannot render is an error: a
// relation whose columns do not pair (core.RelationSchema.Check), a
// tenant-scoped pair table without RenderRelationOpOptions.TenantValue (or
// Unscoped), an add or remove with no values, a null value, a key of
// another length than its columns, values or owners of more than one type
// in a position, or a dialect other than PostgreSQL and SQLite.
func RenderRelationOp(op RelationPatchOp, d SQLDialect, ownerKeys []core.Key, opts RenderRelationOpOptions) ([]RenderedRelationStatement, error) {
	if op.Schema == nil {
		return nil, errors.New("patch: relation patch op has nil schema")
	}
	if len(ownerKeys) == 0 {
		return nil, nil
	}
	rel := op.Schema
	if err := rel.Check(); err != nil {
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
	owners, err := relationKeys(ownerKeys, len(rel.SourceColumns))
	if err != nil {
		return nil, fmt.Errorf("patch: relation %s: owner keys: %w", op.Field, err)
	}
	values, err := relationValueKeys(op.Values, len(rel.TargetColumns))
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

// relationValueKeys are a membership op's values as target keys of width
// columns: a one-column key's value is the scalar itself, a longer key's a
// core.Key (or []any) of a value per column.
func relationValueKeys(values []any, width int) ([]core.Key, error) {
	keys := make([]core.Key, len(values))
	for i, v := range values {
		switch k := v.(type) {
		case core.Key:
			keys[i] = k
		case []any:
			keys[i] = k
		default:
			if width != 1 {
				return nil, fmt.Errorf("value %d is %T; a target key of %d columns is a core.Key", i, v, width)
			}
			keys[i] = core.Key{v}
		}
	}
	return relationKeys(keys, width)
}

// relationKeys are keys as one parameter binds them: each of width values,
// each a database scalar (a driver.Valuer's value, an integer as int64 or
// uint64, a float as float64, a string, a boolean), a position's values all
// of one type, none null.
func relationKeys(keys []core.Key, width int) ([]core.Key, error) {
	out := make([]core.Key, len(keys))
	kinds := make([]reflect.Type, width)
	for i, k := range keys {
		if len(k) != width {
			return nil, fmt.Errorf("key %d has %d values for %d columns", i, len(k), width)
		}
		out[i] = make(core.Key, width)
		for j, part := range k {
			v, err := relationKey(part)
			if err != nil {
				return nil, fmt.Errorf("key %d: %w", i, err)
			}
			if t := reflect.TypeOf(v); kinds[j] == nil {
				kinds[j] = t
			} else if t != kinds[j] {
				return nil, fmt.Errorf("key %d is %s where key 0 is %s; keys are of one type", i, t, kinds[j])
			}
			out[i][j] = v
		}
	}
	return out, nil
}

func relationKey(k any) (any, error) {
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
	return nil, fmt.Errorf("%T is not a key: keys are strings, numbers or booleans", k)
}

// relationRenderer renders one membership change's statements.
type relationRenderer struct {
	rel            *core.RelationSchema
	d              SQLDialect
	opts           RenderRelationOpOptions
	owners, values []core.Key
	hasTenant      bool
}

// statement accumulates one statement's arguments, numbering placeholders
// as they are bound.
type statement struct {
	d    SQLDialect
	args []any
}

func (s *statement) bind(v any) string {
	s.args = append(s.args, v)
	return s.d.Placeholder(len(s.args))
}

// in is the membership of columns of table, each qualified by column, in
// keys, bound as one parameter. One column is `col = ANY($n)` on
// PostgreSQL, the parameter typed as the column's array, and json_each rows
// on SQLite, compared under the column's affinity; several are the row
// value of the columns in the keys (dialect.KeySet).
func (s *statement) in(table string, column func(string) string, columns []string, keys []core.Key, member bool) (string, error) {
	if len(columns) > 1 {
		return dialect.KeySet(s.d, rowValue(column, columns), table, columns, keys, member, s.bind)
	}
	col := column(columns[0])
	values := make([]any, len(keys))
	for i, k := range keys {
		values[i] = k[0]
	}
	arr, err := s.d.MarshalValue(values, core.TypeInfo{Type: core.FieldTypeArray})
	if err != nil {
		return "", err
	}
	param := s.bind(arr)
	switch k := dialect.KindOf(s.d); k {
	case dialect.KindPostgres:
		if member {
			return col + " = ANY(" + param + ")", nil
		}
		return col + " <> ALL(" + param + ")", nil
	case dialect.KindSQLite:
	default:
		return "", dialect.Unsupported(k, "a relation key set")
	}
	if member {
		return col + " IN (SELECT value FROM json_each(" + param + "))", nil
	}
	return col + " NOT IN (SELECT value FROM json_each(" + param + "))", nil
}

// rowValue is the columns, each qualified by column: one column itself,
// several a parenthesized row value.
func rowValue(column func(string) string, columns []string) string {
	if len(columns) == 1 {
		return column(columns[0])
	}
	return "(" + columnList(column, columns) + ")"
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
	owners, err := s.in(rel.JoinTable, column, rel.SourceColumns, r.owners, true)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString("DELETE FROM " + pairs + " WHERE " + owners)
	if r.hasTenant {
		sb.WriteString(" AND " + column(rel.TenantColumn) + " = " + s.bind(r.opts.TenantValue))
	}
	if !keep || len(r.values) > 0 {
		targets, err := s.in(rel.JoinTable, column, rel.TargetColumns, r.values, !keep)
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
		sb.WriteString(" AND " + rowValue(column, rel.TargetColumns) + " IN (SELECT " + columnList(target, rel.TargetKeys) + " FROM " + r.d.QuoteIdent(rel.TargetTable) + " " + relationTarget)
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
// the owners and values are rows of their own (rows).
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
		if err := r.rows(s, &sb); err != nil {
			return RenderedRelationStatement{}, err
		}
		sb.WriteString(" ON CONFLICT DO NOTHING")
		return RenderedRelationStatement{SQL: sb.String(), Args: s.args}, nil
	}

	owner := func(name string) string { return r.qualified(relationOwner, name) }
	target := func(name string) string { return r.qualified(relationTarget, name) }
	sb.WriteString(columnList(owner, r.opts.Owner.Key) + ", " + columnList(target, rel.TargetKeys))
	if r.hasTenant {
		switch {
		case r.opts.Owner.Tenant != "":
			sb.WriteString(", " + owner(r.opts.Owner.Tenant))
		case rel.TargetTenant != "":
			sb.WriteString(", " + target(rel.TargetTenant))
		default:
			return RenderedRelationStatement{}, fmt.Errorf("relation junction %s is tenant-scoped, but neither its owners nor its targets hold a tenant", rel.JoinTable)
		}
	}
	sb.WriteString(" FROM " + q(r.opts.Owner.Table) + " " + relationOwner + ", " + q(rel.TargetTable) + " " + relationTarget)
	owners, err := s.in(r.opts.Owner.Table, owner, r.opts.Owner.Key, r.owners, true)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString(" WHERE " + owners)
	if r.hasTenant && r.opts.Owner.Tenant != "" {
		sb.WriteString(" AND " + owner(r.opts.Owner.Tenant) + " = " + s.bind(r.opts.TenantValue))
	}
	targets, err := s.in(rel.TargetTable, target, rel.TargetKeys, r.values, true)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString(" AND " + targets)
	visible, err := r.opts.TargetVisible(relationTarget, s.bind)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString(visible)
	sb.WriteString(" ON CONFLICT DO NOTHING")
	return RenderedRelationStatement{SQL: sb.String(), Args: s.args}, nil
}

// rows writes the select list and FROM of an insert without a target
// table: the owners and the values each as rows, crossed.
//
// PostgreSQL cannot type an untyped array outside a comparison, and a key's
// Go type does not name its column's type (a string key of a uuid column),
// so each set binds as a JSON array of records read as the pair table's own
// row type, which types each key as its column. A record's member is the
// column's exact name, the name the quoted column reads.
//
// SQLite types values by the column's affinity on insert, so json_each rows
// serve, with WHERE true to part the upsert's ON from a join's.
func (r relationRenderer) rows(s *statement, sb *strings.Builder) error {
	switch k := dialect.KindOf(r.d); k {
	case dialect.KindSQLite:
		return r.sqliteRows(s, sb)
	case dialect.KindPostgres:
		return r.postgresRows(s, sb)
	default:
		return dialect.Unsupported(k, "a relation insert")
	}
}

// sqliteRows reads a one-column key as each json_each row's value, and a
// longer key's columns by position from each row's array.
func (r relationRenderer) sqliteRows(s *statement, sb *strings.Builder) error {
	bindKeys := func(keys []core.Key) (string, error) {
		var doc any = keys
		if len(keys) > 0 && len(keys[0]) == 1 {
			values := make([]any, len(keys))
			for i, k := range keys {
				values[i] = k[0]
			}
			doc = values
		}
		arr, err := s.d.MarshalValue(doc, core.TypeInfo{Type: core.FieldTypeArray})
		if err != nil {
			return "", err
		}
		return s.bind(arr), nil
	}
	ownersParam, err := bindKeys(r.owners)
	if err != nil {
		return err
	}
	valuesParam, err := bindKeys(r.values)
	if err != nil {
		return err
	}
	read := func(alias string, width int) string {
		if width == 1 {
			return alias + ".value"
		}
		positions := make([]string, width)
		for i := range positions {
			positions[i] = "json_extract(" + alias + ".value, '$[" + strconv.Itoa(i) + "]')"
		}
		return strings.Join(positions, ", ")
	}
	sb.WriteString(read("o", len(r.rel.SourceColumns)) + ", " + read("v", len(r.rel.TargetColumns)))
	if r.hasTenant {
		sb.WriteString(", " + s.bind(r.opts.TenantValue))
	}
	sb.WriteString(" FROM json_each(" + ownersParam + ") AS o, json_each(" + valuesParam + ") AS v WHERE true")
	return nil
}

func (r relationRenderer) postgresRows(s *statement, sb *strings.Builder) error {
	rel := r.rel
	var tenantValue any
	if r.hasTenant {
		v, err := relationKey(r.opts.TenantValue)
		if err != nil {
			return fmt.Errorf("tenant: %w", err)
		}
		tenantValue = v
	}
	ownerRows := make([]map[string]any, len(r.owners))
	for i, k := range r.owners {
		ownerRows[i] = make(map[string]any, len(k)+1)
		for j, c := range rel.SourceColumns {
			ownerRows[i][c] = k[j]
		}
		if r.hasTenant {
			ownerRows[i][rel.TenantColumn] = tenantValue
		}
	}
	valueRows := make([]map[string]any, len(r.values))
	for i, k := range r.values {
		valueRows[i] = make(map[string]any, len(k))
		for j, c := range rel.TargetColumns {
			valueRows[i][c] = k[j]
		}
	}
	ownersJSON, err := json.Marshal(ownerRows)
	if err != nil {
		return err
	}
	valuesJSON, err := json.Marshal(valueRows)
	if err != nil {
		return err
	}
	o := func(name string) string { return r.qualified("o", name) }
	v := func(name string) string { return r.qualified("v", name) }
	sb.WriteString(columnList(o, rel.SourceColumns) + ", " + columnList(v, rel.TargetColumns))
	if r.hasTenant {
		sb.WriteString(", " + o(rel.TenantColumn))
	}
	row := "NULL::" + r.d.QuoteIdent(rel.JoinTable)
	sb.WriteString(" FROM jsonb_populate_recordset(" + row + ", " + s.bind(string(ownersJSON)) + "::jsonb) AS o, jsonb_populate_recordset(" + row + ", " + s.bind(string(valuesJSON)) + "::jsonb) AS v")
	return nil
}

// qualified is column of alias, a FROM item of the statement, both quoted.
func (r relationRenderer) qualified(alias, column string) string {
	return r.d.QuoteIdent(alias) + "." + r.d.QuoteIdent(column)
}
