//go:build unit

package filter_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

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

// TestPostgresNativeRowCompare covers a uniform-direction
// composite keyset emits Postgres' native row-value comparison, and the
// tie-breaker joins the ORDER BY so emitted order matches the comparison.
func TestPostgresNativeRowCompare(t *testing.T) {
	cur := mintCursor(t, false,
		filter.CursorField{Name: "created_at", Value: "2026-01-15T10:30:00Z", Direction: filter.SortDesc},
		filter.CursorField{Name: "id", Value: "01HF3K", Direction: filter.SortDesc},
	)
	schema := testSchema()
	schema.PrimaryKey = "id"
	lq := filter.NewListQuery(schema, testSortSchema(), filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListSort("-created_at"),
		filter.ListCursor(cur),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, "(items.created_at, id) < ($1, $2)") {
		t.Errorf("expected native row comparison, got %q", result.Where)
	}
	if result.OrderBy != "ORDER BY items.created_at DESC, id DESC" {
		t.Errorf("expected tie-breaker in ORDER BY, got %q", result.OrderBy)
	}
	if len(result.Args) != 2 {
		t.Errorf("expected 2 cursor args, got %d: %v", len(result.Args), result.Args)
	}
}

// TestSQLiteExpansion asserts the lexicographic OR-expansion with
// numbered (reusable) placeholders and one bound arg per key.
func TestSQLiteExpansion(t *testing.T) {
	cur := mintCursor(t, false,
		filter.CursorField{Name: "created_at", Value: "2026-01-15T10:30:00Z", Direction: filter.SortDesc},
		filter.CursorField{Name: "id", Value: "01HF3K", Direction: filter.SortDesc},
	)
	schema := testSchema()
	schema.PrimaryKey = "id"
	lq := filter.NewListQuery(schema, testSortSchema(), filter.SQLiteDialect{})
	result, err := lq.Build(
		filter.ListSort("-created_at"),
		filter.ListCursor(cur),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "((items.created_at < ?1) OR (items.created_at = ?1 AND id < ?2))"
	if !strings.Contains(result.Where, want) {
		t.Errorf("expected expansion %q, got %q", want, result.Where)
	}
	if len(result.Args) != 2 {
		t.Errorf("expected 2 cursor args (placeholders reuse, args don't duplicate), got %d", len(result.Args))
	}
}

// TestMixedDirectionsExpandOnPostgres: mixed per-field directions cannot
// be a native row comparison — even Postgres takes the OR-expansion.
func TestMixedDirectionsExpandOnPostgres(t *testing.T) {
	cur := mintCursor(t, false,
		filter.CursorField{Name: "name", Value: "m", Direction: filter.SortAsc},
		filter.CursorField{Name: "created_at", Value: "2026-01-15T10:30:00Z", Direction: filter.SortDesc},
		filter.CursorField{Name: "id", Value: "01HF3K", Direction: filter.SortAsc},
	)
	schema := testSchema()
	schema.PrimaryKey = "id"
	lq := filter.NewListQuery(schema, testSortSchema(), filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListSort("name,-created_at"),
		filter.ListCursor(cur),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "((items.name > $1) OR (items.name = $1 AND items.created_at < $2) OR (items.name = $1 AND items.created_at = $2 AND id > $3))"
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
	schema := testSchema()
	schema.PrimaryKey = "id"
	lq := filter.NewListQuery(schema, testSortSchema(), filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListSort("-created_at"),
		filter.ListCursor(cur),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Where, "(items.created_at, id) > ($1, $2)") {
		t.Errorf("expected flipped comparators, got %q", result.Where)
	}
}

// TestCursorFromDifferentSortRejected: a token minted under one sort is
// rejected under another — name, direction, and arity mismatches all fail
// with ECodeInvalidCursor.
func TestCursorFromDifferentSortRejected(t *testing.T) {
	schema := testSchema()
	schema.PrimaryKey = "id"
	lq := filter.NewListQuery(schema, testSortSchema(), filter.PostgresDialect{})
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

// TestCursorWithoutKeysRejected: a cursor with no active sort and no
// tie-breaker has nothing to compare against — explicit error, not silent
// acceptance.
func TestCursorWithoutKeysRejected(t *testing.T) {
	noDefault := &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"name": {SQLExpr: "items.name"},
	}}
	cur := mintCursor(t, false, filter.CursorField{Name: "name", Value: "x", Direction: filter.SortAsc})
	lq := filter.NewListQuery(testSchema(), noDefault, filter.PostgresDialect{})
	_, err := lq.Build(filter.ListCursor(cur))
	assertParseCode(t, err, filter.ECodeInvalidCursor)
}

// A primary key that resolves through
// no schema must be a plain identifier — user text is never emitted as SQL.
func TestPrimaryKeyRejectsNonIdentifier(t *testing.T) {
	schema := testSchema()
	schema.PrimaryKey = "id; DROP TABLE items"
	lq := filter.NewListQuery(schema, testSortSchema(), filter.PostgresDialect{})
	_, err := lq.Build(
		filter.ListSort("-created_at"),
	)
	if err == nil {
		t.Fatal("expected non-identifier tie-breaker to be rejected")
	}
}

// TestTieBreakerOrderByWithoutSort: tie-breaker alone still yields a
// deterministic ORDER BY — cursor paging over an unordered SELECT was
// undefined before P-10.
func TestTieBreakerOrderByWithoutSort(t *testing.T) {
	noDefault := &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"name": {SQLExpr: "items.name"},
	}}
	schema := testSchema()
	schema.PrimaryKey = "id"
	lq := filter.NewListQuery(schema, noDefault, filter.PostgresDialect{})
	result, err := lq.Build()
	if err != nil {
		t.Fatal(err)
	}
	if result.OrderBy != "ORDER BY id ASC" {
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
		{Name: "created_at", Schema: &filter.SortFieldSchema{SQLExpr: "items.created_at"}, Direction: filter.SortDesc},
	}}
	plan := filter.CursorPlan{Keys: filter.Keyset(sort, filter.CursorKey{Name: "id", SQLExpr: "id", Column: "id"})}
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
