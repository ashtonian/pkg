package core

import (
	"fmt"
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

// RelationSchema describes how a field's owner rows relate to other rows
// . Every predicate correlates JoinTable's rows to the
// owner by `JoinTable.SourceColumn = <owner>.PrimaryKey` and compiles to an
// EXISTS / NOT EXISTS. For a many-to-many JoinTable is the pair
// table and TargetColumn its target key; for a has-many or belongs-to
// JoinTable is the target table itself: a has-many correlates the target's
// foreign key to the owner's key, a belongs-to the target's key to the
// owner's foreign key (PrimaryKey).
//
// The struct lives in parse/core (rather than parse/filter) because both
// parse/filter and parse/patch need to consult the same descriptor when
// emitting SQL.
type RelationSchema struct {
	// Kind is the relation's cardinality.
	Kind RelationKind

	// JoinTable is the name of the junction table (e.g. "widget_tags").
	JoinTable string

	// SourceColumn is the column on JoinTable that holds the owner's PK
	// (e.g. "widget_id").
	SourceColumn string

	// TargetColumn is the column on JoinTable that holds the related
	// entity's PK (e.g. "tag_id").
	TargetColumn string

	// PrimaryKey is the owner entity's PK column (defaults to "id" when
	// empty).
	PrimaryKey string

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
	// through its pair table (on TargetTable.TargetKey = JoinTable.TargetColumn)
	// so predicates reach the target's columns; TargetTenant and
	// TargetDeleted scope its rows as TenantColumn and DeletedColumn scope
	// JoinTable's. Empty for a pair table compared by key alone.
	TargetTable   string
	TargetKey     string
	TargetTenant  string
	TargetDeleted string

	// Policy scopes the related target rows by the server's policy for the
	// target table instead of the tenant column.
	Policy bool
}

// RelatedTable is the table whose rows a relation's nested predicates test:
// TargetTable through a pair table, else JoinTable itself.
func (r *RelationSchema) RelatedTable() string {
	if r.TargetTable != "" {
		return r.TargetTable
	}
	return r.JoinTable
}

// ParseRelationTag parses a `relation:"…"` struct-tag value into a
// RelationSchema. The tag format is positional with optional trailing
// fields:
//
//	relation:"<junction>,<src>,<tgt>[,<pk>[,<tenant_col>]]"
//
// Examples:
//
//	relation:"widget_tags,widget_id,tag_id"              // 3-arg
//	relation:"widget_tags,widget_id,tag_id,id"           // 4-arg, explicit pk
//	relation:"widget_tags,widget_id,tag_id,id,tenant_id" // 5-arg, tenant-scoped
//	relation:"system_tags,id,tag_id,id,-"                // 5-arg, explicit no-tenant
//
// Every part must be a plain SQL identifier: the values are emitted into
// SQL verbatim, so a malformed tag fails at schema generation, not in a
// query.
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
	rel := &RelationSchema{
		JoinTable:    parts[0],
		SourceColumn: parts[1],
		TargetColumn: parts[2],
		PrimaryKey:   "id",
	}
	if len(parts) > 3 && parts[3] != "" {
		rel.PrimaryKey = parts[3]
	}
	if len(parts) > 4 && parts[4] != "-" {
		rel.TenantColumn = parts[4]
	}
	for name, ident := range map[string]string{
		"junction": rel.JoinTable, "source column": rel.SourceColumn,
		"target column": rel.TargetColumn, "primary key": rel.PrimaryKey,
	} {
		if !IsIdentifier(ident) {
			return nil, fmt.Errorf("relation tag %s %q is not a SQL identifier", name, ident)
		}
	}
	if rel.TenantColumn != "" && !IsIdentifier(rel.TenantColumn) {
		return nil, fmt.Errorf("relation tag tenant column %q is not a SQL identifier", rel.TenantColumn)
	}
	return rel, nil
}

// IsIdentifier reports whether s is a plain SQL identifier
// ([A-Za-z_][A-Za-z0-9_]*), safe to emit into SQL unquoted.
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
