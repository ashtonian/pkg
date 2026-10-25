package filter

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
)

// DefaultListLimit is the default page size when no limit is specified.
const DefaultListLimit = 50

// MaxListLimit is the maximum allowed page size.
const MaxListLimit = 1000

// ListQuery builds a complete SQL fragment for list endpoints.
type ListQuery struct {
	filterSchema *ResourceSchema
	sortSchema   *SortSchema
	searchSchema *SearchSchema
	dialect      Dialect
	filterConfig *core.ParserConfig
}

// NewListQuery creates a ListQuery builder.
func NewListQuery(filterSchema *ResourceSchema, sortSchema *SortSchema, dialect Dialect) *ListQuery {
	return &ListQuery{filterSchema: filterSchema, sortSchema: sortSchema, dialect: dialect}
}

// WithFilterConfig sets custom filter parsing config.
func (lq *ListQuery) WithFilterConfig(config *core.ParserConfig) *ListQuery {
	lq.filterConfig = config
	return lq
}

// WithSearchSchema sets the full-text search schema. When set, a [ListSearch]
// query composes a dialect-aware search predicate (postgres full-text via
// to_tsvector/plainto_tsquery; LIKE fallback elsewhere) ANDed with the filter.
// Search is WHERE-only — it never reorders results, so keyset (cursor)
// pagination stays stable on the configured sort.
func (lq *ListQuery) WithSearchSchema(schema *SearchSchema) *ListQuery {
	lq.searchSchema = schema
	return lq
}

// ListOption configures a single Build call.
type ListOption func(*listConfig)

type listConfig struct {
	filterJSON  string
	filterAST   *FilterAST
	sortParam   string
	sortResult  *SortResult
	cursor      string
	limit       int
	maxLimit    int
	policy      *PolicyConstraints
	withCount   bool
	facetFields []string
	searchQuery string
	startParam  int
	scope       relationScope
	related     map[string]*PolicyConstraints
}

// ListFilter sets the JSON filter string.
func ListFilter(filterJSON string) ListOption {
	return func(c *listConfig) { c.filterJSON = filterJSON }
}

// ListFilterAST sets a pre-parsed FilterAST.
func ListFilterAST(ast *FilterAST) ListOption {
	return func(c *listConfig) { c.filterAST = ast }
}

// ListSort sets the sort parameter string.
func ListSort(sortParam string) ListOption {
	return func(c *listConfig) { c.sortParam = sortParam }
}

// ListSortResult sets a pre-parsed SortResult.
func ListSortResult(sr *SortResult) ListOption {
	return func(c *listConfig) { c.sortResult = sr }
}

// ListCursor sets the opaque cursor string.
func ListCursor(cursor string) ListOption {
	return func(c *listConfig) { c.cursor = cursor }
}

// ListLimit sets the page size.
func ListLimit(limit int) ListOption {
	return func(c *listConfig) { c.limit = limit }
}

// ListMaxLimit overrides the maximum allowed limit.
func ListMaxLimit(limit int) ListOption {
	return func(c *listConfig) { c.maxLimit = limit }
}

// ListPolicy sets policy constraints.
func ListPolicy(pc *PolicyConstraints) ListOption {
	return func(c *listConfig) { c.policy = pc }
}

// ListWithCount enables COUNT(*) query generation.
func ListWithCount(enable bool) ListOption {
	return func(c *listConfig) { c.withCount = enable }
}

// ListFacets requests facet aggregation queries.
func ListFacets(fields ...string) ListOption {
	return func(c *listConfig) { c.facetFields = fields }
}

// ListSearch sets the per-request full-text search query. The schema comes
// from the ListQuery ([ListQuery.WithSearchSchema]); an empty query or an
// unset schema is a no-op.
func ListSearch(query string) ListOption {
	return func(c *listConfig) { c.searchQuery = query }
}

// ListTenant binds the tenant every tenant-scoped relation junction in the
// filter and facets is restricted to. Required whenever
// the resource declares relations with a TenantColumn.
func ListTenant(value any) ListOption {
	return func(c *listConfig) {
		c.scope.tenant = value
		c.scope.hasTenant = value != nil
	}
}

// ListRelatedPolicies supplies the server's policy for the target tables of
// policy-scoped relations, keyed by table (WithRelatedPolicies).
func ListRelatedPolicies(policies map[string]*PolicyConstraints) ListOption {
	return func(c *listConfig) { c.related = policies }
}

