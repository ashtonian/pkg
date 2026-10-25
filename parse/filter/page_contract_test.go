//go:build unit

package filter

import (
	"encoding/json"
	"strings"
	"testing"
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
	plan := CursorPlan{Keys: []CursorKey{{Name: "id", SQLExpr: "id", Column: "id", Direction: SortAsc}}}
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
