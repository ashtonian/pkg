//go:build unit || integration

package parse_test

import (
	"database/sql"
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
var uuidKeys = []uuid.UUID{
	uuid.MustParse("09000000-0000-4000-8000-000000000000"),
	uuid.MustParse("7f000000-0000-4000-8000-000000000000"),
	uuid.MustParse("80000000-0000-4000-8000-000000000000"),
	uuid.MustParse("a0000000-0000-4000-8000-000000000000"),
	uuid.MustParse("a0000000-0000-4000-8000-00000000000f"),
	uuid.MustParse("ff000000-0000-4000-8000-000000000000"),
}

// uuidKeyRanges are range filters on the key, operands in either case, and
// the keys (indexes into uuidKeys) each selects in PostgreSQL's uuid order:
// the bytes', which is the canonical lowercase text's.
var uuidKeyRanges = []struct {
	name   string
	filter string
	want   []int
}{
	{"gt across the sign bit", `{"id":{"$gt":"7FFFFFFF-FFFF-4FFF-BFFF-FFFFFFFFFFFF"}}`, []int{2, 3, 4, 5}},
	{"gte a key", `{"id":{"$gte":"a0000000-0000-4000-8000-000000000000"}}`, []int{3, 4, 5}},
	{"lt a letter", `{"id":{"$lt":"a0000000-0000-4000-8000-000000000000"}}`, []int{0, 1, 2}},
	{"lte the last byte", `{"id":{"$lte":"A0000000-0000-4000-8000-00000000000E"}}`, []int{0, 1, 2, 3}},
	{"a range", `{"id":{"$gt":"09000000-0000-4000-8000-000000000000","$lt":"ff000000-0000-4000-8000-000000000000"}}`, []int{1, 2, 3, 4}},
}

// runUUIDKeyRanges loads uuidKeys into db, then holds each range to select
// the same keys in SQL on d and in filter.Evaluate over the rows.
func runUUIDKeyRanges(t *testing.T, db *sql.DB, d filter.Dialect, schema string, key func(uuid.UUID) any) {
	t.Helper()
	_, err := db.Exec(schema)
	require.NoError(t, err)
	for i, id := range uuidKeys {
		_, err := db.Exec(`INSERT INTO uuid_keyed (id, name) VALUES (`+d.Placeholder(1)+`, `+d.Placeholder(2)+`)`, key(id), string(rune('a'+i)))
		require.NoError(t, err)
	}
	r, err := parse.NewResource[uuidKeyed]("uuid_keyed")
	require.NoError(t, err)

	for _, tc := range uuidKeyRanges {
		t.Run(tc.name, func(t *testing.T) {
			want := make([]uuid.UUID, len(tc.want))
			for i, k := range tc.want {
				want[i] = uuidKeys[k]
			}
			req, err := r.ParseListInput(parse.ListInput{Filter: tc.filter})
			require.NoError(t, err)

			var evaluated []uuid.UUID
			for _, id := range uuidKeys {
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

			plan, err := r.BuildList(d, req, parse.Scope{})
			require.NoError(t, err)
			var selected []uuid.UUID
			for _, row := range resourceRows(t, db, r, d, plan.Page) {
				selected = append(selected, row.ID)
			}
			assert.Equal(t, want, selected, d.Name())
			assert.True(t, slices.IsSortedFunc(selected, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) }), "sorted by id")
		})
	}
}
