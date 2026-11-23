package core

import (
	"fmt"
	"slices"
	"strings"
)

// RelationKind is a relation's cardinality.
type RelationKind string

const (
	// ManyToMany relates rows through a pair table; the zero kind.
	ManyToMany RelationKind = ""
	// HasMany relates an owner to the target rows whose foreign key holds
	// the owner's key.
	HasMany RelationKind = "has_many"
	// BelongsTo relates an owner to the one target row its foreign key
	// names.
	BelongsTo RelationKind = "belongs_to"
)

// RelationSchema describes how a field's owner rows relate to other rows.
// Every predicate correlates JoinTable's rows to the owner by the AND of
// `JoinTable.SourceColumns[i] = <owner>.OwnerKey[i]` and compiles to an
// EXISTS / NOT EXISTS. For a many-to-many JoinTable is the pair table and
// TargetColumns its target key columns; for a has-many or belongs-to
// JoinTable is the target table itself: a has-many correlates the target's
// foreign key to the owner's key, a belongs-to the target's key to the
// owner's foreign key (OwnerKey). Column lists pair by position, so a key
// of several columns correlates as the AND of its columns' equalities.
//
// The struct lives in parse/core (rather than parse/filter) because both
// parse/filter and parse/patch need to consult the same descriptor when
// emitting SQL.
type RelationSchema struct {
	// Kind is the relation's cardinality.
	Kind RelationKind

	// JoinTable is the name of the junction table (e.g. "widget_tags").
	JoinTable string

	// SourceColumns are the columns on JoinTable that hold the owner's
	// key (e.g. "widget_id"), paired with OwnerKey.
	SourceColumns []string

	// TargetColumns are the columns on JoinTable that hold the related
	// row's key (e.g. "tag_id"), paired with TargetKeys through a target
	// table.
	TargetColumns []string

	// OwnerKey are the owner's columns SourceColumns hold; empty is
	// DefaultPrimaryKey alone (OwnerColumns).
	OwnerKey []string

	// TenantColumn names a tenant-scope column on the junction (typically
	// "tenant_id"). When non-empty, every junction statement parse emits —
	// filter, facet, include and relation patch — binds it to the caller's
	// tenant, and a builder without a bound tenant fails closed unless the
	// caller opts into unscoped relations explicitly. Empty
	// means the junction is not tenant-scoped (a globally shared table).
	TenantColumn string

	// DeletedColumn is JoinTable's soft-delete column: a row with it set
	// is not related.
	DeletedColumn string

	// TargetTable, when set, is the target table a many-to-many joins
	// through its pair table (on the AND of TargetTable.TargetKeys[i] =
	// JoinTable.TargetColumns[i]) so predicates reach the target's columns;
	// TargetTenant and TargetDeleted scope its rows as TenantColumn and
	// DeletedColumn scope JoinTable's. Empty for a pair table compared by
	// key alone.
	TargetTable   string
	TargetKeys    []string
	TargetTenant  string
	TargetDeleted string

	// Policy scopes the related target rows by the server's policy for the
	// target table instead of the tenant column.
	Policy bool
}

// DefaultPrimaryKey is the key column of a resource, or a relation's owner,
// that declares none.
const DefaultPrimaryKey = "id"

// OwnerColumns are OwnerKey, or DefaultPrimaryKey alone when it is empty.
func (r *RelationSchema) OwnerColumns() []string {
	if len(r.OwnerKey) == 0 {
		return []string{DefaultPrimaryKey}
	}
	return r.OwnerKey
}

// Clone is a copy of r sharing none of its column lists; nil for nil.
func (r *RelationSchema) Clone() *RelationSchema {
	if r == nil {
		return nil
	}
	out := *r
	out.SourceColumns = slices.Clone(r.SourceColumns)
	out.TargetColumns = slices.Clone(r.TargetColumns)
	out.OwnerKey = slices.Clone(r.OwnerKey)
	out.TargetKeys = slices.Clone(r.TargetKeys)
	return &out
}

// RelatedTable is the table whose rows a relation's nested predicates test:
// TargetTable through a pair table, else JoinTable itself.
func (r *RelationSchema) RelatedTable() string {
	if r.TargetTable != "" {
		return r.TargetTable
	}
	return r.JoinTable
}

