package parse

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// ListRequest is a list request validated against its resource: what the
// caller asked for. Nothing in it is trusted — the server's decisions travel
// in a Scope. Only a resource's parsers make one (ParseList, ParseListInput,
// ParseListBody, ParseListPost), only that resource's BuildList compiles it
// (ErrForeign), and its fields are read through methods: a server that
// wants another request parses another ListInput.
type ListRequest struct {
	filter         *filter.ParsedFilter
	sort           *filter.SortResult
	cursor         string
	pageSize       int
	includeTotal   bool
	includeDeleted bool
	search         string
	facets         []string
	include        []string
	fields         []string
	// members are the JSON members fields encode as (FieldMembers).
	members []string
	// now is the instant the request was parsed at: its filter's
	// relative-time tokens resolved against it, and BuildList resolves its
	// policy's, the related tables' policies' and its includes' against it.
	now time.Time
	// schema is the filter schema of the resource that parsed the request.
	schema *filter.ResourceSchema
}

// Filter is the JSON filter parameter ANDed with the flat field
// parameters, bounded as one expression; nil filters nothing.
func (req *ListRequest) Filter() *filter.ParsedFilter { return req.filter }

// WithFilter returns a copy of the request filtered by f instead: its own
// filter with the memberships resolved (filter.ResolveMemberships), or
// another filter its resource parsed; nil filters nothing. A filter parsed
// for another schema is ErrForeign.
func (req *ListRequest) WithFilter(f *filter.ParsedFilter) (*ListRequest, error) {
	if f != nil && !f.ParsedFor(req.schema) {
		return nil, fmt.Errorf("parse: %s: list request filter: %w", req.schema.Name, ErrForeign)
	}
	out := *req
	out.filter = f
	return &out, nil
}

// Sort is the requested order; nil orders by the key.
func (req *ListRequest) Sort() *filter.SortResult {
	if req.sort == nil {
		return nil
	}
	return &filter.SortResult{Fields: slices.Clone(req.sort.Fields)}
}

// Cursor is the opaque cursor the request resumes after; empty is the first
// page.
func (req *ListRequest) Cursor() string { return req.cursor }

// PageSize is the page size: the request's, clamped to the resource's
// maximum, or the resource's default.
func (req *ListRequest) PageSize() int { return req.pageSize }

// IncludeTotal reports whether the request asks for the matched
// population's size.
func (req *ListRequest) IncludeTotal() bool { return req.includeTotal }

// IncludeDeleted reports whether the request asks for soft-deleted rows too;
// Scope.IncludeDeleted decides whether the caller gets them.
func (req *ListRequest) IncludeDeleted() bool { return req.includeDeleted }

// Search is the free-text search, trimmed; empty searches nothing.
func (req *ListRequest) Search() string { return req.search }

// Facets names facetable fields to count by.
func (req *ListRequest) Facets() []string { return slices.Clone(req.facets) }

// Include names relation fields whose IDs to project beside the page.
func (req *ListRequest) Include() []string { return slices.Clone(req.include) }

// Fields is the response field mask: the fields each item returns, by name
// under the field-name tag; empty is every field. Each is a field a
// response carries, one whose column the resource reads or one with no
// column, which the service fills. The service applies the mask to the
// items it writes; the page's SELECT reads every read column whatever the
// mask names.
func (req *ListRequest) Fields() []string { return slices.Clone(req.fields) }

// FieldMembers is the field mask as the JSON members its fields encode as,
// the names a JSON response narrows each item to (parsehttp.WriteList). They
// are the Fields when the field-name tag is json. Under another tag each is
// the name encoding/json gives the same struct field, and a field it writes
// no member for has none, so a mask can name fields and have no members.
func (req *ListRequest) FieldMembers() []string { return slices.Clone(req.members) }

