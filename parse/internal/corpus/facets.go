//go:build integration

package corpus

import (
	"database/sql"
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// Array members and columns on each backend: SQLite holds the document and
// both arrays as JSON text, PostgreSQL a jsonb document, a text[] and a
// bigint[] column.
func arrayMatrix(t *testing.T, engine Engine) {
	runOn(t, engine, runArrayMatrix)
}

// runArrayMatrix runs the array cases and facets on b.
func runArrayMatrix(t *testing.T, b sqlBackend) {
	_, db := b.open(t)
	schema, err := filtertest.ArraySchema()
	require.NoError(t, err)
	table := schemaTable(schema)
	b.load(t, db, table, recordRows(t, table, filtertest.ArrayRecords()))
	t.Run("cases", func(t *testing.T) {
		filtertest.RunArrays(t, schema, b.filterIDs(db, schema))
	})
	t.Run("facets", func(t *testing.T) {
		got := facetCounts(t, db, b.dialect, schema, slices.Sorted(maps.Keys(filtertest.ArrayFacets))...)
		assert.Equal(t, filtertest.ArrayFacets, got)
	})
	t.Run("facet orders", func(t *testing.T) {
		assertFacetOrders(t, db, b.dialect, schema, filtertest.ArrayFacetOrders, filtertest.ArrayFacets)
	})
}

// assertFacetOrders requests every order's facet with its limit in one
// facet query and checks the keys each returns, in order. FacetBuckets must
// order the fixture's counts the same way.
func assertFacetOrders(t *testing.T, db *sql.DB, dialect filter.Dialect, schema *filter.ResourceSchema,
	orders map[string]filtertest.FacetOrder, counts map[string]map[string]int64) {
	t.Helper()
	var reqs []filter.FacetRequest
	for _, name := range slices.Sorted(maps.Keys(orders)) {
		reqs = append(reqs, filter.FacetRequest{Field: name, Limit: orders[name].Limit})
	}
	got := map[string][]string{}
	for _, r := range facetQuery(t, db, dialect, schema, reqs) {
		for _, b := range r.Buckets {
			got[r.Field] = append(got[r.Field], b.Key)
		}
	}
	want := map[string][]string{}
	for name, o := range orders {
		want[name] = o.Keys
		var inGo []string
		for _, b := range filter.FacetBuckets(schema.Fields[name], counts[name], o.Limit) {
			inGo = append(inGo, b.Key)
		}
		assert.Equal(t, o.Keys, inGo, "FacetBuckets(%s)", name)
	}
	assert.Equal(t, want, got)
}

// facetCounts counts fields' facets over schema's whole table on db in one
// facet query: each field's bucket keys and their counts.
func facetCounts(t *testing.T, db *sql.DB, dialect filter.Dialect, schema *filter.ResourceSchema, fields ...string) map[string]map[string]int64 {
	t.Helper()
	reqs := make([]filter.FacetRequest, len(fields))
	for i, f := range fields {
		reqs[i] = filter.FacetRequest{Field: f}
	}
	return bucketCounts(facetQuery(t, db, dialect, schema, reqs))
}

// facetQuery runs reqs over schema's whole table on db in one facet query.
func facetQuery(t *testing.T, db *sql.DB, dialect filter.Dialect, schema *filter.ResourceSchema, reqs []filter.FacetRequest) []filter.FacetResult {
	t.Helper()
	q, err := filter.BuildFacetQuery(schema, dialect, "1=1", nil, reqs)
	require.NoError(t, err)
	return queryFacets(t, db, q.SQL, q.Args...)
}

// queryFacets runs a facet query on db and reads its facets.
func queryFacets(t *testing.T, db dbtest.Queryer, query string, args ...any) []filter.FacetResult {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query, args...)
	require.NoError(t, err, query)
	defer func() { require.NoError(t, rows.Close()) }()
	results, err := filter.ScanFacetUnion(rows)
	require.NoError(t, err, query)
	require.NoError(t, rows.Err(), query)
	return results
}

