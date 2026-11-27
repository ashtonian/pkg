//go:build unit

package filter_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// mintCursor builds an encoded token whose field list mirrors the given
// keys, the way a prior page under the same sort would have minted it.
func mintCursor(t *testing.T, reverse bool, fields ...filter.CursorField) string {
	t.Helper()
	cur, err := filter.EncodeCursor(&filter.PageToken{Fields: fields, Reverse: reverse})
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func assertParseCode(t *testing.T, err error, code core.Code) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *core.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *core.ParseError, got %T: %v", err, err)
	}
	if pe.Code != code.String() {
		t.Fatalf("expected code %s, got %s (%v)", code.String(), pe.Code, err)
	}
}

// TestPostgresRowValueSeek covers a uniform-direction composite keyset
// emitting a row-value comparison, and the tie-breaker joining the ORDER BY
// so emitted order matches the comparison.
func TestPostgresRowValueSeek(t *testing.T) {
	cur := mintCursor(t, false,
		filter.CursorField{Name: "created_at", Value: "2026-01-15T10:30:00Z", Direction: filter.SortDesc},
		filter.CursorField{Name: "id", Value: "01HF3K", Direction: filter.SortDesc},
	)
	lq := mustListQuery(t, testSchema(), testSortSchema(), filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListSort("-created_at"),
		filter.ListCursor(cur),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, `("items"."created_at", "items"."id" COLLATE pg_c_utf8) < ($1, $2)`) {
		t.Errorf("expected native row comparison, got %q", result.Where)
	}
	if result.OrderBy != `ORDER BY "items"."created_at" DESC, "items"."id" COLLATE pg_c_utf8 DESC` {
		t.Errorf("expected tie-breaker in ORDER BY, got %q", result.OrderBy)
	}
	if len(result.Args) != 2 {
		t.Errorf("expected 2 cursor args, got %d: %v", len(result.Args), result.Args)
	}
}

// TestSQLiteRowValueSeek: SQLite, like PostgreSQL, seeks a
// uniform-direction composite keyset with a row-value comparison, its text
// keys compared by code point through the operand's collation, which
// leaves the columns bare for the planner.
func TestSQLiteRowValueSeek(t *testing.T) {
	cur := mintCursor(t, false,
		filter.CursorField{Name: "created_at", Value: "2026-01-15T10:30:00Z", Direction: filter.SortDesc},
		filter.CursorField{Name: "id", Value: "01HF3K", Direction: filter.SortDesc},
	)
	lq := mustListQuery(t, testSchema(), testSortSchema(), filter.SQLiteDialect{})
	result, err := lq.Build(
		filter.ListSort("-created_at"),
		filter.ListCursor(cur),
	)
	require.NoError(t, err)
	assert.Contains(t, result.Where, `("items"."created_at", "items"."id") < (?1, ?2 COLLATE BINARY)`, "the operand carries the collation")
	assert.Equal(t, `ORDER BY "items"."created_at" DESC, "items"."id" COLLATE BINARY DESC`, result.OrderBy)
	assert.Len(t, result.Args, 2)
}

// loweredNames reads the name column through lower(), an expression an
// index on lower(name) serves.
type loweredNames struct{}

func (loweredNames) MapColumn(table, column string, _ core.FieldType) string {
	expr := `"` + table + `"."` + column + `"`
	if column == "name" {
		return "lower(" + expr + ")"
	}
	return expr
}

