package client

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// The server's list parameters (parse/filter's Param constants), held equal
// to them by parse's tests.
const (
	paramFilter         = "filter"
	paramSort           = "sort"
	paramCursor         = "cursor"
	paramPageSize       = "page_size"
	paramIncludeTotal   = "include_total"
	paramIncludeDeleted = "include_deleted"
	paramSearch         = "search"
	paramFacets         = "facets"
	paramFields         = "fields"
	paramInclude        = "include"
)

// ListParams is one list request: what parse's Resource.ParseList reads from
// a query string. Zero members are left out, so the server applies its
// defaults.
type ListParams struct {
	Filter Filter
	Sort   Sort
	// Cursor is the end_cursor of the page before the one to fetch; empty
	// fetches the first page.
	Cursor string
	// PageSize is the number of rows per page; 0 is the server's default.
	PageSize int
	// IncludeTotal asks for the number of matching rows.
	IncludeTotal bool
	// IncludeDeleted lists soft-deleted rows too.
	IncludeDeleted bool
	// Search is free text matched against the resource's search fields.
	Search string
	// Facets names the fields to count values of.
	Facets []string
	// Include names the relations to return with each row.
	Include []string
	// Fields names the members each row returns.
	Fields []string
}

// Values encodes p as query parameters. It fails only when the filter
// cannot be encoded (Filter.Err).
func (p ListParams) Values() (url.Values, error) {
	q := url.Values{}
	if !p.Filter.IsZero() {
		doc, err := json.Marshal(p.Filter)
		if err != nil {
			return nil, fmt.Errorf("client: encode filter: %w", err)
		}
		q.Set(paramFilter, string(doc))
	}
	setNonEmpty(q, paramSort, p.Sort.String())
	setNonEmpty(q, paramCursor, p.Cursor)
	if p.PageSize != 0 {
		q.Set(paramPageSize, strconv.Itoa(p.PageSize))
	}
	if p.IncludeTotal {
		q.Set(paramIncludeTotal, "true")
	}
	if p.IncludeDeleted {
		q.Set(paramIncludeDeleted, "true")
	}
	setNonEmpty(q, paramSearch, p.Search)
	setNonEmpty(q, paramFacets, strings.Join(p.Facets, ","))
	setNonEmpty(q, paramInclude, strings.Join(p.Include, ","))
	setNonEmpty(q, paramFields, strings.Join(p.Fields, ","))
	return q, nil
}

func setNonEmpty(q url.Values, name, value string) {
	if value != "" {
		q.Set(name, value)
	}
}
