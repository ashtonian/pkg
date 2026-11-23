package parse

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// RelatedResource is a resource a relation leads to. Every *Resource[U] is
// one; the interface is sealed so a relation's target always owns its key,
// tenancy, soft deletion and filter schema.
type RelatedResource interface {
	Table() string
	PrimaryKeyColumns() []string
	TenantColumnName() string
	DeletedColumnName() string
	FilterSchema() *filter.ResourceSchema
	// liveFilterSchema is the target's own filter schema, not a copy: a
	// relation filters its target with the schema the target parses with,
	// relations the target declares later included.
	liveFilterSchema() *filter.ResourceSchema
	columnType(name string) (reflect.Type, bool)
	columnWire(name string) core.Wire
	computed(column string) bool
	isView() bool
	tenantGraph() *tenantNode
	admits(column string, need columnAccess) error
}

func (r *Resource[T]) liveFilterSchema() *filter.ResourceSchema { return r.filterSchema }

func (r *Resource[T]) isView() bool { return r.view }

func (r *Resource[T]) columnType(name string) (reflect.Type, bool) {
	t, ok := r.tableColumns[name]
	return t, ok
}

// columnWire is the wire form of the field holding column (core.Field.Wire).
func (r *Resource[T]) columnWire(name string) core.Wire {
	if f, ok := r.columnFields[name]; ok {
		return f.Wire()
	}
	return ""
}

