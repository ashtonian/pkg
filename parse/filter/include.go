package filter

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// DefaultMaxIncludePerRelation caps the target rows an include query
// returns per owner, so an entity with thousands of related rows cannot
// turn one page into an unbounded read. Per-call override via
// [WithMaxIncludePerRelation]. .
const DefaultMaxIncludePerRelation = 100

// MaxIncludes is how many relations one list may include.
const MaxIncludes = 3

// IncludeQuery is a single batched read of related IDs: one SQL statement
// that returns (owner_pk, target_id) rows for every owner in ownerPKs, at
// most Limit+1 per owner so Scan can tell a cut list from a complete one.
type IncludeQuery struct {
	// Field is the include-path name on the wire (e.g. "tags").
	Field string
	// Schema points to the relation descriptor used to render SQL.
	Schema *core.RelationSchema
	// SQL is the parameterized SELECT.
	SQL string
	// Args are the bound values: owner PKs first, then the scope's.
	Args []any
	// Limit is the per-owner cap: an owner with more related rows gets the
	// first Limit and is reported truncated.
	Limit int
}

// IncludeRows is an include's result: the related IDs grouped by owner key,
// and the owners whose IDs were cut at the limit.
type IncludeRows struct {
	IDs       map[string][]string
	Truncated []string
}

// SetInclude replaces a field's related IDs and truncated owners. Empty
// projections are omitted. The page retains the supplied maps and slices.
func (p *Page[T]) SetInclude(field string, rows IncludeRows) {
	if len(rows.IDs) > 0 {
		if p.Relations == nil {
			p.Relations = map[string]map[string][]string{}
		}
		p.Relations[field] = rows.IDs
	} else {
		delete(p.Relations, field)
	}
	if len(rows.Truncated) > 0 {
		if p.Truncated == nil {
			p.Truncated = map[string][]string{}
		}
		p.Truncated[field] = rows.Truncated
	} else {
		delete(p.Truncated, field)
	}
}

// IncludeQueryOption configures BuildIncludeQuery and ResolveInclude.
type IncludeQueryOption func(*includeQueryConfig)

type includeQueryConfig struct {
	scope       relationScope
	related     map[string]*PolicyConstraints
	maxPerOwner int
}

// WithIncludeTenant binds the tenant a tenant-scoped relation's junction
// read is restricted to. Required when the relation declares a
// TenantColumn; without it (or WithIncludeUnscoped) the build fails
// .
func WithIncludeTenant(value any) IncludeQueryOption {
	return func(c *includeQueryConfig) {
		c.scope.tenant = value
		c.scope.hasTenant = value != nil
	}
}

// WithIncludeUnscoped deliberately reads a tenant-scoped junction without a
// tenant predicate. Never use it on a request path.
func WithIncludeUnscoped() IncludeQueryOption {
	return func(c *includeQueryConfig) { c.scope.unscoped = true }
}

// WithIncludeRelatedPolicies supplies the policies of policy-scoped
// relations' target tables (WithRelatedPolicies).
func WithIncludeRelatedPolicies(policies map[string]*PolicyConstraints) IncludeQueryOption {
	return func(c *includeQueryConfig) { c.related = policies }
}

// WithMaxIncludePerRelation overrides DefaultMaxIncludePerRelation for
// this call; n <= 0 keeps the default. The cap must leave room for one
// extra row that detects truncation.
func WithMaxIncludePerRelation(n int) IncludeQueryOption {
	return func(c *includeQueryConfig) { c.maxPerOwner = n }
}