// bucketCounts is each facet's bucket keys and their counts.
func bucketCounts(results []filter.FacetResult) map[string]map[string]int64 {
	got := map[string]map[string]int64{}
	for _, r := range results {
		got[r.Field] = map[string]int64{}
		for _, b := range r.Buckets {
			got[r.Field][b.Key] = b.Count
		}
	}
	return got
}

// Numeric facet keys on each backend: REAL columns, a JSON text document and
// a JSON text list on SQLite; a jsonb document, double precision, real,
// bigint and real[] columns on PostgreSQL. Every facet counts in one facet
// query, keyed as BuildFacetQuery and FacetKeys give them.
func numberKeys(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		_, db := b.open(t)
		schema, err := filtertest.NumberKeySchema()
		require.NoError(t, err)
		table := schemaTable(schema)
		b.load(t, db, table, recordRows(t, table, filtertest.NumberKeyRecords()))
		got := facetCounts(t, db, b.dialect, schema, slices.Sorted(maps.Keys(filtertest.NumberKeyFacets))...)
		assert.Equal(t, filtertest.NumberKeyFacets, got)
	})
}

// Facets over paths on each backend: every PathFacets facet over
// PathRecords in one facet query, and each order's keys.
func pathFacets(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		schema := pathSchema(t, "")
		_, db := loadPathRecords(t, b, schema)
		got := facetCounts(t, db, b.dialect, schema, slices.Sorted(maps.Keys(filtertest.PathFacets))...)
		assert.Equal(t, filtertest.PathFacets, got)
		assertFacetOrders(t, db, b.dialect, schema, filtertest.PathFacetOrders, filtertest.PathFacets)
	})
}

// facetDB is a fresh database of b holding parsetest.FacetSchema's items:
// tenant t1's five rows, three active and two pending, and tenant t2's one.
func facetDB(t *testing.T, b sqlBackend) *sql.DB {
	t.Helper()
	db := b.db(t)
	dbtest.Exec(t, db, `CREATE TABLE items (
		id        INTEGER PRIMARY KEY,
		tenant_id TEXT NOT NULL,
		status    TEXT NOT NULL,
		owner_id  TEXT NOT NULL,
		price     `+b.columnType(core.TypeInfo{Type: core.FieldTypeFloat})+` NOT NULL,
		name      TEXT,
		in_stock  `+b.columnType(core.TypeInfo{Type: core.FieldTypeBool})+` NOT NULL DEFAULT FALSE
	)`)
	insert := "INSERT INTO items (id, tenant_id, status, owner_id, price) VALUES (" +
		b.dialect.Placeholder(1) + ", " + b.dialect.Placeholder(2) + ", " + b.dialect.Placeholder(3) + ", " + b.dialect.Placeholder(4) + ", " + b.dialect.Placeholder(5) + ")"
	for i, r := range []struct {
		tid, status, owner string
		price              float64
	}{
		{"t1", "active", "u1", 10},
		{"t1", "active", "u1", 30},
		{"t1", "active", "u2", 75},
		{"t1", "pending", "u1", 120},
		{"t1", "pending", "u3", 200},
		{"t2", "active", "u9", 99}, // wrong tenant; must not appear in t1 facets
	} {
		_, err := db.ExecContext(t.Context(), insert, i+1, r.tid, r.status, r.owner, r.price)
		require.NoError(t, err)
	}
	return db
}

// tenantFacets runs reqs over tenant t1's items on db.
func tenantFacets(t *testing.T, b sqlBackend, db *sql.DB, reqs ...filter.FacetRequest) []filter.FacetResult {
	t.Helper()
	q, err := filter.BuildFacetQuery(parsetest.FacetSchema(), b.dialect, "items.tenant_id = "+b.dialect.Placeholder(1), []any{"t1"}, reqs)
	require.NoError(t, err)
	return queryFacets(t, db, q.SQL, q.Args...)
}

