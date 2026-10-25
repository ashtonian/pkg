//go:build integration

package filter_test

import (
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// --- SQLite integration: parse → SQL → execute → verify ---

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`CREATE TABLE products (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT    NOT NULL,
		category   TEXT    NOT NULL,
		price      REAL    NOT NULL,
		status     TEXT    NOT NULL DEFAULT 'active',
		created_at TEXT    NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		t.Fatal(err)
	}

	products := []struct {
		name, category, status string
		price                  float64
	}{
		{"Laptop", "electronics", "active", 999.99},
		{"Mouse", "electronics", "active", 29.99},
		{"Keyboard", "electronics", "inactive", 89.99},
		{"T-Shirt", "clothing", "active", 19.99},
		{"Jacket", "clothing", "inactive", 65.00},
		{"Go Book", "books", "active", 39.99},
		{"Clean Code", "books", "active", 32.00},
	}
	for _, p := range products {
		_, err := db.Exec(`INSERT INTO products (name, category, price, status) VALUES (?, ?, ?, ?)`,
			p.name, p.category, p.price, p.status)
		if err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func integrationSchema() *filter.ResourceSchema {
	return &filter.ResourceSchema{
		Name: "products",
		Fields: map[string]*filter.FieldSchema{
			"name":     {ExternalName: "name", SQLExpr: "products.name", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpLike, filter.OpILike}},
			"category": {ExternalName: "category", SQLExpr: "products.category", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpIn}, Facetable: true},
			"price":    {ExternalName: "price", SQLExpr: "products.price", DataType: core.FieldTypeFloat, AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpGt, filter.OpGte, filter.OpLt, filter.OpLte}},
			"status":   {ExternalName: "status", SQLExpr: "products.status", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpIn}, EnumValues: []string{"active", "inactive"}},
		},
	}
}

func integrationSortSchema() *filter.SortSchema {
	return &filter.SortSchema{
		Fields: map[string]*filter.SortFieldSchema{
			"name":  {SQLExpr: "products.name"},
			"price": {SQLExpr: "products.price"},
		},
		DefaultField: "name",
		DefaultDir:   filter.SortAsc,
	}
}

func queryProducts(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("query failed: %v\nSQL: %s\nArgs: %v", err, query, args)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var id int
		var name, category, status, createdAt string
		var price float64
		if err := rows.Scan(&id, &name, &category, &price, &status, &createdAt); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func TestIntegration_SimpleEq(t *testing.T) {
	db := setupTestDB(t)
	schema := integrationSchema()
	fp := filter.NewFilterParser(schema, nil)

	ast, err := fp.Parse(`{"category":"electronics"}`)
	if err != nil {
		t.Fatal(err)
	}
	builder := filter.NewSQLBuilder(schema, filter.SQLiteDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}

	query := "SELECT * FROM products WHERE " + result.Where
	names := queryProducts(t, db, query, result.Args...)
	if len(names) != 3 {
		t.Errorf("expected 3 electronics, got %d: %v", len(names), names)
	}
}

func TestIntegration_MultiCondition(t *testing.T) {
	db := setupTestDB(t)
	schema := integrationSchema()
	fp := filter.NewFilterParser(schema, nil)

	ast, err := fp.Parse(`{"category":"electronics","price":{"$lte":50}}`)
	if err != nil {
		t.Fatal(err)
	}
	builder := filter.NewSQLBuilder(schema, filter.SQLiteDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}

	query := "SELECT * FROM products WHERE " + result.Where
	names := queryProducts(t, db, query, result.Args...)
	if len(names) != 1 || names[0] != "Mouse" {
		t.Errorf("expected [Mouse], got %v", names)
	}
}

func TestIntegration_InOperator(t *testing.T) {
	db := setupTestDB(t)
	schema := integrationSchema()
	fp := filter.NewFilterParser(schema, nil)

	ast, err := fp.Parse(`{"category":{"$in":["books","clothing"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	builder := filter.NewSQLBuilder(schema, filter.SQLiteDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}

	query := "SELECT * FROM products WHERE " + result.Where
	names := queryProducts(t, db, query, result.Args...)
	if len(names) != 4 {
		t.Errorf("expected 4 (books+clothing), got %d: %v", len(names), names)
	}
}

func TestIntegration_OrOperator(t *testing.T) {
	db := setupTestDB(t)
	schema := integrationSchema()
	fp := filter.NewFilterParser(schema, nil)

	ast, err := fp.Parse(`{"$or":[{"category":"books"},{"price":{"$gt":500}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	builder := filter.NewSQLBuilder(schema, filter.SQLiteDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}

	query := "SELECT * FROM products WHERE " + result.Where
	names := queryProducts(t, db, query, result.Args...)
	// books: Go Book, Clean Code; price > 500: Laptop
	if len(names) != 3 {
		t.Errorf("expected 3, got %d: %v", len(names), names)
	}
}

func TestIntegration_NotOperator(t *testing.T) {
	db := setupTestDB(t)
	schema := integrationSchema()
	fp := filter.NewFilterParser(schema, nil)

	ast, err := fp.Parse(`{"$not":{"status":"inactive"}}`)
	if err != nil {
		t.Fatal(err)
	}
	builder := filter.NewSQLBuilder(schema, filter.SQLiteDialect{})
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}

	query := "SELECT * FROM products WHERE " + result.Where
	names := queryProducts(t, db, query, result.Args...)
	if len(names) != 5 {
		t.Errorf("expected 5 active, got %d: %v", len(names), names)
	}
}

func TestIntegration_ListQuery_SortAndLimit(t *testing.T) {
	db := setupTestDB(t)
	schema := integrationSchema()
	sortSchema := integrationSortSchema()
	lq := filter.NewListQuery(schema, sortSchema, filter.SQLiteDialect{})

	result, err := lq.Build(
		filter.ListFilter(`{"status":"active"}`),
		filter.ListSort("price"),
		filter.ListLimit(3),
	)
	if err != nil {
		t.Fatal(err)
	}

	query := "SELECT * FROM products " + result.Fragment
	names := queryProducts(t, db, query, result.Args...)
	// ListQuery fetches limit+1 for has-more detection.
	if len(names) > result.FetchLimit {
		t.Fatalf("expected at most %d items (fetchLimit), got %d: %v", result.FetchLimit, len(names), names)
	}
	// Trim to page size (as the caller would).
	if len(names) > result.Limit {
		names = names[:result.Limit]
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 items after trim, got %d: %v", len(names), names)
	}
	// Cheapest first: T-Shirt (19.99), Mouse (29.99), Clean Code (32.00)
	if names[0] != "T-Shirt" {
		t.Errorf("expected T-Shirt first (cheapest), got %s", names[0])
	}
}

func TestIntegration_ListQuery_PolicyConstraints(t *testing.T) {
	db := setupTestDB(t)
	schema := integrationSchema()
	lq := filter.NewListQuery(schema, nil, filter.SQLiteDialect{})

	pc := &filter.PolicyConstraints{
		Constraints: []*filter.PolicyConstraint{
			{Field: "status", Operator: filter.OpEq, Value: "active"},
		},
	}

	// User filter asks for electronics, policy restricts to active only.
	result, err := lq.Build(
		filter.ListFilter(`{"category":"electronics"}`),
		filter.ListPolicy(pc),
		filter.ListLimit(100),
	)
	if err != nil {
		t.Fatal(err)
	}

	query := "SELECT * FROM products " + result.Fragment
	names := queryProducts(t, db, query, result.Args...)
	// Electronics + active: Laptop, Mouse (Keyboard is inactive).
	if len(names) != 2 {
		t.Errorf("expected 2 active electronics, got %d: %v", len(names), names)
	}
}

func TestIntegration_ListQuery_WithCount(t *testing.T) {
	db := setupTestDB(t)
	schema := integrationSchema()
	lq := filter.NewListQuery(schema, nil, filter.SQLiteDialect{})

	result, err := lq.Build(
		filter.ListFilter(`{"status":"active"}`),
		filter.ListWithCount(true),
		filter.ListLimit(2),
	)
	if err != nil {
		t.Fatal(err)
	}

	var total int64
	err = db.QueryRow(result.CountSQL, result.CountArgs...).Scan(&total)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Errorf("expected 5 active total, got %d", total)
	}
}

func TestIntegration_WithStartParam(t *testing.T) {
	db := setupTestDB(t)
	schema := integrationSchema()
	fp := filter.NewFilterParser(schema, nil)

	// Simulate patch scenario: first 2 args are SET clause values.
	setArgs := []any{"updated_name", "active"}

	ast, err := fp.Parse(`{"category":"electronics"}`)
	if err != nil {
		t.Fatal(err)
	}

	builder := filter.NewSQLBuilder(schema, filter.SQLiteDialect{}, filter.WithStartParam(len(setArgs)))
	result, err := builder.Build(ast)
	if err != nil {
		t.Fatal(err)
	}

	// Combine args: SET args + WHERE args.
	allArgs := append(setArgs, result.Args...)

	// Build an UPDATE query using the offset params.
	query := "UPDATE products SET name = ?1, status = ?2 WHERE " + result.Where + " RETURNING *"
	rows, err := db.Query(query, allArgs...)
	if err != nil {
		t.Fatalf("query with startParam failed: %v\nSQL: %s\nArgs: %v", err, query, allArgs)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		count++
		var id int
		var name, category, status, createdAt string
		var price float64
		rows.Scan(&id, &name, &category, &price, &status, &createdAt)
		if name != "updated_name" {
			t.Errorf("expected updated_name, got %s", name)
		}
	}
	if count != 3 {
		t.Errorf("expected 3 electronics updated, got %d", count)
	}
}

func TestIntegration_Facets(t *testing.T) {
	db := setupTestDB(t)
	schema := integrationSchema()
	lq := filter.NewListQuery(schema, nil, filter.SQLiteDialect{})

	result, err := lq.Build(
		filter.ListFilter(`{"status":"active"}`),
		filter.ListFacets("category"),
		filter.ListLimit(100),
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.Facets == nil {
		t.Fatal("expected a facet query")
	}
	rows, err := db.Query(result.Facets.SQL, result.Facets.Args...)
	if err != nil {
		t.Fatal(err)
	}
	facets, err := filter.ScanFacetUnion(rows)
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(facets) != 1 {
		t.Fatalf("expected 1 facet result, got %d", len(facets))
	}
	facetResult := facets[0]

	if len(facetResult.Buckets) == 0 {
		t.Error("expected facet buckets")
	}

	// Verify counts: electronics=2 (active), clothing=1, books=2
	bucketMap := map[string]int64{}
	for _, b := range facetResult.Buckets {
		bucketMap[b.Key] = b.Count
	}
	if bucketMap["electronics"] != 2 {
		t.Errorf("expected 2 active electronics, got %d", bucketMap["electronics"])
	}
	if bucketMap["books"] != 2 {
		t.Errorf("expected 2 active books, got %d", bucketMap["books"])
	}
}

// --- : composite keyset walk (same-value boundaries) ---

// TestIntegration_P10_CursorWalkNoLossNoDup is the P-10 acceptance case:
// 5 rows with 3 sharing a created_at boundary value, page size 2, walk every
// page via the emitted cursor and assert the exact multiset — the
// single-field cursor lost the boundary-sharing rows here.
func TestIntegration_P10_CursorWalkNoLossNoDup(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`CREATE TABLE events (
		id         INTEGER PRIMARY KEY,
		created_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id int64
		at string
	}{
		{1, "2026-01-01T00:00:00Z"},
		{2, "2026-01-02T00:00:00Z"}, // ┐
		{3, "2026-01-02T00:00:00Z"}, // ├ 3 rows share the boundary value
		{4, "2026-01-02T00:00:00Z"}, // ┘
		{5, "2026-01-03T00:00:00Z"},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO events (id, created_at) VALUES (?, ?)`, r.id, r.at); err != nil {
			t.Fatal(err)
		}
	}

	schema := &filter.ResourceSchema{
		Name: "events", PrimaryKey: "id",
		Fields: map[string]*filter.FieldSchema{
			"created_at": {ExternalName: "created_at", SQLExpr: "events.created_at", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpGt, filter.OpLt}},
		},
	}
	sortSchema := &filter.SortSchema{
		Fields: map[string]*filter.SortFieldSchema{
			"created_at": {SQLExpr: "events.created_at"},
		},
	}

	type eventRow struct {
		ID        int64
		CreatedAt string
	}

	walk := func(t *testing.T, sortParam string) []int64 {
		t.Helper()
		lq := filter.NewListQuery(schema, sortSchema, filter.SQLiteDialect{})
		var got []int64
		cursor := ""
		for page := 0; ; page++ {
			if page > 5 {
				t.Fatal("walk did not terminate — cursor loop")
			}
			opts := []filter.ListOption{
				filter.ListSort(sortParam),
				filter.ListLimit(2),
			}
			if cursor != "" {
				opts = append(opts, filter.ListCursor(cursor))
			}
			result, err := lq.Build(opts...)
			if err != nil {
				t.Fatal(err)
			}
			query := "SELECT events.id, events.created_at FROM events " + result.Fragment
			dbRows, err := db.Query(query, result.Args...)
			if err != nil {
				t.Fatalf("query %q: %v", query, err)
			}
			var items []eventRow
			for dbRows.Next() {
				var r eventRow
				if err := dbRows.Scan(&r.ID, &r.CreatedAt); err != nil {
					t.Fatal(err)
				}
				items = append(items, r)
			}
			if err := dbRows.Err(); err != nil {
				t.Fatal(err)
			}
			dbRows.Close()

			page, err := filter.NewPage(items, result.Limit, result.Cursor, func(r eventRow, col string) (any, error) {
				switch col {
				case "created_at":
					return r.CreatedAt, nil
				case "id":
					return r.ID, nil
				}
				return nil, fmt.Errorf("unexpected column %q", col)
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range page.Items {
				got = append(got, it.ID)
			}
			if !page.PageInfo.HasNextPage {
				return got
			}
			cursor = string(page.PageInfo.EndCursor)
		}
	}

	assertExactSet := func(t *testing.T, got []int64) {
		t.Helper()
		if len(got) != 5 {
			t.Fatalf("expected exactly 5 rows across the walk, got %d: %v", len(got), got)
		}
		seen := map[int64]bool{}
		for _, id := range got {
			if seen[id] {
				t.Fatalf("row %d returned twice: %v", id, got)
			}
			seen[id] = true
		}
		for id := int64(1); id <= 5; id++ {
			if !seen[id] {
				t.Fatalf("row %d lost across the walk: %v", id, got)
			}
		}
	}

	t.Run("desc", func(t *testing.T) { assertExactSet(t, walk(t, "-created_at")) })
	t.Run("asc", func(t *testing.T) { assertExactSet(t, walk(t, "created_at")) })
}
