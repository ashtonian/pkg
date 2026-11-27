//go:build integration

package parsepgx_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// These tests hold a statement where the test decides, in a lock or a
// sleep the database reports, and act once pg_stat_activity shows it there:
// no outcome depends on how long anything takes.

// observed is a pool whose connections carry their own application name, so
// the test sees its backends in pg_stat_activity apart from every other
// test's on the shared server, and the products resource on a provider over
// it.
type observed struct {
	pool     *pgxpool.Pool
	app      string
	products *parse.Resource[product]
}

func (c *catalog) observed(t *testing.T, maxConns int32) observed {
	t.Helper()
	app := "parsepgx_" + strings.ToLower(rand.Text())
	ctx := testContext(t)
	pool := c.open(t, ctx, pgx.QueryExecModeCacheStatement, func(cfg *pgxpool.Config) {
		cfg.MaxConns = maxConns
		cfg.ConnConfig.RuntimeParams["application_name"] = app
	})
	o := observed{pool: pool, app: app, products: newProducts(provider(t, ctx, pool))}
	return o
}

// await returns the pids of o's backends that match where, a condition on
// pg_stat_activity, once exactly want of them do.
func (o observed) await(t *testing.T, ctx context.Context, c *catalog, where string, want int) []int32 {
	t.Helper()
	for {
		rows, err := c.pool.Query(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name = $1 AND (`+where+`) ORDER BY pid`, o.app)
		require.NoError(t, err)
		pids, err := pgx.CollectRows(rows, pgx.RowTo[int32])
		require.NoError(t, err)
		if len(pids) == want {
			return pids
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%d of the pool's backends match %s, not %d: %v", len(pids), where, want, ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// ended returns once none of o's backends is in a statement or a
// transaction: whatever a cancelled call left on the server has ended.
func (o observed) ended(t *testing.T, ctx context.Context, c *catalog) {
	t.Helper()
	o.await(t, ctx, c, "state <> 'idle'", 0)
}

// hold runs stmt in a transaction on the catalog's own pool and keeps its
// locks until release commits it.
func (c *catalog) hold(t *testing.T, ctx context.Context, stmt string, args ...any) (release func()) {
	t.Helper()
	tx, err := c.pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	_, err = tx.Exec(ctx, stmt, args...)
	require.NoError(t, err, stmt)
	return func() { require.NoError(t, tx.Commit(ctx)) }
}

// result waits for a call running on another goroutine.
func result(t *testing.T, ctx context.Context, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Fatalf("the call did not return: %v", ctx.Err())
		return nil
	}
}

func (c *catalog) name(t *testing.T, ctx context.Context, product string) string {
	t.Helper()
	var name string
	require.NoError(t, c.pool.QueryRow(ctx, `SELECT name FROM products WHERE id = $1`, c.ids[product]).Scan(&name))
	return name
}

// expiringContext is a context whose deadline passes when the test calls
// expire, not on the clock, so the statement it bounds is seen running
// first.
type expiringContext struct {
	context.Context
	done    chan struct{}
	expired atomic.Bool
}

func withExpiry(t *testing.T, parent context.Context) (ctx *expiringContext, expire func()) {
	c := &expiringContext{Context: parent, done: make(chan struct{})}
	finish := sync.OnceFunc(func() { close(c.done) })
	stop := context.AfterFunc(parent, finish)
	t.Cleanup(func() { stop() })
	return c, func() {
		c.expired.Store(true)
		finish()
	}
}

func (c *expiringContext) Done() <-chan struct{} { return c.done }

func (c *expiringContext) Err() error {
	select {
	case <-c.done:
	default:
		return nil
	}
	if c.expired.Load() {
		return context.DeadlineExceeded
	}
	return c.Context.Err()
}

// Two updates that replace one owner's labels at once serialize on the
// owner's row lock: neither deadlocks, and the labels end as one of them set
// them, never a mix of both.
func TestUpdate_ConcurrentRelationReplacesOfOneOwner(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	o := c.observed(t, 2)
	release := c.hold(t, ctx, `SELECT 1 FROM products WHERE id = $1 FOR UPDATE`, c.ids["a"])

	sets := [][]string{{"x1", "x2"}, {"y1", "y2", "y3"}}
	errs := make([]error, len(sets))
	var wg sync.WaitGroup
	for i, set := range sets {
		body, err := json.Marshal(map[string][]string{"labels": set})
		require.NoError(t, err)
		p := mergePatch(t, o.products, string(body))
		wg.Go(func() {
			_, errs[i] = o.products.Update(ctx, p, parse.ByID(c.ids["a"]), c.scopeA)
		})
	}
	o.await(t, ctx, c, "wait_event_type = 'Lock'", len(sets))
	release()
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "update %d", i)
	}
	got := labels(t, c, "a", c.tenantA)
	assert.True(t, slices.ContainsFunc(sets, func(set []string) bool { return slices.Equal(set, got) }),
		"a's labels are %v, not exactly one update's set", got)
}

// A list cancelled while its page statement waits returns context.Canceled,
// its statement ends on the server, and the pool's one connection slot
// serves the next list.
func TestList_Cancelled(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	o := c.observed(t, 1)
	release := c.hold(t, ctx, `LOCK TABLE products IN ACCESS EXCLUSIVE MODE`)

	req := listRequest(t, o.products, walkQuery)
	listCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := o.products.List(listCtx, req, c.scopeA)
		done <- err
	}()
	o.await(t, ctx, c, "wait_event_type = 'Lock'", 1)
	cancel()
	require.ErrorIs(t, result(t, ctx, done), context.Canceled)
	o.ended(t, ctx, c)
	release()

	page, err := listProducts(t, ctx, o.products, walkQuery, c.scopeA)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, c.names(pageIDs(page)...))
}

// An update cancelled after its owner statement, while its membership
// changes wait, returns context.Canceled and its transaction rolls back on
// the server: the owner row and its labels are as they were, and the pool's
// one connection slot serves the next update.
func TestUpdate_CancelledBetweenStatements(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	o := c.observed(t, 1)
	release := c.hold(t, ctx, `LOCK TABLE product_labels IN SHARE MODE`)

	p := mergePatch(t, o.products, `{"name":"renamed","labels":["new"]}`)
	updateCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := o.products.Update(updateCtx, p, parse.ByID(c.ids["a"]), c.scopeA)
		done <- err
	}()
	pid := o.await(t, ctx, c, "wait_event_type = 'Lock'", 1)[0]
	var ownerRan, waiting bool
	require.NoError(t, c.pool.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND granted AND relation = 'products'::regclass AND mode = 'RowExclusiveLock'),
		EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted AND relation = 'product_labels'::regclass)`, pid).Scan(&ownerRan, &waiting))
	require.True(t, ownerRan, "the owner statement has run in the update's transaction")
	require.True(t, waiting, "the membership changes wait on the held lock")

	cancel()
	require.ErrorIs(t, result(t, ctx, done), context.Canceled)
	o.ended(t, ctx, c)
	release()
	assert.Equal(t, "a", c.name(t, ctx, "a"))
	assert.Equal(t, []string{"blue", "red"}, labels(t, c, "a", c.tenantA))

	_, err := o.products.Update(ctx, mergePatch(t, o.products, `{"labels":["after"]}`), parse.ByID(c.ids["a"]), c.scopeA)
	require.NoError(t, err)
	assert.Equal(t, []string{"after"}, labels(t, c, "a", c.tenantA))
}