// A facet query counts the rows its filter selects: tenant t1's, never
// tenant t2's.
func buildFacetQueryRoundTripTenantScoped(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		got := bucketCounts(tenantFacets(t, b, facetDB(t, b), filter.FacetRequest{Field: "status"}, filter.FacetRequest{Field: "owner_id"}, filter.FacetRequest{Field: "price"}))
		assert.Equal(t, map[string]map[string]int64{
			"status":   {"active": 3, "pending": 2},
			"owner_id": {"u1": 3, "u2": 1, "u3": 1},
			// 10 and 30 in 0-50, 75 in 50-100, 120 and 200 in 100+.
			"price": {"0-50": 2, "50-100": 1, "100+": 2},
		}, got)
	})
}

// A facet's limit keeps its top buckets.
func buildFacetQueryRoundTripPerFacetLimit(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		results := tenantFacets(t, b, facetDB(t, b), filter.FacetRequest{Field: "owner_id", Limit: 1})
		want := []filter.FacetResult{{Field: "owner_id", Buckets: []filter.FacetBucket{{Key: "u1", Count: 3}}}}
		assert.Equal(t, want, results)
	})
}

// Facet rows come back in one order on every run: facets as requested; a
// terms facet by count, ties in the field's type order, its limit keeping
// the lowest tied values; a range facet as declared, whatever its counts.
func buildFacetQueryRoundTripOrder(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		results := tenantFacets(t, b, facetDB(t, b), filter.FacetRequest{Field: "price"}, filter.FacetRequest{Field: "owner_id", Limit: 2}, filter.FacetRequest{Field: "status"})
		want := []filter.FacetResult{
			{Field: "price", Buckets: []filter.FacetBucket{{Key: "0-50", Count: 2}, {Key: "50-100", Count: 1}, {Key: "100+", Count: 2}}},
			{Field: "owner_id", Buckets: []filter.FacetBucket{{Key: "u1", Count: 3}, {Key: "u2", Count: 1}}}, // u3 ties u2 and sorts after it
			{Field: "status", Buckets: []filter.FacetBucket{{Key: "active", Count: 3}, {Key: "pending", Count: 2}}},
		}
		assert.Equal(t, want, results)
	})
}

// Boolean facet keys must be the JSON literals on every backend: a bare
// text cast yields "1"/"0" on SQLite vs "true"/"false" on PostgreSQL,
// and  consumers compare against the JSON form (the admin SPAs'
// facetCount(facets, field, true) stringifies to "true"). Regression for
// the termsKeyExpr boolean CASE normalization.
func buildFacetQueryRoundTripBooleanKeysAreJSONLiterals(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := facetDB(t, b)
		dbtest.Exec(t, db, `UPDATE items SET in_stock = TRUE WHERE tenant_id = 't1' AND status = 'active'`)
		got := bucketCounts(tenantFacets(t, b, db, filter.FacetRequest{Field: "in_stock"}))
		assert.Equal(t, map[string]int64{"true": 3, "false": 2}, got["in_stock"])
	})
}