// ListInput is a list request in transport-neutral form: the parameters
// ParseList reads from a query string, already typed and split.
// ParseListInput validates it as ParseList validates a query, with the same
// limits and error codes, so an RPC or message handler serves the same lists
// as an HTTP endpoint.
type ListInput struct {
	// Filter is a filter document in the JSON grammar; empty filters nothing.
	Filter string
	// FieldFilters are flat filters, ANDed with Filter: a filterable field's
	// name, or the name with an operator suffix (rank_gte, tags_in), to its
	// values as text. Several values on an equality key match any of them.
	// Every key names a field; none is read as a list parameter.
	FieldFilters map[string][]string
	// Sort is the sort grammar: comma-separated fields, '-' for descending.
	Sort   string
	Cursor string
	// PageSize zero takes the resource's default; a size above its maximum is
	// clamped, and a negative one is E2067.
	PageSize       int
	IncludeTotal   bool
	IncludeDeleted bool
	Search         string
	// Facets, Include and Fields are field names. Each name is trimmed, and
	// empty and repeated names are dropped.
	Facets  []string
	Include []string
	Fields  []string
}

// ParseListInput validates a list request against its resource: the JSON and
// flat filters, bounded as one expression, sort, cursor, page size, the total,
// deleted rows, search, facets, includes and the field mask. A negative page
// size, deleted rows of a resource without them, or search on a resource
// without search fields is E2067; a field filter, facet, include or field
// mask name that names no field it can serve is E2071 (an include that is no
// relation, E2083).
func (r *resourceCore) ParseListInput(in ListInput) (*ListRequest, error) {
	req := &ListRequest{
		cursor: in.Cursor, pageSize: r.pageSize,
		includeTotal: in.IncludeTotal, includeDeleted: in.IncludeDeleted,
		now: r.cfg.Now(), schema: r.filterSchema,
	}
	switch {
	case in.PageSize < 0:
		return nil, listParamError(filter.ParamPageSize, fmt.Sprintf("page_size must not be negative, got %d", in.PageSize))
	case in.PageSize > 0:
		req.pageSize = min(in.PageSize, r.maxPageSize)
	}
	if req.includeDeleted && r.deleted == "" {
		return nil, listParamError(filter.ParamIncludeDeleted, r.table+" has no deleted rows")
	}
	if req.search = strings.TrimSpace(in.Search); req.search != "" && !r.searchable() {
		return nil, listParamError(filter.ParamSearch, r.table+" has no search fields")
	}

	var err error
	if req.filter, err = r.parseListFilter(in.Filter, in.FieldFilters, req.now); err != nil {
		return nil, err
	}
	if in.Sort != "" {
		if req.sort, err = filter.ParseSort(r.sortSchema, in.Sort); err != nil {
			return nil, err
		}
	}
	if req.include, err = filter.ParseIncludeNames(r.filterSchema, in.Include); err != nil {
		return nil, err
	}
	if req.facets, err = listNames(in.Facets, r.facetName); err != nil {
		return nil, err
	}
	if req.fields, err = listNames(in.Fields, r.maskName); err != nil {
		return nil, err
	}
	for _, name := range req.fields {
		member := r.maskable[name]
		if member != "" {
			req.members = append(req.members, member)
		}
	}
	return req, nil
}

func (r *resourceCore) facetName(name string) error {
	if f, ok := r.filterSchema.Fields[name]; ok && f.Facetable {
		return nil
	}
	return unknownListField(fmt.Sprintf("facet field '%s' does not exist or is not facetable", name), name)
}

// maskName refuses a field mask name that is no field of the response,
// without saying whether T has a field by that name its response withholds.
func (r *resourceCore) maskName(name string) error {
	_, ok := r.maskable[name]
	if ok {
		return nil
	}
	return unknownListField(fmt.Sprintf("response field '%s' does not exist", name), name)
}

func unknownListField(msg, name string) *core.ParseError {
	return core.NewError(filter.ECodeUnknownField, msg, core.WithField(name))
}

// ParseList reads a list request's query parameters (filter.Param*) into a
// ListInput and validates it with ParseListInput. A list parameter given
// more than once, a page size that is not a positive integer, or a flag that
// is not a boolean, is E2067: facets, include and fields are one
// comma-separated value, as the body form takes one member each. The search
// is read from filter.ParamSearch or from the parameter the search schema
// advertises (core.WithSearchParam), and a request naming both is E2067.
// Every other parameter that AcceptParams does not name is a field filter,
// so a misspelt filter is E2071 and cannot widen a list.
func (r *resourceCore) ParseList(q url.Values) (*ListRequest, error) {
	in, err := r.listInput(q)
	if err != nil {
		return nil, err
	}
	return r.ParseListInput(in)
}