// A row-value seek led by an expression also bounds that key on its own, on
// both dialects: SQLite seeks no row value led by an expression, and the
// bound, which the row value implies, is what an index on the expression
// seeks. A seek led by a bare column is the row value alone.
func TestRowValueSeek_LedByAnExpressionBoundsItsFirstKey(t *testing.T) {
	g := core.NewSchemaGenerator(core.WithColumnMapper(loweredNames{}), core.WithSQLTableName("walk_items"))
	fs, err := filter.GenerateFilterSchema[walkItem](g)
	require.NoError(t, err)
	fs.PrimaryKey = []string{"id"}
	ss, err := filter.GenerateSortSchema[walkItem](g)
	require.NoError(t, err)
	cursor := mintCursor(t, false,
		filter.CursorField{Name: "name", Value: "m", Direction: filter.SortAsc},
		filter.CursorField{Name: "id", Value: int64(7), Direction: filter.SortAsc},
	)
	for _, tt := range []struct {
		name    string
		dialect filter.Dialect
		want    string
	}{
		{"SQLite", filter.SQLiteDialect{}, `(lower("walk_items"."name") COLLATE BINARY >= ?1 AND (lower("walk_items"."name") COLLATE BINARY, "walk_items"."id") > (?1, ?2))`},
		{"PostgreSQL", filter.PostgresDialect{}, `(lower("walk_items"."name") COLLATE pg_c_utf8 >= $1 AND (lower("walk_items"."name") COLLATE pg_c_utf8, "walk_items"."id") > ($1, $2))`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lq := mustListQuery(t, fs, ss, tt.dialect)
			result, err := lq.Build(filter.ListSort("name"), filter.ListCursor(cursor))
			require.NoError(t, err)
			assert.Contains(t, result.Where, tt.want)
			assert.Len(t, result.Args, 2, "the bound reuses the key's placeholder")

			bare, err := lq.Build(filter.ListSort("id"), filter.ListCursor(mintCursor(t, false,
				filter.CursorField{Name: "id", Value: int64(7), Direction: filter.SortAsc})))
			require.NoError(t, err)
			assert.NotContains(t, bare.Where, ">=", "a single bare key seeks alone")
		})
	}
}

// TestSQLiteExpansion asserts the lexicographic OR-expansion a keyset of
// mixed directions takes, with numbered (reusable) placeholders and one
// bound arg per key.
func TestSQLiteExpansion(t *testing.T) {
	cur := mintCursor(t, false,
		filter.CursorField{Name: "name", Value: "m", Direction: filter.SortAsc},
		filter.CursorField{Name: "created_at", Value: "2026-01-15T10:30:00Z", Direction: filter.SortDesc},
		filter.CursorField{Name: "id", Value: "01HF3K", Direction: filter.SortAsc},
	)
	lq := mustListQuery(t, testSchema(), testSortSchema(), filter.SQLiteDialect{})
	result, err := lq.Build(
		filter.ListSort("name,-created_at"),
		filter.ListCursor(cur),
	)
	require.NoError(t, err)
	want := `(("items"."name" > ?1) OR ("items"."name" = ?1 AND "items"."created_at" < ?2) OR ("items"."name" = ?1 AND "items"."created_at" = ?2 AND "items"."id" COLLATE BINARY > ?3))`
	assert.Contains(t, result.Where, want)
	assert.Len(t, result.Args, 3, "placeholders reuse, args don't duplicate")
}

// TestMixedDirectionsExpandOnPostgres: mixed per-field directions cannot
// be a native row comparison — even Postgres takes the OR-expansion.
func TestMixedDirectionsExpandOnPostgres(t *testing.T) {
	cur := mintCursor(t, false,
		filter.CursorField{Name: "name", Value: "m", Direction: filter.SortAsc},
		filter.CursorField{Name: "created_at", Value: "2026-01-15T10:30:00Z", Direction: filter.SortDesc},
		filter.CursorField{Name: "id", Value: "01HF3K", Direction: filter.SortAsc},
	)
	lq := mustListQuery(t, testSchema(), testSortSchema(), filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListSort("name,-created_at"),
		filter.ListCursor(cur),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `(("items"."name" > $1) OR ("items"."name" = $1 AND "items"."created_at" < $2) OR ("items"."name" = $1 AND "items"."created_at" = $2 AND "items"."id" COLLATE pg_c_utf8 > $3))`
	if !strings.Contains(result.Where, want) {
		t.Errorf("expected mixed-direction expansion %q, got %q", want, result.Where)
	}
}

// TestReverseFlipsComparators: a Reverse token flips every comparator
// while the field list still validates against the active sort.
func TestReverseFlipsComparators(t *testing.T) {
	cur := mintCursor(t, true,
		filter.CursorField{Name: "created_at", Value: "2026-01-15T10:30:00Z", Direction: filter.SortDesc},
		filter.CursorField{Name: "id", Value: "01HF3K", Direction: filter.SortDesc},
	)
	lq := mustListQuery(t, testSchema(), testSortSchema(), filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListSort("-created_at"),
		filter.ListCursor(cur),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, `("items"."created_at", "items"."id" COLLATE pg_c_utf8) > ($1, $2)`) {
		t.Errorf("expected flipped comparators, got %q", result.Where)
	}
}

// TestCursorFromDifferentSortRejected: a token minted under one sort is
// rejected under another — name, direction, and arity mismatches all fail
// with ECodeInvalidCursor.
func TestCursorFromDifferentSortRejected(t *testing.T) {
	lq := mustListQuery(t, testSchema(), testSortSchema(), filter.PostgresDialect{})
	cases := []struct {
		name   string
		fields []filter.CursorField
		sort   string
	}{
		{"wrong field", []filter.CursorField{
			{Name: "created_at", Value: "x", Direction: filter.SortAsc},
			{Name: "id", Value: "1", Direction: filter.SortAsc},
		}, "name"},
		{"wrong direction", []filter.CursorField{
			{Name: "created_at", Value: "x", Direction: filter.SortAsc},
			{Name: "id", Value: "1", Direction: filter.SortAsc},
		}, "-created_at"},
		{"wrong arity", []filter.CursorField{
			{Name: "created_at", Value: "x", Direction: filter.SortDesc},
		}, "-created_at"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cur := mintCursor(t, false, tt.fields...)
			_, err := lq.Build(
				filter.ListSort(tt.sort),
				filter.ListCursor(cur),
			)
			assertParseCode(t, err, filter.ECodeInvalidCursor)
		})
	}
}

