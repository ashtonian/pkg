//go:build integration

package parsepgx_test

import (
	"context"
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
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsepgx"
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

var products = parse.MustResource[product]("products",
	parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"), parse.PageSize(2, 10))

// catalog is one schema's products: tenant A holds a–f (d deleted; e and f
// tie on price), tenant B holds g. Labels pair in their row's tenant, except
// one B pair that names A's product a.
type catalog struct {
	dsn      string
	tenantA  uuid.UUID
	tenantB  uuid.UUID
	ids      map[string]uuid.UUID
	pool     *pgxpool.Pool
	scopeA   parse.Scope
	matchedA []uuid.UUID // A's live products sized m
}

func newCatalog(t *testing.T) *catalog {
	t.Helper()
	ctx := testContext(t)
	c := &catalog{dsn: dbtest.PostgresDSN(t), tenantA: uuid.New(), tenantB: uuid.New(), ids: map[string]uuid.UUID{}}
	c.pool = c.open(t, pgx.QueryExecModeCacheStatement)
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

func (c *catalog) open(t *testing.T, mode pgx.QueryExecMode) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(c.dsn)
	require.NoError(t, err)
	cfg.ConnConfig.DefaultQueryExecMode = mode
	cfg.MaxConns = 4
	cfg.MaxConnLifetime = time.Minute
	cfg.MaxConnIdleTime = 30 * time.Second
	cfg.ConnConfig.ConnectTimeout = 10 * time.Second
	pool, err := pgxpool.NewWithConfig(testContext(t), cfg)
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func listPlan(t *testing.T, query string, s parse.Scope) *parse.ListPlan {
	t.Helper()
	q, err := url.ParseQuery(query)
	require.NoError(t, err)
	req, err := products.ParseList(q)
	require.NoError(t, err)
	plan, err := products.BuildList(filter.PostgresDialect{}, req, s)
	require.NoError(t, err)
	return plan
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
func TestQueryPage_WalksATenantsPages(t *testing.T) {
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
		page, err := parsepgx.QueryPage(ctx, c.pool, products, listPlan(t, q, c.scopeA))
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

	back, err := parsepgx.QueryPage(ctx, c.pool, products,
		listPlan(t, walkQuery+"&cursor="+url.QueryEscape(string(pages[1].PageInfo.StartCursor)), c.scopeA))
	require.NoError(t, err)
	assert.Equal(t, pageIDs(pages[0]), pageIDs(back), "the walk back returns the first page")
	assert.False(t, back.PageInfo.HasPrevPage)
	assert.Equal(t, pages[0].Relations, back.Relations)
}

// An owner with more related rows than the include cap gets the first 100
// and is listed in the page's _truncated.
func TestQueryPage_ReportsTruncatedIncludes(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	_, err := c.pool.Exec(ctx, `INSERT INTO product_labels SELECT $1, 'l' || lpad(g::text, 3, '0'), $2 FROM generate_series(1, 101) g`, c.ids["f"], c.tenantA)
	require.NoError(t, err)
	page, err := parsepgx.QueryPage(ctx, c.pool, products, listPlan(t, `name=f&include=labels`, c.scopeA))
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Len(t, page.Relations["labels"][c.ids["f"].String()], 100)
	assert.Equal(t, map[string][]string{"labels": {c.ids["f"].String()}}, page.Truncated)
}

// Another tenant's list reaches neither A's rows nor A's labels.
func TestQueryPage_HoldsRowsAndIncludesToTheTenant(t *testing.T) {
	c := newCatalog(t)
	page, err := parsepgx.QueryPage(testContext(t), c.pool, products,
		listPlan(t, "include=labels&include_total=true", parse.Scope{Tenant: c.tenantB.String()}))
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{c.ids["g"]}, pageIDs(page))
	assert.EqualValues(t, 1, *page.PageInfo.TotalCount)
	assert.Equal(t, map[string]map[string][]string{"labels": {c.ids["g"].String(): {"red"}}}, page.Relations)
}

// A plan that asks for no total, facets or includes leaves them unset, and
// an empty page needs no include read.
func TestQueryPage_ReadsOnlyWhatThePlanAsks(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	page, err := parsepgx.QueryPage(ctx, c.pool, products, listPlan(t, "page_size=10", c.scopeA))
	require.NoError(t, err)
	assert.Len(t, page.Items, 5)
	assert.Nil(t, page.PageInfo.TotalCount)
	assert.Nil(t, page.Facets)
	assert.Nil(t, page.Relations)

	withoutLabels, err := parsepgx.QueryPage(ctx, c.pool, products, listPlan(t, "name=f&include=labels", c.scopeA))
	require.NoError(t, err)
	assert.Len(t, withoutLabels.Items, 1)
	assert.Nil(t, withoutLabels.Relations)
	assert.Nil(t, withoutLabels.Truncated)

	empty, err := parsepgx.QueryPage(ctx, c.pool, products, listPlan(t, "name=nothing&include=labels&facets=category", c.scopeA))
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
			pool := c.open(t, mode)
			page, err := parsepgx.QueryPage(ctx, pool, products, listPlan(t, query, c.scopeA))
			require.NoError(t, err)
			assert.Equal(t, []string{"a", "b"}, c.names(pageIDs(page)...))
			assert.EqualValues(t, 3, *page.PageInfo.TotalCount, "g is B's")
			assert.Equal(t, []filter.FacetBucket{{Key: "tools", Count: 2}, {Key: "books", Count: 1}}, page.Facets["category"])
			assert.Equal(t, map[string][]string{c.ids["a"].String(): {"blue", "red"}, c.ids["b"].String(): {"red"}}, page.Relations["labels"])

			next, err := parsepgx.QueryPage(ctx, pool, products,
				listPlan(t, query+"&cursor="+url.QueryEscape(string(page.PageInfo.EndCursor)), c.scopeA))
			require.NoError(t, err)
			assert.Equal(t, []string{"e"}, c.names(pageIDs(next)...))
			assert.False(t, next.PageInfo.HasNextPage)

			// Rewrites a's array and labels as they are, so modes do not disturb each other.
			got, err := parsepgx.Update(ctx, pool, products,
				update(t, `{"sizes":["m","l"],"labels":["red","blue"]}`, parse.ByIDs(c.ids["a"], c.ids["g"]), c.scopeA))
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, []string{"m", "l"}, got[0].Sizes)
			assert.Equal(t, []string{"blue", "red"}, labels(t, c, "a", c.tenantA))
		})
	}
}

