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
	// keys are the schema's PrimaryKey columns, in key order: every keyset
	// ends with each one its sort does not name.
	keys []CursorKey
}

// NewListQuery creates a ListQuery builder for filterSchema's rows. The
// schema declares its key (PrimaryKey), whose columns are unique together
// among the rows, which ends every keyset so pages neither skip nor repeat
// rows. Each key column must name a typed column through sortSchema, the
// schema's Fields or its SystemFields; a schema without a key, or with a
// key column that is repeated, computed, untyped or no field, is an error.
func NewListQuery(filterSchema *ResourceSchema, sortSchema *SortSchema, dialect Dialect) (*ListQuery, error) {
	if filterSchema == nil {
		return nil, errors.New("filter: a list query needs a filter schema")
	}
	keys, err := listKeys(filterSchema, sortSchema)
	if err != nil {
		return nil, fmt.Errorf("filter: list of %s: %w", filterSchema.Name, err)
	}
	query := &ListQuery{filterSchema: filterSchema, sortSchema: sortSchema, dialect: dialect, keys: keys}
	return query, nil
}

// listKeys resolves the schema's key columns to the typed cursor keys that
// end every keyset, in key order.
func listKeys(fs *ResourceSchema, ss *SortSchema) ([]CursorKey, error) {
	if len(fs.PrimaryKey) == 0 {
		return nil, errors.New("the schema declares no key (ResourceSchema.PrimaryKey)")
	}
	keys := make([]CursorKey, len(fs.PrimaryKey))
	for i, name := range fs.PrimaryKey {
		if slices.Contains(fs.PrimaryKey[:i], name) {
			return nil, fmt.Errorf("key column %q is listed twice", name)
		}
		key, err := listKey(fs, ss, name)
		if err != nil {
			return nil, err
		}
		keys[i] = key
	}
	return keys, nil
}

