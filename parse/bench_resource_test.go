//go:build unit

package parse_test

import (
	"net/url"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

type benchWidget struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,sort,patch,search"`
	Status    string     `json:"status"     db:"status"     parse:"filter,patch,facet" enum:"active,inactive"`
	Rank      int64      `json:"rank"       db:"rank"       parse:"filter,sort,patch"`
	CreatedAt time.Time  `json:"created_at" db:"created_at" parse:"filter,sort"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
}

func benchWidgetResource(b *testing.B) *parse.Resource[benchWidget] {
	b.Helper()
	r, err := parse.NewResource[benchWidget]("widgets", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	if err != nil {
		b.Fatal(err)
	}
	return r
}

// BenchmarkNewResource is the boot cost of one resource: every schema, the
// columns and the parser for its type.
func BenchmarkNewResource(b *testing.B) {
	for b.Loop() {
		if _, err := parse.NewResource[benchWidget]("widgets", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at")); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkResourcePage mints a 25-row page's cursors from its rows, which
// every list request does.
func BenchmarkResourcePage(b *testing.B) {
	r := benchWidgetResource(b)
	req, err := r.ParseList(url.Values{"sort": {"-rank,name"}, "page_size": {"25"}})
	if err != nil {
		b.Fatal(err)
	}
	plan, err := r.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: "t1"})
	if err != nil {
		b.Fatal(err)
	}
	rows := make([]benchWidget, 26)
	for i := range rows {
		rows[i] = benchWidget{ID: "w" + string(rune('a'+i)), TenantID: "t1", Name: "widget", Rank: int64(100 - i)}
	}
	page, err := r.Page(rows, plan)
	if err != nil || len(page.Items) != 25 || page.PageInfo.EndCursor == "" {
		b.Fatalf("page: %+v, %v", page, err)
	}
	for b.Loop() {
		if _, err := r.Page(rows, plan); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkResourceKey reads a row's primary key.
func BenchmarkResourceKey(b *testing.B) {
	r := benchWidgetResource(b)
	row := benchWidget{ID: "w1", Name: "widget"}
	if k, err := r.Key(row); err != nil || k != "w1" {
		b.Fatalf("key: %v, %v", k, err)
	}
	for b.Loop() {
		if _, err := r.Key(row); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseList parses a list request: a JSON filter, a flat filter,
// a sort, a page size and the total.
func BenchmarkParseList(b *testing.B) {
	r := benchWidgetResource(b)
	q := url.Values{
		"filter":        {`{"status":"active","rank":{"$gte":10}}`},
		"name_ilike":    {"%pump%"},
		"sort":          {"-rank,name"},
		"page_size":     {"25"},
		"include_total": {"true"},
	}
	if req, err := r.ParseList(q); err != nil || req.Filter == nil {
		b.Fatalf("parse: %+v, %v", req, err)
	}
	for b.Loop() {
		if _, err := r.ParseList(q); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseMergePatch parses a typed JSON Merge Patch request body.
func BenchmarkParseMergePatch(b *testing.B) {
	r := benchWidgetResource(b)
	body := []byte(`{"name":"pump","status":"inactive","rank":7}`)
	if p, err := r.ParseMergePatch(body); err != nil || len(p.Operations) != 3 {
		b.Fatalf("parse: %+v, %v", p, err)
	}
	for b.Loop() {
		if _, err := r.ParseMergePatch(body); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseJSONPatch parses a JSON Patch request body.
func BenchmarkParseJSONPatch(b *testing.B) {
	r := benchWidgetResource(b)
	body := []byte(`[{"op":"replace","path":"/name","value":"pump"},{"op":"replace","path":"/rank","value":7}]`)
	if p, err := r.ParsePatch(body); err != nil || len(p.Operations) != 2 {
		b.Fatalf("parse: %+v, %v", p, err)
	}
	for b.Loop() {
		if _, err := r.ParsePatch(body); err != nil {
			b.Fatal(err)
		}
	}
}