// listInput is the transport half of ParseList: text to numbers and booleans,
// comma-separated names to lists, and the remaining parameters to field
// filters.
func (r *resourceCore) listInput(q url.Values) (ListInput, error) {
	err := r.listParamsOnce(q)
	if err != nil {
		return ListInput{}, err
	}
	search, err := r.searchValue(q)
	if err != nil {
		return ListInput{}, err
	}
	in := ListInput{
		Filter:  q.Get(filter.ParamFilter),
		Sort:    q.Get(filter.ParamSort),
		Cursor:  q.Get(filter.ParamCursor),
		Search:  search,
		Facets:  splitNames(q.Get(filter.ParamFacets)),
		Include: splitNames(q.Get(filter.ParamInclude)),
		Fields:  splitNames(q.Get(filter.ParamFields)),
	}
	if raw := q.Get(filter.ParamPageSize); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return ListInput{}, listParamError(filter.ParamPageSize, fmt.Sprintf("page_size must be a positive integer, got %q", raw))
		}
		in.PageSize = n
	}
	if in.IncludeTotal, err = boolParam(q, filter.ParamIncludeTotal); err != nil {
		return ListInput{}, err
	}
	if in.IncludeDeleted, err = boolParam(q, filter.ParamIncludeDeleted); err != nil {
		return ListInput{}, err
	}
	for key, values := range q {
		if len(values) == 0 || r.isListParam(key) || slices.Contains(r.accepted, key) {
			continue
		}
		if in.FieldFilters == nil {
			in.FieldFilters = make(map[string][]string, len(q))
		}
		in.FieldFilters[key] = values
	}
	return in, nil
}

// parseListFilter is the JSON filter ANDed with the field filters, strictly:
// a field filter naming no field is E2071.
func (r *resourceCore) parseListFilter(doc string, fields map[string][]string, now time.Time) (*filter.ParsedFilter, error) {
	fp := filter.NewFilterParser(r.filterSchema, r.cfg).At(now)
	jsonAST, err := fp.Parse(doc)
	if err != nil {
		return nil, err
	}
	qp, err := filter.NewQueryParamParser(r.filterSchema, filter.QueryParamFieldsOnly(), filter.QueryParamConfig(r.cfg))
	if err != nil {
		return nil, err
	}
	flatAST, err := qp.At(now).Parse(fields)
	if err != nil {
		return nil, err
	}
	ast, err := fp.And(jsonAST, flatAST)
	if err != nil {
		return nil, err
	}
	if ast == nil || ast.Root == nil {
		return nil, nil
	}
	return filter.NewParsedFilterFor(r.filterSchema, ast), nil
}

func (r *resourceCore) searchable() bool {
	return r.searchSchema != nil && len(r.searchSchema.Fields) > 0
}

// isListParam reports whether a query parameter is one of r's list
// parameters: filter.Param*, or the search parameter its search schema
// advertises.
func (r *resourceCore) isListParam(name string) bool {
	return filter.IsListParam(name) || (r.searchParam != "" && name == r.searchParam)
}

// listParamsOnce refuses a list parameter (isListParam) given more than
// once. q.Get reads the first, so a second filter, sort or page size would
// be dropped without a word; flat field filters give a repeat its meaning, a
// set, and are not checked here.
func (r *resourceCore) listParamsOnce(q url.Values) error {
	for _, name := range slices.Sorted(maps.Keys(q)) {
		n := len(q[name])
		if n > 1 && r.isListParam(name) {
			return listParamError(name, fmt.Sprintf("%s is given %d times; a list parameter is given once", name, n))
		}
	}
	return nil
}

// searchValue reads the free-text search from q: from filter.ParamSearch,
// the parameter every client sends, or from the parameter the search schema
// advertises in its place (core.WithSearchParam). A request that names both
// gives the search twice, which is E2067, as a repeated list parameter is.
func (r *resourceCore) searchValue(q url.Values) (string, error) {
	if r.searchParam == "" {
		return q.Get(filter.ParamSearch), nil
	}
	_, canonical := q[filter.ParamSearch]
	_, advertised := q[r.searchParam]
	switch {
	case canonical && advertised:
		return "", listParamError(r.searchParam, fmt.Sprintf("%s and %s are the one search parameter; give it once", r.searchParam, filter.ParamSearch))
	case advertised:
		return q.Get(r.searchParam), nil
	}
	return q.Get(filter.ParamSearch), nil
}

