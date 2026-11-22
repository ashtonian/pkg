package patch

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// RenderedRelationStatement is one DELETE or INSERT emitted for a
// relation patch op. The repository decorator runs each statement on
// the open transaction in slice order; statements within one rendered
// op are atomic with the owner UPDATE that produced ownerPKs.
type RenderedRelationStatement struct {
	SQL  string
	Args []any
}

// RenderRelationOpOptions configures RenderRelationOp.
type RenderRelationOpOptions struct {
	// TenantValue is bound into every junction statement as the value of
	// RelationSchema.TenantColumn. Required when the schema declares a
	// TenantColumn — rendering fails without it unless Unscoped is set
	//. Ignored for unscoped junctions.
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

// RelationOwner is the table a relation's owners are rows of.
type RelationOwner struct {
	Table, Key, Tenant string
}

// relationTarget is the alias the guarded statements read target rows by.
const relationTarget = "target__"

// RenderRelationOp renders the statements that apply a membership change to
// the pair table for the owners ownerPKs, for the caller to run in order in
// one transaction with the owner statement. Owners and values each bind as
// one array parameter, so a statement's parameter count is independent of
// both counts; pairs written are owners times values.
//
//	replace with values:  DELETE FROM j WHERE <source in owners> [AND tenant = t] AND <target not in values> <visible>
//	                      INSERT INTO j (source, target [, tenant]) SELECT … owners × values … ON CONFLICT DO NOTHING
//	replace with none:    DELETE FROM j WHERE <source in owners> [AND tenant = t] <visible>
//	add:                  INSERT … ON CONFLICT DO NOTHING
//	remove:               DELETE FROM j WHERE <source in owners> [AND tenant = t] AND <target in values> <visible>
//
// A set is `col = ANY($n)` / `col <> ALL($n)` on PostgreSQL and
// `col IN (SELECT value FROM json_each(?n))` / `NOT IN` on SQLite. With a
// target table (RelationSchema.TargetTable) the insert selects the owner rows
// and the visible target rows, and <visible> holds a delete to pairs whose
// target the request may see, so pairs it cannot see stay. Without one,
// PostgreSQL's insert reads its rows as the pair table's own row type
// (jsonb_populate_recordset), which types each key as its column.
//
// No owners need no statements. An operation it cannot render is an error: a
// tenant-scoped pair table without RenderRelationOpOptions.TenantValue (or
// Unscoped), an add or remove with no values, a null value, values or owners
// of more than one type, or a dialect other than PostgreSQL and SQLite.
func RenderRelationOp(op RelationPatchOp, d SQLDialect, ownerPKs []any, opts RenderRelationOpOptions) ([]RenderedRelationStatement, error) {
	if op.Schema == nil {
		return nil, errors.New("patch: relation patch op has nil schema")
	}
	if len(ownerPKs) == 0 {
		return nil, nil
	}
	rel := op.Schema
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
	if rel.TargetTable != "" && (opts.Owner.Table == "" || opts.Owner.Key == "" || opts.TargetVisible == nil) {
		return nil, fmt.Errorf("relation %s: a membership change through a target needs the owner table and the target's visibility", op.Field)
	}
	owners, err := relationKeys(ownerPKs)
	if err != nil {
		return nil, fmt.Errorf("patch: relation %s: owner keys: %w", op.Field, err)
	}
	values, err := relationKeys(op.Values)
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

// relationKeys are keys as one array parameter binds them: each a database
// scalar (a driver.Valuer's value, an integer as int64 or uint64, a float as
// float64, a string, a boolean), all of one type, none null.
func relationKeys(keys []any) ([]any, error) {
	out := make([]any, len(keys))
	var kind reflect.Type
	for i, k := range keys {
		v, err := relationKey(k)
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", i, err)
		}
		if t := reflect.TypeOf(v); kind == nil {
			kind = t
		} else if t != kind {
			return nil, fmt.Errorf("key %d is %s where key 0 is %s; keys are of one type", i, t, kind)
		}
		out[i] = v
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
	owners, values []any
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

// in is col's membership in keys, bound as one array: `col = ANY($n)` on
// PostgreSQL, the parameter typed as the column's array; json_each rows on
// SQLite, compared under the column's affinity.
func (s *statement) in(col string, keys []any, member bool) (string, error) {
	arr, err := s.d.MarshalValue(keys, core.TypeInfo{Type: core.FieldTypeArray})
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

// delete removes the owners' pairs, the tenant's when the pair table holds
// one: those whose target is among the values (keep false, a remove), or
// those whose target is not (keep true, a replace; with no values, every
// pair). Through a target table it reaches only pairs to visible targets.
func (r relationRenderer) delete(keep bool) (RenderedRelationStatement, error) {
	rel := r.rel
	s := &statement{d: r.d}
	var sb strings.Builder
	owners, err := s.in(rel.SourceColumn, r.owners, true)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString("DELETE FROM " + rel.JoinTable + " WHERE " + owners)
	if r.hasTenant {
		sb.WriteString(" AND " + rel.TenantColumn + " = " + s.bind(r.opts.TenantValue))
	}
	if !keep || len(r.values) > 0 {
		targets, err := s.in(rel.TargetColumn, r.values, !keep)
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
		sb.WriteString(" AND " + rel.TargetColumn + " IN (SELECT " + relationTarget + "." + rel.TargetKey + " FROM " + rel.TargetTable + " " + relationTarget)
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
	cols := rel.SourceColumn + ", " + rel.TargetColumn
	if r.hasTenant {
		cols += ", " + rel.TenantColumn
	}
	var sb strings.Builder
	sb.WriteString("INSERT INTO " + rel.JoinTable + " (" + cols + ") SELECT ")
	if rel.TargetTable == "" {
		if err := r.rows(s, &sb); err != nil {
			return RenderedRelationStatement{}, err
		}
		sb.WriteString(" ON CONFLICT DO NOTHING")
		return RenderedRelationStatement{SQL: sb.String(), Args: s.args}, nil
	}

	sb.WriteString(relationOwner + "." + r.opts.Owner.Key + ", " + relationTarget + "." + rel.TargetKey)
	if r.hasTenant {
		switch {
		case r.opts.Owner.Tenant != "":
			sb.WriteString(", " + relationOwner + "." + r.opts.Owner.Tenant)
		case rel.TargetTenant != "":
			sb.WriteString(", " + relationTarget + "." + rel.TargetTenant)
		default:
			return RenderedRelationStatement{}, fmt.Errorf("relation junction %s is tenant-scoped, but neither its owners nor its targets hold a tenant", rel.JoinTable)
		}
	}
	sb.WriteString(" FROM " + r.opts.Owner.Table + " " + relationOwner + ", " + rel.TargetTable + " " + relationTarget)
	owners, err := s.in(relationOwner+"."+r.opts.Owner.Key, r.owners, true)
	if err != nil {
		return RenderedRelationStatement{}, err
	}
	sb.WriteString(" WHERE " + owners)
	if r.hasTenant && r.opts.Owner.Tenant != "" {
		sb.WriteString(" AND " + relationOwner + "." + r.opts.Owner.Tenant + " = " + s.bind(r.opts.TenantValue))
	}
	targets, err := s.in(relationTarget+"."+rel.TargetKey, r.values, true)
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
// column's name as an unquoted identifier names it: folded to lower case.
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

func (r relationRenderer) sqliteRows(s *statement, sb *strings.Builder) error {
	owners, err := s.d.MarshalValue(r.owners, core.TypeInfo{Type: core.FieldTypeArray})
	if err != nil {
		return err
	}
	values, err := s.d.MarshalValue(r.values, core.TypeInfo{Type: core.FieldTypeArray})
	if err != nil {
		return err
	}
	ownersParam, valuesParam := s.bind(owners), s.bind(values)
	sb.WriteString("o.value, v.value")
	if r.hasTenant {
		sb.WriteString(", " + s.bind(r.opts.TenantValue))
	}
	sb.WriteString(" FROM json_each(" + ownersParam + ") AS o, json_each(" + valuesParam + ") AS v WHERE true")
	return nil
}

func (r relationRenderer) postgresRows(s *statement, sb *strings.Builder) error {
	rel := r.rel
	source, target, tenant := strings.ToLower(rel.SourceColumn), strings.ToLower(rel.TargetColumn), strings.ToLower(rel.TenantColumn)
	var tenantValue any
	if r.hasTenant {
		v, err := relationKey(r.opts.TenantValue)
		if err != nil {
			return fmt.Errorf("tenant: %w", err)
		}
		tenantValue = v
	}
	ownerRows := make([]map[string]any, len(r.owners))
	for i, o := range r.owners {
		ownerRows[i] = map[string]any{source: o}
		if r.hasTenant {
			ownerRows[i][tenant] = tenantValue
		}
	}
	valueRows := make([]map[string]any, len(r.values))
	for i, v := range r.values {
		valueRows[i] = map[string]any{target: v}
	}
	ownersJSON, err := json.Marshal(ownerRows)
	if err != nil {
		return err
	}
	valuesJSON, err := json.Marshal(valueRows)
	if err != nil {
		return err
	}
	sb.WriteString("o." + rel.SourceColumn + ", v." + rel.TargetColumn)
	if r.hasTenant {
		sb.WriteString(", o." + rel.TenantColumn)
	}
	sb.WriteString(" FROM jsonb_populate_recordset(NULL::" + rel.JoinTable + ", " + s.bind(string(ownersJSON)) + "::jsonb) AS o, jsonb_populate_recordset(NULL::" + rel.JoinTable + ", " + s.bind(string(valuesJSON)) + "::jsonb) AS v")
	return nil
}