// In a REPEATABLE READ transaction the total describes the same snapshot as
// the page, whatever commits meanwhile.
func TestQueryPage_InATransaction(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	before, err := parsepgx.QueryPage(ctx, tx, products, listPlan(t, walkQuery, c.scopeA))
	require.NoError(t, err)
	_, err = c.pool.Exec(ctx, `UPDATE products SET deleted_at = now() WHERE id = $1`, c.ids["e"])
	require.NoError(t, err)
	after, err := parsepgx.QueryPage(ctx, tx, products, listPlan(t, walkQuery, c.scopeA))
	require.NoError(t, err)
	assert.EqualValues(t, 4, *after.PageInfo.TotalCount, "the transaction's snapshot")
	assert.Equal(t, pageIDs(before), pageIDs(after))
}

// A failing statement returns the database's error, the batch closed.
func TestQueryPage_StatementErrors(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	_, err := c.pool.Exec(ctx, `ALTER TABLE products RENAME COLUMN category TO kind`)
	require.NoError(t, err)
	_, err = parsepgx.QueryPage(ctx, c.pool, products, listPlan(t, "", c.scopeA))
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "42703", pgErr.Code, "undefined column")
	assert.ErrorContains(t, err, "parsepgx: products:")
}

func update(t *testing.T, body string, target parse.Target, s parse.Scope) *parse.UpdatePlan {
	t.Helper()
	p, err := products.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	plan, err := products.BuildUpdate(filter.PostgresDialect{}, p, target, s)
	require.NoError(t, err)
	return plan
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

	got, err := parsepgx.Update(ctx, c.pool, products,
		update(t, `{"price":45,"labels":["sale","red"]}`, parse.ByIDs(c.ids["a"], c.ids["d"], c.ids["g"]), c.scopeA))
	require.NoError(t, err)
	require.Len(t, got, 1, "d is deleted, g is B's")
	assert.Equal(t, c.ids["a"], got[0].ID)
	assert.Equal(t, 45.0, got[0].Price)
	assert.Equal(t, []string{"red", "sale"}, labels(t, c, "a", c.tenantA))
	assert.Equal(t, []string{"green"}, labels(t, c, "a", c.tenantB), "B's pair for a is B's")
	assert.Equal(t, []string{"red"}, labels(t, c, "g", c.tenantB))

	only, err := parsepgx.Update(ctx, c.pool, products, update(t, `{"labels":[]}`, parse.ByID(c.ids["b"]), c.scopeA))
	require.NoError(t, err)
	require.Len(t, only, 1, "a relation-only patch returns its owner")
	assert.Empty(t, labels(t, c, "b", c.tenantA))

	read, err := parsepgx.Update(ctx, c.pool, products, update(t, `{}`, parse.ByID(c.ids["a"]), c.scopeA))
	require.NoError(t, err)
	require.Len(t, read, 1)
	assert.Equal(t, 45.0, read[0].Price, "an empty patch reads the row")

	none, err := parsepgx.Update(ctx, c.pool, products,
		update(t, `{"price":1}`, parse.ByID(c.ids["a"]), parse.Scope{Tenant: c.tenantB.String()}))
	require.NoError(t, err)
	assert.Empty(t, none, "another tenant updates nothing")
}

