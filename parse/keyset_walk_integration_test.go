//go:build integration

package parse_test

import (
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// trackItem is a generated-schema entity: table-qualified sort expressions,
// a UUID primary key, nullable keys (pointers and sql.Null types), a
// timestamp key and a decimal key.
type trackItem struct {
	ID        uuid.UUID        `json:"id"         db:"id"         parse:"filter,sort"`
	Rank      *int64           `json:"rank"       db:"rank"       parse:"filter,sort"`
	Score     sql.NullInt64    `json:"score"      db:"score"      parse:"filter,sort"`
	Name      string           `json:"name"       db:"name"       parse:"filter,sort"`
	CreatedAt time.Time        `json:"created_at" db:"created_at" parse:"filter,sort"`
	SeenAt    sql.NullTime     `json:"seen_at"    db:"seen_at"    parse:"filter,sort"`
	Price     *decimal.Decimal `json:"price"      db:"price"      parse:"filter,sort"`
}

// A seed's score is its rank and its seen_at its created_at, NULL where its
// rank is: the sql.Null keys order as the pointer key does.
type trackSeed struct {
	id      string
	rank    *int64
	name    string
	created int     // seconds after the base instant
	price   *string // decimal text, inserted as written
}

// trackSeeds ids sort a < b < … < g in both dialects. Prices order
// differently as numbers than as text ("10.25" < "9.5"), and d and g hold
// equal prices at different scales.
func trackSeeds() []trackSeed {
	seeds := []trackSeed{
		{id: "a", rank: new(int64(1)), name: "e", created: 5, price: new("10.25")},
		{id: "b", rank: new(int64(2)), name: "d", created: 1, price: new("9.5")},
		{id: "c", rank: nil, name: "c", created: 3, price: new("-1")},
		{id: "d", rank: new(int64(2)), name: "b", created: 4, price: new("2.00")},
		{id: "e", rank: nil, name: "a", created: 2, price: new("100")},
		{id: "f", rank: new(int64(3)), name: "g", created: 6, price: nil},
		{id: "g", rank: nil, name: "f", created: 0, price: new("2")},
	}
	return seeds
}

// Expected presentation order per sort, NULL greatest, ties on id in the
// primary direction.
func trackOrders() map[string]string {
	orders := map[string]string{
		"":            "abcdefg",
		"rank":        "abdfceg",
		"-rank":       "gecfdba",
		"score":       "abdfceg",
		"-score":      "gecfdba",
		"seen_at":     "bdafceg",
		"-seen_at":    "gecfadb",
		"seen_at,id":  "bdafceg",
		"rank,-name":  "abdfgce",
		"-created_at": "fadcebg",
		"name":        "edcbagf",
		"price":       "cdgbaef",
		"-price":      "feabgdc",
	}
	return orders
}

// trackID maps a row letter to a UUID whose last hex digit preserves the
// letter order (a → 1 … g → 7).
func trackID(s string) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-00000000000%x", s[0]-'a'+1))
}

func trackBase() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
}

// Every generated sort walks forwards and backwards over exactly the rows,
// in the expected order, on each backend: UUID text keys, NULL keys, mixed
// directions, timestamps and decimals included. SQLite's decimals walk over
// both numeric affinities a decimal column may have, NUMERIC (INTEGER or
// REAL per value) and REAL; PostgreSQL's native uuid, timestamptz and
// numeric columns place NULLs otherwise by default.
func TestKeysetWalk(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		if b.engine == postgresEngine {
			runTrackWalks(t, b)
			return
		}
		for _, priceType := range []string{"NUMERIC", "REAL"} {
			t.Run("price "+priceType, func(t *testing.T) { runTrackWalks(t, b.with(storing(core.FieldTypeDecimal, priceType))) })
		}
	})
}

// trackTable holds trackSeeds; SQLite's seen_at is a DATETIME column, which
// holds the canonical text as written.
func trackTable() sqlTable {
	table := sqlTable{name: "track_items", key: []string{"id"}, columns: []sqlColumn{
		column[uuid.UUID]("id"),
		column[int64]("rank"),
		column[int64]("score"),
		column[string]("name").required(),
		column[time.Time]("created_at").required(),
		column[time.Time]("seen_at").holding(sqliteEngine, "DATETIME"),
		column[decimal.Decimal]("price"),
	}}
	return table
}

