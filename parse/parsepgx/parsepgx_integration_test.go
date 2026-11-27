//go:build integration

package parsepgx_test

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parsepgx/internal/pgtest"
	"github.com/ashtonian/pkg/parse/patch"
)

type product struct {
	ID       uuid.UUID `json:"id"         db:"id"         parse:"filter,sort" indexed:"true"`
	TenantID uuid.UUID `json:"tenant_id"  db:"tenant_id"`
	Name     string    `json:"name"       db:"name"       parse:"filter,sort,patch"`
	Category string    `json:"category"   db:"category"   parse:"filter,facet,patch" facet:"terms:10"`
	// Ranges declared out of count order: a page lists them as declared.
	Price     float64    `json:"price"      db:"price"      parse:"filter,sort,patch,facet" facet:"range:40-50,0-40,50+"`
	Sizes     []string   `json:"sizes"      db:"sizes"      parse:"filter,patch,facet" indexed:"true"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Labels    []string   `json:"labels"     db:"-"          parse:"filter,include,patch" relation:"product_labels,product_id,label_id,id,tenant_id" indexed:"true"`
}

// newProducts is the products resource on p.
func newProducts(p *parse.Provider) *parse.Resource[product] {
	products := parse.MustResource[product](p, "products",
		parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"), parse.PageSize(2, 10))
	return products
}

// provider opens the PostgreSQL provider on pool.
func provider(t *testing.T, ctx context.Context, pool *pgxpool.Pool, opts ...parsepgx.Option) *parse.Provider {
	t.Helper()
	p, err := parsepgx.New(ctx, pool, opts...)
	require.NoError(t, err)
	return p
}

// catalog is one schema's products: tenant A holds a–f (d deleted; e and f
// tie on price), tenant B holds g. Labels pair in their row's tenant, except
// one B pair that names A's product a.
type catalog struct {
	// config is the schema's; each pool opens on a copy.
	config  *pgxpool.Config
	tenantA uuid.UUID
	tenantB uuid.UUID
	ids     map[string]uuid.UUID
	pool    *pgxpool.Pool
	// provider is the provider on pool, and products the resource on it.
	provider *parse.Provider
	products *parse.Resource[product]
	scopeA   parse.Scope
	matchedA []uuid.UUID // A's live products sized m
}

func newCatalog(t *testing.T) *catalog {
	t.Helper()
	ctx := testContext(t)
	c := &catalog{config: pgtest.PostgresPoolConfig(t), tenantA: uuid.New(), tenantB: uuid.New(), ids: map[string]uuid.UUID{}}
	c.pool = c.open(t, ctx, pgx.QueryExecModeCacheStatement)
	c.provider = provider(t, ctx, c.pool)
	c.products = newProducts(c.provider)
	c.scopeA = parse.Scope{Tenant: c.tenantA.String()}

	for _, stmt := range []string{
		`CREATE TABLE products (id uuid PRIMARY KEY, tenant_id uuid NOT NULL, name text NOT NULL, category text NOT NULL,
			price float8 NOT NULL, sizes text[] NOT NULL DEFAULT '{}', deleted_at timestamptz)`,
		`CREATE TABLE product_labels (product_id uuid NOT NULL, label_id text NOT NULL CHECK (label_id <> 'forbidden'), tenant_id uuid NOT NULL,
			PRIMARY KEY (product_id, label_id, tenant_id))`,
	} {
		_, err := c.pool.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	rows := []struct {
		name, category string
		price          float64
		sizes          []string
		tenant         uuid.UUID
		deleted        bool
	}{
		{"a", "tools", 50, []string{"m", "l"}, c.tenantA, false},
		{"b", "tools", 40, []string{"m"}, c.tenantA, false},
		{"c", "toys", 30, []string{"s"}, c.tenantA, false},
		{"d", "toys", 20, []string{"m"}, c.tenantA, true},
		{"e", "books", 10, []string{"m", "s"}, c.tenantA, false},
		{"f", "books", 10, []string{"m"}, c.tenantA, false},
		{"g", "tools", 60, []string{"m"}, c.tenantB, false},
	}
	for _, r := range rows {
		id := uuid.Must(uuid.NewV7())
		c.ids[r.name] = id
		var deleted *time.Time
		if r.deleted {
			at := time.Now()
			deleted = &at
		}
		_, err := c.pool.Exec(ctx, `INSERT INTO products VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, r.tenant, r.name, r.category, r.price, r.sizes, deleted)
		require.NoError(t, err)
		if r.tenant == c.tenantA && !r.deleted && slices.Contains(r.sizes, "m") {
			c.matchedA = append(c.matchedA, id)
		}
	}
	for _, l := range []struct {
		product, label string
		tenant         uuid.UUID
	}{{"a", "red", c.tenantA}, {"a", "blue", c.tenantA}, {"b", "red", c.tenantA}, {"a", "green", c.tenantB}, {"g", "red", c.tenantB}} {
		_, err := c.pool.Exec(ctx, `INSERT INTO product_labels VALUES ($1, $2, $3)`, c.ids[l.product], l.label, l.tenant)
		require.NoError(t, err)
	}
	return c
}

