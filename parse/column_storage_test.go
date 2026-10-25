//go:build unit

package parse_test

import (
	"database/sql/driver"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

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
	if err := json.Unmarshal(raw, &decoded); err != nil {
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
	r, err := parse.NewResource[columnStorageRow]("documents",
		parse.ColumnType("document_text", core.SQLJSONB),
		parse.ColumnType("document_bytes", core.SQLJSONB),
		parse.ColumnType("document_items", core.SQLJSONB),
		parse.ColumnType("native_items", core.SQLTextArray),
		parse.ColumnType("private_document", core.SQLJSONB),
	)
	require.NoError(t, err)
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
			plan, err := r.BuildList(filter.PostgresDialect{}, req, parse.Scope{})
			require.NoError(t, err)
			assert.Contains(t, plan.Page.Args, tt.encoded)
			assert.Contains(t, plan.Page.SQL, "::jsonb")

			p, err := r.ParseMergePatch([]byte(`{"` + tt.field + `":` + tt.input + `}`))
			require.NoError(t, err)
			pg, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("one"), parse.Scope{})
			require.NoError(t, err)
			assert.Contains(t, pg.Owner.Args, tt.encoded)
			sq, err := r.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID("one"), parse.Scope{})
			require.NoError(t, err)
			assert.Contains(t, sq.Owner.Args, tt.encoded)
		})
	}

	p, err := r.ParseMergePatch([]byte(`{"native":["one","two"]}`))
	require.NoError(t, err)
	pg, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("one"), parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, pg.Owner.Args, []string{"one", "two"})
	sq, err := r.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID("one"), parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, sq.Owner.Args, `["one","two"]`)

	p, err = r.ParseServerMergePatch([]byte(`{"private_document":"hidden"}`))
	require.NoError(t, err)
	pg, err = r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("one"), parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, pg.Owner.Args, `"hidden"`)
}

func TestColumnStorage_ResourceProfilesAreIndependent(t *testing.T) {
	type row struct {
		ID   string `json:"id" db:"id" parse:"sort"`
		Name string `json:"name" db:"stored_name" parse:"filter,patch"`
	}
	document, err := parse.NewResource[row]("documents", parse.ColumnType("stored_name", core.SQLJSONB))
	require.NoError(t, err)
	text, err := parse.NewResource[row]("texts")
	require.NoError(t, err)
	for _, tt := range []struct {
		r       *parse.Resource[row]
		encoded string
	}{
		{document, `"hello"`}, {text, "hello"}, {document, `"hello"`},
	} {
		p, err := tt.r.ParseMergePatch([]byte(`{"name":"hello"}`))
		require.NoError(t, err)
		plan, err := tt.r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("one"), parse.Scope{})
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
		r, err := parse.NewResource[row]("documents", opts...)
		require.NoError(t, err)
		p, err := r.ParseMergePatch([]byte(`{"amount":"200"}`))
		require.NoError(t, err)
		for _, dialect := range []parse.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
			plan, err := r.BuildUpdate(dialect, p, parse.ByID("one"), parse.Scope{})
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
	r, err := parse.NewResource[row]("documents", parse.ColumnType("data", core.SQLJSONB), parse.ColumnType("dynamic", core.SQLJSONB))
	require.NoError(t, err)
	p, err := r.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/data", HasValue: true, Value: map[string]any{"count": 0}}})
	require.NoError(t, err)
	plan, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("one"), parse.Scope{})
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
	_, err := parse.NewResource[sorted]("documents", parse.ColumnType("name", core.SQLJSONB))
	require.ErrorContains(t, err, "not sortable")
	type searched struct {
		ID   string `json:"id" db:"id"`
		Name string `json:"name" db:"name" parse:"search"`
	}
	_, err = parse.NewResource[searched]("documents", parse.ColumnType("name", core.SQLJSONB))
	require.ErrorContains(t, err, "not json")
	type object struct {
		Value string `json:"value"`
	}
	type shaped struct {
		ID   string `json:"id" db:"id"`
		Body object `json:"body" db:"body" parse:"filter,patch"`
	}
	r, err := parse.NewResource[shaped]("documents", parse.ColumnType("body", core.SQLJSONB))
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
		{"json", core.SQLJSON, filter.StorageJSON, "(documents.data)::jsonb"},
		{"jsonb", core.SQLJSONB, filter.StorageJSONB, "documents.data"},
		{"inferred", "", "", "documents.data"},
		{"json again", core.SQLJSON, filter.StorageJSON, "(documents.data)::jsonb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var opts []parse.ResourceOption
			if tt.storage != "" {
				opts = append(opts, parse.ColumnType("data", tt.storage))
			}
			r, err := parse.NewResource[row]("documents", opts...)
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
				plan, err := r.BuildList(filter.PostgresDialect{}, req, parse.Scope{})
				require.NoError(t, err)
				assert.Contains(t, plan.Page.SQL, tt.expr+op.sql+"$1::jsonb")
			}

			req, err := r.ParseList(url.Values{"filter": {`{"data.key":{"$eq":"value"}}`}, "sort": {"data.key"}})
			require.NoError(t, err)
			plan, err := r.BuildList(filter.PostgresDialect{}, req, parse.Scope{})
			require.NoError(t, err)
			assert.Contains(t, plan.Page.SQL, "jsonb_typeof("+tt.expr+" #> '{key}')")
			assert.Contains(t, plan.Page.SQL, "ORDER BY ")
			assert.Contains(t, plan.Page.SQL, tt.expr+" #>> '{key}'")
			if tt.storage != core.SQLJSON {
				assert.NotContains(t, plan.Page.SQL, "(documents.data)::jsonb")
			}
		})
	}
}

func TestColumnStorage_ConflictingDocumentStorageFailsConstruction(t *testing.T) {
	type row struct {
		ID   string          `json:"id" db:"id"`
		Data json.RawMessage `json:"data" db:"data" parse:"filter" storage:"json" paths:"key:string:filter+sort"`
	}
	_, err := parse.NewResource[row]("documents", parse.ColumnType("data", core.SQLJSONB))
	require.ErrorContains(t, err, `field Data: storage "json" conflicts with declared column type "jsonb"`)
	for _, declared := range []core.SQLType{core.SQLJSON, "", core.SQLJSON} {
		var opts []parse.ResourceOption
		if declared != "" {
			opts = append(opts, parse.ColumnType("data", declared))
		}
		r, err := parse.NewResource[row]("documents", opts...)
		require.NoError(t, err)
		assert.Equal(t, filter.StorageJSON, r.FilterSchema().Fields["data"].Storage)
		assert.Equal(t, filter.StorageJSON, r.SortSchema().Fields["data.key"].Path.Storage)
	}
}