// TestTieBreakerOrderByWithoutSort: the key alone still yields a
// deterministic ORDER BY.
func TestTieBreakerOrderByWithoutSort(t *testing.T) {
	noDefault := &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"name": {Table: "items", Column: "name"},
	}}
	lq := mustListQuery(t, testSchema(), noDefault, filter.PostgresDialect{})
	result, err := lq.Build()
	if err != nil {
		t.Fatal(err)
	}
	if result.OrderBy != `ORDER BY "items"."id" COLLATE pg_c_utf8 ASC` {
		t.Errorf("expected PK-only ORDER BY, got %q", result.OrderBy)
	}
}

// TestInt64Above2p53RoundTrip: the msgpack payload preserves int64
// precision beyond JSON's 2^53 — a PK snowflake survives the round trip.
func TestInt64Above2p53RoundTrip(t *testing.T) {
	big := int64(1)<<62 + 3
	cur := mintCursor(t, false, filter.CursorField{Name: "id", Value: big, Direction: filter.SortAsc})
	decoded, err := filter.DecodeCursor(cur)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decoded.Fields[0].Value.(int64)
	if !ok {
		t.Fatalf("expected int64, got %T (%v)", decoded.Fields[0].Value, decoded.Fields[0].Value)
	}
	if got != big {
		t.Fatalf("expected %d, got %d", big, got)
	}
}

// TestCursorPlanToken_MirrorsBuildKeys: the plan's token carries the
// sort fields then the tie-breaker, in Build's derivation order, reads each
// value by row column (never the qualified expression — ),
// and surfaces getter errors instead of minting an unresumable token.
func TestCursorPlanToken_MirrorsBuildKeys(t *testing.T) {
	sort := &filter.SortResult{Fields: []filter.SortField{
		{Name: "created_at", Schema: &filter.SortFieldSchema{Table: "items", Column: "created_at"}, Direction: filter.SortDesc},
	}}
	plan := filter.CursorPlan{Keys: filter.Keyset(sort, filter.CursorKey{Name: "id", Column: "id"})}
	token, err := plan.Token(false, func(col string) (any, error) {
		switch col {
		case "created_at":
			return "2026-01-01", nil
		case "id":
			return "01HF3K", nil
		}
		t.Fatalf("unexpected column %q", col)
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(token.Fields) != 2 || token.Fields[0].Name != "created_at" || token.Fields[1].Name != "id" {
		t.Fatalf("unexpected token fields: %+v", token.Fields)
	}
	// Tie-breaker inherits the primary direction so uniform-direction keys
	// keep the native row-comparison fast path.
	if token.Fields[1].Direction != filter.SortDesc {
		t.Errorf("expected tie-breaker to inherit primary direction, got %s", token.Fields[1].Direction)
	}
	if _, err := plan.Token(false, func(string) (any, error) { return nil, fmt.Errorf("boom") }); err == nil {
		t.Fatal("getter error must surface")
	}
}
