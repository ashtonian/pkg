//go:build unit || concurrency

// The tests that run parse code on several goroutines. Parse starts none of
// its own, so only these can fail under race: the concurrency tag lets
// make test-parse-race build them alone, without instrumenting the package's
// other tests (tools/test_ci_parse_race.py requires the tag on such files).

package parse_test

import (
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type registrySnapshotEntity struct {
	Name string `json:"name" db:"name" parse:"filter,sort,patch,search" filter_ops:"eq" patch_ops:"replace" enum:"a,b"`
}

type filterBudgetDocument struct {
	ID      string         `json:"id" db:"id" parse:"filter"`
	Enabled bool           `json:"enabled" db:"enabled" parse:"filter"`
	Labels  map[string]any `json:"labels" db:"labels" parse:"filter"`
}

func TestSchemaRegistryConcurrentSnapshots(t *testing.T) {
	r := parse.NewSchemaRegistry(core.WithSQLTableName("items"))
	parse.MustRegister[registrySnapshotEntity](r)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 50 {
				if err := parse.Register[registrySnapshotEntity](r); err != nil {
					t.Error(err)
					return
				}
				response := r.SchemaFor("items")
				response.Filter.Fields["name"].EnumValues[0] = "local"
				response.Filter.Fields["name"].Operators[0] = filter.OpNe
				response.Patch.Paths["/name"].AllowedOps[0] = patch.PatchOpRemove
			}
		})
	}
	workers.Wait()
	if got := r.SchemaFor("items"); got.Filter.Fields["name"].EnumValues[0] != "a" {
		t.Fatal("request-local metadata escaped into the registry")
	}
}

func TestParseFilterWithCostIsolatesResourceBudget(t *testing.T) {
	resource, err := parse.NewResource[filterBudgetDocument]("budget_documents")
	require.NoError(t, err)
	const query = `{"enabled":true,"labels":{"$contains":{"role":"worker"}}}`
	_, err = resource.ParseFilter(query)
	require.Error(t, err)
	_, err = resource.ParseFilterWithCost(query, 60)
	require.NoError(t, err)
	_, err = resource.ParseFilter(query)
	require.Error(t, err, "a per-call allowance cannot change public request policy")

	t.Run("public", func(t *testing.T) {
		t.Parallel()
		for range 16 {
			_, err := resource.ParseFilter(query)
			require.Error(t, err)
		}
	})
	t.Run("internal", func(t *testing.T) {
		t.Parallel()
		for range 16 {
			_, err := resource.ParseFilterWithCost(query, 60)
			require.NoError(t, err)
		}
	})
}

// Concurrent first builds for two dialects, of a target and of an owner
// whose relation filter compiles it, each build and read their own
// dialect's views.
func TestReadExpr_ConcurrentFirstUse(t *testing.T) {
	accounts := newComputedAccounts(accessGroups, groupIDsExpr)
	tenants := newAccountTenants(accounts)
	req, err := accounts.ParseList(url.Values{
		filter.ParamFilter: {`{"email":{"$ne":""}}`},
		filter.ParamSort:   {"-member_count"},
		filter.ParamFacets: {"member_count"},
	})
	require.NoError(t, err)
	ownerReq, err := tenants.ParseList(url.Values{filter.ParamFilter: {`{"accounts":{"$some":{"email":{"$ne":""},"groups":{"$some":{"size":2}}}}}`}})
	require.NoError(t, err)
	plans := make([]*parse.ListPlan, 64)
	errs := make([]error, len(plans))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range plans {
		wg.Go(func() {
			<-start
			d := computedDialects[i%2].dialect
			if i%4 < 2 {
				plans[i], errs[i] = accounts.BuildList(d, req, parse.Scope{Tenant: "t1"})
			} else {
				plans[i], errs[i] = tenants.BuildList(d, ownerReq, parse.Scope{Tenant: "t1"})
			}
		})
	}
	close(start)
	wg.Wait()
	for i, plan := range plans {
		require.NoError(t, errs[i])
		tt := computedDialects[i%2]
		assert.Contains(t, plan.Page.SQL, "("+tt.email+") <> ")
		if i%4 < 2 {
			assert.Contains(t, plan.Page.SQL, "("+tt.groupIDs+`) AS "group_ids"`)
		} else {
			assert.Contains(t, plan.Page.SQL, "("+groupSizeExpr+") = ")
		}
	}
}
