package parse

import (
	"cmp"
	"fmt"
	"reflect"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// RelatedResource is a resource a relation leads to. Every *Resource[U] is
// one; the interface is sealed so a relation's target always owns its key,
// tenancy, soft deletion and filter schema.
type RelatedResource interface {
	Table() string
	PrimaryKeyColumn() string
	TenantColumnName() string
	DeletedColumnName() string
	FilterSchema() *filter.ResourceSchema
	columnType(name string) (reflect.Type, bool)
}

func (r *Resource[T]) columnType(name string) (reflect.Type, bool) {
	t, ok := r.tableColumns[name]
	return t, ok
}

// Relation is a declared relation: HasMany, BelongsTo or
// ManyToMany, then Resource.Relate.
type Relation struct {
	kind       core.RelationKind
	target     RelatedResource
	foreignKey string
	references string
	through    Junction
	policy     bool
	indexed    bool
	includable bool
	facetable  bool
	writable   bool
	// label and description are the relation field's discovery metadata,
	// as the label and description tags are a column's.
	label, description string
}

// RelationOption configures a Relation.
type RelationOption func(*Relation)

// Junction is a many-to-many's pair table: Source holds the owner's key,
// Target the target's, Tenant (optional) the tenant of each pair.
type Junction struct {
	Table, Source, Target, Tenant string
}

// Through names a many-to-many's pair table and its owner and target key
// columns.
func Through(table, source, target string) Junction {
	return Junction{Table: table, Source: source, Target: target}
}

// HasMany relates each owner to the target rows whose foreignKey holds the
// owner's key (devices have commands: commands.device_id).
func HasMany(target RelatedResource, foreignKey string, opts ...RelationOption) Relation {
	return newRelation(core.HasMany, target, foreignKey, opts)
}

// BelongsTo relates each owner to the target row its foreignKey column
// names (a command belongs to a device: commands.device_id).
func BelongsTo(target RelatedResource, foreignKey string, opts ...RelationOption) Relation {
	return newRelation(core.BelongsTo, target, foreignKey, opts)
}

// ManyToMany relates owners and targets through a pair table.
func ManyToMany(target RelatedResource, through Junction, opts ...RelationOption) Relation {
	r := newRelation(core.ManyToMany, target, "", opts)
	r.through = through
	return r
}

func newRelation(kind core.RelationKind, target RelatedResource, fk string, opts []RelationOption) Relation {
	r := Relation{kind: kind, target: target, foreignKey: fk}
	for _, opt := range opts {
		opt(&r)
	}
	return r
}

// References names the column a foreign key holds when it is not the
// referenced resource's key: the owner's for a has-many, the target's for a
// belongs-to.
func References(column string) RelationOption { return func(r *Relation) { r.references = column } }

// PolicyScoped holds the related rows to the server's policy for the target
// table (Scope.Related) instead of the request's tenant: a relation to rows
// another tenant shares, such as platform-wide device types.
func PolicyScoped() RelationOption { return func(r *Relation) { r.policy = true } }

// Indexed declares that an index leads with the correlation column — the
// has-many's foreign key, or the pair table's owner column — so the
// relation costs as an indexed field. A belongs-to correlates on the
// target's key and is always indexed.
func Indexed() RelationOption { return func(r *Relation) { r.indexed = true } }

// Includable lets a list include the related rows' keys (`include=name`):
// a has-many's children, a many-to-many's targets, as visible to the
// request. A belongs-to's key is the owner's own foreign-key column.
func Includable() RelationOption { return func(r *Relation) { r.includable = true } }

// Facetable lets a list count its rows per related key (`facets=name`):
// distinct owners per visible child or target. A belongs-to facets through
// its foreign-key column.
func Facetable() RelationOption { return func(r *Relation) { r.facetable = true } }

// Writable lets a patch change a many-to-many's membership (replace, add,
// remove of target keys): pairs are made only with targets the request may
// relate, and removed only from them. The pair table must hold nothing but
// the pairs (and their tenant).
func Writable() RelationOption { return func(r *Relation) { r.writable = true } }

// Describe gives the relation field the label and description schema
// discovery serves for it, as the `label` and `description` tags do for a
// column's field.
func Describe(label, description string) RelationOption {
	return func(r *Relation) { r.label, r.description = label, description }
}

// Relate declares a relation named name on r: requests filter by it with
// the target's own filter (`$some`, `$none`, `$is`, `$is_not`) or by the
// target's key. Columns are checked against both resources' types. Declare
// every relation before the resources serve requests; Relate is not safe
// for concurrent use with them.
func (r *Resource[T]) Relate(name string, rel Relation) error {
	if !core.IsIdentifier(name) {
		return fmt.Errorf("parse: %s: relation name %q is not an identifier", r.table, name)
	}
	fs := r.filterSchema
	if _, ok := fs.Fields[name]; ok {
		return fmt.Errorf("parse: %s: relation %q names an existing field", r.table, name)
	}
	if _, ok := fs.SystemFields[name]; ok {
		return fmt.Errorf("parse: %s: relation %q names a system field", r.table, name)
	}
	if rel.target == nil {
		return fmt.Errorf("parse: %s: relation %q has no target", r.table, name)
	}
	rs, err := r.relationSchema(name, rel)
	if err != nil {
		return err
	}
	if rel.includable && rel.kind == core.BelongsTo {
		return fmt.Errorf("parse: %s: relation %q: a belongs-to's key is the owner's foreign key; it is not included", r.table, name)
	}
	if rel.facetable && (rel.kind == core.BelongsTo || rs.PrimaryKey != r.pk) {
		return fmt.Errorf("parse: %s: relation %q: a facet counts owners per key of a relation on the owner's key; facet a belongs-to by its foreign-key column", r.table, name)
	}
	if rel.writable {
		if rel.kind != core.ManyToMany {
			return fmt.Errorf("parse: %s: relation %q: only a many-to-many's membership is written through its owner; a belongs-to writes its foreign-key column", r.table, name)
		}
		if rs.TenantColumn != "" && r.tenant == "" && rs.TargetTenant == "" {
			return fmt.Errorf("parse: %s: relation %q: the pair table's tenant cannot be taken from the owner or the target", r.table, name)
		}
	}
	t := rel.target
	keyType, _ := t.columnType(t.PrimaryKeyColumn())
	key := core.TypeOf(keyType)
	ops := []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpIn, filter.OpNin, filter.OpNull, filter.OpNotNull, filter.OpSome, filter.OpNone}
	if rel.kind == core.BelongsTo {
		ops = []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpIn, filter.OpNin, filter.OpNull, filter.OpNotNull, filter.OpIs, filter.OpIsNot}
	}
	fs.Fields[name] = &filter.FieldSchema{
		ExternalName:     name,
		DataType:         key.Type,
		NumberKind:       key.NumberKind,
		AllowedOperators: ops,
		Indexed:          rel.indexed || rel.kind == core.BelongsTo,
		Includable:       rel.includable,
		Facetable:        rel.facetable,
		Label:            rel.label,
		Description:      rel.description,
		Relation:         rs,
		Related:          t.FilterSchema(),
		RelatedBy:        r.correlationField(rs.PrimaryKey),
	}
	if rel.writable {
		path := patch.RelationPath(rs, reflect.SliceOf(keyType))
		r.patchSchema.AllowedPaths[core.FormatPointer(name)] = path
		r.serverPatchSchema.AllowedPaths[core.FormatPointer(name)] = path
	}
	return nil
}

