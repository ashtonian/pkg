package client

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// The server's list parameters (parse/filter's Param constants), held equal
// to the names parse records in its testdata/sdk_wire.json by this module's
// tests.
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
	// Cursor is a cursor from an earlier page's page_info: its end_cursor
	// fetches the page after it, its start_cursor the page before it.
	// Empty fetches the first page.
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
	// Fields names the members each row returns, the response field mask. A
	// name that is no field of the response is E2071. The server applies
	// the mask to the rows it writes; its query reads every column either way.
	Fields []string
}

// Values encodes p as query parameters. It fails only with the filter's
// error (Filter.Err).
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

// Body encodes p as a list body: the JSON document a server reads from a
// POST (parse's Resource.ParseListBody), for a request too large for a URL.
// Its members are the query parameters Values sets, with the filter as a
// document and the name lists as arrays; zero members are left out. It
// fails only with the filter's error (Filter.Err).
func (p ListParams) Body() ([]byte, error) {
	var b struct {
		Filter         json.RawMessage `json:"filter,omitempty"`
		Sort           string          `json:"sort,omitempty"`
		Cursor         string          `json:"cursor,omitempty"`
		PageSize       int             `json:"page_size,omitempty"`
		IncludeTotal   bool            `json:"include_total,omitempty"`
		IncludeDeleted bool            `json:"include_deleted,omitempty"`
		Search         string          `json:"search,omitempty"`
		Facets         []string        `json:"facets,omitempty"`
		Include        []string        `json:"include,omitempty"`
		Fields         []string        `json:"fields,omitempty"`
	}
	if !p.Filter.IsZero() {
		doc, err := json.Marshal(p.Filter)
		if err != nil {
			return nil, fmt.Errorf("client: encode filter: %w", err)
		}
		b.Filter = doc
	}
	b.Sort, b.Cursor, b.PageSize = p.Sort.String(), p.Cursor, p.PageSize
	b.IncludeTotal, b.IncludeDeleted, b.Search = p.IncludeTotal, p.IncludeDeleted, p.Search
	b.Facets, b.Include, b.Fields = p.Facets, p.Include, p.Fields
	return json.Marshal(b)
}

func setNonEmpty(q url.Values, name, value string) {
	if value != "" {
		q.Set(name, value)
	}
}
