package filtertest

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// Window is a nested struct member of Settings.
type Window struct {
	From time.Time `json:"from" parse:"filter"`
}

// Settings is a typed (struct) document; its tagged members are paths
// settings.mode, settings.level, settings.window.from.
type Settings struct {
	Mode   string  `json:"mode"   parse:"filter,facet" filter_ops:"eq,ne,gt,lt,in,null,exists"`
	Level  int64   `json:"level"  parse:"filter,sort,facet" facet:"range:0-5,5+"`
	Window *Window `json:"window" parse:"filter"`
	Note   string  `json:"note"`
}

// PathRecord holds two documents: Attrs, a map with declared paths, and
// Settings, a struct whose members are paths. Its table is "path_records".
type PathRecord struct {
	ID       string          `json:"id"       db:"id"       parse:"filter" filter_ops:"eq,in"`
	Attrs    json.RawMessage `json:"attrs"    db:"attrs"    parse:"filter" paths:"color:string:filter+sort+facet;size:int:filter+sort;ratio:float;price:decimal:filter+sort+facet;on:bool:facet;seen:timestamp:filter+sort;owner:uuid;dims.w:float"`
	Settings *Settings       `json:"settings" db:"settings" parse:"filter"`
}

// PathRecords is the path fixture. Rows c, d, f and h hold no typed member
// at any attrs path: c's members have other JSON kinds or shapes (its price
// a string in exponent notation), d's attrs is NULL, f's is empty and h's
// is an array; c's timestamp carries an offset. e's color is JSON null, its
// size is above 2^53 and its timestamp has a short fraction; g's size is a
// decimal, its color upper-case and its owner an upper-case UUID. a's
// timestamp is written as encoding/json writes UTC. Prices are JSON numbers
// (a, e) or strings in plain notation (b with a scale, g), as a decimal
// encodes.
func PathRecords() []PathRecord {
	return []PathRecord{
		{ID: "a", Attrs: json.RawMessage(`{"color":"red","size":3,"ratio":1.5,"price":10.25,"on":true,"seen":"2026-01-02T03:04:05Z","owner":"0198a3c2-7a1e-7c00-8000-000000000001","dims":{"w":2.5}}`),
			Settings: &Settings{Mode: "auto", Level: 2, Window: &Window{From: Base}}},
		{ID: "b", Attrs: json.RawMessage(`{"color":"blue","size":10,"ratio":2,"price":"9.50","on":false,"seen":"2026-01-02T03:04:06.000000000Z","owner":"0198a3c2-7a1e-7c00-8000-000000000002","dims":{"w":10}}`),
			Settings: &Settings{Mode: "manual", Level: 5}},
		{ID: "c", Attrs: json.RawMessage(`{"color":3,"size":"3","ratio":"1.5","price":"1e3","on":"true","seen":"2026-01-02T05:04:05+02:00","owner":"not-a-uuid","dims":5}`)},
		{ID: "d"},
		{ID: "e", Attrs: json.RawMessage(`{"color":null,"size":9007199254740993,"ratio":0.1,"price":-1,"seen":"2026-01-02T03:04:05.5Z"}`), Settings: &Settings{}},
		{ID: "f", Attrs: json.RawMessage(`{}`)},
		{ID: "g", Attrs: json.RawMessage(`{"color":"Red","size":3.5,"ratio":-1,"price":"100","on":true,"owner":"0198A3C2-7A1E-7C00-8000-000000000003"}`),
			Settings: &Settings{Mode: "auto", Level: 7, Window: &Window{From: Base.Add(time.Hour)}}},
		{ID: "h", Attrs: json.RawMessage(`["color","size"]`)},
	}
}

// PathOrders are the presentation orders path sorts give PathRecords: an
// absent member sorts as NULL (greatest), ties on id in the primary
// direction.
var PathOrders = map[string]string{
	"attrs.size":      "agbecdfh",
	"-attrs.size":     "hfdcebga",
	"attrs.seen":      "aebcdfgh",
	"-attrs.seen":     "hgfdcbea",
	"attrs.color":     "gbacdefh",
	"settings.level":  "eabgcdfh",
	"-settings.level": "hfdcgbae",
	"attrs.price":     "ebagcdfh",
	"-attrs.price":    "hfdcgabe",
}

// PathFacets are the buckets path facets count over PathRecords; an absent
// member is in no bucket.
var PathFacets = map[string]map[string]int64{
	"attrs.color":    {"red": 1, "blue": 1, "Red": 1},
	"attrs.on":       {"true": 2, "false": 1},
	"settings.mode":  {"auto": 2, "manual": 1, "": 1},
	"settings.level": {"0-5": 2, "5+": 2},
	"attrs.price":    {"10.25": 1, "9.5": 1, "-1": 1, "100": 1},
}

