//go:build unit

package parse_test

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type limitedDoc struct {
	ID   string         `json:"id"   db:"id"   parse:"filter"`
	Name string         `json:"name" db:"name" parse:"filter,patch"`
	Doc  map[string]any `json:"doc"  db:"doc"  parse:"patch"`
}

// nestedFilter nests a condition in levels $and groups.
func nestedFilter(levels int) string {
	f := `{"name":"a"}`
	for range levels {
		f = `{"$and":[` + f + `,{"name":"b"}]}`
	}
	return f
}

// nestedMergePatch is a merge patch whose deepest member is levels deep.
func nestedMergePatch(levels int) string {
	if levels == 1 {
		return `{"name":"x"}`
	}
	v := "1"
	for range levels - 1 {
		v = `{"k":` + v + `}`
	}
	return `{"doc":` + v + `}`
}

// conditions is a filter of n leaf conditions.
func conditions(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"name":"v%d"}`, i)
	}
	return `{"$or":[` + strings.Join(parts, ",") + `]}`
}

func inSet(n int) string {
	values := make([]string, n)
	for i := range values {
		values[i] = fmt.Sprintf(`"v%d"`, i)
	}
	return `{"name":{"$in":[` + strings.Join(values, ",") + `]}}`
}

// The parser limits mean one thing for filters and patches alike: zero is
// the default for the count limits, zero or less is unlimited for the input
// size, and a negative count limit or list body size fails construction
// naming the option.
func TestParserLimits_OneRule(t *testing.T) {
	resource := func(t *testing.T, opts ...core.Option) *parse.Resource[limitedDoc] {
		t.Helper()
		r, err := parse.NewResource[limitedDoc](onSQLite, "limited", parse.ParserOptions(opts...))
		require.NoError(t, err)
		return r
	}
	def := core.DefaultConfig()

	// depthBound asserts r refuses filters and patches one level past depth,
	// and accepts them at depth.
	depthBound := func(t *testing.T, r *parse.Resource[limitedDoc], depth int) {
		t.Helper()
		// Cost and conditions grow with the depth; only depth is measured.
		_, err := r.ParseFilterWithCost(nestedFilter(depth), 1<<20)
		require.NoError(t, err, "a filter %d deep", depth)
		_, err = r.ParseFilterWithCost(nestedFilter(depth+1), 1<<20)
		require.ErrorContains(t, err, fmt.Sprintf("maximum %d", depth), "a filter %d deep", depth+1)
		_, err = r.ParseMergePatch([]byte(nestedMergePatch(depth)))
		require.NoError(t, err, "a merge patch %d deep", depth)
		_, err = r.ParseMergePatch([]byte(nestedMergePatch(depth + 1)))
		require.ErrorContains(t, err, fmt.Sprintf("maximum is %d", depth), "a merge patch %d deep", depth+1)
		pointer := "/doc" + strings.Repeat("/k", depth)
		_, err = r.ParsePatch([]byte(`[{"op":"replace","path":"` + pointer + `","value":1}]`))
		assert.ErrorContains(t, err, fmt.Sprintf("maximum is %d", depth), "a pointer of %d segments", depth+1)
	}

	t.Run("depth", func(t *testing.T) {
		depthBound(t, resource(t), def.MaxDepth)
		depthBound(t, resource(t, core.WithMaxDepth(0)), def.MaxDepth)
		depthBound(t, resource(t, core.WithMaxDepth(1)), 1)
	})
	t.Run("conditions", func(t *testing.T) {
		for _, r := range []*parse.Resource[limitedDoc]{resource(t), resource(t, core.WithMaxConditions(0))} {
			_, err := r.ParseFilterWithCost(conditions(def.MaxConditions), 1<<20)
			require.NoError(t, err)
			_, err = r.ParseFilterWithCost(conditions(def.MaxConditions+1), 1<<20)
			assert.ErrorContains(t, err, fmt.Sprintf("exceeds maximum %d", def.MaxConditions))
		}
	})
	t.Run("cost", func(t *testing.T) {
		for _, r := range []*parse.Resource[limitedDoc]{resource(t), resource(t, core.WithMaxCost(0))} {
			_, err := r.ParseFilter(conditions(def.MaxConditions))
			assert.ErrorContains(t, err, fmt.Sprintf("exceeds maximum %d", def.MaxCost))
		}
	})
	t.Run("array size", func(t *testing.T) {
		for _, r := range []*parse.Resource[limitedDoc]{resource(t), resource(t, core.WithMaxArraySize(0))} {
			_, err := r.ParseFilter(inSet(def.MaxArraySize))
			require.NoError(t, err)
			_, err = r.ParseFilter(inSet(def.MaxArraySize + 1))
			assert.ErrorContains(t, err, fmt.Sprintf("exceeds maximum %d", def.MaxArraySize))
		}
	})
	t.Run("input size", func(t *testing.T) {
		long := strings.Repeat("x", def.MaxInputSize)
		filterBody, patchBody := `{"name":"`+long+`"}`, []byte(`{"name":"`+long+`"}`)
		_, err := resource(t).ParseFilter(filterBody)
		require.ErrorContains(t, err, "exceeds maximum")
		_, err = resource(t).ParseMergePatch(patchBody)
		require.ErrorContains(t, err, "exceeds maximum")
		for _, size := range []int{0, -1} {
			r := resource(t, core.WithMaxInputSize(size))
			_, err = r.ParseFilter(filterBody)
			require.NoError(t, err, "input size %d is unlimited", size)
			_, err = r.ParseMergePatch(patchBody)
			assert.NoError(t, err, "input size %d is unlimited", size)
		}
	})
	t.Run("negative", func(t *testing.T) {
		for option, opt := range map[string]core.Option{
			"WithMaxDepth(-1)":        core.WithMaxDepth(-1),
			"WithMaxCost(-1)":         core.WithMaxCost(-1),
			"WithMaxConditions(-1)":   core.WithMaxConditions(-1),
			"WithMaxArraySize(-1)":    core.WithMaxArraySize(-1),
			"WithMaxListBodySize(-1)": core.WithMaxListBodySize(-1),
		} {
			_, err := parse.NewResource[limitedDoc](onSQLite, "limited", parse.ParserOptions(opt))
			assert.ErrorContains(t, err, "parse: limited: parser options: "+option+": a limit is never negative")
		}
	})
}

// Two WithGeneratorOptions keep both: the column tag of one and the field
// name tag of the other.
func TestParserOptions_GeneratorOptionsAccumulate(t *testing.T) {
	type row struct {
		ID   string `sql:"id"        bson:"id"   parse:"filter"`
		Name string `sql:"full_name" bson:"name" parse:"filter,patch"`
	}
	r, err := parse.NewResource[row](onSQLite, "rows", parse.ParserOptions(
		core.WithGeneratorOptions(core.WithColumnTag("sql")),
		core.WithGeneratorOptions(core.WithFieldNameTag("bson")),
	))
	require.NoError(t, err)
	require.Contains(t, r.FilterSchema().Fields, "name")
	assert.Equal(t, "full_name", r.FilterSchema().Fields["name"].Column)
}

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
// to NewResource is enforced by the parse entry points: each sub-parser is
// built with the resource's config, never the defaults.
func TestLimitsThreadThroughResource(t *testing.T) {
	t.Run("MaxConditions", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice](onSQLite, "limits_devices", parse.ParserOptions(core.WithMaxConditions(1)))
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
		tp, err := parse.NewResource[limitsDevice](onSQLite, "limits_devices", parse.ParserOptions(core.WithMaxInputSize(4)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = tp.ParseFilter(`{"status": "active"}`)
		if err == nil {
			t.Fatal("20-byte filter accepted with WithMaxInputSize(4)")
		}
	})

	t.Run("MaxInputSize patch", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice](onSQLite, "limits_devices", parse.ParserOptions(core.WithMaxInputSize(4)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = tp.ParsePatch([]byte(`[{"op":"replace","path":"/name","value":"x"}]`))
		if err == nil {
			t.Fatal("41-byte patch accepted with WithMaxInputSize(4)")
		}
	})

	t.Run("MaxInputSize merge patch", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice](onSQLite, "limits_devices", parse.ParserOptions(core.WithMaxInputSize(4)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = tp.ParseMergePatch([]byte(`{"name": "renamed"}`))
		if err == nil {
			t.Fatal("19-byte merge patch accepted with WithMaxInputSize(4)")
		}
	})

	t.Run("MaxDepth", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice](onSQLite, "limits_devices", parse.ParserOptions(core.WithMaxDepth(1)))
		if err != nil {
			t.Fatal(err)
		}
		deep := `{"$or": [{"$and": [{"$or": [{"status": "active"}]}]}]}`
		_, err = tp.ParseFilter(deep)
		if err == nil {
			t.Fatal("depth-3 filter accepted with WithMaxDepth(1)")
		}
	})

	t.Run("MaxArraySize", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice](onSQLite, "limits_devices", parse.ParserOptions(core.WithMaxArraySize(2)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = tp.ParseFilter(`{"count": {"$in": [1, 2, 3]}}`)
		if err == nil {
			t.Fatal("3-element $in accepted with WithMaxArraySize(2)")
		}
	})

	t.Run("MaxCost", func(t *testing.T) {
		tp, err := parse.NewResource[limitsDevice](onSQLite, "limits_devices", parse.ParserOptions(core.WithMaxCost(1)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = tp.ParseFilter(`{"name": {"$like": "%a%"}, "count": {"$gt": 1}}`)
		if err == nil {
			t.Fatal("multi-condition LIKE filter accepted with WithMaxCost(1)")
		}
	})

	t.Run("Codec used by ParseFilter", func(t *testing.T) {
		calls := 0
		tp, err := parse.NewResource[limitsDevice](onSQLite, "limits_devices", parse.ParserOptions(core.WithCodec(countingCodec{unmarshals: &calls})))
		if err != nil {
			t.Fatal(err)
		}
		_, err = tp.ParseFilter(`{"status": "active"}`)
		if err != nil {
			t.Fatal(err)
		}
		if calls == 0 {
			t.Fatal("configured codec's Unmarshal never called by ParseFilter")
		}
	})

	t.Run("Codec used by ParsePatch", func(t *testing.T) {
		calls := 0
		tp, err := parse.NewResource[limitsDevice](onSQLite, "limits_devices", parse.ParserOptions(core.WithCodec(countingCodec{unmarshals: &calls})))
		if err != nil {
			t.Fatal(err)
		}
		_, err = tp.ParsePatch([]byte(`[{"op":"replace","path":"/name","value":"x"}]`))
		if err != nil {
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
	tp, err := parse.NewResource[limitsDevice](onSQLite, "limits_devices", parse.ParserOptions(core.WithClock(func() time.Time { return frozen })))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := tp.ParseFilter(`{"created_at": {"$gt": "now-7d"}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := core.FormatTimestamp(frozen.Add(-7 * 24 * time.Hour))
	root := parsed.AST().Root
	if root == nil || root.Value != want {
		t.Fatalf("resolved value = %v; want %s", root.Value, want)
	}
}

