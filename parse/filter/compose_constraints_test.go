//go:build unit

package filter_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestComposeConstraints(t *testing.T) {
	db := dbtest.SQLite(t)
	for _, stmt := range []string{
		`CREATE TABLE items (id INTEGER PRIMARY KEY, tenant_id TEXT NOT NULL, owner TEXT NOT NULL, deleted_at TEXT)`,
		// 1-3 are t1's (3 deleted), 4-6 t2's (6 deleted).
		`INSERT INTO items VALUES (1,'t1','ann',NULL), (2,'t1','bob',NULL), (3,'t1','ann','2026-01-01T00:00:00Z'),
			(4,'t2','ann',NULL), (5,'t2','bob',NULL), (6,'t2','ann','2026-01-01T00:00:00Z')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	schema := &filter.ResourceSchema{
		Name:       "items",
		PrimaryKey: []string{"id"},
		Fields: map[string]*filter.FieldSchema{
			"owner": {ExternalName: "owner", Table: "items", Column: "owner", DataType: core.FieldTypeString},
		},
		SystemFields: map[string]*filter.FieldSchema{
			"id":         {ExternalName: "id", Table: "items", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64},
			"tenant_id":  {ExternalName: "tenant_id", Table: "items", Column: "tenant_id", DataType: core.FieldTypeString},
			"deleted_at": {ExternalName: "deleted_at", Table: "items", Column: "deleted_at", DataType: core.FieldTypeTimestamp},
		},
	}
	eq := func(field string, v any) *filter.PolicyConstraints {
		return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: field, Operator: filter.OpEq, Value: v}}}
	}
	// t1's rows and t2's row 5, granted.
	governance := func() *filter.PolicyConstraints {
		return &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{eq("tenant_id", "t1"), eq("id", int64(5))}}
	}
	floor := filter.RepositoryScope{TenantColumn: "tenant_id", TenantID: "t1", DeleteColumn: "deleted_at"}
	ids := func(t *testing.T, pc *filter.PolicyConstraints) []int64 {
		t.Helper()
		res, err := mustListQuery(t, schema, nil, filter.SQLiteDialect{}).Build(filter.ListPolicy(pc))
		require.NoError(t, err)
		rows, err := db.Query("SELECT items.id FROM items "+res.Fragment, res.Args...)
		require.NoError(t, err)
		var got []int64
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			got = append(got, id)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		return got
	}

	tests := []struct {
		name       string
		scope      filter.RepositoryScope
		governance *filter.PolicyConstraints
		authz      *filter.PolicyConstraints
		want       []int64
	}{
		{"the tenant floor holds the tenant's live rows", floor, nil, nil, []int64{1, 2}},
		{"deleted rows on request", withDeleted(floor), nil, nil, []int64{1, 2, 3}},
		{"authorization narrows the floor", floor, nil, eq("owner", "ann"), []int64{1}},
		{"governance replaces the floor: the tenant's rows and the grant", floor, governance(), nil, []int64{1, 2, 5}},
		{"governance ignores the scope's tenant", withTenant(floor, "t2"), governance(), nil, []int64{1, 2, 5}},
		{"governance keeps deleted rows masked", floor, &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{eq("tenant_id", "t1"), eq("id", int64(6))}}, nil, []int64{1, 2}},
		{"governance with deleted rows", withDeleted(floor), governance(), nil, []int64{1, 2, 3, 5}},
		{"authorization narrows governance", floor, governance(), eq("owner", "bob"), []int64{2, 5}},
		{"empty governance is the floor", floor, &filter.PolicyConstraints{}, nil, []int64{1, 2}},
		{"governance granting nothing beyond the tenant is the floor", floor, eq("tenant_id", "t1"), nil, []int64{1, 2}},
		{"a table without tenants masks deleted rows only", filter.RepositoryScope{DeleteColumn: "deleted_at"}, nil, nil, []int64{1, 2, 4, 5}},
		{"a floor without a tenant admits no row", withTenant(floor, nil), nil, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ids(t, filter.ComposeConstraints(tt.scope, tt.governance, tt.authz)))
		})
	}

	t.Run("the inputs are not modified", func(t *testing.T) {
		gov, authz := governance(), eq("owner", "bob")
		first := filter.ComposeConstraints(floor, gov, authz)
		second := filter.ComposeConstraints(withDeleted(floor), gov, authz)
		assert.Equal(t, governance(), gov, "a cached governance tree is never extended")
		assert.Equal(t, eq("owner", "bob"), authz)
		assert.Equal(t, []int64{2, 5}, ids(t, first))
		assert.Equal(t, []int64{2, 5}, ids(t, second))
	})
}

func withDeleted(s filter.RepositoryScope) filter.RepositoryScope {
	s.IncludeDeleted = true
	return s
}

func withTenant(s filter.RepositoryScope, tenant any) filter.RepositoryScope {
	s.TenantID = tenant
	return s
}
