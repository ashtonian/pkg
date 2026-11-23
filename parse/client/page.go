package client

import "encoding/json"

// Page is a list answer, as parse's handlers write it: a page of items in
// the order the request sorted them, the page's position among the matched
// rows, and the includes and facet counts the request asked for. T is the
// item: the resource's row type, a narrower one for a field mask (each item
// then holds the masked members only), or json.RawMessage.
type Page[T any] struct {
	// Items are the page's rows. A list that matches nothing answers [].
	Items    []T      `json:"items"`
	PageInfo PageInfo `json:"page_info"`
	// Relations are the keys of the related rows the request's include
	// named: by relation, then by the owning row's key text.
	Relations map[string]map[string][]string `json:"_relations,omitempty"`
	// Truncated names, by relation, the owners whose related keys were cut
	// at the server's per-owner cap.
	Truncated map[string][]string `json:"_truncated,omitempty"`
	// Facets are the matched rows' counts by each field the request's
	// facets named.
	Facets map[string][]FacetBucket `json:"facets,omitempty"`
}

// MarshalJSON writes Items as [] when it is nil, as the server does.
func (p Page[T]) MarshalJSON() ([]byte, error) {
	type page Page[T]
	if p.Items == nil {
		p.Items = []T{}
	}
	return json.Marshal(page(p))
}

// PageInfo is a page's position among the matched rows.
type PageInfo struct {
	HasNextPage bool `json:"has_next_page"`
	HasPrevPage bool `json:"has_prev_page"`
	// StartCursor, sent as ListParams.Cursor, fetches the page before this
	// one; it is set when HasPrevPage is.
	StartCursor string `json:"start_cursor,omitempty"`
	// EndCursor, sent as ListParams.Cursor, fetches the page after this
	// one; it is set when HasNextPage is.
	EndCursor string `json:"end_cursor,omitempty"`
	// TotalCount is the number of matched rows, when the request set
	// IncludeTotal.
	TotalCount *int64 `json:"total_count,omitempty"`
}

// FacetBucket is one value of a faceted field and the number of matched
// rows that hold it. A range facet's Key is the range's name.
type FacetBucket struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}