type account struct {
	ID         string `json:"id"          db:"id"          parse:"filter,sort"`
	TenantID   string `json:"tenant_id"   db:"tenant_id"`
	Email      string `json:"email"       db:"email"       parse:"filter,sort,patch"`
	SecretHash string `json:"-"           db:"secret_hash"`
	UpdatedAt  string `json:"updated_at"  db:"updated_at"`
}

// ReadColumns keeps a resource's sensitive columns in the database: lists
// and updates read only the declared columns, while stamps and validation
// still know every column of T.
func TestReadColumns_LimitWhatTheResourceReads(t *testing.T) {
	r, err := parse.NewResource[account](onSQLite, "accounts", parse.TenantColumn("tenant_id"),
		parse.ReadColumns("updated_at", "email", "id", "tenant_id"))
	require.NoError(t, err)
	assert.Equal(t, []string{`"accounts"."id"`, `"accounts"."tenant_id"`, `"accounts"."email"`, `"accounts"."updated_at"`}, r.Columns(), "in T's field order")

	plan, err := r.BuildList(nil, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(plan.Page.SQL, `SELECT "accounts"."id", "accounts"."tenant_id", "accounts"."email", "accounts"."updated_at" FROM "accounts" `), plan.Page.SQL)

	p, err := r.ParseMergePatch([]byte(`{"email":"a@example.com"}`))
	require.NoError(t, err)
	upd, err := r.BuildUpdate(p, parse.ByID("a1"), parse.Scope{Tenant: "t1"}, parse.StampSQL("updated_at", "NOW()"))
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(upd.Owner.SQL, ` RETURNING "accounts"."id", "accounts"."tenant_id", "accounts"."email", "accounts"."updated_at"`), upd.Owner.SQL)
	assert.NotContains(t, upd.Owner.SQL+plan.Page.SQL, "secret_hash")
}

