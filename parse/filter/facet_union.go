package filter

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// defaultTermsLimit caps a terms facet's bucket count when neither
// FacetRequest.Limit nor FacetSchema.Limit is set.
const defaultTermsLimit = 50

// FacetRequest names a field to facet plus an optional per-request limit.
//
// Limit resolution: FacetRequest.Limit > 0 → that. Else FieldSchema.FacetSchema.Limit > 0 → that.
// Else defaultTermsLimit (50). Range facets ignore Limit (bucket count is fixed by FacetSchema.Ranges).
type FacetRequest struct {
	Field string
	Limit int
}

// BuildFacetQueryOption configures BuildFacetQuery behavior.
type BuildFacetQueryOption func(*facetConfig)

type facetConfig struct {
	scope   relationScope
	related map[string]*PolicyConstraints
}

// WithFacetTenant binds the tenant every tenant-scoped relation facet's
// junction join is restricted to. Required when a requested
// relation facet declares a TenantColumn; without it (or
// WithFacetUnscopedRelations) the build fails.
func WithFacetTenant(value any) BuildFacetQueryOption {
	return func(c *facetConfig) {
		c.scope.tenant = value
		c.scope.hasTenant = value != nil
	}
}

// WithFacetUnscopedRelations deliberately aggregates tenant-scoped relation
// facets without a tenant predicate. Never use it on a request path.
func WithFacetUnscopedRelations() BuildFacetQueryOption {
	return func(c *facetConfig) { c.scope.unscoped = true }
}

// WithFacetRelatedPolicies supplies the policies of policy-scoped
// relations' target tables (WithRelatedPolicies).
func WithFacetRelatedPolicies(policies map[string]*PolicyConstraints) BuildFacetQueryOption {
	return func(c *facetConfig) { c.related = policies }
}

// BuildFacetQuery composes a single CTE + UNION ALL query covering every
// requested facet. The policy-merged filter scans once in the base CTE;
// each facet aggregates over the materialized set.
//
// Result row shape (uniform across dialects):
//
//	facet_field  TEXT      – which facet a row belongs to
//	facet_key    TEXT      – bucket key
//	facet_count  BIGINT    – count of matching rows
//
// Range facets are pre-bucketed in the CTE projection so outer GROUP BYs
// are uniform. Terms facets enforce per-field LIMIT N via inline
// subqueries, keeping the highest counts and, among equal counts, the
// lowest keys in code-point order. Rows come back facet by facet in request
// order: terms buckets by count then key, range buckets as declared. Pair
// with [ScanFacetUnion] to re-group rows into per-field [FacetResult]s.
//
// Relation predicates in filterWhere are correlated EXISTS, so the base CTE
// holds one row per matching owner and every count is a count of owners
// . Relation-typed facet fields JOIN the junction inside
// their own UNION ALL branch. The base CTE projects the owner's
// PK under the alias `__owner_pk`; the relation branch joins on
// `j.<source> = base.__owner_pk` and aggregates over `j.<target>`. Range
// facets on a relation field are rejected here.
//
// Errors when the schema is nil, no facets are requested, or any requested
// field is missing or not [FieldSchema.Facetable].
func BuildFacetQuery(
	schema *ResourceSchema,
	dialect Dialect,
	filterWhere string,
	filterArgs []any,
	facets []FacetRequest,
	opts ...BuildFacetQueryOption,
) (FacetQuery, error) {
	var cfg facetConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return buildFacetQuery(schema, dialect, filterWhere, filterArgs, facets, cfg)
}

