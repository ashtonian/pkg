package parsememory

import (
	"container/heap"
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// List reads the page, the count and the facets in one pass over the
// table's rows, from one snapshot, then, once the page's keys are known
// (backend.ListSink.Keys), the includes from the same snapshot. A failure
// names the part that failed, as the SQL providers name their statements:
// page, facets, or include <field>.
func (e executor) List(ctx context.Context, req backend.ListRequest, sink backend.ListSink) error {
	l, err := e.lister(req.Resource.Table, queryOf(req.List), true)
	if err != nil {
		return err
	}
	res, err := l.pass(ctx)
	if err != nil {
		return err
	}
	page, err := newRows(l.def, req.Resource.Columns, res.page)
	if err == nil {
		err = sink.Page(page)
	}
	if err != nil {
		return parse.StatementFailed(l.table, "page", err)
	}
	if req.List.Count() {
		sink.Count(res.count)
	}
	if len(l.facets) > 0 {
		results, err := l.facetResults(ctx, res)
		if err != nil {
			return parse.StatementFailed(l.table, "facets", err)
		}
		sink.Facets(results)
	}
	if len(req.Include) == 0 {
		return nil
	}
	owners, err := sink.Keys()
	if err != nil || len(owners) == 0 {
		return err
	}
	return l.includes(ctx, req.Include, owners, res.page, sink)
}

// Facets counts the population by the request's facets in one pass, as
// List does; a request naming no facets reads nothing.
func (e executor) Facets(ctx context.Context, req backend.ListRequest) ([]filter.FacetResult, error) {
	if len(req.List.Facets()) == 0 {
		return nil, nil
	}
	l, err := e.lister(req.Resource.Table, queryOf(req.List), false)
	if err != nil {
		return nil, err
	}
	res, err := l.pass(ctx)
	if err != nil {
		return nil, err
	}
	results, err := l.facetResults(ctx, res)
	if err != nil {
		return nil, parse.StatementFailed(l.table, "facets", err)
	}
	return results, nil
}

// query is what a list reads, as its resource composed it
// (filter.ComposedList): the filter, policy included, over the table's
// schema, the keyset and the cursor, the page's limit, the facets and the
// scope its relations resolve under.
type query struct {
	schema *filter.ResourceSchema
	filter *filter.FilterAST
	keys   []filter.CursorKey
	cursor *filter.PageToken
	limit  int
	facets []filter.FacetRequest
	scope  filter.RelationScope
}

// queryOf is list as the store reads it; each facet takes its schema's
// limit.
func queryOf(list *filter.ComposedList) query {
	names := list.Facets()
	facets := make([]filter.FacetRequest, len(names))
	for i, name := range names {
		facets[i] = filter.FacetRequest{Field: name}
	}
	q := query{schema: list.Schema(), filter: list.Filter(), keys: list.Keys(), cursor: list.Cursor(), limit: list.Limit(), facets: facets, scope: list.RelationScope()}
	return q
}

// lister is one list request over one view of the tables.
type lister struct {
	table  string
	def    *tableDef
	q      query
	lookup lookup
	// paged reports a list that reads a page, not facets alone.
	paged bool

	keys []filter.CursorKey
	// keyAt is each key's column position; reverse walks the keyset
	// backward (a page's start cursor), and cursor is the position the
	// page starts past, its values canonical (filter.ValidateCursor).
	keyAt   []int
	reverse bool
	cursor  []any

	facets  []*filter.FacetCount
	facetAt []int

	// where is the filter with its relations and memberships resolved, for
	// a list whose relations are bound to one tenant. A governed list
	// (filter.RelationScope.RowTenant) resolves them per row tenant:
	// tenantAt is the tenant column's position, open the filter with every
	// relation and membership unknown, which decides most rows without
	// resolving them, and byTenant each tenant's resolved filter.
	where    *filter.FilterAST
	tenantAt int
	open     *filter.FilterAST
	byTenant map[tenantGroup]*filter.FilterAST
}

// tenantGroup names a governed list's row tenant: its canonical text, or
// NULL.
type tenantGroup struct {
	null bool
	text string
}

func (e executor) lister(table string, q query, paged bool) (*lister, error) {
	v, err := e.view()
	if err != nil {
		return nil, err
	}
	def, ok := e.db.catalog.table(table)
	if !ok {
		return nil, fmt.Errorf("parsememory: table %q: no resource declares it", table)
	}
	l := &lister{table: table, def: def, q: q, lookup: lookup{catalog: e.db.catalog, view: v}, paged: paged, tenantAt: -1}
	if paged {
		l.keys = q.keys
		l.keyAt = make([]int, len(l.keys))
		for i, k := range l.keys {
			at, ok := def.index[k.Column]
			if !ok {
				return nil, fmt.Errorf("parsememory: sort key %q reads column %q, which table %q does not hold", k.Name, k.Column, table)
			}
			l.keyAt[i] = at
		}
		token := q.cursor
		if token != nil {
			l.reverse = token.Reverse
			l.cursor = make([]any, len(token.Fields))
			for i, f := range token.Fields {
				l.cursor[i] = f.Value
			}
		}
	}
	if len(q.facets) > 0 {
		l.facets, err = filter.NewFacetCounts(q.schema, q.facets)
		if err != nil {
			return nil, err
		}
		l.facetAt = make([]int, len(l.facets))
		for i, f := range l.facets {
			l.facetAt[i] = -1
			if f.Relation {
				continue
			}
			at, ok := def.index[f.Field.RowColumn()]
			if !ok {
				return nil, fmt.Errorf("parsememory: facet %q reads column %q, which table %q does not hold", f.Field.ExternalName, f.Field.RowColumn(), table)
			}
			l.facetAt[i] = at
		}
	}
	return l, nil
}

// passResult is what one pass over the table finds: the page's rows, up to
// the limit plus one in walk order, the population's size, and for
// relation facets its rows by tenant.
type passResult struct {
	page       []*record
	count      int64
	population map[tenantGroup][]*record
	// tenants are the row tenants population groups, by group.
	tenants map[tenantGroup]any
}

// pass reads every row once: each the filter selects is counted and
// faceted, and, past the cursor, offered to a bounded selection of the
// first limit plus one rows in walk order, so the page's memory is bounded
// by the limit and no row is sorted that the page does not hold. Each
// row's sort keys are read to their canonical values once.
func (l *lister) pass(ctx context.Context) (passResult, error) {
	err := l.resolve(ctx)
	if err != nil {
		return passResult{}, err
	}
	res := passResult{}
	relationFacets := slices.ContainsFunc(l.facets, func(f *filter.FacetCount) bool { return f.Relation })
	if relationFacets {
		res.population = map[tenantGroup][]*record{}
		res.tenants = map[tenantGroup]any{}
	}
	top := &topK{limit: l.q.limit + 1, less: l.walkLess}
	t := l.lookup.view.rows(l.table)
	if t == nil {
		return res, nil
	}
	var passErr error
	n := 0
	t.Scan(func(rec *record) bool {
		n++
		if n%checkEvery == 0 {
			passErr = ctx.Err()
			if passErr != nil {
				return false
			}
		}
		passErr = l.offer(ctx, rec, &res, top)
		return passErr == nil
	})
	if passErr != nil {
		return passResult{}, passErr
	}
	res.page = top.sorted()
	if top.err != nil {
		return passResult{}, parse.StatementFailed(l.table, "page", top.err)
	}
	return res, nil
}

// offer passes rec, one row, through the list: the filter, the count, the
// facets and the page.
func (l *lister) offer(ctx context.Context, rec *record, res *passResult, top *topK) error {
	selected, group, tenant, err := l.selects(ctx, rec)
	if err != nil || !selected {
		return err
	}
	res.count++
	for i, f := range l.facets {
		if f.Relation {
			continue
		}
		err = f.Add(rec.value(l.facetAt[i]))
		if err != nil {
			return parse.StatementFailed(l.table, "facets", err)
		}
	}
	if res.population != nil {
		res.population[group] = append(res.population[group], rec)
		res.tenants[group] = tenant
	}
	if !l.paged {
		return nil
	}
	row, err := l.sortRow(rec)
	if err != nil {
		return parse.StatementFailed(l.table, "page", err)
	}
	if l.cursor != nil {
		past, err := l.past(row.values)
		if err != nil || !past {
			return parse.StatementFailed(l.table, "page", err)
		}
	}
	top.offer(row)
	return nil
}

// resolve resolves the filter's relations and memberships for a list bound
// to one tenant, or prepares a governed list's per-tenant resolution.
func (l *lister) resolve(ctx context.Context) error {
	ast := l.q.filter
	if ast == nil {
		return nil
	}
	err := checkEvaluable(l.table, ast.Root)
	if err != nil {
		return err
	}
	scope := l.q.scope
	if scope.RowTenant == "" {
		resolved, err := filter.ResolveMemberships(ctx, filter.NewParsedFilterFor(l.q.schema, ast), l.lookup, filter.ResolveRelationScope(scope))
		if err != nil {
			return parse.StatementFailed(l.table, "page", err)
		}
		l.where = resolved.AST()
		return nil
	}
	at, ok := l.def.index[scope.RowTenant]
	if !ok {
		return fmt.Errorf("parsememory: row tenant column %q is no column of table %q", scope.RowTenant, l.table)
	}
	l.tenantAt = at
	l.open = &filter.FilterAST{Root: unresolved(ast.Root)}
	l.byTenant = map[tenantGroup]*filter.FilterAST{}
	return nil
}

// selects reports whether the list's filter selects rec, and for a
// governed list the row's tenant group and tenant.
func (l *lister) selects(ctx context.Context, rec *record) (bool, tenantGroup, any, error) {
	if l.tenantAt < 0 {
		ok, err := evaluate(l.where, l.def, rec)
		if err != nil {
			return false, tenantGroup{}, nil, parse.StatementFailed(l.table, "page", err)
		}
		return ok, tenantGroup{}, l.q.scope.Tenant, nil
	}
	group, tenant, err := l.rowTenant(rec)
	if err != nil {
		return false, tenantGroup{}, nil, parse.StatementFailed(l.table, "page", err)
	}
	truth, err := filter.Evaluate(l.open.Root, columnRow(l.def, rec))
	if err != nil {
		return false, tenantGroup{}, nil, parse.StatementFailed(l.table, "page", err)
	}
	if truth != filter.Unknown {
		return truth == filter.True, group, tenant, nil
	}
	where, err := l.tenantFilter(ctx, group, tenant)
	if err != nil {
		return false, tenantGroup{}, nil, err
	}
	ok, err := evaluate(where, l.def, rec)
	if err != nil {
		return false, tenantGroup{}, nil, parse.StatementFailed(l.table, "page", err)
	}
	return ok, group, tenant, nil
}

// rowTenant is rec's tenant in a governed list, and its group: the value
// its relations are bound to, noTenant for NULL.
func (l *lister) rowTenant(rec *record) (tenantGroup, any, error) {
	v := rec.value(l.tenantAt)
	_, null, err := core.StoredValue(v)
	if err != nil {
		return tenantGroup{}, nil, err
	}
	if null {
		group := tenantGroup{null: true}
		return group, noTenant{}, nil
	}
	text, err := core.KeyTextOf([]core.TypeInfo{l.def.columns[l.tenantAt].typ}, core.Key{v})
	if err != nil {
		return tenantGroup{}, nil, fmt.Errorf("tenant column %s: %w", l.def.columns[l.tenantAt].name, err)
	}
	group := tenantGroup{text: text}
	return group, v, nil
}

// tenantFilter is the governed list's filter with its relations bound to
// tenant, resolved once per tenant.
func (l *lister) tenantFilter(ctx context.Context, group tenantGroup, tenant any) (*filter.FilterAST, error) {
	where, ok := l.byTenant[group]
	if ok {
		return where, nil
	}
	resolved, err := filter.ResolveMemberships(ctx, filter.NewParsedFilterFor(l.q.schema, l.q.filter), l.lookup, filter.ResolveRelationScope(tenantScope(l.q.scope, tenant)))
	if err != nil {
		return nil, parse.StatementFailed(l.table, "page", err)
	}
	where = resolved.AST()
	l.byTenant[group] = where
	return where, nil
}

// tenantScope is scope with its relations bound to tenant instead of the
// row's tenant column.
func tenantScope(scope filter.RelationScope, tenant any) filter.RelationScope {
	if scope.RowTenant == "" {
		return scope
	}
	scope.RowTenant, scope.Tenant = "", tenant
	return scope
}

// unresolved is n with every relation and membership predicate unknown,
// which filter.Evaluate reads without a lookup: Kleene logic keeps any
// True or False it decides once they resolve.
func unresolved(n *filter.FilterNode) *filter.FilterNode {
	if n == nil {
		return nil
	}
	if n.Op == filter.OpMember || n.Field != nil && n.Field.Relation != nil {
		unknown := &filter.FilterNode{Op: filter.OpUnknown}
		return unknown
	}
	if len(n.Children) == 0 {
		return n
	}
	out := *n
	out.Children = make([]*filter.FilterNode, len(n.Children))
	for i, c := range n.Children {
		out.Children[i] = unresolved(c)
	}
	return &out
}

// sortRow is rec with its sort keys read to their canonical values
// (filter.KeyValue), which every comparison of the walk reads.
func (l *lister) sortRow(rec *record) (sortRow, error) {
	values := make([]any, len(l.keys))
	for i, k := range l.keys {
		v, err := filter.KeyValue(k, rec.value(l.keyAt[i]))
		if err != nil {
			return sortRow{}, fmt.Errorf("sort key %s: %w", k.Name, err)
		}
		values[i] = v
	}
	row := sortRow{rec: rec, values: values}
	return row, nil
}

// direction is k's direction in the walk: flipped for a backward seek.
func (l *lister) direction(k filter.CursorKey) filter.SortDirection {
	if l.reverse {
		return filter.ReverseDirection(k.Direction)
	}
	return k.Direction
}

// compare orders two canonical values of key i in the walk: the key's
// order (filter.CompareKeyValues, NULL greatest) in its direction.
func (l *lister) compare(i int, a, b any) (int, error) {
	c, err := filter.CompareKeyValues(l.keys[i], a, b)
	if err != nil {
		return 0, err
	}
	if l.direction(l.keys[i]) == filter.SortDesc {
		c = -c
	}
	return c, nil
}

// walkLess orders two rows in the walk, key by key.
func (l *lister) walkLess(a, b []any) (bool, error) {
	for i := range l.keys {
		c, err := l.compare(i, a[i], b[i])
		if err != nil || c != 0 {
			return c < 0, err
		}
	}
	return false, nil
}

// past reports whether a row whose sort keys are values lies strictly past
// the cursor in the walk, as the SQL seek decides it
// (filter.SeekPredicate): the OR over each depth i of every earlier key
// equal to the cursor's — NULL equal to NULL — and key i after it. After a
// value walking ascending is a greater value, or NULL for a nullable key;
// walking descending a lesser one, and after NULL every value.
func (l *lister) past(values []any) (bool, error) {
	for i, k := range l.keys {
		x, v := values[i], l.cursor[i]
		xnull, vnull := isNull(x), isNull(v)
		var after bool
		switch {
		case l.direction(k) == filter.SortAsc && vnull:
		case l.direction(k) == filter.SortAsc && xnull:
			after = k.Nullable
		case l.direction(k) == filter.SortDesc && vnull:
			after = !xnull
		case xnull:
		default:
			c, err := l.compare(i, x, v)
			if err != nil {
				return false, err
			}
			after = c > 0
		}
		if after {
			return true, nil
		}
		equal := xnull && vnull
		if !xnull && !vnull {
			c, err := filter.CompareKeyValues(k, x, v)
			if err != nil {
				return false, err
			}
			equal = c == 0
		}
		if !equal {
			return false, nil
		}
	}
	return false, nil
}

func isNull(v any) bool {
	_, null, err := core.StoredValue(v)
	return err == nil && null
}

// ownerKey is rec's key, normalized, as relation facets and includes name
// their owners.
func (l *lister) ownerKey(rec *record) (core.Key, error) {
	key := make(core.Key, len(l.def.key))
	for i, at := range l.def.key {
		key[i] = rec.value(at)
	}
	return core.NormalizeKey(l.def.keyTypes(), key)
}

// facetResults are the facets in request order: each terms and range facet
// as the pass counted it, each relation facet counted from its pairs, per
// tenant for a governed list. A facet without a bucket is left out, as a
// SQL facet statement returns no row for it.
func (l *lister) facetResults(ctx context.Context, res passResult) ([]filter.FacetResult, error) {
	results := make([]filter.FacetResult, 0, len(l.facets))
	for _, f := range l.facets {
		if f.Relation {
			for _, group := range slices.SortedFunc(maps.Keys(res.population), compareGroups) {
				owners, err := l.relationOwners(f.Field.Relation, res.population[group])
				if err != nil {
					return nil, err
				}
				counts, err := filter.ResolveRelationFacet(ctx, f.Field, owners, l.lookup, tenantScope(l.q.scope, res.tenants[group]))
				if err != nil {
					return nil, err
				}
				f.Merge(counts)
			}
		}
		result := f.Result()
		if len(result.Buckets) > 0 {
			results = append(results, result)
		}
	}
	return results, nil
}

// relationOwners are the keys rel's pairs name recs by: their owner
// columns' values, normalized as the source columns' types. A row with a
// NULL among them pairs with nothing, as SQL's join on it matches none.
func (l *lister) relationOwners(rel *core.RelationSchema, recs []*record) ([]core.Key, error) {
	columns := rel.OwnerColumns()
	at := make([]int, len(columns))
	for i, c := range columns {
		p, ok := l.def.index[c]
		if !ok {
			return nil, fmt.Errorf("parsememory: relation owner column %q is no column of table %q", c, l.table)
		}
		at[i] = p
	}
	owners := make([]core.Key, 0, len(recs))
	for _, rec := range recs {
		key := make(core.Key, len(at))
		null := false
		for i, p := range at {
			key[i] = rec.value(p)
			null = null || isNull(key[i])
		}
		if null {
			continue
		}
		normalized, err := core.NormalizeKey(rel.SourceTypes, key)
		if err != nil {
			return nil, err
		}
		owners = append(owners, normalized)
	}
	return owners, nil
}

func compareGroups(a, b tenantGroup) int {
	switch {
	case a.null != b.null:
		if a.null {
			return 1
		}
		return -1
	case a.text < b.text:
		return -1
	case a.text > b.text:
		return 1
	}
	return 0
}

// includes reads each included relation's related keys for owners, the
// page's keys (backend.ListSink.Keys), from the list's snapshot, per tenant
// for a governed list, whose owners' tenants the page's rows hold.
func (l *lister) includes(ctx context.Context, fields []string, owners []core.Key, page []*record, sink backend.ListSink) error {
	scope := l.q.scope
	groups := []tenantGroup{{}}
	byGroup := map[tenantGroup][]core.Key{{}: owners}
	tenants := map[tenantGroup]any{{}: scope.Tenant}
	if scope.RowTenant != "" {
		var err error
		groups, byGroup, tenants, err = l.ownersByTenant(owners, page)
		if err != nil {
			return err
		}
	}
	schema := l.q.schema
	for _, name := range fields {
		out := filter.IncludeRows{IDs: map[string][]string{}}
		for _, group := range groups {
			rows, err := filter.ResolveInclude(ctx, schema.Fields[name], byGroup[group], l.lookup,
				filter.WithIncludeRelationScope(tenantScope(scope, tenants[group])))
			if err != nil {
				return parse.StatementFailed(l.table, "include "+name, err)
			}
			maps.Copy(out.IDs, rows.IDs)
			out.Truncated = append(out.Truncated, rows.Truncated...)
		}
		slices.Sort(out.Truncated)
		sink.Include(name, out)
	}
	return nil
}

// ownersByTenant groups owners by the tenant of the page row each names.
func (l *lister) ownersByTenant(owners []core.Key, page []*record) ([]tenantGroup, map[tenantGroup][]core.Key, map[tenantGroup]any, error) {
	keyTypes := l.def.keyTypes()
	ofOwner := make(map[string]*record, len(page))
	for _, rec := range page {
		key, err := l.ownerKey(rec)
		if err != nil {
			return nil, nil, nil, parse.StatementFailed(l.table, "page", err)
		}
		text, err := core.KeyTextOf(keyTypes, key)
		if err != nil {
			return nil, nil, nil, parse.StatementFailed(l.table, "page", err)
		}
		ofOwner[text] = rec
	}
	byGroup := map[tenantGroup][]core.Key{}
	tenants := map[tenantGroup]any{}
	for _, owner := range owners {
		text, err := core.KeyTextOf(keyTypes, owner)
		if err != nil {
			return nil, nil, nil, parse.StatementFailed(l.table, "page", err)
		}
		rec, ok := ofOwner[text]
		if !ok {
			return nil, nil, nil, fmt.Errorf("parsememory: table %q: owner %s is no row of the page", l.table, text)
		}
		group, tenant, err := l.rowTenant(rec)
		if err != nil {
			return nil, nil, nil, parse.StatementFailed(l.table, "page", err)
		}
		byGroup[group] = append(byGroup[group], owner)
		tenants[group] = tenant
	}
	return slices.SortedFunc(maps.Keys(byGroup), compareGroups), byGroup, tenants, nil
}

// sortRow is a row offered to the page and its sort keys' canonical
// values.
type sortRow struct {
	rec    *record
	values []any
}

// topK keeps the first limit rows offered in walk order: a max-heap of
// them, whose top is the last, which a row before it replaces. Offering n
// rows costs O(n log limit) comparisons and holds limit rows.
type topK struct {
	limit int
	less  func(a, b []any) (bool, error)
	rows  []sortRow
	err   error
}

func (h *topK) Len() int { return len(h.rows) }

// Less orders the heap's rows last first, so its top is the row the page
// would drop first.
func (h *topK) Less(i, j int) bool { return h.before(h.rows[j], h.rows[i]) }

func (h *topK) Swap(i, j int) { h.rows[i], h.rows[j] = h.rows[j], h.rows[i] }

func (h *topK) Push(x any) {
	row, _ := x.(sortRow)
	h.rows = append(h.rows, row)
}

func (h *topK) Pop() any {
	last := h.rows[len(h.rows)-1]
	h.rows = h.rows[:len(h.rows)-1]
	return last
}

// before reports whether a comes before b in the walk, keeping the first
// comparison error.
func (h *topK) before(a, b sortRow) bool {
	less, err := h.less(a.values, b.values)
	if err != nil && h.err == nil {
		h.err = err
	}
	return less
}

func (h *topK) offer(row sortRow) {
	if len(h.rows) < h.limit {
		heap.Push(h, row)
		return
	}
	if h.before(row, h.rows[0]) {
		h.rows[0] = row
		heap.Fix(h, 0)
	}
}

// sorted are the kept rows in walk order.
func (h *topK) sorted() []*record {
	slices.SortFunc(h.rows, func(a, b sortRow) int {
		switch {
		case h.before(a, b):
			return -1
		case h.before(b, a):
			return 1
		}
		return 0
	})
	out := make([]*record, len(h.rows))
	for i, row := range h.rows {
		out[i] = row.rec
	}
	return out
}
