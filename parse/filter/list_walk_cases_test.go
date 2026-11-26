//go:build unit || integration

package filter_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
)

// listWalk pages through a list query's rows, which query reads for a
// built page and id names.
type listWalk[T any] struct {
	lq    *filter.ListQuery
	query func(t *testing.T, res *filter.ListResult) []T
	id    func(T) int64
}

// check walks the list by sort at every page size, forwards from the first
// page and backwards from the last page's start cursor, and holds every
// walk to want, the ids in the order the sort gives: no row is repeated or
// skipped.
func (w listWalk[T]) check(t *testing.T, sort string, want []int64) {
	t.Helper()
	for limit := 1; limit <= len(want); limit++ {
		forward, lastStart := w.walk(t, sort, limit, "", false, len(want))
		assert.Equal(t, want, forward, "sort=%s, %d a page, forwards", sort, limit)

		// The rows before the last page, walked back from its start
		// cursor, then the last page, are the forward walk. A single page
		// has no start cursor: nothing is before it.
		last := len(want) % limit
		if last == 0 {
			last = limit
		}
		var backward []int64
		if lastStart != "" {
			backward, _ = w.walk(t, sort, limit, lastStart, true, len(want))
		}
		assert.Equal(t, want, append(backward, want[len(want)-last:]...), "sort=%s, %d a page, backwards", sort, limit)
	}
}

// walk pages from cursor, following end cursors forwards or start cursors
// backwards, and returns the ids in presentation order and, forwards, the
// last page's start cursor. A walk of more pages than rows does not end.
func (w listWalk[T]) walk(t *testing.T, sort string, limit int, cursor string, backward bool, rows int) ([]int64, string) {
	t.Helper()
	var pages [][]int64
	var lastStart string
	for n := 0; ; n++ {
		require.LessOrEqual(t, n, rows, "sort=%s, %d a page: the walk does not end, pages %v", sort, limit, pages)
		opts := []filter.ListOption{filter.ListSort(sort), filter.ListLimit(limit)}
		if cursor != "" {
			opts = append(opts, filter.ListCursor(cursor))
		}
		res, err := w.lq.Build(opts...)
		require.NoError(t, err)
		page, err := filter.NewPage(w.query(t, res), res.Limit, res.Cursor, func(r T, column string) (any, error) {
			return filter.CursorValueByTag(r, "db", column)
		})
		require.NoError(t, err, "sort=%s, %d a page", sort, limit)
		ids := make([]int64, len(page.Items))
		for i, r := range page.Items {
			ids[i] = w.id(r)
		}
		pages = append(pages, ids)
		lastStart = string(page.PageInfo.StartCursor)
		next, more := page.PageInfo.EndCursor, page.PageInfo.HasNextPage
		if backward {
			next, more = page.PageInfo.StartCursor, page.PageInfo.HasPrevPage
		}
		if !more {
			break
		}
		cursor = string(next)
	}
	if backward {
		slices.Reverse(pages)
	}
	return slices.Concat(pages...), lastStart
}
