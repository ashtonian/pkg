package filter

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
)

// DefaultMaxIncludePerRelation caps the target rows an include query
// returns per owner, so an entity with thousands of related rows cannot
// turn one page into an unbounded read. [WithMaxIncludePerRelation]
// overrides it per call.
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
// and the owners whose IDs were cut at the limit. Every key is its text
// (core.KeyText): a one-column key's value, a longer key the JSON array of
// its values' texts.
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
	scope   relationScope
	related relatedPolicies
	// owner is the owner's schema, whose table the read joins when the
	// relation follows the owner row's tenant (WithIncludeOwner).
	owner       *ResourceSchema
	maxPerOwner int
}

// WithIncludeRelationScope scopes the related rows the include reads
// (RelationScope). A RowTenant is a column of the owner's table, which the
// read joins on the relation's owner key: it needs WithIncludeOwner.
// ResolveInclude refuses a RowTenant: resolve each tenant's owners with
// its Tenant.
func WithIncludeRelationScope(s RelationScope) IncludeQueryOption {
	return func(c *includeQueryConfig) { c.scope, c.related = s.tenancy(), s.policies() }
}

// WithIncludeOwner names the owner's schema, whose table a RowTenant reads
// (WithIncludeRelationScope).
func WithIncludeOwner(owner *ResourceSchema) IncludeQueryOption {
	return func(c *includeQueryConfig) { c.owner = owner }
}

// WithMaxIncludePerRelation overrides DefaultMaxIncludePerRelation for
// this call; n <= 0 keeps the default. The cap must leave room for one
// extra row that detects truncation.
func WithMaxIncludePerRelation(n int) IncludeQueryOption {
	return func(c *includeQueryConfig) { c.maxPerOwner = n }
}