// A membership change that fails undoes the row change with it.
func TestUpdate_RelationFailureRollsBackTheRow(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	_, err := parsepgx.Update(ctx, c.pool, products,
		update(t, `{"name":"renamed","labels":["forbidden"]}`, parse.ByID(c.ids["a"]), c.scopeA))
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "23514", pgErr.Code, "check violation")

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
	got, err := parsepgx.Update(ctx, tx, products,
		update(t, `{"name":"renamed","labels":["new"]}`, parse.ByID(c.ids["a"]), c.scopeA))
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NoError(t, tx.Rollback(ctx))

	var name string
	require.NoError(t, c.pool.QueryRow(ctx, `SELECT name FROM products WHERE id = $1`, c.ids["a"]).Scan(&name))
	assert.Equal(t, "a", name)
	assert.Equal(t, []string{"blue", "red"}, labels(t, c, "a", c.tenantA))
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
	c := &catalog{dsn: dbtest.PostgresDSN(t)}
	ctx := testContext(t)
	pool := c.open(t, pgx.QueryExecModeCacheStatement)
	_, err := pool.Exec(ctx, `CREATE TABLE credentials (id uuid PRIMARY KEY, tenant_id uuid NOT NULL, name text NOT NULL, secret_hash text NOT NULL)`)
	require.NoError(t, err)
	tenant, id := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO credentials VALUES ($1, $2, 'deploy', 'h4sh')`, id, tenant)
	require.NoError(t, err)

	creds := parse.MustResource[credential]("credentials", parse.TenantColumn("tenant_id"), parse.ReadColumns("id", "tenant_id", "name"))
	scope := parse.Scope{Tenant: tenant.String()}
	plan, err := creds.BuildList(filter.PostgresDialect{}, nil, scope)
	require.NoError(t, err)
	page, err := parsepgx.QueryPage(ctx, pool, creds, plan)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "deploy", page.Items[0].Name)
	assert.Empty(t, page.Items[0].SecretHash)

	p, err := creds.ParseMergePatch([]byte(`{"name":"ci"}`))
	require.NoError(t, err)
	upd, err := creds.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID(id), scope)
	require.NoError(t, err)
	rows, err := parsepgx.Update(ctx, pool, creds, upd)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "ci", rows[0].Name)
	assert.Empty(t, rows[0].SecretHash)
}
