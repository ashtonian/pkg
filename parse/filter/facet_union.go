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
// lowest values in the field's type order: numbers by value, other keys in
// code-point order. Rows come back facet by facet in request order: terms
// buckets in that order, range buckets as declared. Pair
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
		// order orders a scalar terms facet's values as their type does,
		// projected beside the bucket; "" where the bucket's text does.
		order string
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
		list := f.DataType == core.FieldTypeArray && f.Relation == nil
		if list {
			if fcfg.Type != FacetTerms {
				return FacetQuery{}, fmt.Errorf("BuildFacetQuery: field %q is a list; only terms-type facets are supported", req.Field)
			}
			if _, ok := dialect.(elementTable); !ok {
				return FacetQuery{}, fmt.Errorf("BuildFacetQuery: field %q is a list; dialect %s reads no array elements", req.Field, dialect.Name())
			}
			hasList = true
		}
		// The external name is request data and need not be an SQL
		// identifier ("display-name"); the projection uses an internal
		// alias and the name travels only as a quoted literal.
		bucket := "facet__" + strconv.Itoa(len(metas))
		order := ""
		if fcfg.Type == FacetTerms && f.Relation == nil && !list {
			order = facetValueOrder(f, dialect)
		}
		metas = append(metas, meta{req: req, field: f, cfg: fcfg, bucketCol: bucket, list: list, order: order})
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
			caseSQL, rangeArgs, consumed, err := rangeBucketProjection(m.field, m.cfg.Ranges, dialect, paramOffset)
			if err != nil {
				return FacetQuery{}, err
			}
			proj.WriteString(caseSQL)
			args = append(args, rangeArgs...)
			paramOffset += consumed
		case m.list:
			// The array, or an array path's document; its branch reads the
			// elements.
			if m.field.Path != nil {
				proj.WriteString(m.field.Path.Column)
			} else {
				proj.WriteString(m.field.SQLExpr)
			}
		default:
			proj.WriteString(termsKeyExpr(m.field, dialect))
		}
		proj.WriteString(" AS ")
		proj.WriteString(m.bucketCol)
		if m.order != "" {
			proj.WriteString(", ")
			proj.WriteString(m.order)
			proj.WriteString(" AS ")
			proj.WriteString(m.bucketCol)
			proj.WriteString("_o")
		}
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
	// ties by value in the type's order (numbers by value, other keys by
	// code point), the order that also picks the buckets a terms LIMIT
	// keeps; ranges in declared order. UNION ALL alone guarantees no order.
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
			// The branch's buckets: k, c and, where the key's text does not
			// order the values, o, which does.
			var in strings.Builder
			ordered := false
			// PostgreSQL's ORDER BY cannot collate an output alias, so the
			// tie-break names the key's expression.
			key := m.bucketCol
			if m.list {
				table := dialect.(elementTable) // checked with the request
				source := listSource(m.field, "base."+m.bucketCol)
				elem := table.scalarElement(source, "facet_elem__")
				order := facetValueOrder(elem, dialect)
				ordered = order != ""
				in.WriteString("k, ")
				if ordered {
					// Two elements of a row that name one key are one row.
					in.WriteString("COUNT(DISTINCT __row) AS c, MIN(v) AS o")
				} else {
					in.WriteString("COUNT(*) AS c")
				}
				in.WriteString(" FROM (SELECT DISTINCT base.__row, ")
				in.WriteString(termsKeyExpr(elem, dialect))
				in.WriteString(" AS k")
				if ordered {
					in.WriteString(", ")
					in.WriteString(order)
					in.WriteString(" AS v")
				}
				in.WriteString(" FROM base, ")
				in.WriteString(table.elementRows(source, "facet_elem__"))
				in.WriteString(" WHERE ")
				in.WriteString(FieldExpr(dialect, elem))
				in.WriteString(" IS NOT NULL) e")
				in.WriteString(ord)
				in.WriteString(" GROUP BY k")
				key = "k"
			} else if rel := m.field.Relation; rel != nil {
				// Cast to TEXT so UNION ALL key columns line up with
				// scalar terms facets that already cast.
				key = relationKeyExpr(rel, dialect)
				in.WriteString(key)
				// Owners, not junction rows: two rows pairing one owner with
				// one target (typed edges) count the owner once.
				in.WriteString(" AS k, COUNT(DISTINCT base.__owner_pk) AS c FROM base INNER JOIN ")
				in.WriteString(rel.JoinTable)
				in.WriteString(" j ON j.")
				in.WriteString(rel.SourceColumn)
				in.WriteString(" = base.__owner_pk")
				in.WriteString(relatedJoin(rel, "j", "target__"))
				clause, err := relatedScope(m.field, dialect, cfg.scope, cfg.related, st, bindTenant, "j", "target__")
				if err != nil {
					return FacetQuery{}, fmt.Errorf("BuildFacetQuery: field %q: %w", m.req.Field, err)
				}
				in.WriteString(clause)
				in.WriteString(" GROUP BY j.")
				in.WriteString(rel.TargetColumn)
			} else {
				ordered = m.order != ""
				in.WriteString(m.bucketCol)
				in.WriteString(" AS k, COUNT(*) AS c")
				if ordered {
					in.WriteString(", MIN(")
					in.WriteString(m.bucketCol)
					in.WriteString("_o) AS o")
				}
				in.WriteString(" FROM base WHERE ")
				in.WriteString(m.bucketCol)
				in.WriteString(" IS NOT NULL GROUP BY ")
				in.WriteString(m.bucketCol)
			}
			tieOrder := ""
			if ordered {
				tieOrder = "o, "
			}
			b.WriteString("k AS facet_key, c AS facet_count, ")
			b.WriteString(ord)
			b.WriteString(" AS facet_ord, ROW_NUMBER() OVER (ORDER BY c DESC, ")
			b.WriteString(tieOrder)
			b.WriteString(dialect.CodePointOrder("k"))
			b.WriteString(") AS facet_rank FROM (SELECT ")
			b.WriteString(in.String())
			b.WriteString(" ORDER BY c DESC, ")
			b.WriteString(tieOrder)
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
	b.WriteString(") facets__ ORDER BY facet_ord, facet_rank")

	return FacetQuery{SQL: b.String(), Args: st.args}, nil
}