// Relation is a declared relation: HasMany, BelongsTo or
// ManyToMany, then Resource.Relate.
type Relation struct {
	kind       core.RelationKind
	target     RelatedResource
	foreignKey []string
	references []string
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

// KeyColumns names the columns a relation correlates a key on: one
// column's name, or a key of several columns' names in key order.
type KeyColumns interface{ ~string | ~[]string }

// columnNames is columns as a list of names.
func columnNames[K KeyColumns](columns K) []string {
	v := reflect.ValueOf(columns)
	if v.Kind() == reflect.String {
		return []string{v.String()}
	}
	out := make([]string, v.Len())
	for i := range out {
		out[i] = v.Index(i).String()
	}
	return out
}

// Junction is a many-to-many's pair table: Source holds the owner's key,
// Target the target's, each a column per key column in key order, and
// Tenant (optional) the tenant of each pair.
type Junction struct {
	Table          string
	Source, Target []string
	Tenant         string
}

// Through names a many-to-many's pair table and its owner and target key
// columns: a column each for one-column keys, a list in key order for a
// key of several.
func Through[S, T KeyColumns](table string, source S, target T) Junction {
	return Junction{Table: table, Source: columnNames(source), Target: columnNames(target)}
}

// HasMany relates each owner to the target rows whose foreignKey holds the
// owner's key (devices have commands: commands.device_id); a key of
// several columns takes a foreign key of as many, paired in key order.
func HasMany[K KeyColumns](target RelatedResource, foreignKey K, opts ...RelationOption) Relation {
	return newRelation(core.HasMany, target, columnNames(foreignKey), opts)
}

// BelongsTo relates each owner to the target row its foreignKey columns
// name (a command belongs to a device: commands.device_id); a target key
// of several columns takes a foreign key of as many, paired in key order.
func BelongsTo[K KeyColumns](target RelatedResource, foreignKey K, opts ...RelationOption) Relation {
	return newRelation(core.BelongsTo, target, columnNames(foreignKey), opts)
}

// ManyToMany relates owners and targets through a pair table.
func ManyToMany(target RelatedResource, through Junction, opts ...RelationOption) Relation {
	r := newRelation(core.ManyToMany, target, nil, opts)
	r.through = through
	return r
}

func newRelation(kind core.RelationKind, target RelatedResource, fk []string, opts []RelationOption) Relation {
	r := Relation{kind: kind, target: target, foreignKey: fk}
	for _, opt := range opts {
		opt(&r)
	}
	return r
}

// References names the columns a foreign key holds when they are not the
// referenced resource's key: the owner's for a has-many, the target's for a
// belongs-to, paired with the foreign key's columns in order.
func References(columns ...string) RelationOption {
	return func(r *Relation) { r.references = slices.Clone(columns) }
}

// PolicyScoped holds the related rows to the server's policy for the target
// table (Scope.Related) instead of the request's tenant: a relation to rows
// another tenant shares, such as platform-wide device types.
func PolicyScoped() RelationOption { return func(r *Relation) { r.policy = true } }

// Indexed declares that an index leads with the correlation columns — the
// has-many's foreign key, or the pair table's owner columns — so the
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
// target's key; the target's computed columns (ReadExpr) compile there as
// the target's expressions for the owner's dialect. Columns are checked
// against both resources' types, and the tenant columns r and every
// resource relating to it then reach, at any depth, against each other:
// one request binds one tenant to all of them. Declare every relation
// before the resources serve requests; Relate changes the tenant key of
// every resource that reaches r, so it is not safe for concurrent use with
// any of them. A WithPaths view, a value for one request, neither declares
// a relation nor is one's target: relate the resources it is derived from.
func (r *Resource[T]) Relate(name string, rel Relation) error {
	if r.view {
		return fmt.Errorf("parse: %s: relation %q: a WithPaths view declares no relations; relate the resource it is derived from", r.table, name)
	}
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
	// Refused here rather than as ambiguous later: a relation under a name
	// policies already resolve would change what those policies constrain.
	if _, ok, _ := fs.PolicyField(name); ok {
		return fmt.Errorf("parse: %s: relation %q is a name server policies resolve (filter.ResourceSchema.PolicyField)", r.table, name)
	}
	if rel.target == nil {
		return fmt.Errorf("parse: %s: relation %q has no target", r.table, name)
	}
	if rel.target.isView() {
		return fmt.Errorf("parse: %s: relation %q: the target is a WithPaths view, a value for one request; relate the resource it is derived from", r.table, name)
	}
	rs, err := r.relationSchema(name, rel)
	if err != nil {
		return err
	}
	if err := r.checkRelationKeys(name, rs, rel.target); err != nil {
		return err
	}
	for _, c := range rs.OwnerColumns() {
		if r.computed(c) {
			return computedCorrelation(r.table, name, c)
		}
	}
	if rel.kind != core.ManyToMany {
		for _, c := range rs.SourceColumns {
			if rel.target.computed(c) {
				return fmt.Errorf("parse: %s: relation %q correlates on %s's computed column %q", r.table, name, rel.target.Table(), c)
			}
		}
	}
	if rel.includable && rel.kind == core.BelongsTo {
		return fmt.Errorf("parse: %s: relation %q: a belongs-to's key is the owner's foreign key; it is not included", r.table, name)
	}
	if rel.includable && !slices.Equal(rs.OwnerColumns(), r.pk) {
		return fmt.Errorf("parse: %s: relation %q: an include groups related keys by the owner's key (%s), and the relation correlates on %s",
			r.table, name, strings.Join(r.pk, ", "), strings.Join(rs.OwnerColumns(), ", "))
	}
	if rel.facetable && (rel.kind == core.BelongsTo || !slices.Equal(rs.OwnerColumns(), r.pk)) {
		return fmt.Errorf("parse: %s: relation %q: a facet counts owners per key of a relation on the owner's key; facet a belongs-to by its foreign-key column", r.table, name)
	}
	if rel.writable {
		if rel.kind != core.ManyToMany {
			return fmt.Errorf("parse: %s: relation %q: only a many-to-many's membership is written through its owner; a belongs-to writes its foreign-key column", r.table, name)
		}
		if rs.TenantColumn != "" && r.tenant == "" && rs.TargetTenant == "" {
			return fmt.Errorf("parse: %s: relation %q: the pair table's tenant cannot be taken from the owner or the target", r.table, name)
		}
		for _, schema := range []*patch.PatchSchema{r.patchSchema, r.serverPatchSchema} {
			if _, taken := schema.Path(core.FormatPointer(name)); taken {
				return fmt.Errorf("parse: %s: relation %q names an existing patch path", r.table, name)
			}
		}
	}
	// The relation's keys are the target's key, in its columns' types and
	// wire forms: the filter shorthands and the membership patch both take
	// them so.
	targetKey := rel.target.PrimaryKeyColumns()
	keys := make([]patch.RelationKey, len(targetKey))
	for i, c := range targetKey {
		typ, _ := rel.target.columnType(c)
		keys[i] = patch.RelationKey{Type: typ, Wire: rel.target.columnWire(c)}
	}
	var membership *patch.PathSchema
	if rel.writable {
		if membership, err = patch.RelationPath(rs, keys...); err != nil {
			return fmt.Errorf("parse: %s: relation %q: %w", r.table, name, err)
		}
	}
	if err := r.checkRelation(name, rs, rel.target, rel.includable || rel.facetable); err != nil {
		return err
	}
	t := rel.target
	// The last check: past it nothing fails, so the graph never holds a
	// relation the resource does not declare.
	if err := r.tenantNode.relate(t.tenantGraph(), rel.policy); err != nil {
		return fmt.Errorf("parse: %s: relation %q: %w", r.table, name, err)
	}
	field := &filter.FieldSchema{
		ExternalName:     name,
		AllowedOperators: relationOperators(rel.kind, len(keys)),
		Indexed:          rel.indexed || rel.kind == core.BelongsTo,
		Includable:       rel.includable,
		Facetable:        rel.facetable,
		Label:            rel.label,
		Description:      rel.description,
		Relation:         rs,
		Related:          t.liveFilterSchema(),
		RelatedBy:        r.correlationFields(rs.OwnerColumns()),
	}
	if len(keys) == 1 {
		key := core.TypeOf(keys[0].Type)
		field.DataType, field.NumberKind, field.Wire = key.Type, key.NumberKind, keys[0].Wire
	} else {
		// A key of several columns is no one value: the field compares
		// none, and includes and facets name each key by its text.
		field.DataType = core.FieldTypeArray
	}
	fs.Fields[name] = field
	if membership != nil {
		// The name is an identifier the schemas do not hold (checked above),
		// so adding its path cannot fail.
		for _, schema := range []*patch.PatchSchema{r.patchSchema, r.serverPatchSchema} {
			if err := schema.AddPath(core.FormatPointer(name), membership); err != nil {
				return fmt.Errorf("parse: %s: relation %q: %w", r.table, name, err)
			}
		}
	}
	return nil
}

// relationOperators are the operators of a relation field of kind whose
// target key has width columns: the key comparisons, which a key of several
// columns has none of, the NULL tests, and the relation predicates.
func relationOperators(kind core.RelationKind, width int) []filter.FilterOperator {
	var ops []filter.FilterOperator
	if width == 1 {
		ops = append(ops, filter.OpEq, filter.OpNe, filter.OpIn, filter.OpNin)
	}
	ops = append(ops, filter.OpNull, filter.OpNotNull)
	if kind == core.BelongsTo {
		return append(ops, filter.OpIs, filter.OpIsNot)
	}
	return append(ops, filter.OpSome, filter.OpNone)
}

// correlationFields are the owner's fields for columns, in order
// (correlationField).
func (r *Resource[T]) correlationFields(columns []string) []*filter.FieldSchema {
	out := make([]*filter.FieldSchema, len(columns))
	for i, c := range columns {
		out[i] = r.correlationField(c)
	}
	return out
}

// correlationField is the owner's field for column: its system field, a
// filter field over the column, or one typed from T.
func (r *Resource[T]) correlationField(column string) *filter.FieldSchema {
	fs := r.filterSchema
	if f, ok := fs.SystemFields[column]; ok {
		return f
	}
	for _, f := range fs.Fields {
		if f.Relation == nil && f.Path == nil && f.RowColumn() == column {
			return f
		}
	}
	info := core.TypeOf(r.tableColumns[column])
	return &filter.FieldSchema{ExternalName: column, Table: r.table, Column: column, DataType: info.Type, NumberKind: info.NumberKind}
}

// checkRelationKeys refuses a relation whose rows correlate on a column
// that is no key (core.IsKeyType): each owner correlation column, each
// target key column and, for a has-many or belongs-to, each target
// correlation column. SQL joins them column to column and the resolvers
// match them as identifiers, so a decimal "1.50" would not meet its 1.5.
// A has-many's or belongs-to's paired columns must also hold one kind of
// key, strings (and UUIDs, which a string may hold) or integers, or SQL
// could not compare them. target is nil for a pair table declared by tag,
// whose target keys the field's element type declares
// (filter.GenerateSchema refuses one that is no key).
func (r *Resource[T]) checkRelationKeys(name string, rs *core.RelationSchema, target RelatedResource) error {
	type column struct {
		table, name string
		typ         reflect.Type
		ok          bool
	}
	var columns []column
	for _, c := range rs.OwnerColumns() {
		typ, ok := r.tableColumns[c]
		columns = append(columns, column{r.table, c, typ, ok})
	}
	if target != nil {
		targetColumns := target.PrimaryKeyColumns()
		if rs.Kind != core.ManyToMany {
			targetColumns = append(targetColumns, rs.SourceColumns...)
		}
		for _, c := range targetColumns {
			typ, ok := target.columnType(c)
			columns = append(columns, column{target.Table(), c, typ, ok})
		}
	}
	for _, c := range columns {
		if !c.ok {
			return fmt.Errorf("parse: %s: relation %q: %q is not a column of %s", r.table, name, c.name, c.table)
		}
		if info := core.TypeOf(c.typ); !core.IsKeyType(info.Type) {
			return fmt.Errorf("parse: %s: relation %q: %s.%s is %s; a relation correlates on a string, UUID or integer key", r.table, name, c.table, c.name, info.Type)
		}
	}
	if target == nil || rs.Kind == core.ManyToMany {
		return nil
	}
	for i, owner := range rs.OwnerColumns() {
		source := rs.SourceColumns[i]
		ownerType, _ := r.tableColumns[owner]
		sourceType, _ := target.columnType(source)
		o, s := core.TypeOf(ownerType).Type, core.TypeOf(sourceType).Type
		if keyKind(o) != keyKind(s) {
			return fmt.Errorf("parse: %s: relation %q: %s.%s is %s and %s.%s, its pair, is %s; paired columns hold one kind of key",
				r.table, name, r.table, owner, o, target.Table(), source, s)
		}
	}
	return nil
}

// keyKind is how SQL compares a key of type t: UUIDs as the text a string
// key may hold, integers as numbers.
func keyKind(t core.FieldType) core.FieldType {
	if t == core.FieldTypeUUID {
		return core.FieldTypeString
	}
	return t
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
	targetKey := t.PrimaryKeyColumns()
	targetTenant := t.TenantColumnName()
	if rel.policy {
		targetTenant = "" // the target's policy decides its rows
	}

	switch rel.kind {
	case core.HasMany:
		local, held := rel.references, "references"
		if len(local) == 0 {
			local, held = slices.Clone(r.pk), r.table+"'s key"
		}
		if err := pairColumns("foreign key", rel.foreignKey, held, local); err != nil {
			return nil, fail("%v", err)
		}
		for _, c := range local {
			if !ownerHas(c) {
				return nil, fail("references %q, which is not a column of %s", c, r.table)
			}
		}
		for _, c := range rel.foreignKey {
			if !targetHas(c) {
				return nil, fail("foreign key %q is not a column of %s", c, t.Table())
			}
		}
		return &core.RelationSchema{
			Kind: core.HasMany, JoinTable: t.Table(), SourceColumns: slices.Clone(rel.foreignKey), TargetColumns: targetKey,
			OwnerKey: local, TenantColumn: targetTenant, DeletedColumn: t.DeletedColumnName(), Policy: rel.policy,
		}, nil
	case core.BelongsTo:
		remote, held := rel.references, "references"
		if len(remote) == 0 {
			remote, held = slices.Clone(targetKey), t.Table()+"'s key"
		}
		if err := pairColumns("foreign key", rel.foreignKey, held, remote); err != nil {
			return nil, fail("%v", err)
		}
		for _, c := range rel.foreignKey {
			if !ownerHas(c) {
				return nil, fail("foreign key %q is not a column of %s", c, r.table)
			}
		}
		for _, c := range remote {
			if !targetHas(c) {
				return nil, fail("references %q, which is not a column of %s", c, t.Table())
			}
		}
		return &core.RelationSchema{
			Kind: core.BelongsTo, JoinTable: t.Table(), SourceColumns: remote, TargetColumns: targetKey,
			OwnerKey: slices.Clone(rel.foreignKey), TenantColumn: targetTenant, DeletedColumn: t.DeletedColumnName(), Policy: rel.policy,
		}, nil
	default:
		j := rel.through
		if len(rel.references) > 0 {
			return nil, fail("a many-to-many references both keys; References does not apply")
		}
		for _, c := range slices.Concat([]string{j.Table}, j.Source, j.Target) {
			if !core.IsIdentifier(c) {
				return nil, fail("pair table %q, (%s), (%s) must be identifiers", j.Table, strings.Join(j.Source, ", "), strings.Join(j.Target, ", "))
			}
		}
		if j.Tenant != "" && !core.IsIdentifier(j.Tenant) {
			return nil, fail("pair tenant column %q is not an identifier", j.Tenant)
		}
		if err := pairColumns("pair table "+j.Table+"'s source", j.Source, r.table+"'s key", r.pk); err != nil {
			return nil, fail("%v", err)
		}
		if err := pairColumns("pair table "+j.Table+"'s target", j.Target, t.Table()+"'s key", targetKey); err != nil {
			return nil, fail("%v", err)
		}
		return &core.RelationSchema{
			Kind: core.ManyToMany, JoinTable: j.Table, SourceColumns: slices.Clone(j.Source), TargetColumns: slices.Clone(j.Target),
			OwnerKey: slices.Clone(r.pk), TenantColumn: j.Tenant,
			TargetTable: t.Table(), TargetKeys: targetKey, TargetTenant: targetTenant, TargetDeleted: t.DeletedColumnName(),
			Policy: rel.policy,
		}, nil
	}
}

// pairColumns refuses columns that cannot pair by position with the key
// columns they hold: none, a name given twice, or another count.
func pairColumns(what string, columns []string, key string, keyColumns []string) error {
	if len(columns) == 0 {
		return fmt.Errorf("%s names no column", what)
	}
	for i, c := range columns {
		if slices.Contains(columns[:i], c) {
			return fmt.Errorf("%s names %q twice", what, c)
		}
	}
	if len(columns) != len(keyColumns) {
		return fmt.Errorf("%s (%s) pairs by position with %s (%s), so it names as many columns",
			what, strings.Join(columns, ", "), key, strings.Join(keyColumns, ", "))
	}
	return nil
}
