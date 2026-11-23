//go:build unit

package httpx

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// only-if-cached forbids origin I/O (RFC 9111 §5.2.1.7) whatever else the
// request carries: the cache answers from storage or with a synthetic 504.
func TestCacheOnlyIfCachedNeverForwards(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		header      []string
		body        bool
		wantStored  int    // status once a fresh response is stored
		wantForward string // Cache-Status fwd reason of the 504
	}{
		{"plain", http.MethodGet, nil, false, 200, "miss"},
		{"no-store", http.MethodGet, []string{"Cache-Control", "no-store"}, false, 200, "miss"},
		{"range", http.MethodGet, []string{"Range", "bytes=0-1"}, false, 504, "bypass"},
		{"if-none-match", http.MethodGet, []string{"If-None-Match", `"v1"`}, false, 504, "bypass"},
		{"if-modified-since", http.MethodGet, []string{"If-Modified-Since", "Mon, 02 Jan 2006 15:04:05 GMT"}, false, 504, "bypass"},
		{"if-match", http.MethodGet, []string{"If-Match", `"v1"`}, false, 504, "bypass"},
		{"get with body", http.MethodGet, nil, true, 504, "bypass"},
		{"head", http.MethodHead, nil, false, 504, "method"},
		{"post", http.MethodPost, nil, true, 504, "method"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &origin{respond: func(int, *http.Request) *http.Response {
				return reply(200, "stored", "Cache-Control", "max-age=600", "ETag", `"v1"`)
			}}
			ct := newCache(t, o)
			probe := func(stage string, wantStatus int) {
				t.Helper()
				var body *trackedBody
				var reqBody io.Reader
				if tt.body {
					body = &trackedBody{Reader: strings.NewReader("{}")}
					reqBody = body
				}
				req, _ := http.NewRequest(tt.method, itemsURL, reqBody)
				req.Header.Set("Cache-Control", "only-if-cached")
				for i := 0; i+1 < len(tt.header); i += 2 {
					req.Header.Add(tt.header[i], tt.header[i+1])
				}
				before := o.count()
				resp, err := ct.RoundTrip(req)
				if err != nil {
					t.Fatalf("%s: %v", stage, err)
				}
				got, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if o.count() != before {
					t.Errorf("%s: only-if-cached reached the origin", stage)
				}
				if resp.StatusCode != wantStatus {
					t.Errorf("%s: status %d, want %d", stage, resp.StatusCode, wantStatus)
				}
				if wantStatus == 200 && string(got) != "stored" {
					t.Errorf("%s: body %q, want the stored response", stage, got)
				}
				want := "httpx; fwd=" + tt.wantForward + "; detail=only-if-cached"
				if cs := resp.Header.Get("Cache-Status"); wantStatus == http.StatusGatewayTimeout && cs != want {
					t.Errorf("%s: Cache-Status %q, want %q", stage, cs, want)
				}
				if body != nil && !body.wasClosed() {
					t.Errorf("%s: request body left open", stage)
				}
			}

			probe("empty", http.StatusGatewayTimeout)
			get(t, ct, itemsURL)
			probe("stored", tt.wantStored)
			if o.count() != 1 {
				t.Errorf("origin requests = %d, want only the storing GET", o.count())
			}
		})
	}
}

// Without only-if-cached, range and caller-conditional requests still reach
// the origin with their headers intact.
func TestCacheForwardsRangeAndConditionalsUnchanged(t *testing.T) {
	o := &origin{respond: func(int, *http.Request) *http.Response {
		return reply(200, "full", "Cache-Control", "max-age=600", "ETag", `"v1"`)
	}}
	ct := newCache(t, o)
	get(t, ct, itemsURL)
	for _, header := range [][]string{
		{"Range", "bytes=0-1"},
		{"If-None-Match", `"v1"`},
		{"If-Modified-Since", "Mon, 02 Jan 2006 15:04:05 GMT"},
	} {
		before := o.count()
		get(t, ct, itemsURL, header...)
		if o.count() != before+1 {
			t.Fatalf("%s request was not forwarded", header[0])
		}
		if got := o.last().Header.Get(header[0]); got != header[1] {
			t.Errorf("%s forwarded as %q, want %q", header[0], got, header[1])
		}
	}
}
