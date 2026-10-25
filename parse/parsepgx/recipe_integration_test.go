//go:build integration

package parsepgx_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

// The README's recipe serves a tenant's pages with their total and facets,
// answers a request error with its code, patches the tenant's device and
// reaches no other tenant's.
func TestRecipe(t *testing.T) {
	ctx := testContext(t)
	c := &catalog{dsn: dbtest.PostgresDSN(t)}
	pool := c.open(t, pgx.QueryExecModeCacheStatement)
	_, err := pool.Exec(ctx, `CREATE TABLE devices (id uuid PRIMARY KEY, tenant_id uuid NOT NULL, name text NOT NULL, status text NOT NULL)`)
	require.NoError(t, err)
	tenantA, tenantB := uuid.New(), uuid.New()
	ids := map[string]uuid.UUID{}
	for _, d := range []struct {
		name, status string
		tenant       uuid.UUID
	}{{"pump", "active", tenantA}, {"fan", "active", tenantA}, {"valve", "offline", tenantA}, {"other", "active", tenantB}} {
		ids[d.name] = uuid.Must(uuid.NewV7())
		_, err := pool.Exec(ctx, `INSERT INTO devices VALUES ($1, $2, $3, $4)`, ids[d.name], d.tenant, d.name, d.status)
		require.NoError(t, err)
	}

	mux := http.NewServeMux()
	(&deviceAPI{db: pool, tenant: authenticatedTenant, log: slog.New(slog.DiscardHandler)}).routes(mux)
	as := func(tenant uuid.UUID) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tenantKey{}, tenant)))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	a, b := as(tenantA), as(tenantB)

	get := func(srv *httptest.Server, query string) (int, filter.Page[Device], parsehttp.Body) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/devices?"+query, nil)
		require.NoError(t, err)
		res, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		var p filter.Page[Device]
		var e parsehttp.Body
		if res.StatusCode == http.StatusOK {
			require.NoError(t, json.NewDecoder(res.Body).Decode(&p))
		} else {
			require.NoError(t, json.NewDecoder(res.Body).Decode(&e))
		}
		return res.StatusCode, p, e
	}

	status, first, _ := get(a, "status=active&sort=name&page_size=1&include_total=true&facets=status")
	require.Equal(t, http.StatusOK, status)
	require.Len(t, first.Items, 1)
	assert.Equal(t, "fan", first.Items[0].Name)
	assert.EqualValues(t, 2, *first.PageInfo.TotalCount, "A's active devices")
	assert.Equal(t, map[string][]filter.FacetBucket{"status": {{Key: "active", Count: 2}}}, first.Facets)

	status, second, _ := get(a, "status=active&sort=name&page_size=1&cursor="+url.QueryEscape(string(first.PageInfo.EndCursor)))
	require.Equal(t, http.StatusOK, status)
	require.Len(t, second.Items, 1)
	assert.Equal(t, "pump", second.Items[0].Name)
	assert.False(t, second.PageInfo.HasNextPage)

	status, _, body := get(a, "colour=red")
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "E2071", body.Code, "an unknown field cannot widen the list")

	patch := func(srv *httptest.Server, id uuid.UUID, body string) (int, Device) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPatch, srv.URL+"/devices/"+id.String(), strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/merge-patch+json")
		res, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		var d Device
		if res.StatusCode == http.StatusOK {
			require.NoError(t, json.NewDecoder(res.Body).Decode(&d))
		}
		return res.StatusCode, d
	}
	status, updated := patch(a, ids["valve"], `{"status":"active"}`)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "active", updated.Status)
	assert.Equal(t, "valve", updated.Name)

	status, _ = patch(b, ids["valve"], `{"status":"offline"}`)
	assert.Equal(t, http.StatusNotFound, status, "B reaches no device of A's")
	status, _ = patch(a, ids["pump"], `{"status":"exploded"}`)
	assert.NotEqual(t, http.StatusOK, status, "a value outside the enum")
	assert.Less(t, status, http.StatusInternalServerError)
}