// BuildIncludeQuery emits the batched read of one relation field's related
// IDs across the supplied owner PK set, the lowest target IDs first, at
// most the per-owner cap plus one (IncludeQuery.Scan cuts and reports it).
// Text keys use code-point order; numeric and UUID keys use native order.
// Both IDs come back as text in the database's canonical spelling, the
// form items carry, whatever the column type and driver (a native UUID
// scanned into any is a byte array):
//
//	SELECT CAST(owner_pk AS TEXT), CAST(target_id AS TEXT) FROM (
//	  SELECT owner_pk, target_id,
//	         ROW_NUMBER() OVER (PARTITION BY owner_pk ORDER BY target_id) AS include_rank
//	  FROM (SELECT DISTINCT j.<source> AS owner_pk, j.<target> AS target_id FROM <junction> j
//	        [JOIN <target> t ON t.<key> = j.<target>]
//	        WHERE j.<source> IN ($1, $2, ...) [AND <scope>]) pairs__) include__
//	WHERE include_rank <= <cap+1> ORDER BY include__.owner_pk, include__.target_id
//
// Pairs are distinct before they are ranked: a junction holding two rows
// for one pair (two typed edges between the same rows) names the target
// once and spends one place of the cap. The cap applies in the database:
// truncating after the scan would still transfer every junction row. A
// declared relation reads only visible related rows: a
// has-many's live children in the tenant, a many-to-many's pairs whose
// target is live and in the tenant — or, for a policy-scoped relation,
// admitted by its target policy (ErrRelatedPolicyRequired without it).
func BuildIncludeQuery(field *FieldSchema, dialect Dialect, ownerPKs []any, opts ...IncludeQueryOption) (IncludeQuery, error) {
	cfg, err := includeConfig(field, opts...)
	if err != nil {
		return IncludeQuery{}, fmt.Errorf("BuildIncludeQuery: %w", err)
	}
	rel := field.Relation
	if len(ownerPKs) == 0 {
		return IncludeQuery{Field: field.ExternalName, Schema: rel, Limit: cfg.maxPerOwner}, nil
	}

	s := &buildState{args: make([]any, 0, len(ownerPKs)+2)}
	bind := s.binder(dialect)
	target := "target__"
	targetKey := "j." + rel.TargetColumn
	if field.DataType == core.FieldTypeString {
		// The projection's collation governs DISTINCT, ranking and output
		// order, including keys stored under a different column collation.
		targetKey = dialect.CodePointOrder(targetKey)
	}

	var b strings.Builder
	b.WriteString("SELECT CAST(include__.owner_pk AS TEXT), CAST(include__.target_id AS TEXT) FROM (")
	b.WriteString("SELECT owner_pk, target_id, ROW_NUMBER() OVER (PARTITION BY owner_pk ORDER BY target_id) AS include_rank FROM (")
	b.WriteString("SELECT DISTINCT j." + rel.SourceColumn + " AS owner_pk, " + targetKey + " AS target_id FROM " + rel.JoinTable + " j")
	b.WriteString(relatedJoin(rel, "j", target))
	b.WriteString(" WHERE j." + rel.SourceColumn + " IN (")
	for i, pk := range ownerPKs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(bind(pk))
	}
	b.WriteByte(')')
	scope, err := relatedScope(field, dialect, cfg.scope, cfg.related, s, bind, "j", target)
	if err != nil {
		return IncludeQuery{}, fmt.Errorf("BuildIncludeQuery: field %q: %w", field.ExternalName, err)
	}
	b.WriteString(scope)
	b.WriteString(") pairs__) include__ WHERE include_rank <= ")
	b.WriteString(strconv.Itoa(cfg.maxPerOwner + 1))
	b.WriteString(" ORDER BY include__.owner_pk, include__.target_id")

	return IncludeQuery{
		Field:  field.ExternalName,
		Schema: rel,
		SQL:    b.String(),
		Args:   s.args,
		Limit:  cfg.maxPerOwner,
	}, nil
}

func includeConfig(field *FieldSchema, opts ...IncludeQueryOption) (*includeQueryConfig, error) {
	if field == nil {
		return nil, fmt.Errorf("field is required")
	}
	if field.Relation == nil {
		return nil, fmt.Errorf("field %q has no relation; only relation fields are includable", field.ExternalName)
	}
	if !field.Includable {
		return nil, fmt.Errorf("field %q is not opted into include via parse:\"include\"", field.ExternalName)
	}
	if field.Relation.Policy && field.Related == nil {
		return nil, fmt.Errorf("policy-scoped relation has no target schema")
	}
	cfg := &includeQueryConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.maxPerOwner <= 0 {
		cfg.maxPerOwner = DefaultMaxIncludePerRelation
	}
	if cfg.maxPerOwner == math.MaxInt {
		return nil, fmt.Errorf("include cap %d leaves no room for the truncation sentinel", cfg.maxPerOwner)
	}
	return cfg, nil
}

