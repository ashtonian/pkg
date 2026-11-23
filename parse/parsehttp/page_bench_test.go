//go:build unit

package parsehttp_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

type benchDevice struct {
	ID        string            `json:"id"         db:"id"         parse:"filter,sort"`
	Name      string            `json:"name"       db:"name"       parse:"filter,sort"`
	Status    string            `json:"status"     db:"status"     parse:"filter"`
	Model     string            `json:"model"      db:"model"`
	Firmware  string            `json:"firmware"   db:"firmware"`
	Rank      int64             `json:"rank"       db:"rank"`
	Labels    map[string]string `json:"labels"     db:"labels"`
	CreatedAt time.Time         `json:"created_at" db:"created_at" parse:"filter,sort"`
}

// discard is a ResponseWriter that keeps nothing, so the benchmark measures
// encoding and masking alone.
type discard struct{ header http.Header }

func (d *discard) Header() http.Header         { return d.header }
func (d *discard) Write(p []byte) (int, error) { return len(p), nil }
func (d *discard) WriteHeader(int)             {}

// BenchmarkWriteList writes a 100-item page as a list answers it: whole,
// and narrowed to two of its eight members.
func BenchmarkWriteList(b *testing.B) {
	res, err := parse.NewResource[benchDevice]("devices")
	if err != nil {
		b.Fatal(err)
	}
	created := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	page := &filter.Page[benchDevice]{PageInfo: filter.PageInfo{HasNextPage: true, EndCursor: "gqFnzgAAAAOhcIGhZpKDoW6kbmFtZQ"}}
	for i := range 100 {
		n := strconv.Itoa(i)
		page.Items = append(page.Items, benchDevice{
			ID: "dev-" + n, Name: "device " + n, Status: "active", Model: "m-200", Firmware: "4.2." + n,
			Rank: int64(i), Labels: map[string]string{"site": "north", "rack": n}, CreatedAt: created.Add(time.Duration(i) * time.Minute),
		})
	}
	r := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/devices", nil)
	for _, bb := range []struct {
		name string
		q    url.Values
	}{
		{"whole", url.Values{}},
		{"masked", url.Values{filter.ParamFields: {"id,name"}}},
	} {
		req, err := res.ParseList(bb.q)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(bb.name, func(b *testing.B) {
			w := &discard{header: http.Header{}}
			b.ReportAllocs()
			for b.Loop() {
				err := parsehttp.WriteList(w, r, req, page)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
