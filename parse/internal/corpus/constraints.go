//go:build integration

package corpus

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// BuildBaseConstraints always constrains a tenant-scoped table: without a
// tenant the floor admits no row, on every tenant column type, instead of
// widening the read or failing as a request error.
func buildBaseConstraintsFailClosedWithoutATenant(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		deleted := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		b.load(t, db, sqlTable{name: "items", key: []string{"id"}, columns: []sqlColumn{column[int64]("id").required(), column[int64]("tenant_id").required(), column[time.Time]("deleted_at")}},
			[][]any{{1, 44, nil}, {2, 45, nil}, {3, 44, deleted}})
		schema := func(tenant core.FieldType, kind reflect.Kind) *filter.ResourceSchema {
			s := &filter.ResourceSchema{
				Name:       "items",
				PrimaryKey: []string{"id"},
				Fields:     map[string]*filter.FieldSchema{},
				SystemFields: map[string]*filter.FieldSchema{
					"id":         {ExternalName: "id", Table: "items", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64},
					"tenant_id":  {ExternalName: "tenant_id", Table: "items", Column: "tenant_id", DataType: tenant, NumberKind: kind},
					"deleted_at": {ExternalName: "deleted_at", Table: "items", Column: "deleted_at", DataType: core.FieldTypeTimestamp},
				},
			}
			return s
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
				assert.Equal(t, tt.want, policyIDs(t, b, db, tt.schema, pc))
			})
		}
	})
}

// A repository scope's floor, governance and authorization compose into
// one policy: the tenant's live rows, governance replacing the tenant
// while keeping deleted rows masked, authorization narrowing either.
func composeConstraints(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		deleted := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		text := func(name string) sqlColumn { return column[string](name).required() }
		// 1-3 are t1's (3 deleted), 4-6 t2's (6 deleted).
		b.load(t, db, sqlTable{name: "items", key: []string{"id"}, columns: []sqlColumn{column[int64]("id").required(), text("tenant_id"), text("owner"), column[time.Time]("deleted_at")}},
			[][]any{{1, "t1", "ann", nil}, {2, "t1", "bob", nil}, {3, "t1", "ann", deleted}, {4, "t2", "ann", nil}, {5, "t2", "bob", nil}, {6, "t2", "ann", deleted}})
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
			pc := &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: field, Operator: filter.OpEq, Value: v}}}
			return pc
		}
		// t1's rows and t2's row 5, granted.
		governance := func() *filter.PolicyConstraints {
			pc := &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{eq("tenant_id", "t1"), eq("id", int64(5))}}
			return pc
		}
		floor := filter.RepositoryScope{TenantColumn: "tenant_id", TenantID: "t1", DeleteColumn: "deleted_at"}
		ids := func(t *testing.T, pc *filter.PolicyConstraints) []int64 {
			t.Helper()
			return policyIDs(t, b, db, schema, pc)
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
	})
}

// policyIDs are the ids of the items pc admits on db: through a list
// query's statement on a SQL engine, as the Memory engine's lists evaluate
// the policy on that engine. A missing tenant is never a request error.
func policyIDs(t *testing.T, b sqlBackend, db *sql.DB, schema *filter.ResourceSchema, pc *filter.PolicyConstraints) []int64 {
	t.Helper()
	if b.engine == Memory {
		ast, err := filter.ApplyPolicyConstraints(schema, nil, pc)
		require.NoError(t, err)
		return b.selectedInts(t, db, schema, ast, "id")
	}
	res, err := mustListQuery(t, schema, nil, b.dialect).Build(filter.ListPolicy(pc))
	require.NoError(t, err)
	return dbtest.QueryRows[int64](t, db, "SELECT items.id FROM items "+res.Fragment, res.Args...)
}

func withDeleted(s filter.RepositoryScope) filter.RepositoryScope {
	s.IncludeDeleted = true
	return s
}

func withTenant(s filter.RepositoryScope, tenant any) filter.RepositoryScope {
	s.TenantID = tenant
	return s
}

