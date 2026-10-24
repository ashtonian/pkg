package filter

// Cursor is an opaque position in a paginated result set.
type Cursor string

// IsZero reports whether the cursor is empty.
func (c Cursor) IsZero() bool { return c == "" }

// PageInfo describes a page's position in the matched result set.
type PageInfo struct {
	HasNextPage bool   `json:"has_next_page"`
	HasPrevPage bool   `json:"has_prev_page"`
	StartCursor Cursor `json:"start_cursor,omitempty"`
	EndCursor   Cursor `json:"end_cursor,omitempty"`
	// TotalCount is populated only when requested.
	TotalCount *int64 `json:"total_count,omitempty"`
}