// setSearchParam takes the query parameter the search schema advertises
// (core.WithSearchParam; empty is filter.ParamSearch), which ParseList reads
// as well as filter.ParamSearch.
func (r *resourceCore) setSearchParam() {
	if r.searchSchema == nil {
		return
	}
	if r.searchSchema.Param == "" {
		r.searchSchema.Param = filter.ParamSearch
	}
	if r.searchSchema.Param != filter.ParamSearch {
		r.searchParam = r.searchSchema.Param
	}
}

// checkQueryParams refuses a query parameter ParseList reads other than as
// a field filter that it would otherwise read as something else: the
// search parameter, or one the endpoint reads itself (AcceptParams), that
// is a list parameter or a flat filter on a request field of fs. ParseList
// would read the one meaning and drop the other: a flat filter it skipped
// would list every row its schema document says it narrows.
func (r *resourceCore) checkQueryParams(fs *filter.ResourceSchema) error {
	p := r.searchParam
	if p != "" && slices.Contains(r.accepted, p) {
		return fmt.Errorf("search parameter %q is a parameter the endpoint reads itself (AcceptParams %q)", p, p)
	}
	for _, own := range r.ownParams() {
		err := filter.CheckAcceptedParam(fs, own.name)
		if err == nil {
			continue
		}
		collision, ok := errors.AsType[*filter.AcceptedParamError](err)
		switch {
		case !ok:
			return fmt.Errorf("%s: %w", own, err)
		case collision.Field == "":
			return fmt.Errorf("%s is the list parameter %s", own, own.name)
		}
		return fmt.Errorf("%s is a flat filter on field %q", own, collision.Field)
	}
	return nil
}

// An ownParam is a query parameter ParseList reads other than as a field
// filter, named as what declares it.
type ownParam struct {
	name string
	// accepted is an AcceptParams name; otherwise it is the search
	// parameter.
	accepted bool
}

func (p ownParam) String() string {
	if p.accepted {
		return fmt.Sprintf("accepted parameter %q (AcceptParams)", p.name)
	}
	return fmt.Sprintf("search parameter %q", p.name)
}

// ownParams are r's search parameter, when its search schema advertises one
// other than filter.ParamSearch, and its AcceptParams names.
func (r *resourceCore) ownParams() []ownParam {
	params := make([]ownParam, 0, len(r.accepted)+1)
	if r.searchParam != "" {
		params = append(params, ownParam{name: r.searchParam})
	}
	for _, name := range r.accepted {
		params = append(params, ownParam{name: name, accepted: true})
	}
	return params
}

func boolParam(q url.Values, name string) (bool, error) {
	raw := q.Get(name)
	if raw == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, listParamError(name, fmt.Sprintf("%s must be true or false, got %q", name, raw))
	}
	return b, nil
}