func buildFacetQuery(schema *ResourceSchema, dialect Dialect, filterWhere string, filterArgs []any, facets []FacetRequest, cfg facetConfig) (FacetQuery, error) {
	if schema == nil {
		return FacetQuery{}, fmt.Errorf("BuildFacetQuery: schema is required")
	}
	if dialect == nil {
		return FacetQuery{}, fmt.Errorf("BuildFacetQuery: dialect is required")
	}
	if len(facets) == 0 {
		return FacetQuery{}, fmt.Errorf("BuildFacetQuery: at least one facet is required")
	}

	type meta struct {
		req       FacetRequest
		field     *FieldSchema
		cfg       *FacetSchema
		bucketCol string // column name in the base CTE projection (scalar facets only)
		// list facets count each distinct element of an array column once
		// per row, as memory backends do.
		list bool
	}
	metas := make([]meta, 0, len(facets))
	hasRelation, hasList := false, false
	relationPK := "id"
	for _, req := range facets {
		f, ok := schema.Fields[req.Field]
		if !ok || !f.Facetable {
			return FacetQuery{}, &core.ParseError{
				Kind:    core.ParseErrorKindFilter,
				Code:    ECodeUnknownField.String(),
				Message: fmt.Sprintf("field '%s' is not facetable", req.Field),
				Path:    req.Field,
			}
		}
		fcfg := f.FacetSchema
		if fcfg == nil {
			fcfg = &FacetSchema{Type: FacetTerms}
		}
		if f.Relation != nil {
			if fcfg.Type != FacetTerms {
				return FacetQuery{}, fmt.Errorf("BuildFacetQuery: field %q is a relation; only terms-type facets are supported", req.Field)
			}
			if !hasRelation {
				if pk := f.Relation.PrimaryKey; pk != "" {
					relationPK = pk
				}
				hasRelation = true
			}
		}
		list := f.DataType == core.FieldTypeArray && f.Relation == nil && f.Path == nil
		if list {
			if fcfg.Type != FacetTerms {
				return FacetQuery{}, fmt.Errorf("BuildFacetQuery: field %q is a list; only terms-type facets are supported", req.Field)
			}
			hasList = true
		}
		// The external name is request data and need not be an SQL
		// identifier ("display-name"); the projection uses an internal
		// alias and the name travels only as a quoted literal.
		bucket := "facet__" + strconv.Itoa(len(metas))
		metas = append(metas, meta{req: req, field: f, cfg: fcfg, bucketCol: bucket, list: list})
	}

	args := make([]any, len(filterArgs))
	copy(args, filterArgs)
	paramOffset := len(filterArgs)

	// Base CTE projection. Scalar/range facets project the value/range
	// bucket; relation facets are not projected here — they JOIN against
	// the junction in their UNION-ALL leg using the owner-PK alias.
	var proj strings.Builder
	first := true
	for _, m := range metas {
		if m.field.Relation != nil {
			continue
		}
		if !first {
			proj.WriteString(", ")
		}
		first = false
		switch {
		case m.cfg.Type == FacetTypeRange:
			caseSQL, rangeArgs, consumed := rangeBucketProjection(FieldExpr(dialect, m.field), m.cfg.Ranges, dialect, paramOffset)
			proj.WriteString(caseSQL)
			args = append(args, rangeArgs...)
			paramOffset += consumed
		case m.list:
			proj.WriteString(FieldExpr(dialect, m.field)) // the array; its branch reads the elements
		default:
			proj.WriteString(termsKeyExpr(m.field, dialect))
		}
		proj.WriteString(" AS ")
		proj.WriteString(m.bucketCol)
	}
	if hasList {
		// A row identity, so an element repeated in one row counts it once.
		proj.WriteString(", ROW_NUMBER() OVER () AS __row")
	}
	if hasRelation {
		if !first {
			proj.WriteString(", ")
		}
		proj.WriteString(schema.Name)
		proj.WriteByte('.')
		proj.WriteString(relationPK)
		proj.WriteString(" AS __owner_pk")
		first = false
	}
	if first {
		// Pure-relation facet request with no scalar facets; we still
		// need a non-empty projection to make the CTE valid. Project
		// the owner PK only.
		proj.WriteString(schema.Name)
		proj.WriteByte('.')
		proj.WriteString(relationPK)
		proj.WriteString(" AS __owner_pk")
	}

	// The tenant is bound once, after the range args, and its placeholder
	// reused by every relation branch; policy values bind after it.
	st := &buildState{paramIndex: paramOffset, args: args}
	tenantPH := ""
	bindTenant := func(v any) string {
		if tenantPH == "" {
			tenantPH = st.binder(dialect)(v)
		}
		return tenantPH
	}

	var b strings.Builder
	b.WriteString("WITH base AS")
	b.WriteString(dialect.CTEMaterializeHint())
	b.WriteString(" (SELECT ")
	b.WriteString(proj.String())
	b.WriteString(" FROM ")
	b.WriteString(schema.Name)
	b.WriteString(" WHERE ")
	b.WriteString(filterWhere)
	b.WriteString(") ")

	// Each branch carries its facet's ordinal and a bucket rank so the outer
	// ORDER BY fixes the row order: facets in request order; terms by count,
	// ties by key in code-point order (the same tie-break picks which buckets
	// a terms LIMIT keeps); ranges in declared order. UNION ALL alone
	// guarantees no order.
	b.WriteString("SELECT facet_field, facet_key, facet_count FROM (")
	for i, m := range metas {
		if i > 0 {
			b.WriteString(" UNION ALL ")
		}
		ord := strconv.Itoa(i)
		b.WriteString("SELECT '")
		b.WriteString(escapeSQLString(m.req.Field))
		b.WriteString("' AS facet_field, ")

		if m.field.Relation != nil || m.cfg.Type == FacetTerms {
			limit := m.req.Limit
			if limit <= 0 {
				limit = m.cfg.Limit
			}
			if limit <= 0 {
				limit = defaultTermsLimit
			}
			b.WriteString("k AS facet_key, c AS facet_count, ")
			b.WriteString(ord)
			b.WriteString(" AS facet_ord, 0 AS facet_rank FROM (SELECT ")
			// PostgreSQL's ORDER BY cannot collate an output alias, so the
			// tie-break names the key's expression.
			key := m.bucketCol
			if m.list {
				elems, value := listElements(dialect, "base."+m.bucketCol)
				key = termsKeyExpr(&FieldSchema{SQLExpr: value, DataType: m.field.Elem}, dialect)
				b.WriteString("k, COUNT(*) AS c FROM (SELECT DISTINCT base.__row, ")
				b.WriteString(key)
				b.WriteString(" AS k FROM base, ")
				b.WriteString(elems)
				b.WriteString(" WHERE ")
				b.WriteString(value)
				b.WriteString(" IS NOT NULL) e")
				b.WriteString(ord)
				b.WriteString(" GROUP BY k")
				key = "k"
			} else if rel := m.field.Relation; rel != nil {
				// Cast to TEXT so UNION ALL key columns line up with
				// scalar terms facets that already cast.
				key = relationKeyExpr(rel, dialect)
				b.WriteString(key)
				// Owners, not junction rows: two rows pairing one owner with
				// one target (typed edges) count the owner once.
				b.WriteString(" AS k, COUNT(DISTINCT base.__owner_pk) AS c FROM base INNER JOIN ")
				b.WriteString(rel.JoinTable)
				b.WriteString(" j ON j.")
				b.WriteString(rel.SourceColumn)
				b.WriteString(" = base.__owner_pk")
				b.WriteString(relatedJoin(rel, "j", "target__"))
				clause, err := relatedScope(m.field, dialect, cfg.scope, cfg.related, st, bindTenant, "j", "target__")
				if err != nil {
					return FacetQuery{}, fmt.Errorf("BuildFacetQuery: field %q: %w", m.req.Field, err)
				}
				b.WriteString(clause)
				b.WriteString(" GROUP BY j.")
				b.WriteString(rel.TargetColumn)
			} else {
				b.WriteString(m.bucketCol)
				b.WriteString(" AS k, COUNT(*) AS c FROM base WHERE ")
				b.WriteString(m.bucketCol)
				b.WriteString(" IS NOT NULL GROUP BY ")
				b.WriteString(m.bucketCol)
			}
			b.WriteString(" ORDER BY c DESC, ")
			b.WriteString(dialect.CodePointOrder(key))
			b.WriteString(" LIMIT ")
			b.WriteString(strconv.Itoa(limit))
			b.WriteString(") s")
			b.WriteString(ord)
			continue
		}

		b.WriteString(m.bucketCol)
		b.WriteString(" AS facet_key, COUNT(*) AS facet_count, ")
		b.WriteString(ord)
		b.WriteString(" AS facet_ord, CASE ")
		b.WriteString(m.bucketCol)
		for r, rng := range m.cfg.Ranges {
			b.WriteString(" WHEN '")
			b.WriteString(escapeSQLString(rng.Key))
			b.WriteString("' THEN ")
			b.WriteString(strconv.Itoa(r))
		}
		b.WriteString(" END AS facet_rank FROM base WHERE ")
		b.WriteString(m.bucketCol)
		b.WriteString(" IS NOT NULL GROUP BY ")
		b.WriteString(m.bucketCol)
	}
	b.WriteString(") facets__ ORDER BY facet_ord, facet_rank, facet_count DESC, ")
	b.WriteString(dialect.CodePointOrder("facet_key"))

	return FacetQuery{SQL: b.String(), Args: st.args}, nil
}

