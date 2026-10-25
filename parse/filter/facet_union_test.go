//go:build unit

package filter_test

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/stretchr/testify/require"
)

// facetSchema returns a small schema with a mix of terms and range facets,
// covering the cases BuildFacetQuery has to handle: text/uuid/int casting,
// per-field LIMIT, and range pre-bucketing.
func facetSchema() *filter.ResourceSchema {
	maxLow := 50.0
	maxMid := 100.0
	return &filter.ResourceSchema{
		Name: "items",
		Fields: map[string]*filter.FieldSchema{
			"status": {
				ExternalName: "status", SQLExpr: "items.status", DataType: core.FieldTypeString,
				AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpIn},
				Facetable:        true,
				FacetSchema:      &filter.FacetSchema{Type: filter.FacetTerms, Limit: 25},
			},
			"owner_id": {
				ExternalName: "owner_id", SQLExpr: "items.owner_id", DataType: core.FieldTypeUUID,
				AllowedOperators: []filter.FilterOperator{filter.OpEq},
				Facetable:        true,
				// No FacetSchema → defaults to terms; default limit 50.
			},
			"price": {
				ExternalName: "price", SQLExpr: "items.price", DataType: core.FieldTypeFloat,
				AllowedOperators: []filter.FilterOperator{filter.OpGt, filter.OpLte},
				Facetable:        true,
				FacetSchema: &filter.FacetSchema{
					Type: filter.FacetTypeRange,
					Ranges: []filter.FacetRange{
						{Key: "0-50", Min: new(float64(0)), Max: &maxLow},
						{Key: "50-100", Min: &maxLow, Max: &maxMid},
						{Key: "100+", Min: &maxMid},
					},
				},
			},
			"name": {
				ExternalName: "name", SQLExpr: "items.name", DataType: core.FieldTypeString,
				// Not facetable.
			},
			"in_stock": {
				ExternalName: "in_stock", SQLExpr: "items.in_stock", DataType: core.FieldTypeBool,
				AllowedOperators: []filter.FilterOperator{filter.OpEq},
				Facetable:        true,
			},
		},
	}
}

func TestBuildFacetQuery_Postgres(t *testing.T) {
	q, err := filter.BuildFacetQuery(
		facetSchema(),
		filter.PostgresDialect{},
		"items.tenant_id = $1",
		[]any{"t1"},
		[]filter.FacetRequest{
			{Field: "status"},
			{Field: "owner_id", Limit: 10},
			{Field: "price"},
		},
	)
	if err != nil {
		t.Fatalf("BuildFacetQuery: %v", err)
	}

	want := []string{
		"WITH base AS MATERIALIZED",
		"items.status AS facet__0",
		"items.owner_id::text AS facet__1",
		"CASE WHEN items.price >= $2 AND items.price < $3 THEN '0-50'",
		"items.price >= $4 AND items.price < $5 THEN '50-100'",
		"items.price >= $6 THEN '100+' END AS facet__2",
		"FROM items WHERE items.tenant_id = $1)",
		"SELECT 'status' AS facet_field",
		"ORDER BY c DESC, facet__0 COLLATE pg_c_utf8 LIMIT 25",
		"SELECT 'owner_id' AS facet_field",
		"ORDER BY c DESC, facet__1 COLLATE pg_c_utf8 LIMIT 10",
		"SELECT 'price' AS facet_field, facet__2 AS facet_key, COUNT(*) AS facet_count, 2 AS facet_ord, " +
			"CASE facet__2 WHEN '0-50' THEN 0 WHEN '50-100' THEN 1 WHEN '100+' THEN 2 END AS facet_rank FROM base WHERE facet__2 IS NOT NULL GROUP BY facet__2",
		"UNION ALL",
		") facets__ ORDER BY facet_ord, facet_rank, facet_count DESC, facet_key COLLATE pg_c_utf8",
	}
	for _, sub := range want {
		if !strings.Contains(q.SQL, sub) {
			t.Errorf("missing substring %q\nGenerated SQL:\n%s", sub, q.SQL)
		}
	}

	// 1 tenant_id arg + 5 range bounds = 6 args.
	if len(q.Args) != 6 {
		t.Errorf("want 6 args, got %d: %v", len(q.Args), q.Args)
	}
	if q.Args[0] != "t1" {
		t.Errorf("first arg = %v, want t1", q.Args[0])
	}
}

