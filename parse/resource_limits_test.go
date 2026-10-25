//go:build unit

package parse_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

// limitsDevice exercises every knob-relevant surface: filterable +
// patchable fields, a timestamp field for clock threading.
type limitsDevice struct {
	ID        string    `json:"id"         parse:"filter"       filter_ops:"eq,in"`
	Name      string    `json:"name"       parse:"filter,patch" filter_ops:"eq,like" patch_ops:"replace"`
	Status    string    `json:"status"     parse:"filter,patch" filter_ops:"eq,ne"   patch_ops:"replace"`
	Count     int       `json:"count"      parse:"filter"       filter_ops:"eq,gt,lt,in"`
	CreatedAt time.Time `json:"created_at" parse:"filter"       filter_ops:"eq,gt,lt"`
}

// countingCodec wraps JSON and records Unmarshal calls so tests can assert the
// configured codec is actually used by Resource entry points.
type countingCodec struct {
	unmarshals *int
}

func (c countingCodec) Encode(v any) ([]byte, error) { return json.Marshal(v) }
func (c countingCodec) Decode(data []byte, v any) error {
	*c.unmarshals++
	return json.Unmarshal(data, v)
}

// TestLimitsThreadThroughResource asserts every core.WithX limit passed
// to NewResource is enforced by the parse entry points. Before
// the sub-parsers were constructed with a nil config, silently substituting
// defaults for all of these.
func TestLimitsThreadThroughResource(t *testing.T) {
	t.Run("MaxConditions", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice]("limits_devices", parse.ParserOptions(core.WithMaxConditions(1)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = tp.ParseFilter(`{"status": "active", "name": "x", "count": 3}`)
		if err == nil {
			t.Fatal("3-condition filter accepted with WithMaxConditions(1)")
		}
		if !strings.Contains(err.Error(), "condition") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("MaxInputSize filter", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice]("limits_devices", parse.ParserOptions(core.WithMaxInputSize(4)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tp.ParseFilter(`{"status": "active"}`); err == nil {
			t.Fatal("20-byte filter accepted with WithMaxInputSize(4)")
		}
	})

	t.Run("MaxInputSize patch", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice]("limits_devices", parse.ParserOptions(core.WithMaxInputSize(4)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tp.ParsePatch([]byte(`[{"op":"replace","path":"/name","value":"x"}]`)); err == nil {
			t.Fatal("41-byte patch accepted with WithMaxInputSize(4)")
		}
	})

	t.Run("MaxInputSize merge patch", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice]("limits_devices", parse.ParserOptions(core.WithMaxInputSize(4)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tp.ParseMergePatch([]byte(`{"name": "renamed"}`)); err == nil {
			t.Fatal("19-byte merge patch accepted with WithMaxInputSize(4)")
		}
	})

	t.Run("MaxDepth", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice]("limits_devices", parse.ParserOptions(core.WithMaxDepth(1)))
		if err != nil {
			t.Fatal(err)
		}
		deep := `{"$or": [{"$and": [{"$or": [{"status": "active"}]}]}]}`
		if _, err = tp.ParseFilter(deep); err == nil {
			t.Fatal("depth-3 filter accepted with WithMaxDepth(1)")
		}
	})

	t.Run("MaxArraySize", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice]("limits_devices", parse.ParserOptions(core.WithMaxArraySize(2)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tp.ParseFilter(`{"count": {"$in": [1, 2, 3]}}`); err == nil {
			t.Fatal("3-element $in accepted with WithMaxArraySize(2)")
		}
	})

	t.Run("MaxCost", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice]("limits_devices", parse.ParserOptions(core.WithMaxCost(1)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tp.ParseFilter(`{"name": {"$like": "%a%"}, "count": {"$gt": 1}}`); err == nil {
			t.Fatal("multi-condition LIKE filter accepted with WithMaxCost(1)")
		}
	})

	t.Run("Codec used by ParseFilter", func(t *testing.T) {
		calls := 0
		tp, err := parse.NewResource[limitsDevice]("limits_devices", parse.ParserOptions(core.WithCodec(countingCodec{unmarshals: &calls})))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tp.ParseFilter(`{"status": "active"}`); err != nil {
			t.Fatal(err)
		}
		if calls == 0 {
			t.Fatal("configured codec's Unmarshal never called by ParseFilter")
		}
	})

	t.Run("Codec used by ParsePatch", func(t *testing.T) {
		calls := 0
		tp, err := parse.NewResource[limitsDevice]("limits_devices", parse.ParserOptions(core.WithCodec(countingCodec{unmarshals: &calls})))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tp.ParsePatch([]byte(`[{"op":"replace","path":"/name","value":"x"}]`)); err != nil {
			t.Fatal(err)
		}
		if calls == 0 {
			t.Fatal("configured codec's Unmarshal never called by ParsePatch")
		}
	})
}

// TestClockThreadsThroughResource asserts core.WithClock pins the
// relative-time anchor for `now±N` tokens through the primary API.
func TestClockThreadsThroughResource(t *testing.T) {
	frozen := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	tp, err := parse.NewResource[limitsDevice]("limits_devices", parse.ParserOptions(core.WithClock(func() time.Time { return frozen })))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := tp.ParseFilter(`{"created_at": {"$gt": "now-7d"}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := core.FormatTimestamp(frozen.Add(-7 * 24 * time.Hour))
	root := parsed.AST.Root
	if root == nil || root.Value != want {
		t.Fatalf("resolved value = %v; want %s", root.Value, want)
	}
}
