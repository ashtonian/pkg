package parse

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// ListRequest is a list request validated against its resource: what the
// caller asked for. Nothing in it is trusted — the server's decisions travel
// in a Scope.
type ListRequest struct {
	// Filter is the JSON filter parameter ANDed with the flat field
	// parameters, bounded as one expression; nil filters nothing.
	Filter *filter.ParsedFilter
	// Sort is the requested order; nil orders by the key.
	Sort     *filter.SortResult
	Cursor   string
	PageSize int
	// IncludeTotal asks for the matched population's size.
	IncludeTotal bool
	// IncludeDeleted asks for soft-deleted rows too; Scope.IncludeDeleted
	// decides whether the caller gets them.
	IncludeDeleted bool
	Search         string
	// Facets names facetable fields to count by.
	Facets []string
	// Include names relation fields whose IDs to project beside the page.
	Include []string
	// Fields is the response field mask: the fields each item returns, by
	// name under the field-name tag; empty is every field. Each is a field a
	// response carries, one whose column the resource reads or one with no
	// column, which the service fills. The service applies the mask to the
	// items it writes; the page's SELECT reads every read column whatever
	// the mask names.
	Fields []string
}

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
func (r *Resource[T]) ParseListInput(in ListInput) (*ListRequest, error) {
	req := &ListRequest{
		Cursor: in.Cursor, PageSize: r.pageSize,
		IncludeTotal: in.IncludeTotal, IncludeDeleted: in.IncludeDeleted,
	}
	switch {
	case in.PageSize < 0:
		return nil, listParamError(filter.ParamPageSize, fmt.Sprintf("page_size must not be negative, got %d", in.PageSize))
	case in.PageSize > 0:
		req.PageSize = min(in.PageSize, r.maxPageSize)
	}
	if req.IncludeDeleted && r.deleted == "" {
		return nil, listParamError(filter.ParamIncludeDeleted, r.table+" has no deleted rows")
	}
	if req.Search = strings.TrimSpace(in.Search); req.Search != "" && !r.searchable() {
		return nil, listParamError(filter.ParamSearch, r.table+" has no search fields")
	}

	var err error
	if req.Filter, err = r.parseListFilter(in.Filter, in.FieldFilters); err != nil {
		return nil, err
	}
	if in.Sort != "" {
		if req.Sort, err = filter.ParseSort(r.sortSchema, in.Sort); err != nil {
			return nil, err
		}
	}
	if req.Include, err = filter.ParseIncludeNames(r.filterSchema, in.Include); err != nil {
		return nil, err
	}
	if req.Facets, err = listNames(in.Facets, r.facetName); err != nil {
		return nil, err
	}
	if req.Fields, err = listNames(in.Fields, r.maskName); err != nil {
		return nil, err
	}
	return req, nil
}

func (r *Resource[T]) facetName(name string) error {
	if f, ok := r.filterSchema.Fields[name]; ok && f.Facetable {
		return nil
	}
	return unknownListField(fmt.Sprintf("facet field '%s' does not exist or is not facetable", name), name)
}

// maskName refuses a field mask name that is no field of the response,
// without saying whether T has a field by that name its response withholds.
func (r *Resource[T]) maskName(name string) error {
	if r.maskable[name] {
		return nil
	}
	return unknownListField(fmt.Sprintf("response field '%s' does not exist", name), name)
}

func unknownListField(msg, name string) *core.ParseError {
	return &core.ParseError{
		Kind: core.ParseErrorKindFilter, Code: filter.ECodeUnknownField.String(),
		Message: msg, Details: map[string]any{"field": name},
	}
}

// ParseList reads a list request's query parameters (filter.Param*) into a
// ListInput and validates it with ParseListInput. A page size that is not a
// positive integer, or a flag that is not a boolean, is E2067. Every other
// parameter that AcceptParams does not name is a field filter, so a misspelt
// filter is E2071 and cannot widen a list.
func (r *Resource[T]) ParseList(q url.Values) (*ListRequest, error) {
	in, err := r.listInput(q)
	if err != nil {
		return nil, err
	}
	return r.ParseListInput(in)
}

