//go:build unit || integration

package parse_test

import (
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// trackItem is a generated-schema entity: table-qualified sort expressions,
// a UUID primary key, a nullable key and a timestamp key.
type trackItem struct {
	ID        uuid.UUID `json:"id"         db:"id"         parse:"filter,sort"`
	Rank      *int64    `json:"rank"       db:"rank"       parse:"filter,sort"`
	Name      string    `json:"name"       db:"name"       parse:"filter,sort"`
	CreatedAt time.Time `json:"created_at" db:"created_at" parse:"filter,sort"`
}

type trackSeed struct {
	id      string
	rank    *int64
	name    string
	created int // seconds after the base instant
}

// trackSeeds ids sort a < b < … < g in both dialects.
var trackSeeds = []trackSeed{
	{id: "a", rank: new(int64(1)), name: "e", created: 5},
	{id: "b", rank: new(int64(2)), name: "d", created: 1},
	{id: "c", rank: nil, name: "c", created: 3},
	{id: "d", rank: new(int64(2)), name: "b", created: 4},
	{id: "e", rank: nil, name: "a", created: 2},
	{id: "f", rank: new(int64(3)), name: "g", created: 6},
	{id: "g", rank: nil, name: "f", created: 0},
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
}

// trackID maps a row letter to a UUID whose last hex digit preserves the
// letter order (a → 1 … g → 7).
func trackID(s string) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-00000000000%x", s[0]-'a'+1))
}

var trackBase = time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)

// trackDB is one database under test.
type trackDB struct {
	db       *sql.DB
	dialect  filter.Dialect
	schema   string
	insert   func(db *sql.DB, s trackSeed) error
	scanTime func(src any) (time.Time, error)
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
	lq := p.ListQuery(tdb.dialect)
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
		items := queryTrack(t, tdb, res)
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

func queryTrack(t *testing.T, tdb trackDB, res *filter.ListResult) []trackItem {
	t.Helper()
	q := "SELECT id, rank, name, created_at FROM track_items " + res.Fragment
	rows, err := tdb.db.Query(q, res.Args...)
	require.NoError(t, err, q)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []trackItem
	for rows.Next() {
		var it trackItem
		var rank sql.NullInt64
		var created any
		require.NoError(t, rows.Scan(&it.ID, &rank, &it.Name, &created))
		if rank.Valid {
			it.Rank = &rank.Int64
		}
		it.CreatedAt, err = tdb.scanTime(created)
		require.NoError(t, err)
		out = append(out, it)
	}
	require.NoError(t, rows.Err())
	return out
}

func trackTime(s trackSeed) time.Time {
	return trackBase.Add(time.Duration(s.created) * time.Second)
}

func scanTimeValue(src any) (time.Time, error) {
	switch v := src.(type) {
	case time.Time:
		return v, nil
	case string:
		return time.Parse(time.RFC3339Nano, v)
	case []byte:
		return time.Parse(time.RFC3339Nano, string(v))
	}
	return time.Time{}, fmt.Errorf("unexpected created_at %T", src)
}
