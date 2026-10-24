//go:build unit

package parse_test

import (
	"sync"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type registrySnapshotEntity struct {
	Name string `json:"name" db:"name" parse:"filter,sort,patch,search" filter_ops:"eq" patch_ops:"replace" enum:"a,b"`
}

func TestSchemaRegistrySnapshotOwnership(t *testing.T) {
	r := parse.NewSchemaRegistry(core.WithSQLTableName("items"))
	parse.MustRegister[registrySnapshotEntity](r)
	response := r.SchemaFor("items")
	response.Filter.Fields["name"].Operators[0] = filter.OpNe
	response.Filter.Fields["name"].EnumValues[0] = "changed"
	response.Patch.Paths["/name"].AllowedOps[0] = patch.PatchOpRemove
	got := r.SchemaFor("items")
	if got.Filter.Fields["name"].Operators[0] != filter.OpEq || got.Filter.Fields["name"].EnumValues[0] != "a" || got.Patch.Paths["/name"].AllowedOps[0] != patch.PatchOpReplace {
		t.Fatal("editing a discovery response changed registered validation metadata")
	}
}

func TestSchemaRegistryReplaceClearsCapabilities(t *testing.T) {
	type plain struct {
		Name string `json:"name" db:"name" parse:"filter,patch"`
	}
	r := parse.NewSchemaRegistry(core.WithSQLTableName("items"))
	parse.MustRegister[registrySnapshotEntity](r)
	parse.MustRegister[plain](r)
	got := r.SchemaFor("items")
	if got == nil || got.Filter == nil || got.Patch == nil {
		t.Fatal("replacement schema is missing")
	}
	if got.Sort != nil || got.Search != nil {
		t.Fatal("replacement retained stale sort/search capabilities")
	}
	if _, ok := r.SortSchema("items"); ok {
		t.Fatal("sort lookup retained the previous schema")
	}
	// A failed replacement must leave the complete previous schema intact.
	if err := parse.Register[int](r); err == nil {
		t.Fatal("invalid registration succeeded")
	}
	if r.SchemaFor("items") == nil {
		t.Fatal("failed registration removed the existing resource")
	}
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