// ListUnscopedRelations deliberately builds tenant-scoped relation junctions
// without a tenant predicate. Never use it on a request path.
func ListUnscopedRelations() ListOption {
	return func(c *listConfig) { c.scope.unscoped = true }
}

// ListStartParam sets the starting parameter index offset. Use this when the
// SQL query has preceding parameters (e.g., SET clause args in an UPDATE).
// The filter's first placeholder will be startParam+1.
func ListStartParam(n int) ListOption {
	return func(c *listConfig) { c.startParam = n }
}

// ListResult contains the composed SQL fragments.
type ListResult struct {
	Fragment   string // WHERE … ORDER BY … LIMIT n, appended to SELECT … FROM <table>
	Where      string
	OrderBy    string
	Args       []any
	FetchLimit int
	Limit      int
	Sort       *SortResult
	// Cursor is the keyset plan for building the page from the fetched
	// rows: pass it with Limit to NewPage.
	Cursor    CursorPlan
	CountSQL  string
	CountArgs []any
	// Facets is the single CTE + UNION ALL facet query (see BuildFacetQuery)
	// when ListFacets was requested; scan its rows with ScanFacetUnion.
	Facets *FacetQuery
}

// Build parses all inputs and composes them into a ListResult.
func (lq *ListQuery) Build(opts ...ListOption) (*ListResult, error) {
	cfg := &listConfig{limit: DefaultListLimit, maxLimit: MaxListLimit}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.limit <= 0 {
		cfg.limit = DefaultListLimit
	}
	if cfg.maxLimit <= 0 {
		cfg.maxLimit = MaxListLimit
	}
	if cfg.limit > cfg.maxLimit {
		cfg.limit = cfg.maxLimit
	}

	ast := cfg.filterAST
	if ast == nil && cfg.filterJSON != "" {
		parsed, err := NewFilterParser(lq.filterSchema, lq.filterConfig).Parse(cfg.filterJSON)
		if err != nil {
			return nil, err
		}
		ast = parsed
	}
	if cfg.policy != nil && !cfg.policy.IsEmpty() {
		var clock func() time.Time
		if lq.filterConfig != nil {
			clock = lq.filterConfig.Clock
		}
		merged, err := applyPolicyConstraints(lq.filterSchema, ast, cfg.policy, clock)
		if err != nil {
			return nil, err
		}
		ast = merged
	}
	builder := &SQLBuilder{schema: lq.filterSchema, dialect: lq.dialect, startParam: cfg.startParam, scope: cfg.scope, related: cfg.related}
	filterSQL, err := builder.Build(ast)
	if err != nil {
		return nil, err
	}

	sortResult := cfg.sortResult
	if sortResult == nil && lq.sortSchema != nil {
		if sortResult, err = ParseSort(lq.sortSchema, cfg.sortParam); err != nil {
			return nil, err
		}
	}

	keys, err := lq.cursorKeys(sortResult)
	if err != nil {
		return nil, err
	}
	plan := CursorPlan{Keys: keys, HasCursor: cfg.cursor != ""}

	args := make([]any, len(filterSQL.Args))
	copy(args, filterSQL.Args)
	paramIdx := cfg.startParam + filterSQL.ParamCount

	whereParts := []string{filterSQL.Where}

	if cfg.cursor != "" {
		token, cursorErr := DecodeCursor(cfg.cursor)
		if cursorErr != nil {
			return nil, cursorErr
		}
		if err = ValidateCursor(token, keys); err != nil {
			return nil, err
		}
		plan.Reverse = token.Reverse
		whereParts = append(whereParts, seekPredicate(lq.dialect, keys, token, func(v any) string {
			paramIdx++
			args = append(args, v)
			return lq.dialect.Placeholder(paramIdx)
		}))
	}

	// Search — WHERE-only, ANDed after filter + cursor. Never reorders the
	// result, so keyset pagination stays stable on the configured sort.
	if lq.searchSchema != nil && cfg.searchQuery != "" {
		sc, searchErr := buildSearchCondition(lq.searchSchema, lq.dialect, cfg.searchQuery, paramIdx)
		if searchErr != nil {
			return nil, searchErr
		}
		if sc != nil {
			whereParts = append(whereParts, sc.Where)
			args = append(args, sc.Args...)
			// paramIdx is not read past this point — search is the last
			// param consumer — so no increment is needed here.
		}
	}

	var where string
	if len(whereParts) == 1 {
		where = whereParts[0]
	} else {
		where = "(" + strings.Join(whereParts, " AND ") + ")"
	}

	// ORDER BY derives from the effective keyset so the tie-breaker
	// participates and matches the seek predicate exactly; a backward seek
	// walks it inverted.
	orderBy := orderByKeys(lq.dialect, keys, plan.Reverse)

	fetchLimit := cfg.limit + 1

	parts := []string{"WHERE " + where}
	if orderBy != "" {
		parts = append(parts, orderBy)
	}
	parts = append(parts, fmt.Sprintf("LIMIT %d", fetchLimit))

	result := &ListResult{
		Fragment:   strings.Join(parts, " "),
		Where:      where,
		OrderBy:    orderBy,
		Args:       args,
		FetchLimit: fetchLimit,
		Limit:      cfg.limit,
		Sort:       sortResult,
		Cursor:     plan,
	}

	// The matched population — filter plus search, no cursor — is what the
	// total and the facet counts describe. These are standalone queries
	// with their own arguments, independent of the main fragment's offset.
	population := filterSQL
	if cfg.startParam != 0 && (cfg.withCount || len(cfg.facetFields) > 0) {
		populationBuilder := *builder
		populationBuilder.startParam = 0
		population, err = populationBuilder.Build(ast)
		if err != nil {
			return nil, err
		}
	}
	popWhere, popArgs, err := AndSearch(lq.searchSchema, lq.dialect, population.Where, population.Args, 0, cfg.searchQuery)
	if err != nil {
		return nil, err
	}

	if cfg.withCount {
		// Relation predicates are correlated EXISTS, so no join multiplies
		// owner rows and a plain COUNT(*) is exact.
		result.CountSQL = "SELECT COUNT(*) FROM " + lq.filterSchema.Name + " WHERE " + popWhere
		result.CountArgs = slices.Clone(popArgs)
	}

	if len(cfg.facetFields) > 0 {
		reqs := make([]FacetRequest, len(cfg.facetFields))
		for i, f := range cfg.facetFields {
			reqs[i] = FacetRequest{Field: f}
		}
		fq, err := buildFacetQuery(lq.filterSchema, lq.dialect, popWhere, popArgs, reqs, facetConfig{scope: cfg.scope, related: cfg.related})
		if err != nil {
			return nil, err
		}
		result.Facets = &fq
	}

	return result, nil
}