// open opens a pool on the catalog's schema in mode; configure adjusts
// its settings first.
func (c *catalog) open(t *testing.T, ctx context.Context, mode pgx.QueryExecMode, configure ...func(*pgxpool.Config)) *pgxpool.Pool {
	t.Helper()
	cfg := c.config.Copy()
	cfg.ConnConfig.DefaultQueryExecMode = mode
	cfg.MaxConns = 4
	cfg.MaxConnLifetime = time.Minute
	cfg.MaxConnIdleTime = 30 * time.Second
	cfg.ConnConfig.ConnectTimeout = 10 * time.Second
	for _, f := range configure {
		f(cfg)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func (c *catalog) names(ids ...uuid.UUID) []string {
	var out []string
	for _, id := range ids {
		for name, known := range c.ids {
			if known == id {
				out = append(out, name)
			}
		}
	}
	return out
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	return pgtest.Context(t)
}

// listRequest is query, a list request's query string, parsed by r.
func listRequest[T any](t *testing.T, r *parse.Resource[T], query string) *parse.ListRequest {
	t.Helper()
	q, err := url.ParseQuery(query)
	require.NoError(t, err)
	req, err := r.ParseList(q)
	require.NoError(t, err)
	return req
}

// listProducts lists query, a list request's query string, on products
// under s.
func listProducts(t *testing.T, ctx context.Context, products *parse.Resource[product], query string, s parse.Scope) (*filter.Page[product], error) {
	t.Helper()
	return products.List(ctx, listRequest(t, products, query), s)
}

func pageIDs(p *filter.Page[product]) []uuid.UUID {
	out := make([]uuid.UUID, len(p.Items))
	for i, it := range p.Items {
		out[i] = it.ID
	}
	return out
}

const walkQuery = `filter={"sizes":{"$contains":["m"]}}&sort=-price&include_total=true&facets=category,price,sizes&include=labels`

// A walk reads every matched row once, in order, each page with the
// population's total and facets and its own rows' labels in its tenant; the
// walk back returns the same pages.
func TestList_WalksATenantsPages(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)

	var pages []*filter.Page[product]
	cursor := ""
	for n := 0; ; n++ {
		require.Less(t, n, 5, "walk did not terminate")
		q := walkQuery
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		page, err := listProducts(t, ctx, c.products, q, c.scopeA)
		require.NoError(t, err)
		require.NotNil(t, page.PageInfo.TotalCount)
		assert.EqualValues(t, 4, *page.PageInfo.TotalCount, "A's live rows sized m")
		assert.Equal(t, map[string][]filter.FacetBucket{
			"category": {{Key: "books", Count: 2}, {Key: "tools", Count: 2}},
			"price":    {{Key: "40-50", Count: 1}, {Key: "0-40", Count: 2}, {Key: "50+", Count: 1}},
			"sizes":    {{Key: "m", Count: 4}, {Key: "l", Count: 1}, {Key: "s", Count: 1}},
		}, page.Facets, "the population's counts on every page, in their order")
		pages = append(pages, page)
		if !page.PageInfo.HasNextPage {
			break
		}
		cursor = string(page.PageInfo.EndCursor)
	}

	var walked []uuid.UUID
	for _, p := range pages {
		walked = append(walked, pageIDs(p)...)
	}
	require.ElementsMatch(t, c.matchedA, walked, "every matched row once")
	assert.Equal(t, []string{"a", "b"}, c.names(walked[:2]...), "by price, descending")
	require.Len(t, pages, 2)

	assert.Equal(t, map[string]map[string][]string{"labels": {
		c.ids["a"].String(): {"blue", "red"},
		c.ids["b"].String(): {"red"},
	}}, pages[0].Relations, "a's green pair is B's")
	assert.Empty(t, pages[1].Relations, "e and f have no labels; empty projections are omitted")

	back, err := listProducts(t, ctx, c.products, walkQuery+"&cursor="+url.QueryEscape(string(pages[1].PageInfo.StartCursor)), c.scopeA)
	require.NoError(t, err)
	assert.Equal(t, pageIDs(pages[0]), pageIDs(back), "the walk back returns the first page")
	assert.False(t, back.PageInfo.HasPrevPage)
	assert.Equal(t, pages[0].Relations, back.Relations)
}

// An owner with more related rows than the include cap gets the first 100
// and is listed in the page's _truncated.
func TestList_ReportsTruncatedIncludes(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	_, err := c.pool.Exec(ctx, `INSERT INTO product_labels SELECT $1, 'l' || lpad(g::text, 3, '0'), $2 FROM generate_series(1, 101) g`, c.ids["f"], c.tenantA)
	require.NoError(t, err)
	page, err := listProducts(t, ctx, c.products, `name=f&include=labels`, c.scopeA)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Len(t, page.Relations["labels"][c.ids["f"].String()], 100)
	assert.Equal(t, map[string][]string{"labels": {c.ids["f"].String()}}, page.Truncated)
}

// Another tenant's list reaches neither A's rows nor A's labels.
func TestList_HoldsRowsAndIncludesToTheTenant(t *testing.T) {
	c := newCatalog(t)
	page, err := listProducts(t, testContext(t), c.products, "include=labels&include_total=true", parse.Scope{Tenant: c.tenantB.String()})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{c.ids["g"]}, pageIDs(page))
	assert.EqualValues(t, 1, *page.PageInfo.TotalCount)
	assert.Equal(t, map[string]map[string][]string{"labels": {c.ids["g"].String(): {"red"}}}, page.Relations)
}

