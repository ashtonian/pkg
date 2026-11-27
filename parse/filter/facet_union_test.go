//go:build unit

package filter_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildFacetQuery_Postgres(t *testing.T) {
	q, err := filter.BuildFacetQuery(
		parsetest.FacetSchema(),
		filter.PostgresDialect{},
		`"items"."tenant_id" = $1`,
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
		`"items"."status" AS facet__0`,
		`"items"."owner_id"::text AS facet__1`,
		`CASE WHEN "items"."price" >= $2 AND "items"."price" < $3 THEN '0-50'`,
		`"items"."price" >= $4 AND "items"."price" < $5 THEN '50-100'`,
		`"items"."price" >= $6 THEN '100+' END AS facet__2`,
		`FROM "items" WHERE "items"."tenant_id" = $1)`,
		"SELECT 'status' AS facet_field",
		"ORDER BY c DESC, facet__0 COLLATE pg_c_utf8 LIMIT 25",
		"SELECT 'owner_id' AS facet_field",
		"ORDER BY c DESC, facet__1 COLLATE pg_c_utf8 LIMIT 10",
		"SELECT 'price' AS facet_field, facet__2 AS facet_key, COUNT(*) AS facet_count, 2 AS facet_ord, " +
			"CASE facet__2 WHEN '0-50' THEN 0 WHEN '50-100' THEN 1 WHEN '100+' THEN 2 END AS facet_rank FROM base WHERE facet__2 IS NOT NULL GROUP BY facet__2",
		"UNION ALL",
		"ROW_NUMBER() OVER (ORDER BY c DESC, k COLLATE pg_c_utf8) AS facet_rank",
		") facets__ ORDER BY facet_ord, facet_rank",
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
		parsetest.FacetSchema(),
		filter.SQLiteDialect{},
		`"items"."tenant_id" = ?1`,
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
	if !strings.Contains(q.SQL, `CAST("items"."owner_id" AS TEXT)`) {
		t.Errorf("expected CAST(... AS TEXT) for SQLite uuid; got:\n%s", q.SQL)
	}
	// String fields shouldn't be cast.
	if strings.Contains(q.SQL, `CAST("items"."status" AS TEXT)`) {
		t.Errorf("string field should not be cast; got:\n%s", q.SQL)
	}
}

func TestBuildFacetQuery_RejectsNonFacetable(t *testing.T) {
	_, err := filter.BuildFacetQuery(
		parsetest.FacetSchema(),
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
		parsetest.FacetSchema(),
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
		parsetest.FacetSchema(), filter.SQLiteDialect{}, "1=1", nil, nil,
	)
	if err == nil {
		t.Fatal("expected error for empty facets list")
	}
}

func TestBuildFacetQuery_DefaultLimit(t *testing.T) {
	// owner_id has no FacetSchema; should default to terms with limit 50.
	q, err := filter.BuildFacetQuery(
		parsetest.FacetSchema(), filter.PostgresDialect{}, "1=1", nil,
		[]filter.FacetRequest{{Field: "owner_id"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.SQL, "LIMIT 50") {
		t.Errorf("expected default LIMIT 50; got:\n%s", q.SQL)
	}
}

// FacetBuckets is the order a backend counting in Go returns, the order
// the SQL facet query returns.
func TestFacetBuckets_OrderAsTheQueryDoes(t *testing.T) {
	got := filter.FacetBuckets(parsetest.FacetSchema().Fields["price"], map[string]int64{"100+": 2, "0-50": 2, "50-100": 1, "unused": 0}, 0)
	if want := []filter.FacetBucket{{Key: "0-50", Count: 2}, {Key: "50-100", Count: 1}, {Key: "100+", Count: 2}}; !reflect.DeepEqual(got, want) {
		t.Errorf("ranges = %+v, want %+v", got, want)
	}
	counts := map[string]int64{"u3": 1, "u1": 3, "u2": 1, "Z": 1, "zero": 0}
	got = filter.FacetBuckets(nil, counts, 3)
	if want := []filter.FacetBucket{{Key: "u1", Count: 3}, {Key: "Z", Count: 1}, {Key: "u2", Count: 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("terms = %+v, want %+v", got, want)
	}
}

// A numeric terms facet breaks count ties by value, as CompareKeyValues
// orders numbers; the limit keeps the lowest values, not the lowest texts.
func TestFacetBuckets_NumbersTieByValue(t *testing.T) {
	keys := func(field *filter.FieldSchema, counts map[string]int64, limit int) []string {
		var out []string
		for _, b := range filter.FacetBuckets(field, counts, limit) {
			out = append(out, b.Key)
		}
		return out
	}
	field := func(t, elem core.FieldType) *filter.FieldSchema {
		return &filter.FieldSchema{ExternalName: "n", DataType: t, Elem: elem, Facetable: true}
	}
	ones := func(ks ...string) map[string]int64 {
		m := map[string]int64{}
		for _, k := range ks {
			m[k] = 1
		}
		return m
	}
	for _, tc := range []struct {
		name   string
		field  *filter.FieldSchema
		counts map[string]int64
		limit  int
		want   []string
	}{
		{"int", field(core.FieldTypeInt, ""), ones("9", "10", "100", "-1", "-10"), 4, []string{"-10", "-1", "9", "10"}},
		{"decimal beyond a double", field(core.FieldTypeDecimal, ""), ones("9007199254740993", "9007199254740992.5", "0.5", "10"), 0,
			[]string{"0.5", "10", "9007199254740992.5", "9007199254740993"}},
		{"count first", field(core.FieldTypeDecimal, ""), map[string]int64{"100": 2, "9": 1, "10": 1}, 2, []string{"100", "9"}},
		{"float, infinities and NaN", field(core.FieldTypeFloat, ""), ones("NaN", "Infinity", "1e+21", "-Infinity", "2"), 0,
			[]string{"-Infinity", "2", "1e+21", "Infinity", "NaN"}},
		{"array elements", field(core.FieldTypeArray, core.FieldTypeInt), ones("10", "9"), 1, []string{"9"}},
		{"strings by code point", field(core.FieldTypeString, ""), ones("9", "10"), 1, []string{"10"}},
	} {
		if got := keys(tc.field, tc.counts, tc.limit); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: keys = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// BuildFacetQuery numbers its own parameters after filterArgs, so a
// fragment numbered from an offset would collide with them; it is refused.
func TestBuildFacetQuery_RefusesAnOffsetFragment(t *testing.T) {
	schema := parsetest.FacetSchema()
	ast, err := filter.NewFilterParser(schema, nil).Parse(`{"status":"active"}`)
	require.NoError(t, err)
	for _, d := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		offset, err := filter.NewSQLBuilder(schema, d, filter.WithStartParam(3)).Build(ast)
		require.NoError(t, err)
		_, err = filter.BuildFacetQuery(schema, d, offset.Where, offset.Args, []filter.FacetRequest{{Field: "status"}})
		assert.ErrorContains(t, err, "names placeholder 4 with 1 arguments", d.Name())

		plain, err := filter.NewSQLBuilder(schema, d).Build(ast)
		require.NoError(t, err)
		_, err = filter.BuildFacetQuery(schema, d, plain.Where, plain.Args, []filter.FacetRequest{{Field: "status"}})
		assert.NoError(t, err, d.Name())
	}
}