// relatedScope is the predicate holding a relation's rows to the visible
// ones, appended to a WHERE over its join table (alias join) and, for a
// many-to-many with a target, the joined target (alias target): the join
// table's tenant (bindTenant), and for a declared relation its live,
// tenant- or policy-scoped related rows — the join table itself for a
// has-many, the target for a many-to-many. Policy values bind through s.
func relatedScope(field *FieldSchema, dialect Dialect, scope relationScope, related map[string]*PolicyConstraints, s *buildState, bindTenant func(any) string, join, target string) (string, error) {
	rel := field.Relation
	var b strings.Builder
	clause, err := scope.clause(rel, join, bindTenant)
	if err != nil {
		return "", err
	}
	b.WriteString(clause)
	if rel.DeletedColumn != "" {
		b.WriteString(" AND " + join + "." + rel.DeletedColumn + " IS NULL")
	}
	visible := join
	if rel.TargetTable != "" {
		visible = target
		tc, scopeErr := targetScope(field, dialect, scope, bindTenant, target)
		if scopeErr != nil {
			return "", scopeErr
		}
		b.WriteString(tc)
	}
	pc, err := policyScope(field, dialect, scope, related, s, visible, join)
	if err != nil {
		return "", err
	}
	b.WriteString(pc)
	return b.String(), nil
}

// targetScope is a many-to-many target's tenant and live-row predicate on
// alias.
func targetScope(field *FieldSchema, dialect Dialect, scope relationScope, bindTenant func(any) string, alias string) (string, error) {
	rel := field.Relation
	var b strings.Builder
	if rel.TargetTenant != "" {
		tc, err := scope.clause(&core.RelationSchema{JoinTable: rel.TargetTable, TenantColumn: rel.TargetTenant}, alias, bindTenant)
		if err != nil {
			return "", err
		}
		b.WriteString(tc)
	}
	if rel.TargetDeleted != "" {
		b.WriteString(" AND " + alias + "." + rel.TargetDeleted + " IS NULL")
	}
	return b.String(), nil
}

// policyScope is a policy-scoped relation's target policy on alias, ""
// for any other relation.
func policyScope(field *FieldSchema, dialect Dialect, scope relationScope, related map[string]*PolicyConstraints, s *buildState, alias string, others ...string) (string, error) {
	rel := field.Relation
	if !rel.Policy {
		return "", nil
	}
	if field.Related == nil {
		return "", fmt.Errorf("policy-scoped relation has no target schema")
	}
	pc, ok := related[rel.RelatedTable()]
	if !ok {
		return "", fmt.Errorf("relation on %s: %w", rel.RelatedTable(), ErrRelatedPolicyRequired)
	}
	ast, err := ApplyPolicyConstraints(field.Related, nil, pc)
	if err != nil || ast == nil || ast.Root == nil {
		return "", err
	}
	root, err := rebaseNode(ast.Root, field.Related.Name, alias, map[*FieldSchema]*FieldSchema{})
	if err != nil {
		return "", err
	}
	pb := &SQLBuilder{schema: &ResourceSchema{Name: alias}, dialect: dialect, scope: scope, related: related, depth: 1, visible: append(others, alias)}
	cond, err := pb.buildNode(s, root)
	if err != nil {
		return "", err
	}
	return " AND " + cond, nil
}