// correlationField is the owner's field for column: its system field, a
// filter field over the column, or one typed from T.
func (r *Resource[T]) correlationField(column string) *filter.FieldSchema {
	fs := r.filterSchema
	if f, ok := fs.SystemFields[column]; ok {
		return f
	}
	expr := r.table + "." + column
	for _, f := range fs.Fields {
		if f.Relation == nil && f.Path == nil && f.SQLExpr == expr {
			return f
		}
	}
	info := core.TypeOf(r.tableColumns[column])
	return &filter.FieldSchema{ExternalName: column, SQLExpr: expr, DataType: info.Type, NumberKind: info.NumberKind}
}

// MustRelate is Relate for startup declarations; it panics on a
// declaration error.
func (r *Resource[T]) MustRelate(name string, rel Relation) {
	if err := r.Relate(name, rel); err != nil {
		panic(err)
	}
}

// relationSchema checks rel's columns against r and its target and
// describes the correlation.
func (r *Resource[T]) relationSchema(name string, rel Relation) (*core.RelationSchema, error) {
	t := rel.target
	fail := func(format string, args ...any) error {
		return fmt.Errorf("parse: %s: relation %q: %s", r.table, name, fmt.Sprintf(format, args...))
	}
	ownerHas := func(col string) bool { _, ok := r.tableColumns[col]; return ok && core.IsIdentifier(col) }
	targetHas := func(col string) bool { _, ok := t.columnType(col); return ok && core.IsIdentifier(col) }
	targetTenant := t.TenantColumnName()
	if rel.policy {
		targetTenant = "" // the target's policy decides its rows
	}

	switch rel.kind {
	case core.HasMany:
		local := cmp.Or(rel.references, r.pk)
		if !ownerHas(local) {
			return nil, fail("references %q, which is not a column of %s", local, r.table)
		}
		if !targetHas(rel.foreignKey) {
			return nil, fail("foreign key %q is not a column of %s", rel.foreignKey, t.Table())
		}
		return &core.RelationSchema{
			Kind: core.HasMany, JoinTable: t.Table(), SourceColumn: rel.foreignKey, TargetColumn: t.PrimaryKeyColumn(),
			PrimaryKey: local, TenantColumn: targetTenant, DeletedColumn: t.DeletedColumnName(), Policy: rel.policy,
		}, nil
	case core.BelongsTo:
		remote := cmp.Or(rel.references, t.PrimaryKeyColumn())
		if !ownerHas(rel.foreignKey) {
			return nil, fail("foreign key %q is not a column of %s", rel.foreignKey, r.table)
		}
		if !targetHas(remote) {
			return nil, fail("references %q, which is not a column of %s", remote, t.Table())
		}
		return &core.RelationSchema{
			Kind: core.BelongsTo, JoinTable: t.Table(), SourceColumn: remote, TargetColumn: t.PrimaryKeyColumn(),
			PrimaryKey: rel.foreignKey, TenantColumn: targetTenant, DeletedColumn: t.DeletedColumnName(), Policy: rel.policy,
		}, nil
	default:
		j := rel.through
		if rel.references != "" {
			return nil, fail("a many-to-many references both keys; References does not apply")
		}
		for _, c := range []string{j.Table, j.Source, j.Target} {
			if !core.IsIdentifier(c) {
				return nil, fail("pair table %q, %q, %q must be identifiers", j.Table, j.Source, j.Target)
			}
		}
		if j.Tenant != "" && !core.IsIdentifier(j.Tenant) {
			return nil, fail("pair tenant column %q is not an identifier", j.Tenant)
		}
		return &core.RelationSchema{
			Kind: core.ManyToMany, JoinTable: j.Table, SourceColumn: j.Source, TargetColumn: j.Target,
			PrimaryKey: r.pk, TenantColumn: j.Tenant,
			TargetTable: t.Table(), TargetKey: t.PrimaryKeyColumn(), TargetTenant: targetTenant, TargetDeleted: t.DeletedColumnName(),
			Policy: rel.policy,
		}, nil
	}
}
