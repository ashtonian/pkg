//go:build unit

package parse_test

// The server's half of parse/client's conformance. parse/client is a module
// of its own that imports no parse package, and parse does not require it:
// its tests build each request this file checks and record it in
// testdata/client_requests.json, failing when the client builds anything
// else. Here the server reads every recorded request: a filter for a corpus
// case means what the corpus input means, and the list parameters, geo
// filters and JSON Patch documents parse as the server's grammar has them.
//
// Regenerate after an intentional change to the client's encoding, from
// parse/client:
//
//	go test -tags=unit -run TestClientRequests -update ./

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

const clientRequestsPath = "testdata/client_requests.json"

// clientRequests is testdata/client_requests.json: the requests parse/client
// builds, by what each is for.
type clientRequests struct {
	// Filters are the documents it builds for filter corpus cases, by case.
	Filters map[string]json.RawMessage `json:"filters"`
	// GeoFilters are geo filters on a place's location, by shape.
	GeoFilters map[string]json.RawMessage `json:"geo_filters"`
	// List is one list request with every parameter set, as a query string
	// and as a POST body.
	List struct {
		Query url.Values      `json:"query"`
		Body  json.RawMessage `json:"body"`
	} `json:"list"`
	// Operations are JSON Patch documents of one operation, by the
	// constructor that built it.
	Operations map[string]json.RawMessage `json:"operations"`
	// JSONPatch replaces, removes and appends members of a
	// schemaDocEntity and changes its tags' membership.
	JSONPatch json.RawMessage `json:"json_patch"`
	// QuotedRelation changes the membership of a relation whose target key
	// travels as a JSON string, by JSON Patch and by merge patch.
	QuotedRelation struct {
		JSONPatch  json.RawMessage `json:"json_patch"`
		MergePatch json.RawMessage `json:"merge_patch"`
	} `json:"quoted_relation"`
}

func readClientRequests(t *testing.T) clientRequests {
	t.Helper()
	data, err := os.ReadFile(clientRequestsPath)
	require.NoError(t, err, "missing fixture; regenerate it from parse/client with -update")
	var requests clientRequests
	require.NoError(t, json.Unmarshal(data, &requests))
	return requests
}

// The server reads what the client builds as it reads the corpus input:
// the same verdict and code, the same canonical AST and the same SQL.
func TestClient_FiltersMeanWhatTheCorpusMeans(t *testing.T) {
	schema := confSchema(t)
	built := readClientRequests(t).Filters
	require.NotEmpty(t, built)
	cases := map[string]bool{}
	for _, want := range readFilterCorpus(t).Cases {
		cases[want.Name] = true
		body, ok := built[want.Name]
		if !ok {
			continue
		}
		t.Run(want.Name, func(t *testing.T) {
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.UseNumber()
			var input map[string]any
			require.NoError(t, dec.Decode(&input))

			got := runCase(t, schema, caseInput{name: want.Name, input: input})
			require.Equal(t, want.Valid, got.Valid, "client sent %s", body)
			assert.Equal(t, want.ErrorCode, got.ErrorCode)
			assert.Equal(t, normalize(t, want.CanonicalAST), normalize(t, got.CanonicalAST))
			assert.Equal(t, want.SQLPostgres, got.SQLPostgres)
			assert.Equal(t, want.SQLSqlite, got.SQLSqlite)
			assert.Equal(t, want.SQLPostgresCode, got.SQLPostgresCode)
			assert.Equal(t, want.SQLSqliteCode, got.SQLSqliteCode)
		})
	}
	for name := range built {
		assert.True(t, cases[name], "the client built a filter for %q, which is no corpus case", name)
	}
}