// listElements is a FROM item reading the elements of array, a base
// column, and the expression of one element: unnest over a PostgreSQL
// array, json_each over SQLite's JSON text.
func listElements(dialect Dialect, array string) (from, value string) {
	if dialect.Name() == "postgres" {
		return "unnest(" + array + ") AS facet_elem__(v)", "facet_elem__.v"
	}
	return "json_each(" + array + ") AS facet_elem__", "facet_elem__.value"
}

// relationKeyExpr renders the junction's target column as TEXT so the
// UNION ALL key column lines up with scalar terms facets.
func relationKeyExpr(rel *core.RelationSchema, dialect Dialect) string {
	expr := "j." + rel.TargetColumn
	if dialect.Name() == "postgres" {
		return expr + "::text"
	}
	return "CAST(" + expr + " AS TEXT)"
}

// termsKeyExpr returns the SQL expression projecting a terms-facet field's
// value as TEXT. UNION ALL branches require a uniform key column type, so
// non-text columns are cast.
func termsKeyExpr(field *FieldSchema, dialect Dialect) string {
	expr := FieldExpr(dialect, field)
	if field.DataType == core.FieldTypeString || field.Path != nil && (field.DataType == core.FieldTypeTimestamp || field.DataType == core.FieldTypeUUID) {
		return expr
	}
	// Boolean keys are normalized to the JSON literals so bucket keys are
	// backend-independent: a bare text cast yields "1"/"0" on SQLite but
	// "true"/"false" on PostgreSQL, and consumers in the SPAs) compare against the JSON form.
	// Standard-SQL CASE works identically on both dialects; NULL stays
	// NULL and is dropped by the scanner like every other facet key.
	if field.DataType == core.FieldTypeBool {
		return "CASE WHEN " + expr + " IS NULL THEN NULL WHEN " + expr + " THEN 'true' ELSE 'false' END"
	}
	if dialect.Name() == "postgres" {
		return expr + "::text"
	}
	return "CAST(" + expr + " AS TEXT)"
}

// ScanFacetUnion reads (facet_field, facet_key, facet_count) rows produced
// by [BuildFacetQuery] and groups them into per-field [FacetResult]s in the
// query's order: facets as requested; a terms facet's buckets by count,
// ties by key; a range facet's as declared.
//
// NULL keys are dropped (BuildFacetQuery's WHERE filters them, but defense
// in depth).
//
// Caller owns the iterator's Close — sql.Rows.Close returns an error while
// pgx.Rows.Close does not, so the contract here stays narrow.
func ScanFacetUnion(rows Rows) ([]FacetResult, error) {
	indexByField := make(map[string]int)
	var results []FacetResult
	for rows.Next() {
		var field, key sql.NullString
		var count int64
		if err := rows.Scan(&field, &key, &count); err != nil {
			return nil, fmt.Errorf("scanning facet union row: %w", err)
		}
		if !field.Valid || !key.Valid {
			continue
		}
		idx, ok := indexByField[field.String]
		if !ok {
			idx = len(results)
			indexByField[field.String] = idx
			results = append(results, FacetResult{Field: field.String})
		}
		results[idx].Buckets = append(results[idx].Buckets, FacetBucket{
			Key:   key.String,
			Count: count,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