// cursorKeys derives the effective keyset: the parsed sort fields followed
// by the declared primary key, resolved through the sort schema, then the filter
// schema; otherwise it must be a plain SQL identifier (a repository's PK
// column) — user text is never emitted as an identifier (same principle as
// ).
func (lq *ListQuery) cursorKeys(sort *SortResult) ([]CursorKey, error) {
	var tieBreaker string
	if lq.filterSchema != nil {
		tieBreaker = lq.filterSchema.PrimaryKey
	}
	if tieBreaker == "" {
		return Keyset(sort, CursorKey{}), nil
	}
	tb := CursorKey{Name: tieBreaker}
	var ff *FieldSchema
	if lq.filterSchema != nil {
		ff = lq.filterSchema.Fields[tieBreaker]
		if ff == nil {
			ff = lq.filterSchema.SystemFields[tieBreaker]
		}
	}
	switch {
	case lq.sortSchema != nil && lq.sortSchema.Fields[tieBreaker] != nil:
		sf := lq.sortSchema.Fields[tieBreaker]
		tb.SQLExpr, tb.Column, tb.DataType = sf.SQLExpr, sf.RowColumn(), sf.DataType
		tb.NumberKind, tb.Nullable = sf.NumberKind, sf.Nullable
	case ff != nil:
		tb.SQLExpr, tb.DataType = ff.SQLExpr, ff.DataType
		tb.NumberKind = ff.NumberKind
		tb.Column = (&SortFieldSchema{SQLExpr: ff.SQLExpr}).RowColumn()
	case identifierRe.MatchString(tieBreaker):
		tb.SQLExpr = tieBreaker
		tb.Column = (&SortFieldSchema{SQLExpr: tieBreaker}).RowColumn()
	default:
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindSort,
			Code:    ECodeSQLGenerationFailed.String(),
			Message: fmt.Sprintf("cursor tie-breaker '%s' is not a schema field or a plain identifier", tieBreaker),
		}
	}
	keys := Keyset(sort, tb)
	for _, k := range keys {
		if k.Column == "" {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindSort,
				Code:    ECodeSortFieldNotAllowed.String(),
				Message: fmt.Sprintf("sort field '%s' is computed and cannot be used with cursor pagination", k.Name),
			}
		}
	}
	return keys, nil
}
