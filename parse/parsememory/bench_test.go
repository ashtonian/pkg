//go:build unit

package parsememory_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/parsememory"
)

// event is a row of the table the benchmarks read: as many rows as the
// default MaxRows allows, each at its own instant, of one of eight kinds,
// and paired with one of sixteen tags.
type event struct {
	ID    int64     `json:"id"    db:"id"    parse:"filter,sort"`
	At    time.Time `json:"at"    db:"at"    parse:"filter,sort"`
	Kind  string    `json:"kind"  db:"kind"  parse:"filter,facet,patch" facet:"terms"`
	Score float64   `json:"score" db:"score" parse:"filter,sort"`
	Tags  []string  `json:"tags"  db:"-"     parse:"filter,include" relation:"event_tags,event_id,tag_id,id"`
}

const benchRows = 100_000

// openEvents is a database at the default MaxRows holding benchRows
// events, and the resource on it.
func openEvents(b *testing.B) (*parsememory.Database, *parse.Resource[event]) {
	b.Helper()
	db, err := parsememory.Open(b.Context(), parsememory.Config{MaxRows: benchRows})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		err := db.Close()
		if err != nil {
			b.Error(err)
		}
	})
	r, err := parse.NewResource[event](db.Provider(), "events")
	if err != nil {
		b.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := make([]parsememory.Row, benchRows)
	for i := range rows {
		// 7919 is prime to benchRows, so the instants and scores are a
		// permutation of the ids'.
		at := start.Add(time.Duration((i*7919)%benchRows) * time.Second)
		rows[i] = parsememory.Row{"id": int64(i), "at": at, "kind": fmt.Sprintf("k%d", i%8), "score": float64((i*7919)%benchRows) / 7}
	}
	err = db.Put(b.Context(), "events", rows...)
	if err != nil {
		b.Fatal(err)
	}
	pairs := make([]parsememory.Row, benchRows)
	for i := range pairs {
		pairs[i] = parsememory.Row{"event_id": int64(i), "tag_id": fmt.Sprintf("t%d", i%16)}
	}
	err = db.Put(b.Context(), "event_tags", pairs...)
	if err != nil {
		b.Fatal(err)
	}
	return db, r
}

// BenchmarkList lists a table of benchRows rows: each list is one pass
// over the rows the filter selects, its page a top-k selection of the
// page size plus one, so the cost is linear in the rows selected and the
// page's memory bounded by its size, whatever the sort. An include reads
// each page row's pairs by a range of the pair table's key, in O(log n).
func BenchmarkList(b *testing.B) {
	_, r := openEvents(b)
	first, err := r.ParseListInput(parse.ListInput{Sort: "-at", PageSize: 50})
	if err != nil {
		b.Fatal(err)
	}
	page, err := r.List(b.Context(), first, parse.Scope{})
	if err != nil {
		b.Fatal(err)
	}
	// A cursor nine tenths of the way through the table in key order.
	deep, err := r.ParseListInput(parse.ListInput{Filter: `{"id":{"$gte":90000}}`, PageSize: 50})
	if err != nil {
		b.Fatal(err)
	}
	deepPage, err := r.List(b.Context(), deep, parse.Scope{})
	if err != nil {
		b.Fatal(err)
	}
	lists := []struct {
		name  string
		input parse.ListInput
	}{
		{"key order", parse.ListInput{PageSize: 50}},
		{"key order past a cursor", parse.ListInput{PageSize: 50, Cursor: string(deepPage.PageInfo.EndCursor)}},
		{"key order with its count", parse.ListInput{PageSize: 50, IncludeTotal: true}},
		{"timestamp", parse.ListInput{Sort: "-at", PageSize: 50}},
		{"timestamp past a cursor", parse.ListInput{Sort: "-at", PageSize: 50, Cursor: string(page.PageInfo.EndCursor)}},
		{"float", parse.ListInput{Sort: "score", PageSize: 50}},
		{"filtered with its count", parse.ListInput{Filter: `{"kind":"k3"}`, Sort: "-at", PageSize: 50, IncludeTotal: true}},
		{"terms facet", parse.ListInput{Sort: "-at", PageSize: 50, Facets: []string{"kind"}}},
		{"key order with an include", parse.ListInput{PageSize: 50, Include: []string{"tags"}}},
	}
	for _, l := range lists {
		req, err := r.ParseListInput(l.input)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(l.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, err := r.List(b.Context(), req, parse.Scope{})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkWrite writes one row of a table of benchRows rows: a write
// copies the table's tree in O(1) and changes it in O(log n), whatever the
// table's size.
func BenchmarkWrite(b *testing.B) {
	db, r := openEvents(b)
	at := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	b.Run("put", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			err := db.Put(b.Context(), "events", parsememory.Row{"id": int64(i % benchRows), "at": at, "kind": "k0", "score": 0.5})
			if err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
	p, err := r.ParseMergePatch([]byte(`{"kind":"k9"}`))
	if err != nil {
		b.Fatal(err)
	}
	b.Run("update by key", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			_, err := r.Update(b.Context(), p, parse.ByID(int64(i%benchRows)), parse.Scope{})
			if err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}