// BuildFacetQuery keeps and orders the buckets FacetBuckets does: numeric
// ties by value on an int and a float column, a decimal path and an int
// array's elements; strings by code point.
func buildFacetQueryRoundTripNumbersTieByValue(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		table := sqlTable{name: "items", key: []string{"id"}, columns: []sqlColumn{
			column[int64]("id"), column[int64]("qty"), column[float64]("ratio"), column[string]("label"),
			column[json.RawMessage]("doc").holding(Postgres, "JSON"), column[[]int64]("scores"),
		}}
		b.load(t, db, table, [][]any{
			{int64(1), int64(9), 9.5, "9", `{"price":9}`, []int64{9, 10}},
			{int64(2), int64(10), 10.25, "10", `{"price":"10.0"}`, []int64{10, 100}},
			{int64(3), int64(100), -1.0, "100", `{"price":100}`, []int64{9}},
			{int64(4), int64(-1), 100.0, "-1", `{"price":-1.5}`, []int64{100}},
		})
		schema := &filter.ResourceSchema{Name: "items", Fields: map[string]*filter.FieldSchema{
			"qty":   {ExternalName: "qty", Table: "items", Column: "qty", DataType: core.FieldTypeInt, Facetable: true},
			"ratio": {ExternalName: "ratio", Table: "items", Column: "ratio", DataType: core.FieldTypeFloat, Facetable: true},
			"label": {ExternalName: "label", Table: "items", Column: "label", DataType: core.FieldTypeString, Facetable: true},
			"doc.price": {ExternalName: "doc.price", DataType: core.FieldTypeDecimal, Facetable: true,
				Path: &filter.JSONPath{Table: "items", Column: "doc", Segments: []string{"price"}, Storage: filter.StorageJSON}},
			"scores": {ExternalName: "scores", Table: "items", Column: "scores", DataType: core.FieldTypeArray, Elem: core.FieldTypeInt, Facetable: true},
		}}
		reqs := []filter.FacetRequest{{Field: "qty", Limit: 3}, {Field: "ratio", Limit: 3}, {Field: "label", Limit: 3}, {Field: "doc.price", Limit: 3}, {Field: "scores"}}
		results := facetQuery(t, db, b.dialect, schema, reqs)
		one := func(ks ...string) []filter.FacetBucket {
			out := make([]filter.FacetBucket, len(ks))
			for i, k := range ks {
				out[i] = filter.FacetBucket{Key: k, Count: 1}
			}
			return out
		}
		want := []filter.FacetResult{
			{Field: "qty", Buckets: one("-1", "9", "10")},
			{Field: "ratio", Buckets: one("-1", "9.5", "10.25")},
			{Field: "label", Buckets: one("-1", "10", "100")},
			{Field: "doc.price", Buckets: one("-1.5", "9", "10")},
			{Field: "scores", Buckets: []filter.FacetBucket{{Key: "9", Count: 2}, {Key: "10", Count: 2}, {Key: "100", Count: 2}}},
		}
		assert.Equal(t, want, results)
		for _, r := range results {
			if len(r.Buckets) == 0 {
				continue
			}
			counts := map[string]int64{}
			for _, bucket := range r.Buckets {
				counts[bucket.Key] = bucket.Count
			}
			assert.Equal(t, r.Buckets, filter.FacetBuckets(schema.Fields[r.Field], counts, 0), "%s: FacetBuckets orders the query's buckets", r.Field)
		}
	})
}

// textKey types a relation column holding a string key.
func textKey() []core.TypeInfo {
	types := []core.TypeInfo{{Type: core.FieldTypeString}}
	return types
}