// PathCases are the path filters and the rows each matches. An absent
// member is NULL: no comparison but the NULL tests matches it.
func PathCases() []Case {
	return []Case{
		{Name: "path string eq", Filter: `{"attrs.color":"red"}`, Want: ids("a")},
		{Name: "path string eq is case-sensitive", Filter: `{"attrs.color":"RED"}`, Want: ids("")},
		{Name: "path string ne excludes absent", Filter: `{"attrs.color":{"$ne":"red"}}`, Want: ids("bg")},
		{Name: "path string in", Filter: `{"attrs.color":{"$in":["red","blue"]}}`, Want: ids("ab")},
		{Name: "path string nin excludes absent", Filter: `{"attrs.color":{"$nin":["red"]}}`, Want: ids("bg")},
		{Name: "path string like", Filter: `{"attrs.color":{"$like":"%e%"}}`, Want: ids("abg")},
		{Name: "path string ilike", Filter: `{"attrs.color":{"$ilike":"r%"}}`, Want: ids("ag")},
		{Name: "path null true is every absent member", Filter: `{"attrs.color":{"$null":true}}`, Want: ids("cdefh")},
		{Name: "path exists", Filter: `{"attrs.color":{"$exists":true}}`, Want: ids("abg")},
		{Name: "path not excludes absent", Filter: `{"$not":{"attrs.color":"red"}}`, Want: ids("bg")},

		{Name: "path int eq", Filter: `{"attrs.size":3}`, Want: ids("a")},
		{Name: "path int eq above 2^53", Filter: `{"attrs.size":9007199254740993}`, Want: ids("e")},
		{Name: "path int gt above 2^53", Filter: `{"attrs.size":{"$gt":9007199254740992}}`, Want: ids("e")},
		{Name: "path int gt compares a decimal member", Filter: `{"attrs.size":{"$gt":3}}`, Want: ids("beg")},
		{Name: "path int lt", Filter: `{"attrs.size":{"$lt":4}}`, Want: ids("ag")},
		{Name: "path int in", Filter: `{"attrs.size":{"$in":[3,10]}}`, Want: ids("ab")},
		{Name: "path int null", Filter: `{"attrs.size":{"$null":true}}`, Want: ids("cdfh")},
		{Name: "path int fraction rejected", Filter: `{"attrs.size":1.5}`, ParseErr: "E2073"},

		{Name: "path float eq integer member", Filter: `{"attrs.ratio":2}`, Want: ids("b")},
		{Name: "path float eq", Filter: `{"attrs.ratio":1.5}`, Want: ids("a")},
		{Name: "path float lt", Filter: `{"attrs.ratio":{"$lt":1}}`, Want: ids("eg")},
		{Name: "path float gte", Filter: `{"attrs.ratio":{"$gte":0.1}}`, Want: ids("abe")},

		{Name: "path decimal eq number member", Filter: `{"attrs.price":"10.25"}`, Want: ids("a")},
		{Name: "path decimal eq string member ignores scale", Filter: `{"attrs.price":9.5}`, Want: ids("b")},
		{Name: "path decimal gt orders by value", Filter: `{"attrs.price":{"$gt":"9.5"}}`, Want: ids("ag")},
		{Name: "path decimal lt", Filter: `{"attrs.price":{"$lt":10}}`, Want: ids("be")},
		{Name: "path decimal in", Filter: `{"attrs.price":{"$in":[-1,"1E2"]}}`, Want: ids("eg")},
		{Name: "path decimal exponent string is absent", Filter: `{"attrs.price":{"$null":true}}`, Want: ids("cdfh")},
		{Name: "path decimal invalid request", Filter: `{"attrs.price":"cheap"}`, ParseErr: "E2073"},

		{Name: "path bool eq true", Filter: `{"attrs.on":true}`, Want: ids("ag")},
		{Name: "path bool eq false", Filter: `{"attrs.on":false}`, Want: ids("b")},
		{Name: "path bool ne excludes absent", Filter: `{"attrs.on":{"$ne":true}}`, Want: ids("b")},

		{Name: "path timestamp eq", Filter: `{"attrs.seen":"2026-01-02T03:04:05Z"}`, Want: ids("a")},
		{Name: "path timestamp eq with a fraction", Filter: `{"attrs.seen":"2026-01-02T03:04:05.500Z"}`, Want: ids("e")},
		{Name: "path timestamp gt orders padded fractions", Filter: `{"attrs.seen":{"$gt":"2026-01-02T03:04:05Z"}}`, Want: ids("be")},
		{Name: "path timestamp with an offset is absent", Filter: `{"attrs.seen":{"$null":true}}`, Want: ids("cdfgh")},
		{Name: "path timestamp relative token", Filter: `{"attrs.seen":{"$lt":"now"}}`, Want: ids("abe")},

		{Name: "path uuid eq", Filter: `{"attrs.owner":"0198a3c2-7a1e-7c00-8000-000000000002"}`, Want: ids("b")},
		{Name: "path uuid eq folds the member's case", Filter: `{"attrs.owner":"0198a3c2-7a1e-7c00-8000-000000000003"}`, Want: ids("g")},
		{Name: "path uuid malformed member is absent", Filter: `{"attrs.owner":{"$null":true}}`, Want: ids("cdefh")},
		{Name: "path uuid invalid request", Filter: `{"attrs.owner":"nope"}`, ParseErr: "E2116"},
		{Name: "path string default operators exclude ordering", Filter: `{"attrs.color":{"$gt":"a"}}`, ParseErr: "E2072"},

		{Name: "nested path", Filter: `{"attrs.dims.w":{"$gt":5}}`, Want: ids("b")},
		{Name: "nested path through a scalar is absent", Filter: `{"attrs.dims.w":{"$null":true}}`, Want: ids("cdefgh")},

		{Name: "struct member eq", Filter: `{"settings.mode":"auto"}`, Want: ids("ag")},
		{Name: "struct member int", Filter: `{"settings.level":{"$gte":5}}`, Want: ids("bg")},
		{Name: "struct member gt code point", Filter: `{"settings.mode":{"$gt":"auto"}}`, Want: ids("b")},
		{Name: "struct member lt orders the empty string first", Filter: `{"settings.mode":{"$lt":"auto"}}`, Want: ids("e")},
		{Name: "struct zero member is present", Filter: `{"settings.level":0}`, Want: ids("e")},
		{Name: "nested struct member", Filter: `{"settings.window.from":{"$gt":"2026-01-02T03:04:05Z"}}`, Want: ids("g")},
		{Name: "struct null document", Filter: `{"settings.mode":{"$null":true}}`, Want: ids("cdfh")},
		{Name: "untagged struct member is not a path", Filter: `{"settings.note":"x"}`, ParseErr: "E2071"},
		{Name: "undeclared map path", Filter: `{"attrs.shade":"x"}`, ParseErr: "E2071"},

		{Name: "paths compose", Filter: `{"$or":[{"attrs.color":"blue"},{"settings.level":7}]}`, Want: ids("bg")},

		{Name: "has_key: object keys and array string elements", Filter: `{"attrs":{"$has_key":"color"}}`, Want: ids("abcegh")},
		{Name: "has_key: a key whose value is null", Filter: `{"attrs":{"$has_key":"size"}}`, Want: ids("abcegh")},
		{Name: "has_key: nested keys are not top-level", Filter: `{"attrs":{"$has_key":"w"}}`, Want: ids("")},
		{Name: "has_key: not excludes a NULL document", Filter: `{"$not":{"attrs":{"$has_key":"color"}}}`, Want: ids("f")},
		{Name: "has_key requires a string", Filter: `{"attrs":{"$has_key":3}}`, ParseErr: "E2073"},
	}
}