// A plan that asks for no total, facets or includes leaves them unset, and
// an empty page needs no include read.
func TestList_ReadsOnlyWhatThePlanAsks(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	page, err := listProducts(t, ctx, c.products, "page_size=10", c.scopeA)
	require.NoError(t, err)
	assert.Len(t, page.Items, 5)
	assert.Nil(t, page.PageInfo.TotalCount)
	assert.Nil(t, page.Facets)
	assert.Nil(t, page.Relations)

	withoutLabels, err := listProducts(t, ctx, c.products, "name=f&include=labels", c.scopeA)
	require.NoError(t, err)
	assert.Len(t, withoutLabels.Items, 1)
	assert.Nil(t, withoutLabels.Relations)
	assert.Nil(t, withoutLabels.Truncated)

	empty, err := listProducts(t, ctx, c.products, "name=nothing&include=labels&facets=category", c.scopeA)
	require.NoError(t, err)
	assert.Empty(t, empty.Items)
	assert.Nil(t, empty.Relations)
	assert.Nil(t, empty.Facets, "no rows, no buckets")
}

// List and update plans encode in every pgx execution mode, the
// no-describe ones included: UUID lists, text arrays, floats and keyset
// seeks.
func TestEveryExecMode(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	query := `filter=` + url.QueryEscape(`{"sizes":{"$contains":["m"]},"id":{"$in":["`+
		c.ids["a"].String()+`","`+c.ids["b"].String()+`","`+c.ids["e"].String()+`","`+c.ids["g"].String()+`"]}}`) +
		`&sort=-price&include_total=true&facets=category&include=labels`

	for name, mode := range map[string]pgx.QueryExecMode{
		"cache statement": pgx.QueryExecModeCacheStatement,
		"cache describe":  pgx.QueryExecModeCacheDescribe,
		"describe exec":   pgx.QueryExecModeDescribeExec,
		"exec":            pgx.QueryExecModeExec,
		"simple protocol": pgx.QueryExecModeSimpleProtocol,
	} {
		t.Run(name, func(t *testing.T) {
			products := newProducts(provider(t, ctx, c.open(t, ctx, mode)))
			page, err := listProducts(t, ctx, products, query, c.scopeA)
			require.NoError(t, err)
			assert.Equal(t, []string{"a", "b"}, c.names(pageIDs(page)...))
			assert.EqualValues(t, 3, *page.PageInfo.TotalCount, "g is B's")
			assert.Equal(t, []filter.FacetBucket{{Key: "tools", Count: 2}, {Key: "books", Count: 1}}, page.Facets["category"])
			assert.Equal(t, map[string][]string{c.ids["a"].String(): {"blue", "red"}, c.ids["b"].String(): {"red"}}, page.Relations["labels"])

			next, err := listProducts(t, ctx, products, query+"&cursor="+url.QueryEscape(string(page.PageInfo.EndCursor)), c.scopeA)
			require.NoError(t, err)
			assert.Equal(t, []string{"e"}, c.names(pageIDs(next)...))
			assert.False(t, next.PageInfo.HasNextPage)

			// Rewrites a's array and labels as they are, so modes do not disturb each other.
			got, err := products.Update(ctx, mergePatch(t, products, `{"sizes":["m","l"],"labels":["red","blue"]}`), parse.ByIDs(c.ids["a"], c.ids["g"]), c.scopeA)
			require.NoError(t, err)
			require.Len(t, got.Changed, 1)
			assert.Empty(t, got.Unchanged)
			assert.Equal(t, []string{"m", "l"}, got.Changed[0].Sizes)
			assert.Equal(t, []string{"blue", "red"}, labels(t, c, "a", c.tenantA))

			// The guarded form reuses its placeholders in the WHERE: a text,
			// an array and a float the database types once for both.
			got, err = products.Update(ctx, mergePatch(t, products, `{"name":"a","price":50,"sizes":["m","l"],"labels":["red","blue"]}`),
				parse.ByIDs(c.ids["a"], c.ids["g"]), c.scopeA, parse.SkipUnchanged())
			require.NoError(t, err)
			assert.Empty(t, got.Changed)
			require.Len(t, got.Unchanged, 1, "a is unchanged, and found")
			assert.Equal(t, c.ids["a"], got.Unchanged[0].ID)
			assert.Zero(t, got.RelationRows, "its labels are already red and blue")
		})
	}
}