// BuildIncludeQuery emits the batched read of one relation field's related
// IDs across the supplied owner keys, the lowest target IDs first, at
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
// A key of several columns projects a column per key column (owner_pk,
// owner_pk_1, …; target_id, target_id_1, …), ranks and orders by all of
// them, binds the owner keys as one row-value set and comes back as its
// text (core.KeyText), so Scan reads every include alike.
//
// Pairs are distinct before they are ranked: a junction holding two rows
// for one pair (two typed edges between the same rows) names the target
// once and spends one place of the cap. The cap applies in the database:
// truncating after the scan would still transfer every junction row. A
// declared relation reads only visible related rows: a
// has-many's live children in the tenant, a many-to-many's pairs whose
// target is live and in the tenant — or, for a policy-scoped relation,
// admitted by its target policy (ErrRelatedPolicyRequired without it).
func BuildIncludeQuery(field *FieldSchema, dialect Dialect, ownerKeys []core.Key, opts ...IncludeQueryOption) (IncludeQuery, error) {
	cfg, err := includeConfig(field, opts...)
	if err != nil {
		return IncludeQuery{}, fmt.Errorf("BuildIncludeQuery: %w", err)
	}
	rel := field.Relation
	if len(ownerKeys) == 0 {
		return IncludeQuery{Field: field.ExternalName, Schema: rel, Limit: cfg.maxPerOwner}, nil
	}

	s := &buildState{args: make([]any, 0, len(ownerKeys)+2)}
	bind := s.binder(dialect)
	owners, targets := includeColumns("owner_pk", len(rel.SourceColumns)), includeColumns("target_id", len(rel.TargetColumns))
	selected := make([]string, 0, len(owners)+2*len(targets))
	for i, c := range rel.SourceColumns {
		selected = append(selected, includeKey(dialect, qualified(dialect, junctionAlias, c), rel.SourceTypes[i])+" AS "+owners[i])
	}
	// A target whose text does not order as its value orders by a column
	// of its own (KeyOrder), so the cap keeps the keys ResolveInclude keeps.
	orders := slices.Clone(targets)
	passed := slices.Clone(targets)
	for i, c := range rel.TargetColumns {
		column, t := qualified(dialect, junctionAlias, c), rel.TargetTypes[i]
		targetKey := includeKey(dialect, column, t)
		if t.Type == core.FieldTypeString {
			// The projection's collation governs DISTINCT, ranking and output
			// order, including keys stored under a different column collation.
			targetKey = dialect.CodePointOrder(targetKey)
		}
		selected = append(selected, targetKey+" AS "+targets[i])
		if !storedCanonical(t.Type) {
			orders[i] = targets[i] + "_o"
			passed = append(passed, orders[i])
			selected = append(selected, KeyOrder(dialect, column, t)+" AS "+orders[i])
		}
	}
	ownerList := strings.Join(owners, ", ")

	var b strings.Builder
	b.WriteString("SELECT " + includeText(dialect, owners) + ", " + includeText(dialect, targets) + " FROM (")
	b.WriteString("SELECT " + ownerList + ", " + strings.Join(passed, ", ") + ", ROW_NUMBER() OVER (PARTITION BY " + ownerList + " ORDER BY " + strings.Join(orders, ", ") + ") AS include_rank FROM (")
	b.WriteString("SELECT DISTINCT " + strings.Join(selected, ", ") + " FROM " + dialect.QuoteIdent(rel.JoinTable) + " " + junctionAlias)
	b.WriteString(relatedJoin(dialect, rel, junctionAlias, targetAlias))
	scope := cfg.scope
	if scope.row.column != "" {
		b.WriteString(" JOIN " + dialect.QuoteIdent(cfg.owner.Name) + " " + includeOwnerAlias + " ON " + correlation(dialect, includeOwnerAlias, rel.OwnerColumns(), junctionAlias, rel.SourceColumns, rel.SourceTypes))
		scope = scope.over(dialect, cfg.owner)
		scope.row.expr = qualified(dialect, includeOwnerAlias, scope.row.column)
	}
	in, err := includeOwners(field, dialect, ownerKeys, bind)
	if err != nil {
		return IncludeQuery{}, fmt.Errorf("BuildIncludeQuery: field %q: %w", field.ExternalName, err)
	}
	b.WriteString(" WHERE " + in)
	relScope, err := relatedScope(field, dialect, scope, cfg.related, s, bind, junctionAlias, targetAlias)
	if err != nil {
		return IncludeQuery{}, fmt.Errorf("BuildIncludeQuery: field %q: %w", field.ExternalName, err)
	}
	b.WriteString(relScope)
	b.WriteString(") pairs__) include__ WHERE include_rank <= ")
	b.WriteString(strconv.Itoa(cfg.maxPerOwner + 1))
	b.WriteString(" ORDER BY include__." + strings.Join(slices.Concat(owners, orders), ", include__."))

	return IncludeQuery{
		Field:  field.ExternalName,
		Schema: rel,
		SQL:    b.String(),
		Args:   s.args,
		Limit:  cfg.maxPerOwner,
	}, nil
}

// includeColumns are the aliases an include projects a key of n columns
// under: name, then name_1, name_2, ….
func includeColumns(name string, n int) []string {
	out := make([]string, n)
	out[0] = name
	for i := 1; i < n; i++ {
		out[i] = name + "_" + strconv.Itoa(i)
	}
	return out
}

// includeKey is a key column of the junction, read as column and typed t,
// as an include projects it: a string, UUID or integer column itself, any
// other type's its canonical text (keyValueText), equal exactly where the
// values are, so DISTINCT and the per-owner partition take equal values as
// one.
func includeKey(dialect Dialect, column string, t core.TypeInfo) string {
	if storedCanonical(t.Type) {
		return column
	}
	return keyValueText(dialect, column, t)
}

// includeText is the text an include names a key by, its columns being the
// include__ projection's columns: one column's own text, several columns'
// core.KeyText.
func includeText(dialect Dialect, columns []string) string {
	if len(columns) == 1 {
		return "CAST(include__." + columns[0] + " AS TEXT)"
	}
	exprs := make([]string, len(columns))
	for i, c := range columns {
		exprs[i] = "include__." + c
	}
	return dialect.keyText(exprs)
}