func runTrackWalks(t *testing.T, b sqlBackend) {
	t.Helper()
	db := b.open(t)
	seeds := trackSeeds()
	rows := make([][]any, len(seeds))
	for i, s := range seeds {
		rows[i] = []any{trackID(s.id), s.rank, s.rank, s.name, trackTime(s), trackSeen(s), s.price}
	}
	b.load(t, db, trackTable(), rows)
	p, err := parse.NewResource[trackItem]("track_items")
	require.NoError(t, err)

	for sortParam, want := range trackOrders() {
		t.Run("sort="+sortParam, func(t *testing.T) {
			forward, lastStart := walkTrack(t, db, b.dialect, p, sortParam, "", false)
			assert.Equal(t, want, forward, "forward walk")

			// Walk back from the last page's start cursor; the rows seen,
			// followed by the last page, must reproduce the forward order.
			lastPage := forward[len(forward)-lastPageLen(len(forward)):]
			backward, _ := walkTrack(t, db, b.dialect, p, sortParam, lastStart, true)
			assert.Equal(t, want, backward+lastPage, "backward walk")
		})
	}
}

const trackLimit = 2

func lastPageLen(n int) int {
	r := n % trackLimit
	if r != 0 {
		return r
	}
	return trackLimit
}

// walkTrack pages through the list from cursor, following end cursors
// forwards or start cursors backwards, and returns the ids seen in
// presentation order (plus, going forwards, the last page's start cursor).
func walkTrack(t *testing.T, db *sql.DB, d filter.Dialect, p *parse.Resource[trackItem], sortParam, cursor string, backward bool) (string, string) {
	t.Helper()
	lq, err := p.ListQuery(d)
	require.NoError(t, err)
	var pages []string
	var lastStart string
	for n := 0; ; n++ {
		require.Less(t, n, 10, "walk did not terminate")
		opts := []filter.ListOption{filter.ListSort(sortParam), filter.ListLimit(trackLimit)}
		if cursor != "" {
			opts = append(opts, filter.ListCursor(cursor))
		}
		res, err := lq.Build(opts...)
		require.NoError(t, err)
		items := queryTrack(t, db, d, p, res)
		page, err := filter.NewPage(items, res.Limit, res.Cursor, func(it trackItem, col string) (any, error) {
			return filter.CursorValueByTag(it, "db", col)
		})
		require.NoError(t, err)
		ids := ""
		for _, it := range page.Items {
			ids += trackSuffix(it.ID)
		}
		pages = append(pages, ids)
		lastStart = string(page.PageInfo.StartCursor)
		if backward {
			if !page.PageInfo.HasPrevPage {
				break
			}
			cursor = string(page.PageInfo.StartCursor)
		} else {
			if !page.PageInfo.HasNextPage {
				break
			}
			cursor = string(page.PageInfo.EndCursor)
		}
	}
	out := ""
	if backward {
		for _, page := range slices.Backward(pages) {
			out += page
		}
	} else {
		for _, pg := range pages {
			out += pg
		}
	}
	return out, lastStart
}

func trackSuffix(id uuid.UUID) string {
	return string(rune('a' + id[15] - 1))
}

func queryTrack(t *testing.T, db *sql.DB, d filter.Dialect, p *parse.Resource[trackItem], res *filter.ListResult) []trackItem {
	t.Helper()
	columns, err := p.Columns(d)
	require.NoError(t, err)
	st := parse.Statement{SQL: "SELECT " + joinColumns(columns) + " FROM " + d.QuoteIdent("track_items") + " " + res.Fragment, Args: res.Args}
	return resourceRows(t, db, p, d, st)
}

func trackTime(s trackSeed) time.Time {
	return trackBase().Add(time.Duration(s.created) * time.Second)
}

// trackSeen is a seed's seen_at: its created_at, or NULL where its rank is.
func trackSeen(s trackSeed) *time.Time {
	if s.rank == nil {
		return nil
	}
	at := trackTime(s)
	return &at
}
