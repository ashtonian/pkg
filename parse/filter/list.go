package filter

import (
	"errors"
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
	// key is the schema's PrimaryKey, the final column of every keyset.
	key CursorKey
}

// NewListQuery creates a ListQuery builder for filterSchema's rows. The
// schema declares its key (PrimaryKey), unique among the rows, which ends
// every keyset so pages neither skip nor repeat rows. It must name a typed
// column through sortSchema, the schema's Fields or its SystemFields; a
// schema without one, or with a key that is computed, untyped or no field,
// is an error.
func NewListQuery(filterSchema *ResourceSchema, sortSchema *SortSchema, dialect Dialect) (*ListQuery, error) {
	if filterSchema == nil {
		return nil, errors.New("filter: a list query needs a filter schema")
	}
	key, err := listKey(filterSchema, sortSchema)
	if err != nil {
		return nil, fmt.Errorf("filter: list of %s: %w", filterSchema.Name, err)
	}
	return &ListQuery{filterSchema: filterSchema, sortSchema: sortSchema, dialect: dialect, key: key}, nil
}

// listKey resolves the schema's key to the typed cursor key that ends every
// keyset: a sort field, else a request or system field of that name.
func listKey(fs *ResourceSchema, ss *SortSchema) (CursorKey, error) {
	name := fs.PrimaryKey
	if name == "" {
		return CursorKey{}, errors.New("the schema declares no key (ResourceSchema.PrimaryKey)")
	}
	key := CursorKey{Name: name}
	var sf *SortFieldSchema
	if ss != nil {
		sf = ss.Fields[name]
	}
	ff := fs.Fields[name]
	if ff == nil {
		ff = fs.SystemFields[name]
	}
	switch {
	case sf != nil:
		key.Table, key.Column, key.Expr, key.DataType = sf.Table, sf.RowColumn(), sf.Expr, sf.DataType
		key.NumberKind, key.Nullable = sf.NumberKind, sf.Nullable
	case ff != nil:
		key.Table, key.Column, key.Expr, key.DataType = ff.Table, ff.RowColumn(), ff.Expr, ff.DataType
		key.NumberKind = ff.NumberKind
	default:
		return CursorKey{}, fmt.Errorf("key %q is no sort, filter or system field", name)
	}
	if key.DataType == "" {
		return CursorKey{}, fmt.Errorf("key %q declares no type", name)
	}
	if key.Column == "" || fs.computes(key.Column) {
		return CursorKey{}, fmt.Errorf("key %q is computed; a keyset reads its key from a column", name)
	}
	return key, nil
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
	now         time.Time
	withCount   bool
	facetFields []string
	searchQuery string
	startParam  int
	scope       relationScope
	related     relatedPolicies
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

// ListNow resolves the relative-time tokens of the filter JSON, the policy
// and the related tables' policies against now: the instant the request
// was parsed at, so they agree with its filter. Without it, Build reads the
// parser clock once. A RelationScope.Now holds for the related policies.
func ListNow(now time.Time) ListOption {
	return func(c *listConfig) { c.now = now }
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

// ListRelationScope scopes the relations the filter, count and facets
// reach (RelationScope).
func ListRelationScope(s RelationScope) ListOption {
	return func(c *listConfig) { c.scope, c.related = s.tenancy(), s.policies() }
}

// ListStartParam sets the starting parameter index offset. Use this when the
// SQL query has preceding parameters (e.g., SET clause args in an UPDATE).
// The filter's first placeholder will be startParam+1. A negative offset is
// an error from Build.
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

	now := cfg.now
	if now.IsZero() {
		now = lq.filterConfig.Now()
	}
	related := cfg.related.at(now)
	ast := cfg.filterAST
	if ast == nil && cfg.filterJSON != "" {
		parsed, err := NewFilterParser(lq.filterSchema, lq.filterConfig).At(now).Parse(cfg.filterJSON)
		if err != nil {
			return nil, err
		}
		ast = parsed
	}
	if cfg.policy != nil && !cfg.policy.IsEmpty() {
		merged, err := applyPolicyConstraints(lq.filterSchema, ast, cfg.policy, now)
		if err != nil {
			return nil, err
		}
		ast = merged
	}
	// The sort keys and search fields read computed columns as the filter
	// does; the builder resolves its own.
	exprs, err := lq.filterSchema.ComputedSQL(lq.dialect)
	if err != nil {
		return nil, err
	}
	search := columnExprs(exprs).search(lq.searchSchema)
	builder := &SQLBuilder{schema: lq.filterSchema, dialect: lq.dialect, startParam: cfg.startParam, scope: cfg.scope.over(lq.dialect, lq.filterSchema), related: related}
	filterSQL, err := builder.build(ast, exprs)
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
	for i, k := range keys {
		keys[i] = columnExprs(exprs).key(k)
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
	if search != nil && cfg.searchQuery != "" {
		sc, searchErr := buildSearchCondition(search, lq.dialect, cfg.searchQuery, paramIdx)
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
	orderBy := OrderByKeys(lq.dialect, keys, plan.Reverse)

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
		population, err = populationBuilder.build(ast, exprs)
		if err != nil {
			return nil, err
		}
	}
	popWhere, popArgs, err := AndSearch(search, lq.dialect, population.Where, population.Args, 0, cfg.searchQuery)
	if err != nil {
		return nil, err
	}

	if cfg.withCount {
		// Relation predicates are correlated EXISTS, so no join multiplies
		// owner rows and a plain COUNT(*) is exact.
		result.CountSQL = "SELECT COUNT(*) FROM " + lq.dialect.QuoteIdent(lq.filterSchema.Name) + " WHERE " + popWhere
		result.CountArgs = slices.Clone(popArgs)
	}

	if len(cfg.facetFields) > 0 {
		reqs := make([]FacetRequest, len(cfg.facetFields))
		for i, f := range cfg.facetFields {
			reqs[i] = FacetRequest{Field: f}
		}
		fq, err := buildFacetQuery(lq.filterSchema, lq.dialect, popWhere, popArgs, reqs, facetConfig{scope: cfg.scope, related: related})
		if err != nil {
			return nil, err
		}
		result.Facets = &fq
	}

	return result, nil
}

// cursorKeys derives the effective keyset: the parsed sort fields followed
// by the key.
func (lq *ListQuery) cursorKeys(sort *SortResult) ([]CursorKey, error) {
	keys := Keyset(sort, lq.key)
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