func TestClient_JSONPatchOperationsParse(t *testing.T) {
	res, err := parse.NewResource[schemaDocEntity](onPostgres, "doc_entities", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	body := readClientRequests(t).JSONPatch
	parsed, err := res.ParsePatch(body)
	require.NoError(t, err, "%s", body)

	type opAt struct{ op, path string }
	var got []opAt
	for _, op := range parsed.Operations {
		got = append(got, opAt{string(op.Op), op.Path})
	}
	assert.Equal(t, []opAt{
		{string(patch.PatchOpReplace), "/name"}, {string(patch.PatchOpRemove), "/note"}, {string(patch.PatchOpReplace), "/scores"},
		{string(patch.PatchOpAdd), "/tags"}, {string(patch.PatchOpRemove), "/tags"}, {string(patch.PatchOpReplace), "/tags"},
	}, got)
	assert.Equal(t, []any{"t3"}, parsed.Operations[4].Value, "a relation's remove carries its keys")
}

// opEntity declares every RFC 6902 operation on its members, so each
// constructor's document reaches BuildUpdate.
type opEntity struct {
	ID   string  `json:"id"   db:"id"`
	Name string  `json:"name" db:"name" parse:"patch" patch_ops:"replace,add,move,copy,test"`
	Note *string `json:"note" db:"note" parse:"patch" patch_ops:"replace,add,remove,move,copy,test"`
}

// Every Operation constructor writes a document the server parses, and
// BuildUpdate compiles it or answers the code its docs give.
func TestClient_EveryOperationConstructorParses(t *testing.T) {
	docs, err := parse.NewResource[schemaDocEntity](onPostgres, "doc_entities", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	ops, err := parse.NewResource[opEntity](onPostgres, "op_entities")
	require.NoError(t, err)
	tests := map[string]struct {
		docs bool // on schemaDocEntity, whose array and relation take it; else opEntity
		want patch.PatchOp
		code string // BuildUpdate's refusal; empty when it compiles
	}{
		"replace":          {false, patch.PatchOpReplace, ""},
		"remove":           {false, patch.PatchOpRemove, ""},
		"add":              {false, patch.PatchOpAdd, ""},
		"move":             {false, patch.PatchOpMove, patch.ECodeOpNotSQLCompilable.String()},
		"copy":             {false, patch.PatchOpCopy, patch.ECodeOpNotSQLCompilable.String()},
		"test":             {false, patch.PatchOpTest, patch.ECodeTestOpSQL.String()},
		"append":           {true, patch.PatchOpArrayAppend, ""},
		"array append":     {true, patch.PatchOpArrayAppend, ""},
		"relation add":     {true, patch.PatchOpAdd, ""},
		"relation remove":  {true, patch.PatchOpRemove, ""},
		"relation replace": {true, patch.PatchOpReplace, ""},
	}
	built := readClientRequests(t).Operations
	require.Equal(t, slices.Sorted(maps.Keys(tests)), slices.Sorted(maps.Keys(built)), "the client builds a document per constructor this test checks")
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			body := built[name]
			var parsed *patch.ParsedPatch
			if tt.docs {
				parsed, err = docs.ParsePatch(body)
			} else {
				parsed, err = ops.ParsePatch(body)
			}
			require.NoError(t, err, "%s", body)
			require.Len(t, parsed.Operations, 1)
			assert.Equal(t, tt.want, parsed.Operations[0].Op)

			if tt.docs {
				_, err = docs.BuildUpdate(parsed, parse.ByID("e1"), parse.Scope{Tenant: "t1"})
			} else {
				_, err = ops.BuildUpdate(parsed, parse.ByID("e1"), parse.Scope{})
			}
			if tt.code == "" {
				assert.NoError(t, err, "%s", body)
				return
			}
			pe, ok := errors.AsType[*parse.ParseError](err)
			require.True(t, ok, "%s: want a ParseError, got %v", body, err)
			assert.Equal(t, tt.code, pe.Code, "%s", body)
		})
	}
}

// The client's list parameters are the server's: everything ListParams
// encodes, ParseList reads as ParseListInput reads the same request, and
// the body is the same request.
func TestClient_ListParamsAreTheServersParameters(t *testing.T) {
	res := widgets(t)
	list := readClientRequests(t).List
	for key := range list.Query {
		assert.True(t, filter.IsListParam(key), "%q is no list parameter", key)
	}
	assert.Len(t, list.Query, 10, "every member encodes to its own parameter")

	got, err := res.ParseList(list.Query)
	require.NoError(t, err)
	want, err := res.ParseListInput(parse.ListInput{
		Filter: `{"$and":[{"name":{"$eq":"beta"}},{"rank":{"$gte":1}}]}`, Sort: "-rank,name", Cursor: "abc",
		PageSize: 3, IncludeTotal: true, IncludeDeleted: true, Search: "b",
		Facets: []string{"name"}, Include: []string{"tags"}, Fields: []string{"id", "name"},
	})
	require.NoError(t, err)
	assert.Equal(t, want, got)

	fromBody, err := res.ParseListBody(bytes.NewReader(list.Body))
	require.NoError(t, err)
	assert.Equal(t, want, fromBody, "the body is the same request as the query string")
	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(list.Body, &members))
	for key := range members {
		assert.True(t, filter.IsListParam(key), "%q is no list parameter", key)
	}
	assert.Len(t, members, 10, "every member encodes to its own body member")
}

func TestClient_GeoFiltersParse(t *testing.T) {
	type place struct {
		ID       string         `json:"id"       db:"id"`
		Location map[string]any `json:"location" parse:"filter" filter_ops:"near,within,intersects" geo:"jsonb" geo_srid:"4326"`
	}
	res, err := parse.NewResource[place](onPostgres, "places")
	require.NoError(t, err)
	built := readClientRequests(t).GeoFilters
	require.NotEmpty(t, built)
	for name, body := range built {
		_, err = res.ParseFilter(string(body))
		assert.NoError(t, err, "%s: %s", name, body)
	}
}