// A deadline that passes while a policy's subquery sleeps ends the update
// with context.DeadlineExceeded the same way: the sleep ends on the server,
// nothing is written, and the pool's one connection slot serves the next
// update.
func TestUpdate_DeadlineInAPolicySubquery(t *testing.T) {
	c := newCatalog(t)
	ctx := testContext(t)
	o := c.observed(t, 1)
	slow := c.scopeA
	slow.Policy = &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{RawSQL: `EXISTS (SELECT 1 FROM pg_sleep(3600))`}}}

	p := mergePatch(t, o.products, `{"name":"renamed","labels":["new"]}`)
	updateCtx, expire := withExpiry(t, ctx)
	done := make(chan error, 1)
	go func() {
		_, err := o.products.Update(updateCtx, p, parse.ByID(c.ids["a"]), slow)
		done <- err
	}()
	o.await(t, ctx, c, "wait_event = 'PgSleep'", 1)
	expire()
	require.ErrorIs(t, result(t, ctx, done), context.DeadlineExceeded)
	o.ended(t, ctx, c)
	assert.Equal(t, "a", c.name(t, ctx, "a"))
	assert.Equal(t, []string{"blue", "red"}, labels(t, c, "a", c.tenantA))

	_, err := o.products.Update(ctx, mergePatch(t, o.products, `{"labels":["after"]}`), parse.ByID(c.ids["a"]), c.scopeA)
	require.NoError(t, err)
	assert.Equal(t, []string{"after"}, labels(t, c, "a", c.tenantA))
}
