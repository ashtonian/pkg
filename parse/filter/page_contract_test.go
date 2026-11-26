//go:build unit || concurrency

package filter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageJSONShape(t *testing.T) {
	t.Parallel()
	p := Page[map[string]any]{
		Items: []map[string]any{{"id": "1"}},
		Relations: map[string]map[string][]string{
			"tags": {"1": {"a", "b"}},
		},
	}
	p.PageInfo.HasNextPage = true

	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)

	// Items, metadata and projections share one flat response envelope.
	for _, want := range []string{`"items"`, `"page_info"`, `"_relations"`, `"has_next_page":true`} {
		if !strings.Contains(got, want) {
			t.Errorf("Page JSON missing %s\n got: %s", want, got)
		}
	}
	if strings.Contains(got, `"ListResult"`) || strings.Contains(got, `"list_result"`) {
		t.Errorf("page members must marshal flat, got: %s", got)
	}
}

func TestPageRelationsOmitEmpty(t *testing.T) {
	t.Parallel()
	var p Page[string]
	p.Items = []string{"x"}

	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "_relations") {
		t.Errorf("nil Relations must omit `_relations`, got: %s", b)
	}
}

func TestNewPageTruncatesAndMintsCursor(t *testing.T) {
	t.Parallel()
	type row struct{ ID string }
	plan := CursorPlan{Keys: []CursorKey{{Name: "id", Column: "id", Direction: SortAsc}}}
	get := func(r row, _ string) (any, error) { return r.ID, nil }

	// limit+1 rows fetched → truncate, HasNextPage, EndCursor minted.
	page, err := NewPage([]row{{"a"}, {"b"}, {"c"}}, 2, plan, get)
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	if len(page.Items) != 2 || !page.PageInfo.HasNextPage {
		t.Fatalf("want 2 items + HasNextPage, got %d items, HasNextPage=%v", len(page.Items), page.PageInfo.HasNextPage)
	}
	if page.PageInfo.EndCursor.IsZero() {
		t.Fatal("EndCursor must be minted when more rows exist")
	}
	// The minted cursor round-trips to the boundary row's keyset.
	token, err := DecodeCursor(string(page.PageInfo.EndCursor))
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if len(token.Fields) != 1 || token.Fields[0].Name != "id" || token.Fields[0].Value != "b" {
		t.Fatalf("cursor keyset = %+v, want single field id=b", token.Fields)
	}

	// Exactly-limit rows → no truncation, no next page, no cursor.
	last, err := NewPage([]row{{"a"}, {"b"}}, 2, plan, get)
	if err != nil {
		t.Fatalf("NewPage(last): %v", err)
	}
	if last.PageInfo.HasNextPage || !last.PageInfo.EndCursor.IsZero() {
		t.Fatalf("final page must have no next cursor, got HasNextPage=%v EndCursor=%q", last.PageInfo.HasNextPage, last.PageInfo.EndCursor)
	}
}

// No page reports a direction it has no cursor for: an empty one, whose
// cursor outlived the rows on its side, reports none, so the client starts
// again from the first page.
func TestNewPage_EveryFlagHasItsCursor(t *testing.T) {
	t.Parallel()
	type row struct{ ID string }
	keys := []CursorKey{{Name: "id", Column: "id", Direction: SortAsc}}
	get := func(r row, _ string) (any, error) { return r.ID, nil }
	plans := map[string]CursorPlan{
		"first page":                 {Keys: keys},
		"forward seek with a cursor": {Keys: keys, HasCursor: true},
		"reverse seek":               {Keys: keys, HasCursor: true, Reverse: true},
	}
	for name, plan := range plans {
		for _, rows := range [][]row{nil, {}, {{"a"}}, {{"a"}, {"b"}}, {{"a"}, {"b"}, {"c"}}} {
			page, err := NewPage(rows, 2, plan, get)
			require.NoError(t, err)
			info := page.PageInfo
			assert.Equal(t, info.HasNextPage, !info.EndCursor.IsZero(), "%s, %d rows: has_next_page iff end_cursor", name, len(rows))
			assert.Equal(t, info.HasPrevPage, !info.StartCursor.IsZero(), "%s, %d rows: has_prev_page iff start_cursor", name, len(rows))
			if len(rows) == 0 {
				assert.Equal(t, PageInfo{}, info, "%s: an empty page reports no direction", name)
			}
		}
	}
}

// A page holds at least one row: a limit below 1 is the caller's error,
// with or without rows, never a panic on the page's last row.
func TestNewPage_RefusesALimitBelowOne(t *testing.T) {
	t.Parallel()
	plan := CursorPlan{Keys: []CursorKey{{Name: "id", Column: "id", Direction: SortAsc}}}
	get := func(id string, _ string) (any, error) { return id, nil }
	for _, limit := range []int{0, -1} {
		for _, rows := range [][]string{nil, {"a"}, {"a", "b"}} {
			page, err := NewPage(rows, limit, plan, get)
			require.ErrorContains(t, err, "is below 1", "limit %d, %d rows", limit, len(rows))
			assert.Nil(t, page)
		}
	}
}

// A query that matches nothing hands NewPage nil rows; the page still
// encodes "items":[], as a client reading an array expects, beside the
// metadata an executor sets on it.
func TestNewPage_EmptyPageEncodesItemsAsAnArray(t *testing.T) {
	t.Parallel()
	type row struct {
		ID string `json:"id"`
	}
	keys := []CursorKey{{Name: "id", Column: "id", Direction: SortAsc}}
	get := func(r row, _ string) (any, error) { return r.ID, nil }
	for _, tt := range []struct {
		name string
		rows []row
		plan CursorPlan
	}{
		{"nil rows, first page", nil, CursorPlan{Keys: keys}},
		{"no rows, first page", []row{}, CursorPlan{Keys: keys}},
		{"nil rows after a forward seek", nil, CursorPlan{Keys: keys, HasCursor: true}},
		{"no rows after a reverse seek", make([]row, 0, 4), CursorPlan{Keys: keys, HasCursor: true, Reverse: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			page, err := NewPage(tt.rows, 2, tt.plan, get)
			require.NoError(t, err)
			require.NotNil(t, page.Items)
			assert.Empty(t, page.Items)

			total := int64(0)
			page.PageInfo.TotalCount = &total
			page.Facets = map[string][]FacetBucket{"name": {}}
			got, err := json.Marshal(page)
			require.NoError(t, err)
			assert.JSONEq(t, `{"items":[],"page_info":{"has_next_page":false,"has_prev_page":false,"total_count":0},"facets":{"name":[]}}`, string(got))
		})
	}
}