// In a REPEATABLE READ transaction the total describes the same snapshot as
// the page, whatever commits meanwhile.
func TestList_InATransaction(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	scoped, err := c.products.In(parsepgx.Tx(tx))
	require.NoError(t, err)

	before, err := listProducts(t, ctx, scoped, walkQuery, c.scopeA)
	require.NoError(t, err)
	_, err = c.pool.Exec(ctx, `UPDATE products SET deleted_at = now() WHERE id = $1`, c.ids["e"])
	require.NoError(t, err)
	after, err := listProducts(t, ctx, scoped, walkQuery, c.scopeA)
	require.NoError(t, err)
	assert.EqualValues(t, 4, *after.PageInfo.TotalCount, "the transaction's snapshot")
	assert.Equal(t, pageIDs(before), pageIDs(after))
}

// A failing statement returns the database's error as the statement it
// failed, the batch closed.
func TestList_StatementErrors(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	_, err := c.pool.Exec(ctx, `ALTER TABLE products RENAME COLUMN category TO kind`)
	require.NoError(t, err)
	_, err = listProducts(t, ctx, c.products, "", c.scopeA)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "42703", pgErr.Code, "undefined column")
	failed, ok := errors.AsType[*parse.StatementError](err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, "products", failed.Table)
	assert.Equal(t, "page", failed.Statement)
	require.ErrorContains(t, err, "parse: products: page: ")

	page, err := listProducts(t, ctx, c.products, "", c.scopeA)
	require.Error(t, err, "the column is still missing")
	assert.Nil(t, page)
	_, err = c.pool.Exec(ctx, `ALTER TABLE products RENAME COLUMN kind TO category`)
	require.NoError(t, err)
	page, err = listProducts(t, ctx, c.products, "", c.scopeA)
	require.NoError(t, err, "the pool's connections are usable after the failed batch")
	assert.Len(t, page.Items, 2)
}

// mergePatch is body, a JSON Merge Patch, parsed by r.
func mergePatch[T any](t *testing.T, r *parse.Resource[T], body string) *patch.ParsedPatch {
	t.Helper()
	p, err := r.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	return p
}

func labels(t *testing.T, c *catalog, name string, tenant uuid.UUID) []string {
	t.Helper()
	rows, err := c.pool.Query(testContext(t), `SELECT label_id FROM product_labels WHERE product_id = $1 AND tenant_id = $2 ORDER BY label_id`, c.ids[name], tenant)
	require.NoError(t, err)
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return out
}

