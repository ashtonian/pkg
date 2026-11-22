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

// BuildBaseConstraints always constrains a tenant-scoped table: without a
// tenant the floor admits no row, on every tenant column type, instead of
// widening the read or failing as a request error.
func TestBuildBaseConstraints_FailClosedWithoutATenant(t *testing.T) {
	db := dbtest.SQLite(t)
	for _, stmt := range []string{
		`CREATE TABLE items (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, deleted_at TEXT)`,
		`INSERT INTO items VALUES (1, 44, NULL), (2, 45, NULL), (3, 44, '2026-01-01T00:00:00Z')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	schema := func(tenant core.FieldType, kind reflect.Kind) *filter.ResourceSchema {
		return &filter.ResourceSchema{
			Name:       "items",
			PrimaryKey: "id",
			Fields:     map[string]*filter.FieldSchema{},
			SystemFields: map[string]*filter.FieldSchema{
				"id":         {ExternalName: "id", SQLExpr: "items.id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64},
				"tenant_id":  {ExternalName: "tenant_id", SQLExpr: "items.tenant_id", DataType: tenant, NumberKind: kind},
				"deleted_at": {ExternalName: "deleted_at", SQLExpr: "items.deleted_at", DataType: core.FieldTypeTimestamp},
			},
		}
	}
	tests := []struct {
		name      string
		tenantCol string
		tenantID  any
		schema    *filter.ResourceSchema
		want      []int64
	}{
		{"an integer tenant", "tenant_id", int64(44), schema(core.FieldTypeInt, reflect.Int64), []int64{1}},
		{"a nil tenant on an integer column", "tenant_id", nil, schema(core.FieldTypeInt, reflect.Int64), nil},
		{"an empty tenant on an integer column", "tenant_id", "", schema(core.FieldTypeInt, reflect.Int64), nil},
		{"a nil tenant on a UUID column", "tenant_id", nil, schema(core.FieldTypeUUID, reflect.Invalid), nil},
		{"an empty tenant on a string column", "tenant_id", "", schema(core.FieldTypeString, reflect.Invalid), nil},
		{"no tenant column", "", nil, schema(core.FieldTypeInt, reflect.Int64), []int64{1, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pc := filter.BuildBaseConstraints(tt.tenantCol, tt.tenantID, "deleted_at", false)
			res, err := mustListQuery(t, tt.schema, nil, filter.SQLiteDialect{}).Build(filter.ListPolicy(pc))
			require.NoError(t, err, "a missing tenant is never a request error")
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
			assert.Equal(t, tt.want, got)
		})
	}
}