// includeOwners is the predicate holding the junction's rows to the owner
// keys, bound as one set typed as the relation's source columns, so the
// statement's text does not vary with the page: the source column `$in`
// the keys for a one-column key, the row value of the source columns in
// them for a longer one (Dialect.KeyIn).
func includeOwners(field *FieldSchema, dialect Dialect, ownerKeys []core.Key, bind func(any) string) (string, error) {
	rel := field.Relation
	if len(rel.SourceColumns) == 1 {
		values := make([]any, len(ownerKeys))
		for i, k := range ownerKeys {
			n, err := core.NormalizeKey(rel.SourceTypes, k)
			if err != nil {
				return "", fmt.Errorf("owner key %d: %w", i, err)
			}
			values[i] = n[0]
		}
		source := keyField(rel.SourceColumns[0], "", rel.SourceTypes[0])
		source.Table, source.Expr = junctionAlias, ""
		in, err := dialect.Condition(source, OpIn, values, bind)
		if err != nil {
			return "", fmt.Errorf("owner keys: %w", err)
		}
		return in, nil
	}
	in, err := dialect.KeyIn(rel.JoinTable, junctionAlias, rel.SourceColumns, rel.SourceTypes, ownerKeys, true, bind)
	if err != nil {
		return "", fmt.Errorf("owner keys: %w", err)
	}
	return in, nil
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
		return nil, unresolvableRelation(field, "is policy-scoped without a target schema to hold its policy")
	}
	err := field.Relation.Check()
	if err != nil {
		return nil, fmt.Errorf("field %q: %w", field.ExternalName, err)
	}
	if len(field.RelatedBy) != len(field.Relation.SourceColumns) {
		return nil, fmt.Errorf("field %q has %d owner correlation fields (FieldSchema.RelatedBy) to type the keys of %d columns", field.ExternalName, len(field.RelatedBy), len(field.Relation.SourceColumns))
	}
	cfg := &includeQueryConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	cfg.related = cfg.related.at(time.Now().UTC())
	if err := cfg.scope.check(); err != nil {
		return nil, err
	}
	if cfg.scope.row.column != "" && cfg.owner == nil {
		return nil, fmt.Errorf("a row tenant needs the owner's schema (WithIncludeOwner)")
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
func relatedScope(field *FieldSchema, dialect Dialect, scope relationScope, related relatedPolicies, s *buildState, bindTenant func(any) string, join, target string) (string, error) {
	rel := field.Relation
	exprs, err := targetExprs(field, dialect)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	clause, err := scope.clause(dialect, rel.JoinTable, rel.TenantColumn, join, joinTenantType(field), bindTenant)
	if err != nil {
		return "", err
	}
	b.WriteString(clause)
	if rel.DeletedColumn != "" {
		b.WriteString(" AND " + qualified(dialect, join, rel.DeletedColumn) + " IS NULL")
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
	pc, err := policyScope(field, exprs, dialect, scope, related, s, visible, join)
	if err != nil {
		return "", err
	}
	b.WriteString(pc)
	return b.String(), nil
}

// targetExprs are a declared relation's target's computed columns resolved
// for dialect: a statement that reads the target compiles its schema, and a
// dialect one of its expressions refuses fails it.
func targetExprs(field *FieldSchema, dialect Dialect) (columnExprs, error) {
	if field.Related == nil {
		return nil, nil
	}
	exprs, err := field.Related.ComputedSQL(dialect)
	return exprs, err
}

// targetScope is a many-to-many target's tenant and live-row predicate on
// alias.
func targetScope(field *FieldSchema, dialect Dialect, scope relationScope, bindTenant func(any) string, alias string) (string, error) {
	rel := field.Relation
	var b strings.Builder
	tc, err := scope.clause(dialect, rel.TargetTable, rel.TargetTenant, alias, tenantColumnType(field.Related, rel.TargetTenant), bindTenant)
	if err != nil {
		return "", err
	}
	b.WriteString(tc)
	if rel.TargetDeleted != "" {
		b.WriteString(" AND " + qualified(dialect, alias, rel.TargetDeleted) + " IS NULL")
	}
	return b.String(), nil
}

// policyScope is a policy-scoped relation's target policy on alias, ""
// for any other relation.
func policyScope(field *FieldSchema, exprs columnExprs, dialect Dialect, scope relationScope, related relatedPolicies, s *buildState, alias string, others ...string) (string, error) {
	rel := field.Relation
	if !rel.Policy {
		return "", nil
	}
	if field.Related == nil {
		return "", unresolvableRelation(field, "is policy-scoped without a target schema to hold its policy")
	}
	root, err := related.on(field, nil)
	if err != nil || root == nil {
		return "", err
	}
	root, err = rebaseNode(root, field.Related.Name, alias, exprs, map[*FieldSchema]*FieldSchema{})
	if err != nil {
		return "", err
	}
	pb := &SQLBuilder{schema: &ResourceSchema{Name: alias}, dialect: dialect, scope: scope, related: related, depth: 1, visible: append(others, alias), exprs: exprs}
	cond, err := pb.buildNode(s, root)
	if err != nil {
		return "", computedCause(err)
	}
	return " AND " + cond, nil
}

// VisibleRelated is the predicate holding a declared many-to-many's target
// rows, under alias, to those a request may relate: in the tenant (or
// admitted by the target's policy) and live. It begins with " AND ", is ""
// when nothing limits the targets, and binds every value with bind — for a
// statement another package numbers, such as a membership patch's guarded
// insert. scope's Tenant, Related and Now are read: the request's tenant,
// the targets' policies and the instant their relative-time tokens resolve
// against (the wall clock when zero).
func VisibleRelated(field *FieldSchema, dialect Dialect, alias string, scope RelationScope, bind func(any) string) (string, error) {
	if field == nil || field.Relation == nil || field.Relation.TargetTable == "" {
		return "", fmt.Errorf("VisibleRelated: field is not a many-to-many with a target")
	}
	exprs, err := targetExprs(field, dialect)
	if err != nil {
		return "", err
	}
	tenancy := relationScope{tenant: scope.Tenant}
	tc, err := targetScope(field, dialect, tenancy, bind, alias)
	if err != nil {
		return "", err
	}
	now := scope.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	policies := relatedPolicies{byTable: scope.Related, now: now}
	pc, err := policyScope(field, exprs, dialect, tenancy, policies, &buildState{bind: bind}, alias)
	if err != nil {
		return "", err
	}
	return tc + pc, nil
}

// Aliases the relation statements of includes and relation facets read
// the junction table, the related target rows and, for an include scoped
// to each owner row's tenant, the owner rows by.
const (
	junctionAlias     = "j"
	targetAlias       = "target__"
	includeOwnerAlias = "owner__"
)

// relatedJoin is the JOIN of a many-to-many's target onto its pair table
// (alias join), as target; empty for every other relation.
func relatedJoin(d Dialect, rel *core.RelationSchema, join, target string) string {
	if rel.TargetTable == "" {
		return ""
	}
	return " JOIN " + d.QuoteIdent(rel.TargetTable) + " " + target + " ON " + correlation(d, target, rel.TargetKeys, join, rel.TargetColumns, rel.TargetTypes)
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
		owner := core.KeyText(core.Key{ownerPK})
		if q.Limit > 0 && len(out.IDs[owner]) == q.Limit {
			if !slices.Contains(out.Truncated, owner) {
				out.Truncated = append(out.Truncated, owner)
			}
			continue
		}
		out.IDs[owner] = append(out.IDs[owner], core.KeyText(core.Key{targetID}))
	}
	if err := rows.Err(); err != nil {
		return IncludeRows{}, err
	}
	slices.Sort(out.Truncated)
	return out, nil
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
		f, ok := schema.RequestField(name)
		if !ok {
			return nil, core.NewError(ECodeUnknownField, fmt.Sprintf("include field '%s' does not exist", name), core.WithField(name))
		}
		if f.Relation == nil || !f.Includable {
			return nil, core.NewError(ECodeInvalidRelation, fmt.Sprintf("field '%s' is not an includable relation", name),
				core.WithField(name))
		}
		fields = append(fields, name)
	}
	if len(fields) > MaxIncludes {
		return nil, core.NewError(ECodeInvalidRelation, fmt.Sprintf("a list includes at most %d relations, got %d", MaxIncludes, len(fields)),
			core.WithParameter(ParamInclude))
	}
	return fields, nil
}