// An update changes and returns the scope's target rows only, and changes
// the memberships of those rows only, together.
func TestUpdate_ChangesTheScopesTargetsAndTheirRelations(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)

	got, err := c.products.Update(ctx, mergePatch(t, c.products, `{"price":45,"labels":["sale","red"]}`), parse.ByIDs(c.ids["a"], c.ids["d"], c.ids["g"]), c.scopeA)
	require.NoError(t, err)
	require.Len(t, got.Changed, 1, "d is deleted, g is B's")
	assert.Empty(t, got.Unchanged)
	assert.Equal(t, c.ids["a"], got.Changed[0].ID)
	assert.Equal(t, 45.0, got.Changed[0].Price)
	assert.Equal(t, int64(2), got.RelationRows, "blue removed, sale added")
	assert.Equal(t, []string{"red", "sale"}, labels(t, c, "a", c.tenantA))
	assert.Equal(t, []string{"green"}, labels(t, c, "a", c.tenantB), "B's pair for a is B's")
	assert.Equal(t, []string{"red"}, labels(t, c, "g", c.tenantB))

	only, err := c.products.Update(ctx, mergePatch(t, c.products, `{"labels":[]}`), parse.ByID(c.ids["b"]), c.scopeA)
	require.NoError(t, err)
	assert.Empty(t, only.Changed, "a relation-only patch writes no column")
	require.Len(t, only.Unchanged, 1, "and returns its owner")
	assert.Equal(t, int64(1), only.RelationRows, "red removed")
	assert.Empty(t, labels(t, c, "b", c.tenantA))
	again, err := c.products.Update(ctx, mergePatch(t, c.products, `{"labels":[]}`), parse.ByID(c.ids["b"]), c.scopeA)
	require.NoError(t, err)
	require.Len(t, again.Unchanged, 1)
	assert.Zero(t, again.RelationRows, "no membership changed")

	read, err := c.products.Update(ctx, mergePatch(t, c.products, `{}`), parse.ByID(c.ids["a"]), c.scopeA)
	require.NoError(t, err)
	assert.Empty(t, read.Changed)
	require.Len(t, read.Unchanged, 1, "an empty patch reads the row")
	assert.Equal(t, 45.0, read.Unchanged[0].Price)

	none, err := c.products.Update(ctx, mergePatch(t, c.products, `{"price":1}`), parse.ByID(c.ids["a"]), parse.Scope{Tenant: c.tenantB.String()})
	require.NoError(t, err)
	assert.Zero(t, none, "another tenant updates nothing")
}