// stockTable is stock_items, keyed by (warehouse_id, sku).
func stockTable() sqlTable {
	table := sqlTable{name: "stock_items", key: []string{"warehouse_id", "sku"}, columns: []sqlColumn{
		column[string]("warehouse_id").required(), column[string]("sku").required(), column[int64]("qty").required(),
	}}
	return table
}

// stockSchema keys stock_items by (warehouse_id, sku) and filters qty.
func stockSchema() *filter.ResourceSchema {
	field := func(name string, t core.FieldType) *filter.FieldSchema {
		field := &filter.FieldSchema{ExternalName: name, Table: "stock_items", Column: name, DataType: t}
		return field
	}
	schema := &filter.ResourceSchema{
		Name:         "stock_items",
		PrimaryKey:   []string{"warehouse_id", "sku"},
		Fields:       map[string]*filter.FieldSchema{"qty": field("qty", core.FieldTypeInt)},
		SystemFields: map[string]*filter.FieldSchema{"warehouse_id": field("warehouse_id", core.FieldTypeString), "sku": field("sku", core.FieldTypeString)},
	}
	return schema
}

// keyWhere is c, a constraint on stock_items, compiled for d.
func keyWhere(t *testing.T, d filter.Dialect, c *filter.PolicyConstraint) (*filter.SQLResult, error) {
	t.Helper()
	ast, err := filter.ApplyPolicyConstraints(stockSchema(), nil, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{c}})
	if err != nil {
		return nil, err
	}
	return filter.NewSQLBuilder(stockSchema(), d).Build(ast)
}

// keyQuantities is the qty of each stock item c admits on db, ascending.
func keyQuantities(t *testing.T, b sqlBackend, db *sql.DB, c *filter.PolicyConstraint) []int64 {
	t.Helper()
	ast, err := filter.ApplyPolicyConstraints(stockSchema(), nil, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{c}})
	require.NoError(t, err)
	return b.selectedInts(t, db, stockSchema(), ast, "qty")
}

// A key set selects the rows whose columns together equal one key, not the
// rows matching each column alone, in SQL and in memory alike.
func keyConstraintSelectsWholeKeys(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		rows := []stockItem{{"w1", "a", 1}, {"w1", "b", 2}, {"w2", "a", 3}, {"w2", "b", 4}}
		b.load(t, db, stockTable(), recordRows(t, stockTable(), rows))
		c := &filter.PolicyConstraint{Fields: []string{"warehouse_id", "sku"}, Operator: filter.OpIn, Value: []core.Key{{"w1", "b"}, {"w2", "a"}, {"w9", "a"}}}
		assert.Equal(t, []int64{2, 3}, keyQuantities(t, b, db, c))

		ast, err := filter.ApplyPolicyConstraints(stockSchema(), nil, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{c}})
		require.NoError(t, err)
		var inMemory []int64
		for _, r := range rows {
			values := map[string]any{"warehouse_id": r.WarehouseID, "sku": r.SKU, "qty": r.Qty}
			truth, err := filter.Evaluate(ast.Root, filter.RowFunc(func(f *filter.FieldSchema) (any, bool, error) {
				v, ok := values[f.Column]
				return v, ok, nil
			}))
			require.NoError(t, err)
			if truth == filter.True {
				inMemory = append(inMemory, r.Qty)
			}
		}
		assert.Equal(t, []int64{2, 3}, inMemory)
	})
}

// An empty key set admits no row, of one column as of several, in SQL and in
// memory: a subject granted no key reads nothing.
func keyConstraintEmptySetAdmitsNoRow(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		b.load(t, db, stockTable(), [][]any{{"w1", "a", int64(1)}})
		for name, fields := range map[string][]string{"one column": {"warehouse_id"}, "several columns": {"warehouse_id", "sku"}} {
			t.Run(name, func(t *testing.T) {
				c := &filter.PolicyConstraint{Fields: fields, Operator: filter.OpIn, Value: []core.Key{}}
				for _, d := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
					_, err := keyWhere(t, d, c)
					require.NoError(t, err, d.Name())
				}
				assert.Empty(t, keyQuantities(t, b, db, c))

				ast, err := filter.ApplyPolicyConstraints(stockSchema(), nil, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{c}})
				require.NoError(t, err)
				truth, err := filter.Evaluate(ast.Root, filter.RowFunc(func(f *filter.FieldSchema) (any, bool, error) {
					return map[string]any{"warehouse_id": "w1", "sku": "a"}[f.Column], true, nil
				}))
				require.NoError(t, err)
				assert.Equal(t, filter.False, truth)
			})
		}
	})
}

