//go:build unit || integration

package parse_test

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// trackItem is a generated-schema entity: table-qualified sort expressions,
// a UUID primary key, nullable keys, a timestamp key and a decimal key.
type trackItem struct {
	ID        uuid.UUID        `json:"id"         db:"id"         parse:"filter,sort"`
	Rank      *int64           `json:"rank"       db:"rank"       parse:"filter,sort"`
	Name      string           `json:"name"       db:"name"       parse:"filter,sort"`
	CreatedAt time.Time        `json:"created_at" db:"created_at" parse:"filter,sort"`
	Price     *decimal.Decimal `json:"price"      db:"price"      parse:"filter,sort"`
}

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
var trackSeeds = []trackSeed{
	{id: "a", rank: new(int64(1)), name: "e", created: 5, price: new("10.25")},
	{id: "b", rank: new(int64(2)), name: "d", created: 1, price: new("9.5")},
	{id: "c", rank: nil, name: "c", created: 3, price: new("-1")},
	{id: "d", rank: new(int64(2)), name: "b", created: 4, price: new("2.00")},
	{id: "e", rank: nil, name: "a", created: 2, price: new("100")},
	{id: "f", rank: new(int64(3)), name: "g", created: 6, price: nil},
	{id: "g", rank: nil, name: "f", created: 0, price: new("2")},
}

// Expected presentation order per sort, NULL greatest, ties on id in the
// primary direction.
var trackOrders = map[string]string{
	"":            "abcdefg",
	"rank":        "abdfceg",
	"-rank":       "gecfdba",
	"rank,-name":  "abdfgce",
	"-created_at": "fadcebg",
	"name":        "edcbagf",
	"price":       "cdgbaef",
	"-price":      "feabgdc",
}

// trackID maps a row letter to a UUID whose last hex digit preserves the
// letter order (a → 1 … g → 7).
func trackID(s string) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-00000000000%x", s[0]-'a'+1))
}

var trackBase = time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)

// trackDB is one database under test.
type trackDB struct {
	db      *sql.DB
	dialect filter.Dialect
	schema  string
	insert  func(db *sql.DB, s trackSeed) error
}

func runTrackWalks(t *testing.T, tdb trackDB) {
	t.Helper()
	_, err := tdb.db.Exec(tdb.schema)
	require.NoError(t, err)
	for _, s := range trackSeeds {
		require.NoError(t, tdb.insert(tdb.db, s))
	}
	p, err := parse.NewResource[trackItem]("track_items")
	require.NoError(t, err)

	for sortParam, want := range trackOrders {
		t.Run("sort="+sortParam, func(t *testing.T) {
			forward, lastStart := walkTrack(t, tdb, p, sortParam, "", false)
			assert.Equal(t, want, forward, "forward walk")

			// Walk back from the last page's start cursor; the rows seen,
			// followed by the last page, must reproduce the forward order.
			lastPage := forward[len(forward)-lastPageLen(len(forward)):]
			backward, _ := walkTrack(t, tdb, p, sortParam, lastStart, true)
			assert.Equal(t, want, backward+lastPage, "backward walk")
		})
	}
}

const trackLimit = 2

func lastPageLen(n int) int {
	if r := n % trackLimit; r != 0 {
		return r
	}
	return trackLimit
}

// walkTrack pages through the list from cursor, following end cursors
// forwards or start cursors backwards, and returns the ids seen in
// presentation order (plus, going forwards, the last page's start cursor).
func walkTrack(t *testing.T, tdb trackDB, p *parse.Resource[trackItem], sortParam, cursor string, backward bool) (string, string) {
	t.Helper()
	lq, err := p.ListQuery(tdb.dialect)
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
		items := queryTrack(t, tdb, p, res)
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

func queryTrack(t *testing.T, tdb trackDB, p *parse.Resource[trackItem], res *filter.ListResult) []trackItem {
	t.Helper()
	columns, err := p.Columns(tdb.dialect)
	require.NoError(t, err)
	st := parse.Statement{SQL: "SELECT " + strings.Join(columns, ", ") + " FROM " + tdb.dialect.QuoteIdent("track_items") + " " + res.Fragment, Args: res.Args}
	return resourceRows(t, tdb.db, p, tdb.dialect, st)
}

func trackTime(s trackSeed) time.Time {
	return trackBase.Add(time.Duration(s.created) * time.Second)
}