// versioned reads products with a version and a stamp the server moves.
type versioned struct {
	ID        uuid.UUID  `json:"id"         db:"id"         parse:"filter,sort" indexed:"true"`
	TenantID  uuid.UUID  `json:"tenant_id"  db:"tenant_id"`
	Price     float64    `json:"price"      db:"price"      parse:"filter,patch"`
	Version   int64      `json:"version"    db:"version"`
	UpdatedAt *time.Time `json:"updated_at" db:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Labels    []string   `json:"labels"     db:"-"          parse:"filter,include,patch" relation:"product_labels,product_id,label_id,id,tenant_id" indexed:"true"`
}

func versionedProducts(p *parse.Provider) *parse.Resource[versioned] {
	r := parse.MustResource[versioned](p, "products", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	return r
}

// countedProduct reads its label count through an expression over the
// junction, so an update that changes memberships rereads its rows.
type countedProduct struct {
	ID         uuid.UUID  `json:"id"          db:"id"          parse:"filter,sort" indexed:"true"`
	TenantID   uuid.UUID  `json:"tenant_id"   db:"tenant_id"`
	Price      float64    `json:"price"       db:"price"       parse:"filter,patch"`
	Version    int64      `json:"version"     db:"version"`
	LabelCount int64      `json:"label_count" db:"label_count"`
	DeletedAt  *time.Time `json:"deleted_at"  db:"deleted_at"`
	Labels     []string   `json:"labels"      db:"-"           parse:"filter,include,patch" relation:"product_labels,product_id,label_id,id,tenant_id" indexed:"true"`
}

func countedProducts(p *parse.Provider) *parse.Resource[countedProduct] {
	r := parse.MustResource[countedProduct](p, "products", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"),
		parse.ReadExpr("label_count", parse.SQL(`(SELECT count(*) FROM product_labels l WHERE l.product_id = "products"."id" AND l.tenant_id = "products"."tenant_id")`)))
	return r
}

// updateIn applies p to target under s on r, or with inTx in a
// transaction of the caller's that commits it, r scoped to it.
func updateIn[T any](t *testing.T, ctx context.Context, pool *pgxpool.Pool, inTx bool, r *parse.Resource[T], p *patch.ParsedPatch, target parse.Target, s parse.Scope, opts ...parse.UpdateOption) parse.UpdateResult[T] {
	t.Helper()
	if !inTx {
		res, err := r.Update(ctx, p, target, s, opts...)
		require.NoError(t, err)
		return res
	}
	var res parse.UpdateResult[T]
	require.NoError(t, pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		scoped, err := r.In(parsepgx.Tx(tx))
		if err != nil {
			return err
		}
		res, err = scoped.Update(ctx, p, target, s, opts...)
		return err
	}))
	return res
}

// A SkipUnchanged plan, on a pool or in the caller's transaction, writes,
// stamps and counts only the rows it changes, reports them apart from the
// targets it left as they were, and changes the memberships of both; with
// computed columns over the pairs, both sides are read again after them.
func TestUpdate_SkipUnchanged(t *testing.T) {
	guardedOpts := []parse.UpdateOption{parse.StampSQL("updated_at", "now()"), parse.Increment("version"), parse.SkipUnchanged()}
	for name, inTx := range map[string]bool{"on the pool": false, "in the caller's transaction": true} {
		t.Run(name, func(t *testing.T) {
			setup := func(t *testing.T) (*catalog, context.Context, *parse.Resource[versioned]) {
				c := newCatalog(t)
				ctx := testContext(t)
				_, err := c.pool.Exec(ctx, `ALTER TABLE products ADD COLUMN version bigint NOT NULL DEFAULT 1, ADD COLUMN updated_at timestamptz`)
				require.NoError(t, err)
				return c, ctx, versionedProducts(c.provider)
			}
			// guarded is a SkipUnchanged update of body, whose plan reads
			// every target before it writes.
			guarded := func(t *testing.T, ctx context.Context, c *catalog, r *parse.Resource[versioned], body string, target parse.Target) parse.UpdateResult[versioned] {
				t.Helper()
				p := mergePatch(t, r, body)
				plan, err := r.BuildUpdate(p, target, c.scopeA, guardedOpts...)
				require.NoError(t, err)
				require.NotNil(t, plan.Current)
				return updateIn(t, ctx, c.pool, inTx, r, p, target, c.scopeA, guardedOpts...)
			}

			t.Run("columns and relations", func(t *testing.T) {
				c, ctx, r := setup(t)
				got := guarded(t, ctx, c, r, `{"price":50,"labels":["red","sale"]}`, parse.ByIDs(c.ids["a"], c.ids["b"], c.ids["d"], c.ids["g"]))
				require.Len(t, got.Changed, 1, "b changed")
				require.Len(t, got.Unchanged, 1, "a already cost 50; d is deleted, g is B's")
				assert.Equal(t, []string{"b"}, c.names(got.Changed[0].ID))
				assert.Equal(t, []string{"a"}, c.names(got.Unchanged[0].ID))
				assert.Equal(t, 50.0, got.Changed[0].Price)
				assert.Equal(t, int64(2), got.Changed[0].Version)
				assert.NotNil(t, got.Changed[0].UpdatedAt)
				assert.Equal(t, int64(1), got.Unchanged[0].Version, "the unchanged row keeps its version")
				assert.Nil(t, got.Unchanged[0].UpdatedAt, "and its stamp")
				assert.Equal(t, int64(3), got.RelationRows, "a's blue removed, a's and b's sale added")
				assert.Equal(t, []string{"red", "sale"}, labels(t, c, "a", c.tenantA), "the unchanged row's memberships change too")
				assert.Equal(t, []string{"red", "sale"}, labels(t, c, "b", c.tenantA))
				assert.Equal(t, []string{"green"}, labels(t, c, "a", c.tenantB), "another tenant's pair stays")

				var version int64
				var stamped bool
				require.NoError(t, c.pool.QueryRow(ctx, `SELECT version, updated_at IS NOT NULL FROM products WHERE id = $1`, c.ids["a"]).Scan(&version, &stamped))
				assert.Equal(t, int64(1), version, "the stored unchanged row was not written")
				assert.False(t, stamped)

				assert.Zero(t, guarded(t, ctx, c, r, `{"price":50}`, parse.ByID(c.ids["g"])), "another tenant's row is not found")
			})
			t.Run("columns alone", func(t *testing.T) {
				c, ctx, r := setup(t)
				got := guarded(t, ctx, c, r, `{"price":50}`, parse.ByIDs(c.ids["a"], c.ids["b"]))
				require.Len(t, got.Changed, 1)
				require.Len(t, got.Unchanged, 1)
				assert.Equal(t, []string{"b"}, c.names(got.Changed[0].ID))
				assert.Equal(t, []string{"a"}, c.names(got.Unchanged[0].ID))
				assert.Zero(t, got.RelationRows)
			})
			t.Run("a reread", func(t *testing.T) {
				c, ctx, _ := setup(t)
				counted := countedProducts(c.provider)
				got := updateIn(t, ctx, c.pool, inTx, counted, mergePatch(t, counted, `{"price":50,"labels":["red"]}`),
					parse.ByIDs(c.ids["a"], c.ids["b"]), c.scopeA, parse.Increment("version"), parse.SkipUnchanged())
				require.Len(t, got.Changed, 1)
				require.Len(t, got.Unchanged, 1)
				assert.Equal(t, []string{"b"}, c.names(got.Changed[0].ID))
				assert.Equal(t, []string{"a"}, c.names(got.Unchanged[0].ID))
				assert.Equal(t, int64(1), got.Unchanged[0].LabelCount, "a's count is read after its blue pair went")
				assert.Equal(t, int64(1), got.Changed[0].LabelCount)
				assert.Equal(t, int64(2), got.Changed[0].Version, "the reread keeps the changed side's write")
				assert.Equal(t, int64(1), got.RelationRows)
			})
		})
	}
}

// A membership change that fails undoes the row change with it.
func TestUpdate_RelationFailureRollsBackTheRow(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	_, err := c.products.Update(ctx, mergePatch(t, c.products, `{"name":"renamed","labels":["forbidden"]}`), parse.ByID(c.ids["a"]), c.scopeA)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "23514", pgErr.Code, "check violation")
	failed, ok := errors.AsType[*parse.StatementError](err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, "products", failed.Table)
	assert.Equal(t, "relations", failed.Statement)

	var name string
	require.NoError(t, c.pool.QueryRow(ctx, `SELECT name FROM products WHERE id = $1`, c.ids["a"]).Scan(&name))
	assert.Equal(t, "a", name)
	assert.Equal(t, []string{"blue", "red"}, labels(t, c, "a", c.tenantA))
}

// Inside the caller's transaction an update is a savepoint: the caller's
// rollback undoes it.
func TestUpdate_InTheCallersTransaction(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	tx, err := c.pool.Begin(ctx)
	require.NoError(t, err)
	scoped, err := c.products.In(parsepgx.Tx(tx))
	require.NoError(t, err)
	got, err := scoped.Update(ctx, mergePatch(t, scoped, `{"name":"renamed","labels":["new"]}`), parse.ByID(c.ids["a"]), c.scopeA)
	require.NoError(t, err)
	require.Len(t, got.Changed, 1)
	require.NoError(t, tx.Rollback(ctx))

	assert.Equal(t, "a", c.name(t, ctx, "a"))
	assert.Equal(t, []string{"blue", "red"}, labels(t, c, "a", c.tenantA))
}

// A failed update in the caller's transaction rolls back to its savepoint:
// the transaction keeps what ran before it and stays usable, and its commit
// keeps an update that ran after.
func TestUpdate_FailedInTheCallersTransactionLeavesItUsable(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	tx, err := c.pool.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE products SET name = 'before' WHERE id = $1`, c.ids["b"])
	require.NoError(t, err)
	scoped, err := c.products.In(parsepgx.Tx(tx))
	require.NoError(t, err)

	_, err = scoped.Update(ctx, mergePatch(t, scoped, `{"name":"renamed","labels":["forbidden"]}`), parse.ByID(c.ids["a"]), c.scopeA)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "23514", pgErr.Code, "check violation")

	got, err := scoped.Update(ctx, mergePatch(t, scoped, `{"labels":["after"]}`), parse.ByID(c.ids["a"]), c.scopeA)
	require.NoError(t, err, "the transaction is not aborted")
	assert.Equal(t, int64(3), got.RelationRows, "blue and red removed, after added")
	require.NoError(t, tx.Commit(ctx))

	assert.Equal(t, "a", c.name(t, ctx, "a"), "the failed update's write is gone")
	assert.Equal(t, "before", c.name(t, ctx, "b"), "the caller's own write is kept")
	assert.Equal(t, []string{"after"}, labels(t, c, "a", c.tenantA))
}