// VisibleRelated is the predicate holding a declared many-to-many's target
// rows, under alias, to those a request may relate: in the tenant (or
// admitted by the target's policy) and live. It begins with " AND ", is ""
// when nothing limits the targets, and binds every value with bind — for a
// statement another package numbers, such as a membership patch's guarded
// insert.
func VisibleRelated(field *FieldSchema, dialect Dialect, alias string, tenant any, related map[string]*PolicyConstraints, bind func(any) string) (string, error) {
	if field == nil || field.Relation == nil || field.Relation.TargetTable == "" {
		return "", fmt.Errorf("VisibleRelated: field is not a many-to-many with a target")
	}
	scope := relationScope{tenant: tenant, hasTenant: tenant != nil}
	tc, err := targetScope(field, dialect, scope, bind, alias)
	if err != nil {
		return "", err
	}
	pc, err := policyScope(field, dialect, scope, related, &buildState{bind: bind}, alias)
	if err != nil {
		return "", err
	}
	return tc + pc, nil
}

// relatedJoin is the JOIN of a many-to-many's target onto its pair table
// (alias join), as target; empty for every other relation.
func relatedJoin(rel *core.RelationSchema, join, target string) string {
	if rel.TargetTable == "" {
		return ""
	}
	return " JOIN " + rel.TargetTable + " " + target + " ON " + target + "." + rel.TargetKey + " = " + join + "." + rel.TargetColumn
}

// Scan reads the (owner_pk, target_id) rows of q, grouped by owner key as
// text: an owner with more than q.Limit IDs keeps the first Limit and is
// listed in Truncated, in key order. Caller owns the iterator's Close.
func (q IncludeQuery) Scan(rows Rows) (IncludeRows, error) {
	out := IncludeRows{IDs: make(map[string][]string)}
	for rows.Next() {
		var ownerPK, targetID any
		if err := rows.Scan(&ownerPK, &targetID); err != nil {
			return IncludeRows{}, fmt.Errorf("scanning include row: %w", err)
		}
		owner := stringifyKey(ownerPK)
		if q.Limit > 0 && len(out.IDs[owner]) == q.Limit {
			if !slices.Contains(out.Truncated, owner) {
				out.Truncated = append(out.Truncated, owner)
			}
			continue
		}
		out.IDs[owner] = append(out.IDs[owner], stringifyKey(targetID))
	}
	if err := rows.Err(); err != nil {
		return IncludeRows{}, err
	}
	slices.Sort(out.Truncated)
	return out, nil
}

func stringifyKey(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ParseIncludes validates a list request's `include` parameter
// (ParamInclude): comma-separated relation fields to project as
// `_relations`, at most MaxIncludes. Each must be an includable relation
// field; duplicates collapse and order is kept.
func ParseIncludes(schema *ResourceSchema, param string) ([]string, error) {
	return ParseIncludeNames(schema, strings.Split(param, ","))
}

// ParseIncludeNames is ParseIncludes over names already split: each is
// trimmed, empty and repeated names are dropped, and the rest must be
// includable relation fields, at most MaxIncludes of them.
func ParseIncludeNames(schema *ResourceSchema, names []string) ([]string, error) {
	var fields []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || slices.Contains(fields, name) {
			continue
		}
		f, ok := schema.Fields[name]
		if !ok {
			return nil, &core.ParseError{
				Kind: core.ParseErrorKindFilter, Code: ECodeUnknownField.String(),
				Message: fmt.Sprintf("include field '%s' does not exist", name), Details: map[string]any{"field": name},
			}
		}
		if f.Relation == nil || !f.Includable {
			return nil, &core.ParseError{
				Kind: core.ParseErrorKindFilter, Code: ECodeInvalidRelation.String(),
				Message: fmt.Sprintf("field '%s' is not an includable relation", name), Details: map[string]any{"field": name},
			}
		}
		fields = append(fields, name)
	}
	if len(fields) > MaxIncludes {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindFilter, Code: ECodeInvalidRelation.String(),
			Message: fmt.Sprintf("a list includes at most %d relations, got %d", MaxIncludes, len(fields)),
			Details: map[string]any{"parameter": ParamInclude},
		}
	}
	return fields, nil
}
