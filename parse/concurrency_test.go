//go:build unit || concurrency

package parse_test

import (
	"fmt"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/patch"
)

type registrySnapshotEntity struct {
	ID   string `json:"id"   db:"id"`
	Name string `json:"name" db:"name" parse:"filter,sort,patch,search" filter_ops:"eq" patch_ops:"replace" enum:"a,b"`
}

type filterBudgetDocument struct {
	ID      string         `json:"id" db:"id" parse:"filter"`
	Enabled bool           `json:"enabled" db:"enabled" parse:"filter"`
	Labels  map[string]any `json:"labels" db:"labels" parse:"filter"`
}

// Registrations under new names run beside snapshots of a registered one,
// and what a snapshot's reader changes stays in the snapshot.
func TestSchemaRegistryConcurrentSnapshots(t *testing.T) {
	r := parse.NewSchemaRegistry(core.WithSQLTableName("items"))
	parse.MustRegister[registrySnapshotEntity](r)
	var workers sync.WaitGroup
	for w := range 8 {
		workers.Go(func() {
			for i := range 50 {
				res, err := parse.NewResource[registrySnapshotEntity](onPostgres, fmt.Sprintf("items_%d_%d", w, i))
				if err == nil {
					err = parse.RegisterResource(r, res)
				}
				if err != nil {
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
	resource, err := parse.NewResource[filterBudgetDocument](onPostgres, "budget_documents")
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

// Concurrent builds on two providers, of a target and of an owner whose
// relation filter compiles it, each read their own provider's dialect's
// expressions.
func TestReadExpr_ConcurrentBuilds(t *testing.T) {
	type computedSet struct {
		accounts      *parse.Resource[parsetest.ComputedAccount]
		tenants       *parse.Resource[parsetest.AccountTenant]
		req, ownerReq *parse.ListRequest
	}
	sets := make([]computedSet, len(computedDialects()))
	for i, tt := range computedDialects() {
		_, accounts, tenants, _ := parsetest.NewComputedResources(providertest.Compile(tt.dialect))
		req, err := accounts.ParseList(url.Values{
			filter.ParamFilter: {`{"email":{"$ne":""}}`},
			filter.ParamSort:   {"-member_count"},
			filter.ParamFacets: {"member_count"},
		})
		require.NoError(t, err)
		ownerReq, err := tenants.ParseList(url.Values{filter.ParamFilter: {`{"accounts":{"$some":{"email":{"$ne":""},"groups":{"$some":{"size":2}}}}}`}})
		require.NoError(t, err)
		sets[i] = computedSet{accounts: accounts, tenants: tenants, req: req, ownerReq: ownerReq}
	}
	plans := make([]*parse.ListPlan, 64)
	errs := make([]error, len(plans))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range plans {
		wg.Go(func() {
			<-start
			set := sets[i%2]
			if i%4 < 2 {
				plans[i], errs[i] = set.accounts.BuildList(set.req, parse.Scope{Tenant: "t1"})
			} else {
				plans[i], errs[i] = set.tenants.BuildList(set.ownerReq, parse.Scope{Tenant: "t1"})
			}
		})
	}
	close(start)
	wg.Wait()
	for i, plan := range plans {
		require.NoError(t, errs[i])
		tt := computedDialects()[i%2]
		assert.Contains(t, plan.Page.SQL, "("+tt.email+") <> ")
		if i%4 < 2 {
			assert.Contains(t, plan.Page.SQL, "("+tt.groupIDs+`) AS "group_ids"`)
		} else {
			assert.Contains(t, plan.Page.SQL, "("+parsetest.GroupSizeExpr+") = ")
		}
	}
}