func TestReadColumns_RefusesWhatPagesCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		read []string
		want string
	}{
		{"not a column", []string{"id", "nope"}, `read column "nope" is not a column`},
		{"listed twice", []string{"id", "email", "id"}, `read column "id" is listed twice`},
		{"without the key", []string{"email"}, `omit the key column "id"`},
		{"without a sort key", []string{"id", "tenant_id"}, `sort key "email": accounts does not read column "email"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse.NewResource[account](onSQLite, "accounts", parse.ReadColumns(tc.read...))
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

type profile struct {
	ID    string         `json:"id"    db:"id"    parse:"filter,sort"`
	Attrs map[string]any `json:"attrs" db:"attrs" parse:"filter"`
}

// A request's path sort keys and facets read their document column, so a
// view can neither sort nor facet by a document the resource does not read,
// even one it may filter by.
func TestReadColumns_RefusesAPathSortOrFacetOnAnUnreadDocument(t *testing.T) {
	r, err := parse.NewResource[profile](onSQLite, "profiles", parse.ReadColumns("id"), parse.QueryOnlyColumns("attrs"))
	require.NoError(t, err)
	_, err = r.WithPaths(filter.PathDecl{Path: "attrs.rank", Type: core.FieldTypeInt, Sort: true})
	require.ErrorContains(t, err, `sort key "attrs.rank": profiles does not read column "attrs"`)
	_, err = r.WithPaths(filter.PathDecl{Path: "attrs.tier", Type: core.FieldTypeString, Facet: &filter.FacetSchema{Type: filter.FacetTerms}})
	require.ErrorContains(t, err, `facet "attrs.tier": profiles does not read column "attrs"`)
	_, err = r.WithPaths(filter.PathDecl{Path: "attrs.rank", Type: core.FieldTypeInt})
	assert.NoError(t, err, "a query-only document filters by its paths")
}

type apiKey struct {
	ID     string          `json:"id"     db:"id"     parse:"filter,sort"`
	Status string          `json:"status" db:"status" parse:"filter,facet"`
	Token  string          `json:"token"  db:"token"  parse:"filter,facet"`
	Attrs  json.RawMessage `json:"attrs"  db:"attrs"  parse:"filter" paths:"env:string:facet"`
}

// A facet's buckets are keyed by its column's values, so a facet over a
// column the resource does not read would answer with the withheld values,
// whether or not requests may filter by it.
func TestReadColumns_RefusesAFacetOnAnUnreadColumn(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []parse.ResourceOption
		want string
	}{
		{"a column's facet", []parse.ResourceOption{parse.ReadColumns("id", "status", "attrs"), parse.QueryOnlyColumns("token")}, `facet "token": api_keys does not read column "token"`},
		{"a document path's facet", []parse.ResourceOption{parse.ReadColumns("id", "status", "token"), parse.QueryOnlyColumns("attrs")}, `facet "attrs.env": api_keys does not read column "attrs"`},
		{"a mapped column's facet", []parse.ResourceOption{mappedColumns(), parse.ReadColumns("id", "status", "attrs"), parse.QueryOnlyColumns("token")}, `facet "token": api_keys does not read column "token"`},
		{"a mapped document path's facet", []parse.ResourceOption{mappedColumns(), parse.ReadColumns("id", "status", "token"), parse.QueryOnlyColumns("attrs")}, `facet "attrs.env": api_keys does not read column "attrs"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse.NewResource[apiKey](onSQLite, "api_keys", tc.opts...)
			assert.ErrorContains(t, err, tc.want)
		})
	}

	r, err := parse.NewResource[apiKey](onSQLite, "api_keys", mappedColumns(), parse.ReadColumns("id", "status", "token", "attrs"))
	require.NoError(t, err)
	_, err = r.ParseList(url.Values{filter.ParamFacets: {"status,token,attrs.env"}})
	assert.NoError(t, err, "facets on read columns")
}