// PathParser returns a filter parser over schema — PathSchema, or a copy
// declaring another storage — with its clock pinned to Now.
func PathParser(schema *filter.ResourceSchema) *filter.FilterParser {
	cfg := core.DefaultConfig()
	cfg.Clock = Now
	return filter.NewFilterParser(schema, cfg)
}

// PathSchema returns PathRecord's generated filter schema.
func PathSchema() (*filter.ResourceSchema, error) {
	return filter.GenerateFilterSchema[PathRecord](core.NewSchemaGenerator())
}

// RunPaths evaluates every path case, parsed against schema (PathSchema or a
// copy declaring the backend's storage), on backend, which evaluates against
// PathRecords.
func RunPaths(t *testing.T, schema *filter.ResourceSchema, backend Backend) {
	parser := PathParser(schema)
	for _, c := range PathCases() {
		t.Run(c.Name, func(t *testing.T) {
			ast, err := parser.Parse(c.Filter)
			if c.ParseErr != "" {
				var pe *core.ParseError
				if !errors.As(err, &pe) || pe.Code != c.ParseErr {
					t.Fatalf("parse %s: err = %v, want %s", c.Filter, err, c.ParseErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse %s: %v", c.Filter, err)
			}
			got, err := backend(t, filter.NewParsedFilter(ast))
			if err != nil {
				t.Fatalf("%s: %v", c.Filter, err)
			}
			slices.Sort(got)
			want := c.Want
			if want == nil {
				want = []string{}
			}
			if got == nil {
				got = []string{}
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s:\n got %v\nwant %v", c.Filter, got, want)
			}
		})
	}
}