// listInput is the transport half of ParseList: text to numbers and booleans,
// comma-separated names to lists, and the remaining parameters to field
// filters.
func (r *Resource[T]) listInput(q url.Values) (ListInput, error) {
	in := ListInput{
		Filter:  q.Get(filter.ParamFilter),
		Sort:    q.Get(filter.ParamSort),
		Cursor:  q.Get(filter.ParamCursor),
		Search:  q.Get(filter.ParamSearch),
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
	var err error
	if in.IncludeTotal, err = boolParam(q, filter.ParamIncludeTotal); err != nil {
		return ListInput{}, err
	}
	if in.IncludeDeleted, err = boolParam(q, filter.ParamIncludeDeleted); err != nil {
		return ListInput{}, err
	}
	for key, values := range q {
		if len(values) == 0 || filter.IsListParam(key) || slices.Contains(r.accepted, key) {
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
func (r *Resource[T]) parseListFilter(doc string, fields map[string][]string) (*filter.ParsedFilter, error) {
	fp := filter.NewFilterParser(r.filterSchema, r.cfg)
	jsonAST, err := fp.Parse(doc)
	if err != nil {
		return nil, err
	}
	flatAST, err := filter.NewQueryParamParser(r.filterSchema, filter.QueryParamFieldsOnly(), filter.QueryParamConfig(r.cfg)).Parse(fields)
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
	return filter.NewParsedFilter(ast), nil
}

func (r *Resource[T]) searchable() bool {
	return r.searchSchema != nil && len(r.searchSchema.Fields) > 0
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
	return &core.ParseError{
		Kind: core.ParseErrorKindFilter, Code: filter.ECodeInvalidListParam.String(),
		Message: msg, Details: map[string]any{"parameter": param},
	}
}

// Scope is what the server decides about a list; nothing in it comes from
// the request.
type Scope struct {
	// Tenant holds rows, and tenant-scoped relation junctions, to one tenant.
	// Required for a resource with a tenant column. It is the tenant key's
	// text, normalized once to the type of every tenant column the resource
	// reaches: the key of a string column, a UUID's canonical text, or an
	// integer's decimal digits ("44"). Text one of them cannot hold is
	// ErrTenantInvalid.
	Tenant string
	// Governance, when set, replaces the tenant floor: it decides tenancy
	// itself, grants included (filter.ComposeConstraints).
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

// ErrTenantRequired is a list of a tenant-scoped resource built without a
// tenant. It is the server's mistake, never the request's.
var ErrTenantRequired = errors.New("parse: a tenant-scoped resource is listed without Scope.Tenant")

// Statement is one parameterized SQL statement.
type Statement struct {
	SQL  string
	Args []any
}

// ListPlan is a list request compiled for one dialect.
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

	dialect filter.Dialect
	// fields is the filter schema the plan compiled, its includes' targets
	// compiled for dialect.
	fields *filter.ResourceSchema
	table  string
	// tenant is Scope.Tenant in the tenant key's type; nil without one.
	tenant  any
	related map[string]*filter.PolicyConstraints
}

// Dialect is the dialect the plan's statements are written for.
func (p *ListPlan) Dialect() filter.Dialect { return p.dialect }

// BuildList compiles req for dialect d under scope s: the tenant floor (or
// governance), soft-delete masking and policy ANDed with the request's
// filter, the request's sort with the key as the final keyset column, the
// cursor's seek, search, and the count and facets when asked. A nil req is a
// first page with no filter. Computed columns (ReadExpr) compile as d's
// expressions; a dialect one refuses is an error.
func (r *Resource[T]) BuildList(d filter.Dialect, req *ListRequest, s Scope) (*ListPlan, error) {
	v, err := r.dialectView(d)
	if err != nil {
		return nil, err
	}
	if req == nil {
		req = &ListRequest{}
	}
	tenant, err := r.bindTenant(s.Tenant)
	if err != nil {
		return nil, err
	}
	policy := filter.ComposeConstraints(filter.RepositoryScope{
		TenantColumn: r.tenant, TenantID: tenant,
		DeleteColumn: r.deleted, IncludeDeleted: s.IncludeDeleted && r.deleted != "",
	}, s.Governance, s.Policy)

	// Relations the facets, includes and policies reach compile their
	// targets for d; the filter's own reach theirs as filterAST walks it.
	if v, err = v.withTargets(d, slices.Concat(req.Facets, req.Include, policyFields(policy))...); err != nil {
		return nil, err
	}

	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = r.pageSize
	}
	opts := []filter.ListOption{
		filter.ListLimit(pageSize),
		filter.ListMaxLimit(r.maxPageSize),
		filter.ListPolicy(policy),
		filter.ListCursor(req.Cursor),
		filter.ListSearch(req.Search),
		filter.ListWithCount(req.IncludeTotal),
		filter.ListRelatedPolicies(s.Related),
	}
	if tenant != nil {
		opts = append(opts, filter.ListTenant(tenant))
	}
	if req.Filter != nil {
		ast, err := v.filterAST(d, req.Filter.AST)
		if err != nil {
			return nil, err
		}
		opts = append(opts, filter.ListFilterAST(ast))
	}
	if req.Sort != nil {
		opts = append(opts, filter.ListSortResult(v.sortResult(req.Sort)))
	}
	if len(req.Facets) > 0 {
		opts = append(opts, filter.ListFacets(req.Facets...))
	}
	res, err := v.listQuery(d, r.cfg).Build(opts...)
	if err != nil {
		return nil, err
	}
	plan := &ListPlan{
		Page:     Statement{SQL: "SELECT " + strings.Join(r.columns(d, v), ", ") + " FROM " + r.table + " " + res.Fragment, Args: res.Args},
		Fragment: res.Fragment,
		Facets:   res.Facets,
		Include:  slices.Clone(req.Include),
		Keyset:   res.Cursor,
		Limit:    res.Limit,
		dialect:  d,
		fields:   v.filter,
		table:    r.table,
		tenant:   tenant,
		related:  s.Related,
	}
	if req.IncludeTotal {
		plan.Count = &Statement{SQL: res.CountSQL, Args: res.CountArgs}
	}
	return plan, nil
}

// Page builds the page from rows fetched with plan.Page, in query order:
// truncated to the limit, restored to presentation order after a backward
// seek, with has-next / has-previous and both cursors read from the rows'
// columns.
func (r *Resource[T]) Page(items []T, plan *ListPlan) (*filter.Page[T], error) {
	if err := r.ownsPlan(plan); err != nil {
		return nil, err
	}
	return filter.NewPage(items, plan.Limit, plan.Keyset, func(item T, column string) (any, error) {
		// Not addressable: Interface then reads the field without copying it.
		return r.columnValue(reflect.ValueOf(item), column)
	})
}

// PagePointers is Page for rows scanned as pointers.
func (r *Resource[T]) PagePointers(items []*T, plan *ListPlan) (*filter.Page[*T], error) {
	if err := r.ownsPlan(plan); err != nil {
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
func (r *Resource[T]) IncludeQueries(plan *ListPlan, ownerKeys []any) ([]filter.IncludeQuery, error) {
	if err := r.ownsPlan(plan); err != nil {
		return nil, err
	}
	if len(plan.Include) == 0 || len(ownerKeys) == 0 {
		return nil, nil
	}
	opts := []filter.IncludeQueryOption{filter.WithIncludeRelatedPolicies(plan.related)}
	if plan.tenant != nil {
		opts = append(opts, filter.WithIncludeTenant(plan.tenant))
	}
	out := make([]filter.IncludeQuery, 0, len(plan.Include))
	for _, name := range plan.Include {
		q, err := filter.BuildIncludeQuery(plan.fields.Fields[name], plan.dialect, ownerKeys, opts...)
		if err != nil {
			return nil, err
		}
		if q.SQL != "" {
			out = append(out, q)
		}
	}
	return out, nil
}

// ownsPlan refuses a plan r did not build: its keyset and includes name
// another table's columns.
func (r *Resource[T]) ownsPlan(plan *ListPlan) error {
	if plan == nil {
		return fmt.Errorf("parse: %s: nil list plan", r.table)
	}
	if plan.table != r.table {
		return fmt.Errorf("parse: %s: list plan built for %q", r.table, plan.table)
	}
	return nil
}
