//go:build unit

package parsehttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type codeTag struct {
	ID   int64  `json:"id"   db:"id"`
	Name string `json:"name" db:"name" parse:"filter"`
}

type codeItem struct {
	ID       int64          `json:"id"       db:"id"       parse:"filter,sort"`
	Owner    uuid.UUID      `json:"owner"    db:"owner"    parse:"filter"`
	Name     string         `json:"name"     db:"name"     parse:"filter,sort,patch" patch_ops:"replace,test,move"`
	Note     *string        `json:"note"     db:"note"     parse:"patch" patch_ops:"replace,remove,move"`
	Status   string         `json:"status"   db:"status"   parse:"filter,patch" enum:"active,offline"`
	Rank     int64          `json:"rank"     db:"rank"     parse:"filter,sort,patch"`
	Location map[string]any `json:"location" db:"location" parse:"filter" filter_ops:"near,within" geo:"jsonb"`
	Spot     map[string]any `json:"spot"     db:"spot"     parse:"filter" filter_ops:"near" geo:"geography"`
}

func codeItems(t *testing.T) *parse.Resource[codeItem] {
	t.Helper()
	tags, err := parse.NewResource[codeTag]("tags")
	require.NoError(t, err)
	items, err := parse.NewResource[codeItem]("items")
	require.NoError(t, err)
	require.NoError(t, items.Relate("tags", parse.ManyToMany(tags, parse.Through("item_tags", "item_id", "tag_id"), parse.Writable())))
	return items
}

// relationTestSchema is a hand-built patch schema whose relation path
// admits test, an operation no junction statement expresses.
func relationTestSchema(t *testing.T) *patch.PatchSchema {
	t.Helper()
	s, err := patch.NewPatchSchema("items", map[string]*patch.PathSchema{
		"/tags": {
			AllowedOps: []patch.PatchOp{patch.PatchOpTest},
			DataType:   core.FieldTypeArray, GoType: reflect.TypeFor[[]string](),
			Relation: &core.RelationSchema{JoinTable: "item_tags", SourceColumns: []string{"item_id"}, TargetColumns: []string{"tag_id"}},
		},
	})
	require.NoError(t, err)
	return s
}

func repeated(n int, item, sep string) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = strings.ReplaceAll(item, "%d", fmt.Sprint(i))
	}
	return strings.Join(parts, sep)
}