// A relation terms facet on a numeric target key breaks count ties, and
// cuts at its limit, by the keys' values, as FacetBuckets does: 2 before
// 10, 9 before 10.5. A string key keeps code-point order.
func buildFacetQueryRelationFacetNumbersTieByValue(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		target := func(ft core.FieldType) string { return b.columnType(core.TypeInfo{Type: ft}) }
		dbtest.Exec(t, db,
			`CREATE TABLE items (id TEXT PRIMARY KEY)`,
			`CREATE TABLE item_ranks (item_id TEXT NOT NULL, rank_id `+target(core.FieldTypeInt)+` NOT NULL)`,
			`CREATE TABLE item_prices (item_id TEXT NOT NULL, price `+target(core.FieldTypeDecimal)+` NOT NULL)`,
			`CREATE TABLE item_weights (item_id TEXT NOT NULL, weight `+target(core.FieldTypeFloat)+` NOT NULL)`,
			`CREATE TABLE item_codes (item_id TEXT NOT NULL, code TEXT NOT NULL)`,
			`INSERT INTO items VALUES ('a'), ('b'), ('c')`,
			`INSERT INTO item_ranks VALUES ('a', 10), ('b', 2), ('c', 1), ('a', 1)`,
			`INSERT INTO item_prices VALUES ('a', '10.5'), ('b', '9'), ('c', '-1'), ('a', '-1')`,
			`INSERT INTO item_weights VALUES ('a', 10.25), ('b', 9.5), ('c', -1), ('a', -1)`,
			`INSERT INTO item_codes VALUES ('a', '10'), ('b', '9'), ('c', '-1'), ('a', '-1')`)
		related := func(table, column string, t core.FieldType) *filter.FieldSchema {
			field := &filter.FieldSchema{
				ExternalName: column, DataType: t, Facetable: true,
				Relation: &core.RelationSchema{
					JoinTable: table, SourceColumns: []string{"item_id"}, TargetColumns: []string{column}, OwnerKey: []string{"id"},
					SourceTypes: textKey(), TargetTypes: []core.TypeInfo{{Type: t}},
				},
			}
			return field
		}
		schema := &filter.ResourceSchema{
			Name: "items", PrimaryKey: []string{"id"},
			SystemFields: map[string]*filter.FieldSchema{
				"id": {ExternalName: "id", Table: "items", Column: "id", DataType: core.FieldTypeString},
			},
			Fields: map[string]*filter.FieldSchema{
				"rank_id": related("item_ranks", "rank_id", core.FieldTypeInt),
				"price":   related("item_prices", "price", core.FieldTypeDecimal),
				"weight":  related("item_weights", "weight", core.FieldTypeFloat),
				"code":    related("item_codes", "code", core.FieldTypeString),
			},
		}
		q, err := filter.BuildFacetQuery(schema, b.dialect, "1=1", nil,
			[]filter.FacetRequest{{Field: "rank_id", Limit: 2}, {Field: "price", Limit: 2}, {Field: "weight", Limit: 2}, {Field: "code", Limit: 2}},
			filter.WithFacetRelationScope(filter.RelationScope{}))
		require.NoError(t, err)
		results := queryFacets(t, db, q.SQL, q.Args...)

		want := map[string][]filter.FacetBucket{
			"rank_id": {{Key: "1", Count: 2}, {Key: "2", Count: 1}},
			"price":   {{Key: "-1", Count: 2}, {Key: "9", Count: 1}},
			"weight":  {{Key: "-1", Count: 2}, {Key: "9.5", Count: 1}},
			"code":    {{Key: "-1", Count: 2}, {Key: "10", Count: 1}},
		}
		assert.Equal(t, want, facetBuckets(results), q.SQL)
		all := map[string]map[string]int64{
			"rank_id": {"1": 2, "10": 1, "2": 1},
			"price":   {"-1": 2, "10.5": 1, "9": 1},
			"weight":  {"-1": 2, "10.25": 1, "9.5": 1},
			"code":    {"-1": 2, "10": 1, "9": 1},
		}
		for name, counts := range all {
			assert.Equal(t, want[name], filter.FacetBuckets(schema.Fields[name], counts, 2), "FacetBuckets(%s)", name)
		}
	})
}

// A relation facet groups its string keys by code point, as includes name
// them, whatever the junction column's collation: a case-insensitive
// column's "Red" and "red" are two keys. The column is NOCASE on SQLite and
// of a nondeterministic case-insensitive ICU collation on PostgreSQL.
func buildFacetQueryRelationFacetKeysByCodePoint(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	db := b.db(t)
	dbtest.Exec(t, db,
		`CREATE TABLE items (id TEXT PRIMARY KEY)`,
		`CREATE TABLE item_tags (item_id TEXT NOT NULL, tag `+caseInsensitiveText(t, b, db)+` NOT NULL)`,
		`INSERT INTO items VALUES ('a'), ('b'), ('c')`,
		`INSERT INTO item_tags VALUES ('a', 'Red'), ('b', 'red'), ('c', 'red')`)
	schema := &filter.ResourceSchema{
		Name: "items", PrimaryKey: []string{"id"},
		SystemFields: map[string]*filter.FieldSchema{
			"id": {ExternalName: "id", Table: "items", Column: "id", DataType: core.FieldTypeString},
		},
		Fields: map[string]*filter.FieldSchema{
			"tags": {
				ExternalName: "tags", DataType: core.FieldTypeString, Facetable: true,
				Relation: &core.RelationSchema{JoinTable: "item_tags", SourceColumns: []string{"item_id"}, TargetColumns: []string{"tag"}, OwnerKey: []string{"id"}, SourceTypes: textKey(), TargetTypes: textKey()},
			},
		},
	}
	q, err := filter.BuildFacetQuery(schema, b.dialect, "1=1", nil, []filter.FacetRequest{{Field: "tags"}},
		filter.WithFacetRelationScope(filter.RelationScope{}))
	require.NoError(t, err)
	assert.Equal(t, []filter.FacetBucket{{Key: "red", Count: 2}, {Key: "Red", Count: 1}}, facetBuckets(queryFacets(t, db, q.SQL, q.Args...))["tags"], q.SQL)
}