// splitNames splits a comma-separated parameter; an absent one is no names.
func splitNames(raw string) []string {
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

// listNames trims names and drops empty and repeated ones, refusing a name
// check refuses. Each name is checked before it is kept, so the kept names,
// and the search for repeats among them, are bounded by the fields check
// admits rather than by the request.
func listNames(names []string, check func(name string) error) ([]string, error) {
	var out []string
	for _, name := range names {
		if name = strings.TrimSpace(name); name == "" || slices.Contains(out, name) {
			continue
		}
		if err := check(name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, nil
}

func listParamError(param, msg string) *core.ParseError {
	return core.NewError(filter.ECodeInvalidListParam, msg, core.WithParameter(param))
}

// Scope is what the server decides about a list; nothing in it comes from
// the request.
type Scope struct {
	// Tenant holds rows, and tenant-scoped relation junctions, to one tenant.
	// Required for a resource with a tenant column, unless Governance is
	// set. It is the tenant key's text, normalized once to the type of
	// every tenant column the resource reaches: the key of a string column,
	// a UUID's canonical text, or an integer's decimal digits ("44"). Text
	// one of them cannot hold is ErrTenantInvalid.
	Tenant string
	// Governance, when set and not empty (filter.PolicyConstraints.IsEmpty:
	// no constraint at any depth), replaces the tenant floor: it
	// decides which rows of which tenants the list reads, grants included
	// (filter.ComposeConstraints). A row's relations are its own tenant's:
	// relation predicates, counts, facets and includes hold every
	// tenant-scoped related row, at every depth, to the tenant of the
	// listed row it hangs from (filter.RelationScope.RowTenant), so a granted row of
	// another tenant is filtered and included by its own related rows.
	// Tenant is then not used; on a resource without a tenant column,
	// whose rows have no tenant, relations stay held to Tenant.
	Governance *filter.PolicyConstraints
	// Policy is the authorization constraint, ANDed with everything else.
	// A constraint names a system field, or exactly one of a request field
	// by name, a field without the filter capability by external name, or
	// any field by its column, hidden ones included; a name designating
	// more than one is filter.ErrAmbiguousPolicyField
	// (filter.ResourceSchema.PolicyField).
	Policy *filter.PolicyConstraints
	// Related is the caller's policy for the target table of each
	// policy-scoped relation (PolicyScoped), keyed by table: the related
	// rows a relation predicate may reach. A predicate on such a relation
	// without its table's policy is filter.ErrRelatedPolicyRequired.
	Related map[string]*filter.PolicyConstraints
	// IncludeDeleted shows soft-deleted rows.
	IncludeDeleted bool
}

// governed reports whether Governance replaces the tenant floor, as
// filter.ComposeConstraints decides: a nil or empty tree does not.
func (s Scope) governed() bool { return s.Governance != nil && !s.Governance.IsEmpty() }

// ErrTenantRequired is a list of a tenant-scoped resource built with
// neither Scope.Tenant nor Scope.Governance, or an update without
// Scope.Tenant. It is the server's mistake, never the request's.
var ErrTenantRequired = errors.New("a tenant-scoped resource needs Scope.Tenant")

// Statement is one parameterized SQL statement.
type Statement struct {
	SQL  string
	Args []any
}

// ListPlan is a list request compiled for its resource's provider
// (BuildList).
type ListPlan struct {
	// Page selects Columns for the page: up to Limit+1 rows, in keyset order.
	Page Statement
	// Fragment is Page's `WHERE … ORDER BY … LIMIT n` alone, with Page.Args,
	// for a caller that selects its own projection from the resource's table.
	Fragment string
	// Count is the matched population's size — filter, policy and search,
	// no cursor — when the request asked for it.
	Count *Statement
	// Facets counts the same population by the requested fields.
	Facets *filter.FacetQuery
	// Include names the relation fields to read once the page's keys are
	// known (IncludeQueries).
	Include []string
	// Keyset turns the fetched rows into a page (Page).
	Keyset filter.CursorPlan
	Limit  int

	// fields is the filter schema the plan compiled, its includes' targets
	// included.
	fields *filter.ResourceSchema
	table  string
	// relations is the scope the plan's statements reach related rows
	// with, and its includes read them with: Scope.Tenant in the tenant
	// key's type, or, for a governed list (Scope.Governance), each owner
	// row's own tenant; and Scope.Related.
	relations filter.RelationScope
}

// BuildList compiles req under scope s into the statements List runs: the
// tenant floor (or governance), soft-delete masking and policy ANDed with
// the request's filter, the request's sort with the key as the final keyset
// column, the cursor's seek, search, and the count and facets when asked.
// The SQL is the provider's dialect's. A nil req is a first page with no
// filter; a request another resource parsed, a WithPaths view's or its
// base's included, is ErrForeign.
func (r *resourceCore) BuildList(req *ListRequest, s Scope) (*ListPlan, error) {
	if req == nil {
		req = &ListRequest{pageSize: r.pageSize, now: r.cfg.Now(), schema: r.filterSchema}
	}
	if req.schema != r.filterSchema {
		return nil, fmt.Errorf("parse: %s: list request: %w", r.table, ErrForeign)
	}
	// A governed list's rows span tenants; a resource with a tenant column
	// holds each row's relations to the row's own.
	rowTenant := s.governed() && r.tenant != ""
	var (
		tenant any
		err    error
	)
	if !rowTenant {
		if tenant, err = r.bindTenant(s.Tenant); err != nil {
			return nil, err
		}
	}
	policy := filter.ComposeConstraints(filter.RepositoryScope{
		TenantColumn: r.tenant, TenantID: tenant,
		DeleteColumn: r.deleted, IncludeDeleted: s.IncludeDeleted && r.deleted != "",
	}, s.Governance, s.Policy)

	relations := filter.RelationScope{Tenant: tenant, Related: s.Related, Now: req.now}
	if rowTenant {
		relations.RowTenant = r.tenant
	}
	opts := []filter.ListOption{
		filter.ListLimit(req.pageSize),
		filter.ListMaxLimit(r.maxPageSize),
		filter.ListPolicy(policy),
		filter.ListNow(req.now),
		filter.ListCursor(req.cursor),
		filter.ListSearch(req.search),
		filter.ListWithCount(req.includeTotal),
		filter.ListRelationScope(relations),
	}
	if req.filter != nil {
		opts = append(opts, filter.ListFilterAST(req.filter.AST()))
	}
	if req.sort != nil {
		opts = append(opts, filter.ListSortResult(req.sort))
	}
	if len(req.facets) > 0 {
		opts = append(opts, filter.ListFacets(req.facets...))
	}
	lq, err := newListQuery(r.filterSchema, r.sortSchema, r.searchSchema, r.dialect, r.cfg)
	if err != nil {
		return nil, err
	}
	res, err := lq.Build(opts...)
	if err != nil {
		return nil, err
	}
	plan := &ListPlan{
		Page:      Statement{SQL: "SELECT " + r.projection + " FROM " + r.dialect.QuoteIdent(r.table) + " " + res.Fragment, Args: res.Args},
		Fragment:  res.Fragment,
		Facets:    res.Facets,
		Include:   slices.Clone(req.include),
		Keyset:    res.Cursor,
		Limit:     res.Limit,
		fields:    r.filterSchema,
		table:     r.table,
		relations: relations,
	}
	if req.includeTotal {
		plan.Count = &Statement{SQL: res.CountSQL, Args: res.CountArgs}
	}
	return plan, nil
}

// Page builds the page from rows fetched with plan.Page, in query order:
// truncated to the limit, restored to presentation order after a backward
// seek, with has-next / has-previous and both cursors read from the rows'
// columns.
func (r *Resource[T]) Page(items []T, plan *ListPlan) (*filter.Page[T], error) {
	err := r.ownsPlan(plan)
	if err != nil {
		return nil, err
	}
	return filter.NewPage(items, plan.Limit, plan.Keyset, func(item T, column string) (any, error) {
		// Not addressable: Interface then reads the field without copying it.
		return r.columnValue(reflect.ValueOf(item), column)
	})
}

// PagePointers is Page for rows scanned as pointers.
func (r *Resource[T]) PagePointers(items []*T, plan *ListPlan) (*filter.Page[*T], error) {
	err := r.ownsPlan(plan)
	if err != nil {
		return nil, err
	}
	return filter.NewPage(items, plan.Limit, plan.Keyset, func(item *T, column string) (any, error) {
		return r.columnValue(reflect.ValueOf(item), column)
	})
}

// IncludeQueries returns, for each relation field plan includes, the
// statement reading its IDs for the page's owner keys (Key), scoped as the
// list was. Read each one's rows with query.Scan, then attach the result
// with page.SetInclude(query.Field, rows). No keys need no statements.
func (r *resourceCore) IncludeQueries(plan *ListPlan, ownerKeys []Key) ([]filter.IncludeQuery, error) {
	if err := r.ownsPlan(plan); err != nil {
		return nil, err
	}
	if len(plan.Include) == 0 || len(ownerKeys) == 0 {
		return nil, nil
	}
	owners, err := r.ownerKeys(ownerKeys)
	if err != nil {
		return nil, err
	}
	opts := []filter.IncludeQueryOption{filter.WithIncludeRelationScope(plan.relations), filter.WithIncludeOwner(plan.fields)}
	out := make([]filter.IncludeQuery, 0, len(plan.Include))
	for _, name := range plan.Include {
		q, err := filter.BuildIncludeQuery(plan.fields.Fields[name], r.dialect, owners, opts...)
		if err != nil {
			return nil, err
		}
		if q.SQL != "" {
			out = append(out, q)
		}
	}
	return out, nil
}