// Every code in the catalog is one a request can provoke, answered on the
// wire with its code and the status the table gives it. A code nothing
// raises has no case and fails here; a code added to the table fails until a
// request raises it.
func TestEveryCodeIsAnsweredWithItsStatus(t *testing.T) {
	items := codeItems(t)
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, items))
	get := httptest.NewRequest(http.MethodGet, "/items", nil)
	def := core.DefaultConfig()

	answer := func(err error) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		if !WriteError(rec, get, err) {
			rec.Code = -1
			rec.Body.WriteString(fmt.Sprintf("not a ParseError: %v", err))
		}
		return rec
	}
	filterErr := func(f string) error {
		_, err := items.ParseFilterWithCost(f, 1<<20)
		return err
	}
	list := func(in parse.ListInput) error {
		_, err := items.ParseListInput(in)
		return err
	}
	build := func(d filter.Dialect, f string) error {
		req, err := items.ParseListInput(parse.ListInput{Filter: f})
		if err != nil {
			return err
		}
		_, err = items.BuildList(d, req, parse.Scope{})
		return err
	}
	jsonPatch := func(body string, opts ...parse.PatchOption) error {
		_, err := items.ParsePatch([]byte(body), opts...)
		return err
	}
	update := func(body string) error {
		parsed, err := items.ParsePatch([]byte(body))
		if err != nil {
			return err
		}
		_, err = items.BuildUpdate(filter.SQLiteDialect{}, parsed, parse.ByID(int64(1)), parse.Scope{})
		return err
	}
	policy := func(opt patch.PolicyOption) parse.PatchOption {
		pc, err := patch.NewPatchPolicyConstraints(opt)
		require.NoError(t, err)
		return parse.WithPatchPolicy(pc, nil)
	}

	cases := map[parse.Code]func() *httptest.ResponseRecorder{
		filter.ECodeQueryTooExpensive: func() *httptest.ResponseRecorder {
			_, err := items.ParseFilterWithCost(`{"$or":[{"name":"a"},{"name":"b"}]}`, 1)
			return answer(err)
		},
		filter.ECodeInvalidArray: func() *httptest.ResponseRecorder { return answer(filterErr(`{"name":{"$in":"a"}}`)) },
		filter.ECodeInputTooLarge: func() *httptest.ResponseRecorder {
			return answer(filterErr(`{"name":"` + strings.Repeat("x", def.MaxInputSize) + `"}`))
		},
		patch.ECodeOpNotAllowed: func() *httptest.ResponseRecorder {
			return answer(jsonPatch(`[{"op":"remove","path":"/name"}]`))
		},
		filter.ECodeCursorTooLarge: func() *httptest.ResponseRecorder {
			req, err := items.ParseListInput(parse.ListInput{Sort: "name", PageSize: 1})
			require.NoError(t, err)
			plan, err := items.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
			require.NoError(t, err)
			long := strings.Repeat("n", 8192)
			_, err = items.Page([]codeItem{{ID: 1, Name: long}, {ID: 2, Name: long + "z"}}, plan)
			return answer(err)
		},
		patch.ECodeTooManyOps: func() *httptest.ResponseRecorder {
			return answer(jsonPatch(`[` + repeated(patch.DefaultMaxOperations+1, `{"op":"replace","path":"/name","value":"v%d"}`, ",") + `]`))
		},
		filter.ECodeInvalidListParam: func() *httptest.ResponseRecorder {
			_, err := items.ParseList(map[string][]string{filter.ParamPageSize: {"many"}})
			return answer(err)
		},
		patch.ECodeTestOpSQL: func() *httptest.ResponseRecorder {
			return answer(update(`[{"op":"test","path":"/name","value":"x"}]`))
		},
		patch.ECodeOpNotSQLCompilable: func() *httptest.ResponseRecorder {
			return answer(update(`[{"op":"move","from":"/note","path":"/name"}]`))
		},
		filter.ECodeUnknownField:      func() *httptest.ResponseRecorder { return answer(filterErr(`{"colour":"red"}`)) },
		filter.ECodeInvalidOperator:   func() *httptest.ResponseRecorder { return answer(filterErr(`{"rank":{"$like":"1%"}}`)) },
		filter.ECodeValueTypeMismatch: func() *httptest.ResponseRecorder { return answer(filterErr(`{"rank":"high"}`)) },
		filter.ECodeDepthExceeded: func() *httptest.ResponseRecorder {
			f := `{"name":"a"}`
			for range def.MaxDepth + 1 {
				f = `{"$and":[` + f + `]}`
			}
			return answer(filterErr(f))
		},
		patch.ECodeInvalidPatchOp: func() *httptest.ResponseRecorder {
			return answer(jsonPatch(`[{"op":"upsert","path":"/name","value":"x"}]`))
		},
		patch.ECodePatchFieldNotFound: func() *httptest.ResponseRecorder {
			return answer(jsonPatch(`[{"op":"replace","path":"/colour","value":"x"}]`))
		},
		patch.ECodePatchValueInvalid: func() *httptest.ResponseRecorder {
			return answer(jsonPatch(`[{"op":"replace","path":"/name","value":5}]`))
		},
		parse.ECodeSchemaNotFound: func() *httptest.ResponseRecorder {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/{resource}/schema", SchemaHandler(reg))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/gadgets/schema", nil))
			return rec
		},
		filter.ECodeSortFieldNotAllowed: func() *httptest.ResponseRecorder { return answer(list(parse.ListInput{Sort: "status"})) },
		filter.ECodeSortFieldLimit: func() *httptest.ResponseRecorder {
			return answer(list(parse.ListInput{Sort: repeated(32, "name", ",")}))
		},
		filter.ECodeInvalidCursor: func() *httptest.ResponseRecorder {
			// The cursor is opaque until the list is built under its keyset.
			req, err := items.ParseListInput(parse.ListInput{Sort: "name", Cursor: "not-a-cursor"})
			require.NoError(t, err)
			_, err = items.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
			return answer(err)
		},
		filter.ECodeInvalidRelation: func() *httptest.ResponseRecorder { return answer(list(parse.ListInput{Include: []string{"name"}})) },
		patch.ECodePatchPolicyForbidden: func() *httptest.ResponseRecorder {
			return answer(jsonPatch(`[{"op":"replace","path":"/name","value":"x"}]`, policy(patch.ForbidPaths("/name"))))
		},
		patch.ECodePatchPolicyValue: func() *httptest.ResponseRecorder {
			return answer(jsonPatch(`[{"op":"replace","path":"/name","value":"x"}]`,
				policy(patch.ConstrainValues("/name", patch.ValueConstraint{AllowedValues: []any{"y"}}))))
		},
		filter.ECodeInvalidJSON:      func() *httptest.ResponseRecorder { return answer(filterErr(`{"name":`)) },
		filter.ECodeInvalidEnumValue: func() *httptest.ResponseRecorder { return answer(filterErr(`{"status":"lost"}`)) },
		filter.ECodeArrayTooLarge: func() *httptest.ResponseRecorder {
			return answer(filterErr(`{"name":{"$in":[` + repeated(def.MaxArraySize+1, `"v%d"`, ",") + `]}}`))
		},
		filter.ECodeMaxConditions: func() *httptest.ResponseRecorder {
			return answer(filterErr(`{"$or":[` + repeated(def.MaxConditions+1, `{"name":"v%d"}`, ",") + `]}`))
		},
		filter.ECodeInvalidGeoCoords: func() *httptest.ResponseRecorder {
			return answer(filterErr(`{"location":{"$near":{"lat":999,"lon":-74.006,"distance":"5km"}}}`))
		},
		filter.ECodeInvalidDistance: func() *httptest.ResponseRecorder {
			return answer(filterErr(`{"location":{"$near":{"lat":40.71,"lon":-74.00,"distance":"5parsecs"}}}`))
		},
		filter.ECodeInvalidGeoPolygon: func() *httptest.ResponseRecorder {
			return answer(filterErr(`{"location":{"$within":{"points":[{"lat":40.77,"lon":-73.98},{"lat":40.73,"lon":-74.02}]}}}`))
		},
		filter.ECodeGeoNotSupported: func() *httptest.ResponseRecorder {
			return answer(build(filter.SQLiteDialect{}, `{"spot":{"$near":{"lat":40.71,"lon":-74.00,"distance":"5km"}}}`))
		},
		filter.ECodeInvalidUUID: func() *httptest.ResponseRecorder { return answer(filterErr(`{"owner":"not-a-uuid"}`)) },
		patch.ECodeRelationVerbNotAllowed: func() *httptest.ResponseRecorder {
			parsed, err := patch.NewParser(relationTestSchema(t), nil).Parse([]byte(`[{"op":"test","path":"/tags","value":["a"]}]`))
			require.NoError(t, err)
			_, err = patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
			return answer(err)
		},
		patch.ECodeRelationValueInvalid: func() *httptest.ResponseRecorder {
			return answer(jsonPatch(`[{"op":"add","path":"/tags","value":[]}]`))
		},
		patch.ECodeRelationPatchTooLarge: func() *httptest.ResponseRecorder {
			return answer(jsonPatch(`[{"op":"add","path":"/tags","value":[` + repeated(patch.DefaultMaxRelationValues+1, "%d", ",") + `]}]`))
		},
		parse.ECodeSchemaEncodingFailed: func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			newOptions(nil).serveSchema(rec, get, http.StatusOK, unencodableSchema{})
			return rec
		},
	}

	for _, code := range parse.Codes() {
		t.Run(code.String(), func(t *testing.T) {
			provoke, ok := cases[code]
			require.True(t, ok, "no request raises %s", code)
			rec := provoke()
			var body Body
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
			assert.Equal(t, code.String(), body.Code, body.Message)
			assert.Equal(t, code.ErrorHTTPStatus(), rec.Code, body.Message)
		})
	}
	assert.Len(t, cases, len(parse.Codes()), "a case for a code the table does not define")
}

// The table is the catalog, and each grammar lists the codes it raises:
// together with schema discovery's they are the table, and none lists a code
// outside it.
func TestCodes_TheGrammarsListTheTable(t *testing.T) {
	table := parse.Codes()
	assert.True(t, slices.IsSorted(table))
	discovery := []parse.Code{parse.ECodeSchemaNotFound, parse.ECodeSchemaEncodingFailed}
	listed := slices.Concat(filter.Codes(), patch.Codes(), discovery)
	for _, code := range listed {
		assert.Contains(t, table, code, "listed and not in the table")
	}
	for _, code := range table {
		assert.Contains(t, listed, code, "in the table and listed by nothing")
	}
}