// Relation facets whose relations join different owner keys each join on
// their own: a junction keyed by the owner's code counts by code, beside
// one keyed by its id, and neither borrows the other's column.
func buildFacetQueryRelationFacetsOwnKeyEach(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		schema := &filter.ResourceSchema{
			Name: "docs", PrimaryKey: []string{"id"},
			SystemFields: map[string]*filter.FieldSchema{
				"id": {ExternalName: "id", Table: "docs", Column: "id", DataType: core.FieldTypeString},
			},
			Fields: map[string]*filter.FieldSchema{
				"tags": {
					ExternalName: "tags", DataType: core.FieldTypeString, Facetable: true,
					Relation: &core.RelationSchema{JoinTable: "doc_tags", SourceColumns: []string{"doc_id"}, TargetColumns: []string{"tag"}, OwnerKey: []string{"id"}, SourceTypes: textKey(), TargetTypes: textKey()},
				},
				"labels": {
					ExternalName: "labels", DataType: core.FieldTypeString, Facetable: true,
					Relation: &core.RelationSchema{JoinTable: "doc_labels", SourceColumns: []string{"doc_code"}, TargetColumns: []string{"label"}, OwnerKey: []string{"code"}, SourceTypes: textKey(), TargetTypes: textKey()},
				},
			},
		}
		db := b.db(t)
		dbtest.Exec(t, db,
			`CREATE TABLE docs (id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE)`,
			`CREATE TABLE doc_tags (doc_id TEXT NOT NULL, tag TEXT NOT NULL)`,
			`CREATE TABLE doc_labels (doc_code TEXT NOT NULL, label TEXT NOT NULL)`,
			// d1's code is d2's id: joined on the wrong column, labels would
			// count d2's label under d1.
			`INSERT INTO docs VALUES ('d1', 'd2'), ('d2', 'c2')`,
			`INSERT INTO doc_tags VALUES ('d1', 'red'), ('d2', 'red')`,
			`INSERT INTO doc_labels VALUES ('d2', 'one'), ('c2', 'two'), ('c2', 'three'), ('d1', 'none')`)
		q, err := filter.BuildFacetQuery(schema, b.dialect, "1=1", nil,
			[]filter.FacetRequest{{Field: "tags"}, {Field: "labels"}},
			filter.WithFacetRelationScope(filter.RelationScope{}))
		require.NoError(t, err)
		assert.Contains(t, q.SQL, `"docs"."id" AS __owner_pk, "docs"."code" AS __owner_pk_1`)

		got := bucketCounts(queryFacets(t, db, q.SQL, q.Args...))
		assert.Equal(t, map[string]int64{"red": 2}, got["tags"])
		assert.Equal(t, map[string]int64{"one": 1, "two": 1, "three": 1}, got["labels"])
	})
}