type ticket struct {
	ID     string `json:"id"     db:"id"     parse:"filter,sort"`
	Title  string `json:"title"  db:"title"  parse:"filter,sort"`
	Status string `json:"status" db:"status" parse:"filter"`
	Body   string `json:"body"   db:"body"   parse:"search"`
}

// A filter or search returns no values, but its matches disclose them, so
// by default it may not read a column the resource does not read.
func TestReadColumns_RefusesAPredicateOnAnUnreadColumn(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []parse.ResourceOption
		want string
	}{
		{"a filter field", []parse.ResourceOption{parse.ReadColumns("id", "title", "body")}, `filter field "status": tickets does not read column "status"; QueryOnlyColumns admits`},
		{"a search field", []parse.ResourceOption{parse.ReadColumns("id", "title", "status")}, `search field "body": tickets does not read column "body"; QueryOnlyColumns admits`},
		{"a mapped filter field", []parse.ResourceOption{mappedColumns(), parse.ReadColumns("id", "title", "body")}, `filter field "status": tickets does not read column "status"`},
		{"a mapped search field", []parse.ResourceOption{mappedColumns(), parse.ReadColumns("id", "title", "status")}, `search field "body": tickets does not read column "body"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse.NewResource[ticket](onSQLite, "tickets", tc.opts...)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

type member struct {
	ID     string `json:"id"      db:"id"      parse:"filter,sort"`
	TeamID string `json:"team_id" db:"team_id"`
}

// A relation pairs owner and target rows by a column on each side: a
// belongs-to by the owner's foreign key, a has-many by the target's. Filtering
// by the relation discloses that column as filtering by the column would, and
// an include returns the pairs outright, so each side's read set holds.
func TestReadColumns_RefusesARelationOverAnUnreadColumn(t *testing.T) {
	teams := parse.MustResource[team](onSQLite, "teams")
	withheld := parse.MustResource[member](onSQLite, "members", parse.ReadColumns("id"))
	queryOnly := parse.MustResource[member](onSQLite, "members", parse.ReadColumns("id"), parse.QueryOnlyColumns("team_id"))

	err := withheld.Relate("team", parse.BelongsTo(teams, "team_id"))
	require.ErrorContains(t, err, `relation "team": members does not read column "team_id"; QueryOnlyColumns admits`, "the owner's foreign key")
	assert.NotContains(t, withheld.FilterSchema().Fields, "team", "a refused relation is not declared")
	require.NoError(t, queryOnly.Relate("team", parse.BelongsTo(teams, "team_id")))

	owners := parse.MustResource[team](onSQLite, "teams")
	err = owners.Relate("members", parse.HasMany(withheld, "team_id"))
	require.ErrorContains(t, err, `parse: teams: relation "members": members does not read column "team_id"; QueryOnlyColumns admits`, "the target's foreign key")
	err = owners.Relate("members", parse.HasMany(queryOnly, "team_id", parse.Includable()))
	require.ErrorContains(t, err, `relation "members": members does not read column "team_id"`, "an include returns the pairs")
	assert.NoError(t, owners.Relate("members", parse.HasMany(queryOnly, "team_id")))
}

// A field whose SQL reads a column T does not declare cannot bypass the read
// set: the resource cannot say whether it reads that column.
func TestReadColumns_RefusesAFieldOverAnUndeclaredColumn(t *testing.T) {
	type user struct {
		ID   string `json:"id"   db:"id"   parse:"filter,sort"`
		Hash string `json:"hash" db:"-"    parse:"filter"`
	}
	_, err := parse.NewResource[user](onSQLite, "users", parse.ReadColumns("id"))
	assert.ErrorContains(t, err, `filter field "hash": "hash" is not a column of users`)
}

// QueryOnlyColumns lets requests filter and search by columns the resource
// does not read; the page still selects only the read columns.
func TestQueryOnlyColumns_FilterAndSearchUnreadColumns(t *testing.T) {
	r, err := parse.NewResource[ticket](onSQLite, "tickets", parse.ReadColumns("id", "title"), parse.QueryOnlyColumns("status", "body"))
	require.NoError(t, err)
	req, err := r.ParseList(url.Values{filter.ParamFilter: {`{"status":{"$eq":"open"}}`}, filter.ParamSearch: {"pump"}})
	require.NoError(t, err)
	plan, err := r.BuildList(req, parse.Scope{})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(plan.Page.SQL, `SELECT "tickets"."id", "tickets"."title" FROM "tickets" `), plan.Page.SQL)
	assert.Contains(t, plan.Fragment, `"tickets"."status"`)
	assert.Contains(t, plan.Fragment, `"tickets"."body"`)
}

func TestQueryOnlyColumns_NameUnreadColumnsForPredicatesOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []parse.ResourceOption
		want string
	}{
		{"not a column", []parse.ResourceOption{parse.ReadColumns("id", "title", "status"), parse.QueryOnlyColumns("body", "nope")}, `query-only column "nope" is not a column`},
		{"listed twice", []parse.ResourceOption{parse.ReadColumns("id", "title", "status"), parse.QueryOnlyColumns("body", "body")}, `query-only column "body" is listed twice`},
		{"a read column", []parse.ResourceOption{parse.ReadColumns("id", "title", "status"), parse.QueryOnlyColumns("body", "title")}, `query-only column "title" is read`},
		{"every column read", []parse.ResourceOption{parse.QueryOnlyColumns("body")}, `query-only column "body" is read`},
		{"a sort key", []parse.ResourceOption{parse.ReadColumns("id", "status"), parse.QueryOnlyColumns("title", "body")}, `sort key "title": tickets does not read column "title"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse.NewResource[ticket](onSQLite, "tickets", tc.opts...)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

type session struct {
	ID      string   `json:"id"      db:"id"      parse:"filter,sort"`
	Addr    string   `json:"addr"    db:"addr"    parse:"patch"`
	Net     *string  `json:"net"     db:"net"`
	Every   string   `json:"every"   db:"every"`
	Allowed []string `json:"allowed" db:"allowed"`
	Nets    []string `json:"nets"    db:"nets"`
	Spans   []string `json:"spans"   db:"spans"`
	Count   int      `json:"count"   db:"count"`
}

func sessionResource(t *testing.T, p *parse.Provider, opts ...parse.ResourceOption) (*parse.Resource[session], error) {
	t.Helper()
	return parse.NewResource[session](p, "sessions", append([]parse.ResourceOption{
		parse.ColumnType("addr", core.SQLInet),
		parse.ColumnType("net", core.SQLCIDR),
		parse.ColumnType("every", core.SQLInterval),
		parse.ColumnType("allowed", core.SQLInetArray),
		parse.ColumnType("nets", core.SQLCIDRArray),
		parse.ColumnType("spans", core.SQLIntervalArray),
	}, opts...)...)
}

// A declared column is read as the dialect reads its type, under its own
// name; the others, and every column on SQLite, are read as they are.
func TestColumnType_Projection(t *testing.T) {
	onPG, err := sessionResource(t, onPostgres)
	require.NoError(t, err)
	// An interval reads as its ISO 8601 text under any IntervalStyle; the
	// PostgreSQL integration suite checks the text itself.
	pg := filter.PostgresDialect{}
	assert.Equal(t, []string{
		`"sessions"."id"`,
		`abbrev("sessions"."addr") AS "addr"`,
		`"sessions"."net"::text AS "net"`,
		pg.ReadColumn(`"sessions"."every"`, core.SQLInterval) + ` AS "every"`,
		`CASE WHEN "sessions"."allowed" IS NULL THEN NULL ELSE ARRAY(SELECT abbrev(a) FROM unnest("sessions"."allowed") WITH ORDINALITY AS u(a, n) ORDER BY n) END AS "allowed"`,
		`"sessions"."nets"::text[] AS "nets"`,
		`CASE WHEN "sessions"."spans" IS NULL THEN NULL ELSE ARRAY(SELECT ` + pg.ReadColumn("a", core.SQLInterval) +
			` FROM unnest("sessions"."spans") WITH ORDINALITY AS u(a, n) ORDER BY n) END AS "spans"`,
		`"sessions"."count"`,
	}, onPG.Columns())
	assert.NotContains(t, pg.ReadColumn(`"sessions"."every"`, core.SQLInterval), "::text AS",
		"a text cast would follow the session's IntervalStyle")
	onLite, err := sessionResource(t, onSQLite)
	require.NoError(t, err)
	assert.Equal(t, []string{
		`"sessions"."id"`, `"sessions"."addr"`, `"sessions"."net"`, `"sessions"."every"`,
		`"sessions"."allowed"`, `"sessions"."nets"`, `"sessions"."spans"`, `"sessions"."count"`,
	}, onLite.Columns())

	only, err := sessionResource(t, onPostgres, parse.ReadColumns("id", "addr"))
	require.NoError(t, err)
	assert.Equal(t, []string{`"sessions"."id"`, `abbrev("sessions"."addr") AS "addr"`}, only.Columns())
}

// Every dialect's projection reads each column under the name ColumnNames
// gives at the same position.
func TestColumnType_ColumnNamesNameTheProjection(t *testing.T) {
	names := []string{"id", "addr", "net", "every", "allowed", "nets", "spans", "count"}
	for _, on := range []struct {
		p *parse.Provider
		d filter.Dialect
	}{{onPostgres, filter.PostgresDialect{}}, {onSQLite, filter.SQLiteDialect{}}} {
		r, err := sessionResource(t, on.p)
		require.NoError(t, err)
		assert.Equal(t, names, r.ColumnNames())
		d := on.d
		columns := r.Columns()
		require.Len(t, columns, len(names))
		for i, column := range columns {
			quoted := d.QuoteIdent(names[i])
			assert.True(t, column == d.QuoteIdent("sessions")+"."+quoted || strings.HasSuffix(column, " AS "+quoted), "%T reads %q as %q", d, names[i], column)
		}
	}

	only, err := sessionResource(t, onPostgres, parse.ReadColumns("id", "addr"))
	require.NoError(t, err)
	assert.Equal(t, []string{"id", "addr"}, only.ColumnNames())

	r, err := sessionResource(t, onPostgres)
	require.NoError(t, err)
	r.ColumnNames()[0] = "changed"
	assert.Equal(t, "id", r.ColumnNames()[0], "the names are a copy")
}

// A list's page and an update's RETURNING read the same projection.
func TestColumnType_PlansReadTheProjection(t *testing.T) {
	r, err := sessionResource(t, onPostgres)
	require.NoError(t, err)
	req, err := r.ParseList(url.Values{})
	require.NoError(t, err)
	list, err := r.BuildList(req, parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, list.Page.SQL, `SELECT "sessions"."id", abbrev("sessions"."addr") AS "addr", `)

	p, err := r.ParseMergePatch([]byte(`{"addr":"10.0.0.1"}`))
	require.NoError(t, err)
	upd, err := r.BuildUpdate(p, parse.ByID("s1"), parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, upd.Owner.SQL, ` RETURNING "sessions"."id", abbrev("sessions"."addr") AS "addr", `)
}

func TestColumnType_RefusesWhatItCannotRead(t *testing.T) {
	for name, tc := range map[string]struct {
		opt  parse.ResourceOption
		want string
	}{
		"not a column":      {parse.ColumnType("ip", core.SQLInet), `column type for "ip", which is not a column`},
		"declared twice":    {parse.ColumnType("addr", core.SQLCIDR), `column "addr" has its type declared twice`},
		"unknown type":      {parse.ColumnType("count", "money"), `declares type "money", which has no read rule`},
		"scalar into int":   {parse.ColumnType("count", core.SQLInet), `column "count" is inet, read into a string, not int`},
		"array into string": {parse.ColumnType("id", core.SQLInetArray), `column "id" is inet[], read into a slice of strings, not string`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := sessionResource(t, onPostgres, tc.opt)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

type columnStorageAmount struct{ Text string }

func (a columnStorageAmount) MarshalJSON() ([]byte, error) { return json.Marshal(a.Text) }

func (a *columnStorageAmount) UnmarshalJSON(raw []byte) error {
	return json.Unmarshal(raw, &a.Text)
}

func (a columnStorageAmount) Value() (driver.Value, error) { return a.Text, nil }

type columnStorageTransform struct {
	Count int `json:"count"`
}

func (v *columnStorageTransform) UnmarshalJSON(raw []byte) error {
	type value columnStorageTransform
	var decoded value
	err := json.Unmarshal(raw, &decoded)
	if err != nil {
		return err
	}
	decoded.Count++
	*v = columnStorageTransform(decoded)
	return nil
}

type columnStorageRow struct {
	ID      string   `json:"id" db:"id" parse:"sort"`
	Text    string   `json:"text" db:"document_text" parse:"filter,patch"`
	Bytes   []byte   `json:"bytes" db:"document_bytes" parse:"filter,patch"`
	Items   []string `json:"items" db:"document_items" parse:"filter,patch"`
	Native  []string `json:"native" db:"native_items" parse:"filter,patch"`
	Private string   `json:"-" db:"private_document" parse:"server"`
}

func TestColumnStorage_JSONFilterAndPatchBinding(t *testing.T) {
	documents := func(p *parse.Provider) *parse.Resource[columnStorageRow] {
		r, err := parse.NewResource[columnStorageRow](p, "documents",
			parse.ColumnType("document_text", core.SQLJSONB),
			parse.ColumnType("document_bytes", core.SQLJSONB),
			parse.ColumnType("document_items", core.SQLJSONB),
			parse.ColumnType("native_items", core.SQLTextArray),
			parse.ColumnType("private_document", core.SQLJSONB),
		)
		require.NoError(t, err)
		return r
	}
	r, lite := documents(onPostgres), documents(onSQLite)
	// update builds body's merge patch for ByID("one") with each resource.
	update := func(t *testing.T, r *parse.Resource[columnStorageRow], body string) *parse.UpdatePlan {
		t.Helper()
		p, err := r.ParseMergePatch([]byte(body))
		require.NoError(t, err)
		plan, err := r.BuildUpdate(p, parse.ByID("one"), parse.Scope{})
		require.NoError(t, err)
		return plan
	}
	for _, tt := range []struct {
		field, input, encoded string
	}{
		{"text", `"hello"`, `"hello"`},
		{"bytes", `"YQ=="`, `"YQ=="`},
		{"items", `["one","two"]`, `["one","two"]`},
	} {
		t.Run(tt.field, func(t *testing.T) {
			req, err := r.ParseList(url.Values{"filter": {`{"` + tt.field + `":{"$eq":` + tt.input + `}}`}})
			require.NoError(t, err)
			plan, err := r.BuildList(req, parse.Scope{})
			require.NoError(t, err)
			assert.Contains(t, plan.Page.Args, tt.encoded)
			assert.Contains(t, plan.Page.SQL, "::jsonb")

			body := `{"` + tt.field + `":` + tt.input + `}`
			assert.Contains(t, update(t, r, body).Owner.Args, tt.encoded)
			assert.Contains(t, update(t, lite, body).Owner.Args, tt.encoded)
		})
	}

	assert.Contains(t, update(t, r, `{"native":["one","two"]}`).Owner.Args, []string{"one", "two"})
	assert.Contains(t, update(t, lite, `{"native":["one","two"]}`).Owner.Args, `["one","two"]`)

	p, err := r.ParseServerMergePatch([]byte(`{"private_document":"hidden"}`))
	require.NoError(t, err)
	pg, err := r.BuildUpdate(p, parse.ByID("one"), parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, pg.Owner.Args, `"hidden"`)
}

func TestColumnStorage_ResourceProfilesAreIndependent(t *testing.T) {
	type row struct {
		ID   string `json:"id" db:"id" parse:"sort"`
		Name string `json:"name" db:"stored_name" parse:"filter,patch"`
	}
	document, err := parse.NewResource[row](onPostgres, "documents", parse.ColumnType("stored_name", core.SQLJSONB))
	require.NoError(t, err)
	text, err := parse.NewResource[row](onPostgres, "texts")
	require.NoError(t, err)
	for _, tt := range []struct {
		r       *parse.Resource[row]
		encoded string
	}{
		{document, `"hello"`}, {text, "hello"}, {document, `"hello"`},
	} {
		p, err := tt.r.ParseMergePatch([]byte(`{"name":"hello"}`))
		require.NoError(t, err)
		plan, err := tt.r.BuildUpdate(p, parse.ByID("one"), parse.Scope{})
		require.NoError(t, err)
		assert.Contains(t, plan.Owner.Args, tt.encoded)
	}
}

func TestColumnStorage_LowerLevelDeclarationOwnership(t *testing.T) {
	type row struct {
		Name string `json:"name" db:"stored_name" parse:"filter"`
	}
	types := map[string]core.SQLType{"stored_name": core.SQLJSONB}
	g := core.NewSchemaGenerator(core.WithColumnTypes(types))
	types["stored_name"] = core.SQLInet
	schema, err := filter.GenerateFilterSchema[row](g)
	require.NoError(t, err)
	assert.Equal(t, core.FieldTypeJSON, schema.Fields["name"].DataType)
}

func TestColumnStorage_DeclaredJSONControlsDriverValues(t *testing.T) {
	type row struct {
		ID     string              `json:"id" db:"id"`
		Amount columnStorageAmount `json:"amount" db:"amount" parse:"patch" patch_ops:"replace"`
	}
	for _, declared := range []bool{false, true} {
		var opts []parse.ResourceOption
		if declared {
			opts = append(opts, parse.ColumnType("amount", core.SQLJSONB))
		}
		for _, provider := range []*parse.Provider{onPostgres, onSQLite} {
			r, err := parse.NewResource[row](provider, "documents", opts...)
			require.NoError(t, err)
			p, err := r.ParseMergePatch([]byte(`{"amount":"200"}`))
			require.NoError(t, err)
			plan, err := r.BuildUpdate(p, parse.ByID("one"), parse.Scope{})
			require.NoError(t, err)
			if declared {
				assert.Contains(t, plan.Owner.Args, `"200"`)
			} else {
				assert.Contains(t, plan.Owner.Args, columnStorageAmount{Text: "200"})
			}
		}
	}
}

func TestColumnStorage_AppliedJSONMatchesSQLDecode(t *testing.T) {
	type row struct {
		ID      string                 `json:"id" db:"id"`
		Data    columnStorageTransform `json:"data" db:"data" parse:"patch"`
		Dynamic any                    `json:"dynamic" db:"dynamic" parse:"patch"`
	}
	r, err := parse.NewResource[row](onPostgres, "documents", parse.ColumnType("data", core.SQLJSONB), parse.ColumnType("dynamic", core.SQLJSONB))
	require.NoError(t, err)
	p, err := r.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/data", HasValue: true, Value: map[string]any{"count": 0}}})
	require.NoError(t, err)
	plan, err := r.BuildUpdate(p, parse.ByID("one"), parse.Scope{})
	require.NoError(t, err)
	var expected columnStorageTransform
	raw, ok := plan.Owner.Args[0].(string)
	require.True(t, ok)
	require.NoError(t, json.Unmarshal([]byte(raw), &expected))
	applied, err := r.ApplyValidatedPatch(p, row{ID: "one"})
	require.NoError(t, err)
	assert.Equal(t, expected, applied.Data)
	assert.Equal(t, 1, applied.Data.Count)

	p, err = r.ParseMergePatch([]byte(`{"dynamic":{"count":1}}`))
	require.NoError(t, err)
	applied, err = r.ApplyValidatedPatch(p, applied)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"count": float64(1)}, applied.Dynamic)
	assert.Equal(t, expected, applied.Data)
}

func TestColumnStorage_JSONCapabilitiesFollowStorage(t *testing.T) {
	type sorted struct {
		ID   string `json:"id" db:"id"`
		Name string `json:"name" db:"name" parse:"sort"`
	}
	_, err := parse.NewResource[sorted](onPostgres, "documents", parse.ColumnType("name", core.SQLJSONB))
	require.ErrorContains(t, err, "not sortable")
	type searched struct {
		ID   string `json:"id" db:"id"`
		Name string `json:"name" db:"name" parse:"search"`
	}
	_, err = parse.NewResource[searched](onPostgres, "documents", parse.ColumnType("name", core.SQLJSONB))
	require.ErrorContains(t, err, "not json")
	type object struct {
		Value string `json:"value"`
	}
	type shaped struct {
		ID   string `json:"id" db:"id"`
		Body object `json:"body" db:"body" parse:"filter,patch"`
	}
	r, err := parse.NewResource[shaped](onPostgres, "documents", parse.ColumnType("body", core.SQLJSONB))
	require.NoError(t, err)
	assert.Equal(t, core.FieldTypeObject, r.FilterSchema().Fields["body"].DataType)
}

func TestColumnStorage_JSONPredicatesAndPathsFollowPhysicalType(t *testing.T) {
	type row struct {
		ID   string          `json:"id" db:"id"`
		Data json.RawMessage `json:"data" db:"data" parse:"filter" paths:"key:string:filter+sort"`
	}
	for _, tt := range []struct {
		name    string
		storage core.SQLType
		want    filter.JSONStorage
		expr    string
	}{
		{"json", core.SQLJSON, filter.StorageJSON, `("documents"."data")::jsonb`},
		{"jsonb", core.SQLJSONB, filter.StorageJSONB, `"documents"."data"`},
		{"inferred", "", "", `"documents"."data"`},
		{"json again", core.SQLJSON, filter.StorageJSON, `("documents"."data")::jsonb`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var opts []parse.ResourceOption
			if tt.storage != "" {
				opts = append(opts, parse.ColumnType("data", tt.storage))
			}
			r, err := parse.NewResource[row](onPostgres, "documents", opts...)
			require.NoError(t, err)
			assert.Equal(t, tt.want, r.FilterSchema().Fields["data"].Storage)
			pathStorage := tt.want
			if pathStorage == "" {
				pathStorage = filter.StorageJSONB
			}
			assert.Equal(t, pathStorage, r.FilterSchema().Fields["data.key"].Path.Storage)
			assert.Equal(t, pathStorage, r.SortSchema().Fields["data.key"].Path.Storage)

			for _, op := range []struct{ input, sql string }{
				{`{"data":{"$eq":{"key":"value"}}}`, " = "},
				{`{"data":{"$contains":{"key":"value"}}}`, " @> "},
			} {
				req, err := r.ParseList(url.Values{"filter": {op.input}})
				require.NoError(t, err)
				plan, err := r.BuildList(req, parse.Scope{})
				require.NoError(t, err)
				assert.Contains(t, plan.Page.SQL, tt.expr+op.sql+"$1::jsonb")
			}

			req, err := r.ParseList(url.Values{"filter": {`{"data.key":{"$eq":"value"}}`}, "sort": {"data.key"}})
			require.NoError(t, err)
			plan, err := r.BuildList(req, parse.Scope{})
			require.NoError(t, err)
			assert.Contains(t, plan.Page.SQL, "jsonb_typeof("+tt.expr+" -> 'key')")
			assert.Contains(t, plan.Page.SQL, "ORDER BY ")
			assert.Contains(t, plan.Page.SQL, tt.expr+" ->> 'key'")
			if tt.storage != core.SQLJSON {
				assert.NotContains(t, plan.Page.SQL, `("documents"."data")::jsonb`)
			}
		})
	}
}

func TestColumnStorage_ConflictingDocumentStorageFailsConstruction(t *testing.T) {
	type row struct {
		ID   string          `json:"id" db:"id"`
		Data json.RawMessage `json:"data" db:"data" parse:"filter" storage:"json" paths:"key:string:filter+sort"`
	}
	_, err := parse.NewResource[row](onPostgres, "documents", parse.ColumnType("data", core.SQLJSONB))
	require.ErrorContains(t, err, `field Data: storage "json" conflicts with declared column type "jsonb"`)
	for _, declared := range []core.SQLType{core.SQLJSON, "", core.SQLJSON} {
		var opts []parse.ResourceOption
		if declared != "" {
			opts = append(opts, parse.ColumnType("data", declared))
		}
		r, err := parse.NewResource[row](onPostgres, "documents", opts...)
		require.NoError(t, err)
		assert.Equal(t, filter.StorageJSON, r.FilterSchema().Fields["data"].Storage)
		assert.Equal(t, filter.StorageJSON, r.SortSchema().Fields["data.key"].Path.Storage)
	}
}
