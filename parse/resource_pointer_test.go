//go:build unit

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

type pointerResourceRow struct {
	ID string `json:"id" db:"id"`
}

func TestResource_PointerRowsKeepTheirKeys(t *testing.T) {
	first, second := pointerResourceRow{ID: "r1"}, pointerResourceRow{ID: "r2"}
	t.Run("value model", func(t *testing.T) {
		checkResourceRowPages(t, []pointerResourceRow{first, second})
	})
	t.Run("pointer model", func(t *testing.T) {
		checkResourceRowPages(t, []*pointerResourceRow{&first, &second})
	})
	for _, resource := range []interface{ Key(any) (any, error) }{
		parse.MustResource[pointerResourceRow]("rows"),
		parse.MustResource[*pointerResourceRow]("rows"),
	} {
		for _, item := range []any{first, &first, new(&first)} {
			key, err := resource.Key(item)
			require.NoError(t, err)
			assert.Equal(t, "r1", key)
		}
		type otherRow pointerResourceRow
		for _, item := range []any{nil, (*pointerResourceRow)(nil), new((*pointerResourceRow)(nil)), otherRow(first), 1} {
			_, err := resource.Key(item)
			require.ErrorContains(t, err, "rows")
		}
	}
	r := parse.MustResource[*pointerResourceRow]("rows", parse.PageSize(1, 10))
	plan, err := r.BuildList(filter.SQLiteDialect{}, nil, parse.Scope{})
	require.NoError(t, err)
	_, err = r.Page([]*pointerResourceRow{nil, &second}, plan)
	require.ErrorContains(t, err, "nil row")
}

func checkResourceRowPages[T any](t *testing.T, items []T) {
	t.Helper()
	r := parse.MustResource[T]("rows", parse.PageSize(1, 10))
	plan, err := r.BuildList(filter.SQLiteDialect{}, nil, parse.Scope{})
	require.NoError(t, err)
	page, err := r.Page(items, plan)
	require.NoError(t, err)
	require.True(t, page.PageInfo.HasNextPage)
	cursor, err := filter.DecodeCursor(string(page.PageInfo.EndCursor))
	require.NoError(t, err)
	require.Len(t, cursor.Fields, 1)
	assert.Equal(t, "r1", cursor.Fields[0].Value)

	pointers := []*T{&items[0], &items[1]}
	pointerPage, err := r.PagePointers(pointers, plan)
	require.NoError(t, err)
	assert.Equal(t, page.PageInfo, pointerPage.PageInfo)
	_, err = r.PagePointers([]*T{nil, &items[1]}, plan)
	require.ErrorContains(t, err, "nil row")
}