// A relation to a key of several columns facets each target by its key's
// text (core.KeyText), which the statement writes per row on PostgreSQL as
// on SQLite.
func buildFacetQueryRelationFacetTargetKeyOfSeveralColumns(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		schema := &filter.ResourceSchema{
			Name: "tags", PrimaryKey: []string{"id"},
			SystemFields: map[string]*filter.FieldSchema{
				"id": {ExternalName: "id", Table: "tags", Column: "id", DataType: core.FieldTypeString},
			},
			Fields: map[string]*filter.FieldSchema{
				"stock": {
					ExternalName: "stock", DataType: core.FieldTypeArray, Facetable: true,
					FacetSchema: &filter.FacetSchema{Type: filter.FacetTerms, Limit: 10},
					Relation: &core.RelationSchema{
						JoinTable: "stock_tags", SourceColumns: []string{"tag_id"}, TargetColumns: []string{"warehouse_id", "sku"},
						OwnerKey: []string{"id"}, SourceTypes: textKey(), TargetTypes: []core.TypeInfo{{Type: core.FieldTypeString}, {Type: core.FieldTypeString}},
					},
				},
			},
		}
		build := func(d filter.Dialect) (filter.FacetQuery, error) {
			return filter.BuildFacetQuery(schema, d, "1=1", nil, []filter.FacetRequest{{Field: "stock"}},
				filter.WithFacetRelationScope(filter.RelationScope{}))
		}
		for _, tt := range []struct {
			name    string
			dialect filter.Dialect
			want    string
			group   string
		}{
			{"postgres", filter.PostgresDialect{}, `array_to_json(ARRAY[`, `GROUP BY ("j"."warehouse_id" COLLATE pg_c_utf8), ("j"."sku" COLLATE pg_c_utf8)`},
			{"sqlite", filter.SQLiteDialect{}, `json_array(`, `GROUP BY ("j"."warehouse_id" COLLATE BINARY), ("j"."sku" COLLATE BINARY)`},
		} {
			t.Run(tt.name, func(t *testing.T) {
				q, err := build(tt.dialect)
				require.NoError(t, err)
				assert.Contains(t, q.SQL, tt.want)
				assert.Contains(t, q.SQL, tt.group)
			})
		}

		db := b.db(t)
		// SQLite holds the warehouse as an integer the key's text reads;
		// PostgreSQL's column is of the key's declared type.
		stockTags := sqlTable{name: "stock_tags", columns: []sqlColumn{
			column[string]("tag_id").required(), column[string]("warehouse_id").required().holding(SQLite, "INTEGER"), column[string]("sku").required(),
		}}
		dbtest.Exec(t, db, `CREATE TABLE tags (id TEXT PRIMARY KEY)`, `INSERT INTO tags VALUES ('red'), ('blue')`)
		// Each column alone ties: only the whole key tells (1,a) from
		// (1,b) and (2,a).
		b.load(t, db, stockTags, [][]any{{"red", "1", "a"}, {"blue", "1", "a"}, {"red", "1", "b"}, {"blue", "2", "a"}})
		q, err := build(b.dialect)
		require.NoError(t, err)
		assert.Equal(t, []filter.FacetBucket{{Key: `["1","a"]`, Count: 2}, {Key: `["1","b"]`, Count: 1}, {Key: `["2","a"]`, Count: 1}},
			facetBuckets(queryFacets(t, db, q.SQL, q.Args...))["stock"])
	})
}

// A list column's terms facet counts each distinct element once per row,
// the rows holding it, as memory backends count it: a repeated element is
// one row, an absent list none.
func buildFacetQueryRoundTripListElements(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		table := sqlTable{name: "posts", key: []string{"id"}, columns: []sqlColumn{column[int64]("id"), column[[]string]("tags")}}
		b.load(t, db, table, [][]any{
			{int64(1), []string{"a", "b"}}, {int64(2), []string{"a", "a"}}, {int64(3), []string{"c", "b"}}, {int64(4), nil}, {int64(5), []string{}},
		})
		schema := &filter.ResourceSchema{Name: "posts", Fields: map[string]*filter.FieldSchema{
			"tags": {
				ExternalName: "tags", Table: "posts", Column: "tags", DataType: core.FieldTypeArray, Elem: core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpContains}, Facetable: true,
			},
		}}
		for _, tc := range []struct {
			limit int
			want  []filter.FacetBucket
		}{
			{0, []filter.FacetBucket{{Key: "a", Count: 2}, {Key: "b", Count: 2}, {Key: "c", Count: 1}}},
			{1, []filter.FacetBucket{{Key: "a", Count: 2}}},
		} {
			results := facetQuery(t, db, b.dialect, schema, []filter.FacetRequest{{Field: "tags", Limit: tc.limit}})
			assert.Equal(t, []filter.FacetResult{{Field: "tags", Buckets: tc.want}}, results, "limit %d", tc.limit)
		}

		schema.Fields["tags"].FacetSchema = &filter.FacetSchema{Type: filter.FacetTypeRange, Ranges: []filter.FacetRange{{Key: "0+", Min: new(float64(0))}}}
		_, err := filter.BuildFacetQuery(schema, b.dialect, "1=1", nil, []filter.FacetRequest{{Field: "tags"}})
		require.Error(t, err, "a range facet over a list is refused")
	})
}

