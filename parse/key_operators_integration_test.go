//go:build integration

package parse_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// uuidKeyed is a resource keyed by a UUID, which a request filter may range
// over.
type uuidKeyed struct {
	ID   uuid.UUID `json:"id"   db:"id"`
	Name string    `json:"name" db:"name" parse:"filter"`
}

// uuidKeys straddle the boundaries a UUID order could disagree on: a digit
// and a letter in the first position, a first byte below and above 0x80 (a
// signed comparison puts 0x80 first), and the last byte alone.
func uuidKeys() []uuid.UUID {
	keys := []uuid.UUID{
		uuid.MustParse("09000000-0000-4000-8000-000000000000"),
		uuid.MustParse("7f000000-0000-4000-8000-000000000000"),
		uuid.MustParse("80000000-0000-4000-8000-000000000000"),
		uuid.MustParse("a0000000-0000-4000-8000-000000000000"),
		uuid.MustParse("a0000000-0000-4000-8000-00000000000f"),
		uuid.MustParse("ff000000-0000-4000-8000-000000000000"),
	}
	return keys
}

type uuidKeyRange struct {
	name   string
	filter string
	want   []int
}

// uuidKeyRanges are range filters on the key, operands in either case, and
// the keys (indexes into uuidKeys) each selects in PostgreSQL's uuid order:
// the bytes', which is the canonical lowercase text's.
func uuidKeyRanges() []uuidKeyRange {
	ranges := []uuidKeyRange{
		{"gt across the sign bit", `{"id":{"$gt":"7FFFFFFF-FFFF-4FFF-BFFF-FFFFFFFFFFFF"}}`, []int{2, 3, 4, 5}},
		{"gte a key", `{"id":{"$gte":"a0000000-0000-4000-8000-000000000000"}}`, []int{3, 4, 5}},
		{"lt a letter", `{"id":{"$lt":"a0000000-0000-4000-8000-000000000000"}}`, []int{0, 1, 2}},
		{"lte the last byte", `{"id":{"$lte":"A0000000-0000-4000-8000-00000000000E"}}`, []int{0, 1, 2, 3}},
		{"a range", `{"id":{"$gt":"09000000-0000-4000-8000-000000000000","$lt":"ff000000-0000-4000-8000-000000000000"}}`, []int{1, 2, 3, 4}},
	}
	return ranges
}

// A UUID key's range selects the same rows in filter.Evaluate and on each
// backend: SQLite holds the canonical lowercase text, and PostgreSQL's uuid
// order is its bytes'.
func TestUUIDKeyRanges(t *testing.T) {
	runSQLBackends(t, runUUIDKeyRanges)
}

// runUUIDKeyRanges loads uuidKeys on b, then holds each range to select the
// same keys in SQL and in filter.Evaluate over the rows.
func runUUIDKeyRanges(t *testing.T, b sqlBackend) {
	t.Helper()
	provider, db := b.open(t)
	keys := uuidKeys()
	rows := make([][]any, len(keys))
	for i, id := range keys {
		rows[i] = []any{id, string(rune('a' + i))}
	}
	b.load(t, db, sqlTable{name: "uuid_keyed", key: []string{"id"}, columns: []sqlColumn{column[uuid.UUID]("id"), column[string]("name").required()}}, rows)
	r, err := parse.NewResource[uuidKeyed](provider, "uuid_keyed")
	require.NoError(t, err)

	for _, tc := range uuidKeyRanges() {
		t.Run(tc.name, func(t *testing.T) {
			want := make([]uuid.UUID, len(tc.want))
			for i, k := range tc.want {
				want[i] = keys[k]
			}
			req, err := r.ParseListInput(parse.ListInput{Filter: tc.filter})
			require.NoError(t, err)

			var evaluated []uuid.UUID
			for _, id := range keys {
				row := filter.ColumnRow(func(column string) (any, bool) {
					_, name, _ := strings.Cut(column, ".")
					return id, name == "id"
				})
				truth, err := filter.Evaluate(req.Filter().AST().Root, row)
				require.NoError(t, err)
				if truth == filter.True {
					evaluated = append(evaluated, id)
				}
			}
			assert.Equal(t, want, evaluated, "filter.Evaluate")

			plan, err := r.BuildList(req, parse.Scope{})
			require.NoError(t, err)
			selected := itemIDs(resourceRows(t, r, plan.Page), func(k uuidKeyed) uuid.UUID { return k.ID })
			assert.Equal(t, want, selected, provider.Name())
			assert.True(t, slices.IsSortedFunc(selected, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) }), "sorted by id")
		})
	}
}