// A transaction the provider begins holds several operations until it
// commits, and a rollback after the commit does nothing.
func TestProvider_BeginHoldsOperationsUntilCommit(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	tx, err := c.provider.Begin(ctx)
	require.NoError(t, err)
	scoped, err := c.products.In(tx)
	require.NoError(t, err)
	_, err = scoped.Update(ctx, mergePatch(t, scoped, `{"name":"renamed"}`), parse.ByID(c.ids["a"]), c.scopeA)
	require.NoError(t, err)
	page, err := listProducts(t, ctx, scoped, "name=renamed", c.scopeA)
	require.NoError(t, err)
	assert.Len(t, page.Items, 1, "the transaction reads its own write")
	assert.Equal(t, "a", c.name(t, ctx, "a"), "outside it the write is not yet visible")

	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, tx.Rollback(ctx), "a rollback after the commit does nothing")
	assert.Equal(t, "renamed", c.name(t, ctx, "a"))

	rolled, err := c.provider.Begin(ctx)
	require.NoError(t, err)
	scoped, err = c.products.In(rolled)
	require.NoError(t, err)
	_, err = scoped.Update(ctx, mergePatch(t, scoped, `{"name":"discarded"}`), parse.ByID(c.ids["a"]), c.scopeA)
	require.NoError(t, err)
	require.NoError(t, rolled.Rollback(ctx))
	assert.Equal(t, "renamed", c.name(t, ctx, "a"))
}