// WithFacetFrom facets the rows of a caller's FROM clause, an aliased table
// joined to another, keyed and ordered as over one table; without it the
// base reads the schema's table.
func buildFacetQueryRoundTripFacetFrom(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		db := b.db(t)
		dbtest.Exec(t, db,
			`CREATE TABLE owners (id INTEGER PRIMARY KEY, active INTEGER)`,
			`INSERT INTO owners (id, active) VALUES (1, 1), (2, 0)`)
		docs := sqlTable{name: "docs", key: []string{"id"}, columns: []sqlColumn{
			column[int64]("id"), column[int64]("owner_id"), column[string]("status"),
			column[json.RawMessage]("doc").holding(Postgres, "JSON"), column[[]string]("tags"),
		}}
		b.load(t, db, docs, [][]any{
			{int64(1), int64(1), "open", `{"price":10.0}`, []string{"a", "b"}},
			{int64(2), int64(1), "open", `{"price":9}`, []string{"a", "a"}},
			{int64(3), int64(1), "done", `{"price":10}`, nil},
			{int64(4), int64(2), "open", `{"price":9}`, []string{"b"}},
		})
		schema := &filter.ResourceSchema{Name: "docs", Fields: map[string]*filter.FieldSchema{
			"status": {ExternalName: "status", Table: "d", Column: "status", DataType: core.FieldTypeString, Facetable: true},
			"doc.price": {ExternalName: "doc.price", DataType: core.FieldTypeDecimal, Facetable: true,
				Path: &filter.JSONPath{Table: "d", Column: "doc", Segments: []string{"price"}, Storage: filter.StorageJSON}},
			"tags": {ExternalName: "tags", Table: "d", Column: "tags", DataType: core.FieldTypeArray, Elem: core.FieldTypeString, Facetable: true},
		}}
		reqs := []filter.FacetRequest{{Field: "status"}, {Field: "doc.price"}, {Field: "tags"}}
		const from = "docs d JOIN owners o ON o.id = d.owner_id"
		active := "o.active = " + b.dialect.Placeholder(1)
		q, err := filter.BuildFacetQuery(schema, b.dialect, active, []any{1}, reqs, filter.WithFacetFrom(from))
		require.NoError(t, err)
		require.Contains(t, q.SQL, " FROM "+from+" WHERE "+active)
		want := []filter.FacetResult{
			{Field: "status", Buckets: []filter.FacetBucket{{Key: "open", Count: 2}, {Key: "done", Count: 1}}},
			{Field: "doc.price", Buckets: []filter.FacetBucket{{Key: "10", Count: 2}, {Key: "9", Count: 1}}},
			{Field: "tags", Buckets: []filter.FacetBucket{{Key: "a", Count: 2}, {Key: "b", Count: 1}}},
		}
		assert.Equal(t, want, queryFacets(t, db, q.SQL, q.Args...), q.SQL)

		plain, err := filter.BuildFacetQuery(schema, b.dialect, "1=1", nil, reqs)
		require.NoError(t, err)
		require.Contains(t, plain.SQL, ` FROM "docs" WHERE 1=1`)
	})
}

// facetBuckets is each facet's buckets by its field.
func facetBuckets(results []filter.FacetResult) map[string][]filter.FacetBucket {
	m := make(map[string][]filter.FacetBucket, len(results))
	for _, r := range results {
		m[r.Field] = r.Buckets
	}
	return m
}