// listKey resolves one key column to its typed cursor key: a sort field,
// else a request or system field of that name.
func listKey(fs *ResourceSchema, ss *SortSchema, name string) (CursorKey, error) {
	if name == "" {
		return CursorKey{}, errors.New("the schema declares an empty key column (ResourceSchema.PrimaryKey)")
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

// ComposedList is one list request over a ListQuery's rows, composed
// before any dialect is involved (Compose): the filter with the policy
// merged in, the effective keyset and the cursor's position on it, the
// clamped limit, the search query, and the count and facet requests, all at
// one instant. Compile renders it to SQL for a dialect; a backend that
// evaluates rows itself reads it through its accessors. It is read only:
// every list it describes is one Compose admitted.
type ComposedList struct {
	query *ListQuery
	// ast is the request's filter with the policy merged in; nil admits
	// every row.
	ast  *FilterAST
	sort *SortResult
	// keys are the effective keyset, its computed columns read through
	// their expressions.
	keys []CursorKey
	// token is the decoded cursor, validated against keys; nil for a first
	// page.
	token      *PageToken
	limit      int
	search     string
	count      bool
	facets     []string
	scope      RelationScope
	tenancy    relationScope
	related    relatedPolicies
	now        time.Time
	startParam int
}

// Filter is the request's filter with the policy merged in, as the page,
// count and facets select by it; nil admits every row.
func (c *ComposedList) Filter() *FilterAST { return c.ast }

// Sort is the request's parsed sort; nil when the query has no sort schema.
func (c *ComposedList) Sort() *SortResult { return c.sort }

// Keys is the effective keyset: the sort's fields, then the key columns it
// does not name.
func (c *ComposedList) Keys() []CursorKey { return slices.Clone(c.keys) }

// Cursor is the decoded cursor, validated against Keys: the page starts
// past its position, walking the keyset backward when its Reverse is set.
// Nil for a first page.
func (c *ComposedList) Cursor() *PageToken { return c.token }

// Plan is the keyset plan that turns the fetched rows into a page
// (NewPage).
func (c *ComposedList) Plan() CursorPlan {
	plan := CursorPlan{Keys: slices.Clone(c.keys), HasCursor: c.token != nil}
	if c.token != nil {
		plan.Reverse = c.token.Reverse
	}
	return plan
}

// Limit is the page size, clamped to the query's maximum: a page fetches
// Limit+1 rows to tell whether another follows.
func (c *ComposedList) Limit() int { return c.limit }

// Search is the full-text search query, "" for none. It narrows the page,
// the count and the facets and never orders them.
func (c *ComposedList) Search() string { return c.search }

// SearchSchema is the query's search schema; nil when it has none.
func (c *ComposedList) SearchSchema() *SearchSchema { return c.query.searchSchema }

// Count reports whether the request counts the matched population: the
// rows its filter and search select, with no cursor.
func (c *ComposedList) Count() bool { return c.count }

// Facets are the fields the request counts the matched population by.
func (c *ComposedList) Facets() []string { return slices.Clone(c.facets) }

// RelationScope is how the filter, count, facets and includes reach related
// rows, its Now the list's instant.
func (c *ComposedList) RelationScope() RelationScope { return c.scope }

// Now is the instant the filter's, the policy's and the related policies'
// relative-time tokens resolved against.
func (c *ComposedList) Now() time.Time { return c.now }

// Schema is the filter schema of the rows the list reads.
func (c *ComposedList) Schema() *ResourceSchema { return c.query.filterSchema }

// Build composes the request opts describe (Compose) and renders it for the
// query's dialect (Compile).
func (lq *ListQuery) Build(opts ...ListOption) (*ListResult, error) {
	c, err := lq.Compose(opts...)
	if err != nil {
		return nil, err
	}
	return c.Compile(lq.dialect)
}

// Compose composes the request opts describe without a dialect: it clamps
// the limit, fixes the instant, parses the filter and merges the policy
// into it, parses the sort, derives the keyset, and decodes the cursor and
// validates it against the keyset. A request Compose refuses is refused
// whichever backend would run it.
func (lq *ListQuery) Compose(opts ...ListOption) (*ComposedList, error) {
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
	// The offset and the relation scope are checked with the filter, before
	// the sort and the cursor are read, so a list that misstates either
	// fails on it first whichever backend runs it.
	err := checkStartParam(cfg.startParam)
	if err != nil {
		return nil, err
	}
	err = cfg.scope.check()
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
	// The sort keys read computed columns as the filter does.
	exprs := columnExprs(lq.filterSchema.ComputedSQL())
	for i, k := range keys {
		keys[i] = exprs.key(k)
	}

	c := &ComposedList{
		query: lq, ast: ast, sort: sortResult, keys: keys, limit: cfg.limit,
		search: cfg.searchQuery, count: cfg.withCount, facets: slices.Clone(cfg.facetFields),
		tenancy: cfg.scope, related: related, now: now, startParam: cfg.startParam,
	}
	c.scope = RelationScope{Tenant: cfg.scope.tenant, RowTenant: cfg.scope.row.column, Unscoped: cfg.scope.unscoped, Related: related.byTable, Now: related.now}
	if cfg.cursor != "" {
		token, err := DecodeCursor(cfg.cursor)
		if err != nil {
			return nil, err
		}
		if err = ValidateCursor(token, keys); err != nil {
			return nil, err
		}
		c.token = token
	}
	return c, nil
}

// Compile renders c for dialect d: the page's fragment (WHERE … ORDER BY …
// LIMIT Limit+1) and its arguments, and the count and facet statements over
// the matched population. The schema's computed columns (Expr) are SQL of
// the provider the schema was built for, which d must be.
func (c *ComposedList) Compile(d Dialect) (*ListResult, error) {
	lq := c.query
	if d == nil {
		return nil, fmt.Errorf("filter: list of %s: no dialect to compile for", lq.filterSchema.Name)
	}
	// The search fields read computed columns as the filter does; the
	// builder resolves its own.
	exprs := lq.filterSchema.ComputedSQL()
	search := columnExprs(exprs).search(lq.searchSchema)
	builder := &SQLBuilder{schema: lq.filterSchema, dialect: d, startParam: c.startParam, scope: c.tenancy.over(d, lq.filterSchema), related: c.related}
	filterSQL, err := builder.build(c.ast, exprs)
	if err != nil {
		return nil, err
	}

	keys := c.keys
	plan := c.Plan()
	args := make([]any, len(filterSQL.Args))
	copy(args, filterSQL.Args)
	paramIdx := c.startParam + filterSQL.ParamCount

	whereParts := []string{filterSQL.Where}

	if c.token != nil {
		whereParts = append(whereParts, seekPredicate(d, keys, c.token, func(v any) string {
			paramIdx++
			args = append(args, v)
			return d.Placeholder(paramIdx)
		}))
	}

	// Search — WHERE-only, ANDed after filter + cursor. Never reorders the
	// result, so keyset pagination stays stable on the configured sort.
	if search != nil && c.search != "" {
		sc, searchErr := buildSearchCondition(search, d, c.search, paramIdx)
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
	orderBy := OrderByKeys(d, keys, plan.Reverse)

	fetchLimit := c.limit + 1

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
		Limit:      c.limit,
		Sort:       c.sort,
		Cursor:     plan,
	}

	// The matched population — filter plus search, no cursor — is what the
	// total and the facet counts describe. These are standalone queries
	// with their own arguments, independent of the main fragment's offset.
	population := filterSQL
	if c.startParam != 0 && (c.count || len(c.facets) > 0) {
		populationBuilder := *builder
		populationBuilder.startParam = 0
		population, err = populationBuilder.build(c.ast, exprs)
		if err != nil {
			return nil, err
		}
	}
	popWhere, popArgs, err := AndSearch(search, d, population.Where, population.Args, 0, c.search)
	if err != nil {
		return nil, err
	}

	if c.count {
		// Relation predicates are correlated EXISTS, so no join multiplies
		// owner rows and a plain COUNT(*) is exact.
		result.CountSQL = "SELECT COUNT(*) FROM " + d.QuoteIdent(lq.filterSchema.Name) + " WHERE " + popWhere
		result.CountArgs = slices.Clone(popArgs)
	}

	if len(c.facets) > 0 {
		reqs := make([]FacetRequest, len(c.facets))
		for i, f := range c.facets {
			reqs[i] = FacetRequest{Field: f}
		}
		fq, err := buildFacetQuery(lq.filterSchema, d, popWhere, popArgs, reqs, facetConfig{scope: c.tenancy, related: c.related})
		if err != nil {
			return nil, err
		}
		result.Facets = &fq
	}

	return result, nil
}

// cursorKeys derives the effective keyset: the parsed sort fields followed
// by the key columns it does not name.
func (lq *ListQuery) cursorKeys(sort *SortResult) ([]CursorKey, error) {
	keys := Keyset(sort, lq.keys...)
	for _, k := range keys {
		if k.Column == "" {
			return nil, core.NewError(ECodeSortFieldNotAllowed, fmt.Sprintf("sort field '%s' is computed and cannot be used with cursor pagination", k.Name),
				core.WithField(k.Name))
		}
	}
	return keys, nil
}