// facetValueOrder renders what orders f's values as their type does
// (CompareKeyValues): a number exactly by value. It is "" for a type whose
// key text (termsKeyExpr) orders by code point as its values do.
func facetValueOrder(f *FieldSchema, dialect Dialect) string {
	switch f.DataType {
	case core.FieldTypeInt, core.FieldTypeFloat:
		return FieldExpr(dialect, f)
	case core.FieldTypeDecimal:
		return dialect.DecimalOrder(FieldExpr(dialect, f))
	}
	return ""
}

// listSource is a list facet's array field reading column, the base CTE's
// projection of its array or of its array path's document.
func listSource(f *FieldSchema, column string) *FieldSchema {
	out := *f
	if f.Path != nil {
		p := *f.Path
		p.Column = column
		out.Path = &p
		return &out
	}
	out.SQLExpr = column
	return &out
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

// termsKeyExpr renders a terms facet field's value as its key, the
// canonical text FacetKeys gives it, as TEXT: UNION ALL branches share one
// key column type.
func termsKeyExpr(field *FieldSchema, dialect Dialect) string {
	expr := FieldExpr(dialect, field)
	switch field.DataType {
	case core.FieldTypeString:
		return expr
	case core.FieldTypeBool:
		// The JSON literals: a text cast is "1" / "0" on SQLite.
		return "CASE WHEN " + expr + " IS NULL THEN NULL WHEN " + expr + " THEN 'true' ELSE 'false' END"
	case core.FieldTypeInt, core.FieldTypeFloat, core.FieldTypeDecimal:
		if k, ok := dialect.(numberKeyer); ok {
			return k.numberKey(field)
		}
	case core.FieldTypeTimestamp, core.FieldTypeUUID:
		if field.Path != nil {
			return expr // PathExpr reads both as canonical text
		}
	}
	if dialect.Name() == "postgres" {
		return expr + "::text"
	}
	return "CAST(" + expr + " AS TEXT)"
}

// ScanFacetUnion reads (facet_field, facet_key, facet_count) rows produced
// by [BuildFacetQuery] and groups them into per-field [FacetResult]s in the
// query's order: facets as requested; a terms facet's buckets by count,
// ties in the field's type order; a range facet's as declared.
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