// The provider opens under either code-point collation PostgreSQL 17+
// has, and names itself postgres.
func TestNew_OpensUnderEitherCollation(t *testing.T) {
	c := &catalog{config: pgtest.PostgresPoolConfig(t)}
	ctx := testContext(t)
	pool := c.open(t, ctx, pgx.QueryExecModeCacheStatement)
	for _, opts := range [][]parsepgx.Option{nil, {parsepgx.Collation(filter.CollationC)}, {parsepgx.Collation(filter.CollationPgCUTF8)}} {
		p := provider(t, ctx, pool, opts...)
		assert.Equal(t, parsepgx.Name, p.Name())
	}
}

type credential struct {
	ID         uuid.UUID `json:"id"          db:"id"          parse:"filter,sort"`
	TenantID   uuid.UUID `json:"tenant_id"   db:"tenant_id"`
	Name       string    `json:"name"        db:"name"        parse:"filter,sort,patch"`
	SecretHash string    `json:"-"           db:"secret_hash"`
}

// A resource that does not read a sensitive column scans its pages and
// update results with that field zero; the column never leaves the database.
func TestReadColumns_LeaveSensitiveColumnsInTheDatabase(t *testing.T) {
	c := &catalog{config: pgtest.PostgresPoolConfig(t)}
	ctx := testContext(t)
	pool := c.open(t, ctx, pgx.QueryExecModeCacheStatement)
	_, err := pool.Exec(ctx, `CREATE TABLE credentials (id uuid PRIMARY KEY, tenant_id uuid NOT NULL, name text NOT NULL, secret_hash text NOT NULL)`)
	require.NoError(t, err)
	tenant, id := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO credentials VALUES ($1, $2, 'deploy', 'h4sh')`, id, tenant)
	require.NoError(t, err)

	creds := parse.MustResource[credential](provider(t, ctx, pool), "credentials", parse.TenantColumn("tenant_id"), parse.ReadColumns("id", "tenant_id", "name"))
	scope := parse.Scope{Tenant: tenant.String()}
	page, err := creds.List(ctx, nil, scope)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "deploy", page.Items[0].Name)
	assert.Empty(t, page.Items[0].SecretHash)

	res, err := creds.Update(ctx, mergePatch(t, creds, `{"name":"ci"}`), parse.ByID(id), scope)
	require.NoError(t, err)
	require.Len(t, res.Changed, 1)
	assert.Equal(t, "ci", res.Changed[0].Name)
	assert.Empty(t, res.Changed[0].SecretHash)
}