func TestBuildFacetQuery_SQLite(t *testing.T) {
	q, err := filter.BuildFacetQuery(
		facetSchema(),
		filter.SQLiteDialect{},
		"items.tenant_id = ?1",
		[]any{"t1"},
		[]filter.FacetRequest{
			{Field: "status"},
			{Field: "owner_id"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(q.SQL, "MATERIALIZED") {
		t.Errorf("SQLite must not emit MATERIALIZED hint; got:\n%s", q.SQL)
	}
	if !strings.Contains(q.SQL, "CAST(items.owner_id AS TEXT)") {
		t.Errorf("expected CAST(... AS TEXT) for SQLite uuid; got:\n%s", q.SQL)
	}
	// String fields shouldn't be cast.
	if strings.Contains(q.SQL, "CAST(items.status AS TEXT)") {
		t.Errorf("string field should not be cast; got:\n%s", q.SQL)
	}
}

func TestBuildFacetQuery_RejectsNonFacetable(t *testing.T) {
	_, err := filter.BuildFacetQuery(
		facetSchema(),
		filter.SQLiteDialect{},
		"1=1", nil,
		[]filter.FacetRequest{{Field: "name"}},
	)
	if err == nil {
		t.Fatal("expected error for non-facetable field")
	}
	if !strings.Contains(err.Error(), "not facetable") || !errors.Is(err, filter.ECodeUnknownField) {
		t.Errorf("error %q should be E2071 naming the field not facetable", err)
	}
}

func TestBuildFacetQuery_RejectsUnknownField(t *testing.T) {
	_, err := filter.BuildFacetQuery(
		facetSchema(),
		filter.SQLiteDialect{},
		"1=1", nil,
		[]filter.FacetRequest{{Field: "missing"}},
	)
	// A request naming an unknown facet is the caller's mistake (E2071, 422),
	// never a server error.
	if !errors.Is(err, filter.ECodeUnknownField) {
		t.Fatalf("expected E2071, got %v", err)
	}
}

func TestBuildFacetQuery_RejectsEmpty(t *testing.T) {
	_, err := filter.BuildFacetQuery(
		facetSchema(), filter.SQLiteDialect{}, "1=1", nil, nil,
	)
	if err == nil {
		t.Fatal("expected error for empty facets list")
	}
}

func TestBuildFacetQuery_DefaultLimit(t *testing.T) {
	// owner_id has no FacetSchema; should default to terms with limit 50.
	q, err := filter.BuildFacetQuery(
		facetSchema(), filter.PostgresDialect{}, "1=1", nil,
		[]filter.FacetRequest{{Field: "owner_id"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.SQL, "LIMIT 50") {
		t.Errorf("expected default LIMIT 50; got:\n%s", q.SQL)
	}
}

// --- BuildFacetQuery + ScanFacetUnion round-trip via in-memory SQLite ---

func setupFacetDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	if _, err := db.Exec(`CREATE TABLE items (
		id        INTEGER PRIMARY KEY,
		tenant_id TEXT NOT NULL,
		status    TEXT NOT NULL,
		owner_id  TEXT NOT NULL,
		price     REAL NOT NULL,
		name      TEXT,
		in_stock  BOOLEAN NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}

	rows := []struct {
		tid, status, owner string
		price              float64
	}{
		{"t1", "active", "u1", 10},
		{"t1", "active", "u1", 30},
		{"t1", "active", "u2", 75},
		{"t1", "pending", "u1", 120},
		{"t1", "pending", "u3", 200},
		{"t2", "active", "u9", 99}, // wrong tenant; must not appear in t1 facets
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO items (tenant_id, status, owner_id, price) VALUES (?, ?, ?, ?)`,
			r.tid, r.status, r.owner, r.price,
		); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestBuildFacetQuery_RoundTrip_TenantScoped(t *testing.T) {
	db := setupFacetDB(t)

	q, err := filter.BuildFacetQuery(
		facetSchema(),
		filter.SQLiteDialect{},
		"items.tenant_id = ?1",
		[]any{"t1"},
		[]filter.FacetRequest{
			{Field: "status"},
			{Field: "owner_id"},
			{Field: "price"},
		},
	)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	rows, err := db.Query(q.SQL, q.Args...)
	if err != nil {
		t.Fatalf("execute: %v\nSQL:\n%s", err, q.SQL)
	}
	defer func() { require.NoError(t, rows.Close()) }()
	results, err := filter.ScanFacetUnion(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	got := indexFacets(results)

	// Tenant t1 has 5 rows: 3 active, 2 pending. Tenant t2's row must not leak.
	checkBuckets(t, got["status"], map[string]int64{"active": 3, "pending": 2})

	// owner_id: u1 → 3, u2 → 1, u3 → 1.
	checkBuckets(t, got["owner_id"], map[string]int64{"u1": 3, "u2": 1, "u3": 1})

	// price ranges: 10,30 in 0-50 (2); 75 in 50-100 (1); 120,200 in 100+ (2).
	checkBuckets(t, got["price"], map[string]int64{"0-50": 2, "50-100": 1, "100+": 2})
}

func TestBuildFacetQuery_RoundTrip_PerFacetLimit(t *testing.T) {
	db := setupFacetDB(t)

	q, err := filter.BuildFacetQuery(
		facetSchema(), filter.SQLiteDialect{}, "items.tenant_id = ?1",
		[]any{"t1"},
		[]filter.FacetRequest{{Field: "owner_id", Limit: 1}},
	)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(q.SQL, q.Args...)
	if err != nil {
		t.Fatalf("execute: %v\nSQL:\n%s", err, q.SQL)
	}
	defer func() { require.NoError(t, rows.Close()) }()
	results, err := filter.ScanFacetUnion(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || len(results[0].Buckets) != 1 {
		t.Errorf("expected 1 bucket due to LIMIT 1, got results=%+v", results)
	}
	if results[0].Buckets[0].Key != "u1" || results[0].Buckets[0].Count != 3 {
		t.Errorf("expected top bucket u1=3, got %+v", results[0].Buckets[0])
	}
}

// Facet rows come back in one order on every run: facets as requested; a
// terms facet by count, ties by key, its limit keeping the lowest tied keys;
// a range facet as declared, whatever its counts.
func TestBuildFacetQuery_RoundTrip_Order(t *testing.T) {
	db := setupFacetDB(t)
	q, err := filter.BuildFacetQuery(
		facetSchema(), filter.SQLiteDialect{}, "items.tenant_id = ?1", []any{"t1"},
		[]filter.FacetRequest{{Field: "price"}, {Field: "owner_id", Limit: 2}, {Field: "status"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(q.SQL, q.Args...)
	if err != nil {
		t.Fatalf("execute: %v\nSQL:\n%s", err, q.SQL)
	}
	results, err := filter.ScanFacetUnion(rows)
	if closeErr := errors.Join(err, rows.Close()); closeErr != nil {
		t.Fatal(closeErr)
	}
	want := []filter.FacetResult{
		{Field: "price", Buckets: []filter.FacetBucket{{Key: "0-50", Count: 2}, {Key: "50-100", Count: 1}, {Key: "100+", Count: 2}}},
		{Field: "owner_id", Buckets: []filter.FacetBucket{{Key: "u1", Count: 3}, {Key: "u2", Count: 1}}}, // u3 ties u2 and sorts after it
		{Field: "status", Buckets: []filter.FacetBucket{{Key: "active", Count: 3}, {Key: "pending", Count: 2}}},
	}
	if !reflect.DeepEqual(results, want) {
		t.Errorf("facets = %+v\nwant     %+v", results, want)
	}
}

// FacetBuckets is the order a backend counting in Go returns, the order
// the SQL facet query returns.
func TestFacetBuckets_OrderAsTheQueryDoes(t *testing.T) {
	price := facetSchema().Fields["price"].FacetSchema
	got := filter.FacetBuckets(price, map[string]int64{"100+": 2, "0-50": 2, "50-100": 1, "unused": 0}, 0)
	if want := []filter.FacetBucket{{Key: "0-50", Count: 2}, {Key: "50-100", Count: 1}, {Key: "100+", Count: 2}}; !reflect.DeepEqual(got, want) {
		t.Errorf("ranges = %+v, want %+v", got, want)
	}
	counts := map[string]int64{"u3": 1, "u1": 3, "u2": 1, "Z": 1, "zero": 0}
	got = filter.FacetBuckets(nil, counts, 3)
	if want := []filter.FacetBucket{{Key: "u1", Count: 3}, {Key: "Z", Count: 1}, {Key: "u2", Count: 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("terms = %+v, want %+v", got, want)
	}
}

// A list column's terms facet counts each distinct element once per row,
// the rows holding it, as memory backends count it: a repeated element is
// one row, an absent list none.
func TestBuildFacetQuery_RoundTrip_ListElements(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	for _, stmt := range []string{
		`CREATE TABLE posts (id INTEGER PRIMARY KEY, tags TEXT)`,
		`INSERT INTO posts (tags) VALUES ('["a","b"]'), ('["a","a"]'), ('["c","b"]'), (NULL), ('[]')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	schema := &filter.ResourceSchema{Name: "posts", Fields: map[string]*filter.FieldSchema{
		"tags": {
			ExternalName: "tags", SQLExpr: "posts.tags", DataType: core.FieldTypeArray, Elem: core.FieldTypeString,
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
		q, err := filter.BuildFacetQuery(schema, filter.SQLiteDialect{}, "1=1", nil, []filter.FacetRequest{{Field: "tags", Limit: tc.limit}})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := db.Query(q.SQL, q.Args...)
		if err != nil {
			t.Fatalf("execute: %v\nSQL:\n%s", err, q.SQL)
		}
		results, err := filter.ScanFacetUnion(rows)
		if closeErr := errors.Join(err, rows.Close()); closeErr != nil {
			t.Fatal(closeErr)
		}
		if want := []filter.FacetResult{{Field: "tags", Buckets: tc.want}}; !reflect.DeepEqual(results, want) {
			t.Errorf("limit %d: facets = %+v, want %+v", tc.limit, results, want)
		}
	}

	schema.Fields["tags"].FacetSchema = &filter.FacetSchema{Type: filter.FacetTypeRange, Ranges: []filter.FacetRange{{Key: "0+", Min: new(float64(0))}}}
	if _, err := filter.BuildFacetQuery(schema, filter.SQLiteDialect{}, "1=1", nil, []filter.FacetRequest{{Field: "tags"}}); err == nil {
		t.Error("a range facet over a list is refused")
	}
}

// --- helpers ---

func indexFacets(results []filter.FacetResult) map[string][]filter.FacetBucket {
	m := make(map[string][]filter.FacetBucket, len(results))
	for _, r := range results {
		m[r.Field] = r.Buckets
	}
	return m
}

func checkBuckets(t *testing.T, got []filter.FacetBucket, want map[string]int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("bucket count mismatch: got %d (%+v), want %d (%v)", len(got), got, len(want), want)
		return
	}
	for _, b := range got {
		w, ok := want[b.Key]
		if !ok {
			t.Errorf("unexpected bucket %q", b.Key)
			continue
		}
		if b.Count != w {
			t.Errorf("bucket %q count = %d, want %d", b.Key, b.Count, w)
		}
	}
}

// Boolean facet keys must be the JSON literals on every backend: a bare
// text cast yields "1"/"0" on SQLite vs "true"/"false" on PostgreSQL,
// and  consumers compare against the JSON form (the admin SPAs'
// facetCount(facets, field, true) stringifies to "true"). Regression for
// the termsKeyExpr boolean CASE normalization.
func TestBuildFacetQuery_RoundTrip_BooleanKeysAreJSONLiterals(t *testing.T) {
	db := setupFacetDB(t)
	if _, err := db.Exec(`UPDATE items SET in_stock = 1 WHERE tenant_id = 't1' AND status = 'active'`); err != nil {
		t.Fatal(err)
	}

	q, err := filter.BuildFacetQuery(
		facetSchema(),
		filter.SQLiteDialect{},
		"items.tenant_id = ?1",
		[]any{"t1"},
		[]filter.FacetRequest{{Field: "in_stock"}},
	)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	rows, err := db.Query(q.SQL, q.Args...)
	if err != nil {
		t.Fatalf("execute: %v\nSQL:\n%s", err, q.SQL)
	}
	defer func() { require.NoError(t, rows.Close()) }()
	results, err := filter.ScanFacetUnion(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	got := indexFacets(results)
	checkBuckets(t, got["in_stock"], map[string]int64{"true": 3, "false": 2})
}