// Check refuses a relation whose column lists cannot pair: no source or
// target column, a source list of another length than the owner key, a
// target key of another length than the target columns, a list naming a
// column twice, or a name that is no SQL identifier. Every statement
// builder checks a relation before writing it, since the names reach SQL
// verbatim.
func (r *RelationSchema) Check() error {
	if !IsIdentifier(r.JoinTable) {
		return fmt.Errorf("relation table %q is not a SQL identifier", r.JoinTable)
	}
	lists := []struct {
		name    string
		columns []string
	}{{"source", r.SourceColumns}, {"target", r.TargetColumns}, {"owner key", r.OwnerColumns()}, {"target key", r.TargetKeys}}
	for _, l := range lists {
		for i, c := range l.columns {
			if !IsIdentifier(c) {
				return fmt.Errorf("relation %s: %s column %q is not a SQL identifier", r.JoinTable, l.name, c)
			}
			if slices.Contains(l.columns[:i], c) {
				return fmt.Errorf("relation %s: %s columns name %q twice", r.JoinTable, l.name, c)
			}
		}
	}
	switch {
	case len(r.SourceColumns) == 0 || len(r.TargetColumns) == 0:
		return fmt.Errorf("relation %s names no source or no target column", r.JoinTable)
	case len(r.SourceColumns) != len(r.OwnerColumns()):
		return fmt.Errorf("relation %s: source columns (%s) do not pair with the owner key (%s)",
			r.JoinTable, strings.Join(r.SourceColumns, ", "), strings.Join(r.OwnerColumns(), ", "))
	case r.TargetTable != "" && len(r.TargetKeys) != len(r.TargetColumns):
		return fmt.Errorf("relation %s: target columns (%s) do not pair with %s's key (%s)",
			r.JoinTable, strings.Join(r.TargetColumns, ", "), r.TargetTable, strings.Join(r.TargetKeys, ", "))
	}
	return nil
}

// ParseRelationTag parses a `relation:"…"` struct-tag value into a
// RelationSchema. The tag format is positional with optional trailing
// fields:
//
//	relation:"<junction>,<src>,<tgt>[,<pk>[,<tenant_col>]]"
//
// A key of several columns joins them with `+` within its part, in key
// order: <src> and <pk> pair by position.
//
// Examples:
//
//	relation:"widget_tags,widget_id,tag_id"                     // 3-arg
//	relation:"widget_tags,widget_id,tag_id,id"                  // 4-arg, explicit pk
//	relation:"widget_tags,widget_id,tag_id,id,tenant_id"        // 5-arg, tenant-scoped
//	relation:"system_tags,id,tag_id,id,-"                       // 5-arg, explicit no-tenant
//	relation:"stock_tags,warehouse_id+sku,tag_id,warehouse_id+sku" // composite owner key
//
// The tagged field is a slice of target keys, so <tgt> names one column.
// Every part must be a plain SQL identifier, or identifiers joined by `+`:
// the values are emitted into SQL verbatim, so a malformed tag fails at
// schema generation, not in a query.
func ParseRelationTag(tag string) (*RelationSchema, error) {
	parts := strings.Split(tag, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) < 3 {
		return nil, fmt.Errorf("relation tag requires at least 3 parts: join_table,source_column,target_column")
	}
	if len(parts) > 5 {
		return nil, fmt.Errorf("relation tag has %d parts; expected at most 5 (<junction>,<src>,<tgt>[,<pk>[,<tenant_col>]])", len(parts))
	}
	columns := func(part string) []string {
		names := strings.Split(part, "+")
		for i := range names {
			names[i] = strings.TrimSpace(names[i])
		}
		return names
	}
	rel := &RelationSchema{
		JoinTable:     parts[0],
		SourceColumns: columns(parts[1]),
		TargetColumns: columns(parts[2]),
		OwnerKey:      []string{DefaultPrimaryKey},
	}
	if len(parts) > 3 && parts[3] != "" {
		rel.OwnerKey = columns(parts[3])
	}
	if len(parts) > 4 && parts[4] != "-" {
		rel.TenantColumn = parts[4]
	}
	// In the tag's own order, so a tag with several bad parts always reports
	// the first.
	if !IsIdentifier(rel.JoinTable) {
		return nil, fmt.Errorf("relation tag junction %q is not a SQL identifier", rel.JoinTable)
	}
	for _, part := range []struct {
		name    string
		columns []string
	}{{"source column", rel.SourceColumns}, {"target column", rel.TargetColumns}, {"primary key", rel.OwnerKey}} {
		for _, c := range part.columns {
			if !IsIdentifier(c) {
				return nil, fmt.Errorf("relation tag %s %q is not a SQL identifier", part.name, c)
			}
		}
	}
	if rel.TenantColumn != "" && !IsIdentifier(rel.TenantColumn) {
		return nil, fmt.Errorf("relation tag tenant column %q is not a SQL identifier", rel.TenantColumn)
	}
	if len(rel.SourceColumns) != len(rel.OwnerKey) {
		return nil, fmt.Errorf("relation tag source columns %s do not pair with primary key %s: %d and %d columns",
			parts[1], strings.Join(rel.OwnerKey, "+"), len(rel.SourceColumns), len(rel.OwnerKey))
	}
	if len(rel.TargetColumns) > 1 {
		return nil, fmt.Errorf("relation tag target column %s names several columns; a tagged relation's field holds one-column target keys", parts[2])
	}
	if err := rel.Check(); err != nil {
		return nil, fmt.Errorf("relation tag: %w", err)
	}
	return rel, nil
}

// IsIdentifier reports whether s is a plain SQL identifier
// ([A-Za-z_][A-Za-z0-9_]*): the names parse accepts for tables and columns.
// It does not say a name is no reserved word; every statement quotes the
// names it writes (Dialect.QuoteIdent), so a reserved or mixed-case name
// reads the column spelt exactly so.
func IsIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