// stockCode is a codec: the driver binds and stores it upper-cased.
type stockCode string

func (c stockCode) Value() (driver.Value, error) { return strings.ToUpper(string(c)), nil }

// A policy's Go values name what the driver stores for them: a key
// position, a scalar operand and a set member that is a codec, a NULL-able
// type or a scanned interface names the stored row, negated or not, in SQL
// and in filter.Evaluate alike.
func policyStoredValues(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		// Each sku as the driver stored it.
		rows := []stockItem{{"w1", "A", 1}, {"w1", "B", 2}, {"w2", "A", 3}, {"w2", "B", 4}}
		b.load(t, db, stockTable(), recordRows(t, stockTable(), rows))
		key := []string{"warehouse_id", "sku"}
		scanned := func(v any) *any { return &v }
		for _, tc := range []struct {
			name string
			c    *filter.PolicyConstraint
			want []int64
		}{
			{"a key set", &filter.PolicyConstraint{Fields: key, Operator: filter.OpIn, Value: []core.Key{{"w1", stockCode("a")}, {"w2", stockCode("b")}}}, []int64{1, 4}},
			{"a negated key set", &filter.PolicyConstraint{Fields: key, Operator: filter.OpIn, Value: []core.Key{{"w1", stockCode("a")}}, Negate: true}, []int64{2, 3, 4}},
			{"a key", &filter.PolicyConstraint{Fields: key, Operator: filter.OpEq, Value: core.Key{"w2", stockCode("a")}}, []int64{3}},
			{"a negated key", &filter.PolicyConstraint{Fields: key, Operator: filter.OpEq, Value: core.Key{"w2", stockCode("a")}, Negate: true}, []int64{1, 2, 4}},
			{"a one-column key set", &filter.PolicyConstraint{Fields: key[1:], Operator: filter.OpIn, Value: []core.Key{{stockCode("b")}}}, []int64{2, 4}},
			{"an operand", &filter.PolicyConstraint{Field: "sku", Operator: filter.OpEq, Value: stockCode("a")}, []int64{1, 3}},
			{"a negated operand", &filter.PolicyConstraint{Field: "sku", Operator: filter.OpEq, Value: stockCode("a"), Negate: true}, []int64{2, 4}},
			{"a NULL-able operand", &filter.PolicyConstraint{Field: "sku", Operator: filter.OpEq, Value: sql.NullString{String: "B", Valid: true}}, []int64{2, 4}},
			{"set members", &filter.PolicyConstraint{Field: "sku", Operator: filter.OpIn, Value: []any{stockCode("b"), scanned(stockCode("z"))}}, []int64{2, 4}},
			{"negated set members", &filter.PolicyConstraint{Field: "sku", Operator: filter.OpIn, Value: []any{scanned(stockCode("b"))}, Negate: true}, []int64{1, 3}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ast, err := filter.ApplyPolicyConstraints(stockSchema(), nil, &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{tc.c}})
				require.NoError(t, err)
				assert.Equal(t, tc.want, b.selectedInts(t, db, stockSchema(), ast, "qty"), b.engine)

				var evaluated []int64
				for _, r := range rows {
					values := map[string]any{"warehouse_id": r.WarehouseID, "sku": r.SKU, "qty": r.Qty}
					truth, err := filter.Evaluate(ast.Root, filter.RowFunc(func(f *filter.FieldSchema) (any, bool, error) {
						v, ok := values[f.Column]
						return v, ok, nil
					}))
					require.NoError(t, err)
					if truth == filter.True {
						evaluated = append(evaluated, r.Qty)
					}
				}
				assert.Equal(t, tc.want, evaluated, "Evaluate")
			})
		}
	})
}
