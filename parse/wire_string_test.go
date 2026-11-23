//go:build unit

package parse_test

import (
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// quotedItem's quoted fields travel as JSON strings, as encoding/json writes
// them; Count is a plain number. Key is the key, so the quoted int64 ID can
// be patched.
type quotedItem struct {
	Key   int64   `json:"key"          db:"key"   parse:"filter,sort"`
	ID    int64   `json:"id,string"    db:"id"    parse:"filter,sort,patch" patch_ops:"replace,test,copy"`
	Big   uint64  `json:"big,string"   db:"big"   parse:"filter,patch"`
	Ratio float64 `json:"ratio,string" db:"ratio" parse:"filter,patch"`
	On    bool    `json:"on,string"    db:"on"    parse:"filter,patch"`
	Opt   *int64  `json:"opt,string"   db:"opt"   parse:"filter,patch" filter_ops:"eq,in,nin,null" patch_ops:"add,replace,remove,copy"`
	Count int64   `json:"count"        db:"count" parse:"filter,patch" patch_ops:"replace,copy" max:"100"`
}

func quotedItems(t *testing.T) *parse.Resource[quotedItem] {
	t.Helper()
	r, err := parse.NewResource[quotedItem]("quoted_items", parse.PrimaryKey("key"))
	require.NoError(t, err)
	return r
}

func TestWireStringFilters(t *testing.T) {
	r := quotedItems(t)
	tests := []struct {
		name, filter string
		want         any
	}{
		{"int64 max", `{"id":"9223372036854775807"}`, int64(math.MaxInt64)},
		{"int64 min", `{"id":{"$gt":"-9223372036854775808"}}`, int64(math.MinInt64)},
		{"past 2^53, exactly", `{"id":{"$lte":"9007199254740993"}}`, int64(9007199254740993)},
		{"a set", `{"id":{"$in":["1","9007199254740993"]}}`, []any{int64(1), int64(9007199254740993)}},
		{"uint64 max", `{"big":"18446744073709551615"}`, uint64(math.MaxUint64)},
		{"a float", `{"ratio":{"$ne":"0.5"}}`, 0.5},
		{"a bool", `{"on":"true"}`, true},
		{"a bool's bare boolean, the same Go value", `{"on":false}`, false},
		{"an integral spelling", `{"id":"1.0e3"}`, int64(1000)},
		{"a pointer", `{"opt":{"$nin":["3"]}}`, []any{int64(3)}},
		{"an unquoted field keeps numbers", `{"count":7}`, int64(7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := r.ParseFilter(tt.filter)
			require.NoError(t, err)
			assert.Equal(t, tt.want, parsed.AST().Root.Value)
		})
	}

	for name, f := range map[string]string{
		"a bare number":              `{"id":9223372036854775807}`,
		"a number past 2^53":         `{"id":9007199254740993}`,
		"a bare number in a set":     `{"id":{"$in":["1",2]}}`,
		"text that is no number":     `{"id":"seven"}`,
		"text with spaces":           `{"id":" 7"}`,
		"past int64":                 `{"id":"9223372036854775808"}`,
		"a negative unsigned":        `{"big":"-1"}`,
		"a fraction for an integer":  `{"id":"1.5"}`,
		"text for an unquoted field": `{"count":"7"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := r.ParseFilter(f)
			assert.True(t, errors.Is(err, filter.ECodeValueTypeMismatch), "%s: %v", f, err)
		})
	}
	_, err := r.ParseFilter(`{"id":9}`)
	assert.ErrorContains(t, err, `json:",string"`)

	// Null tests and sorts take no field value.
	_, err = r.ParseFilter(`{"opt":{"$null":true}}`)
	assert.NoError(t, err)
	_, err = r.ParseSort("-id")
	assert.NoError(t, err)
}

// A query parameter's text is the wire form already.
func TestWireStringQueryParams(t *testing.T) {
	r := quotedItems(t)
	parsed, err := r.ParseRequestFilter(url.Values{"id": {"9223372036854775807"}})
	require.NoError(t, err)
	assert.Equal(t, int64(math.MaxInt64), parsed.AST().Root.Value)

	parsed, err = r.ParseRequestFilter(url.Values{"id_in": {"1,9007199254740993"}})
	require.NoError(t, err)
	assert.Equal(t, []any{int64(1), int64(9007199254740993)}, parsed.AST().Root.Value)

	parsed, err = r.ParseRequestFilter(url.Values{"on": {"false"}})
	require.NoError(t, err)
	assert.Equal(t, false, parsed.AST().Root.Value)

	// A bool reads its parameter as any bool parameter does.
	parsed, err = r.ParseRequestFilter(url.Values{"on": {"1"}})
	require.NoError(t, err)
	assert.Equal(t, true, parsed.AST().Root.Value)
	_, err = r.ParseRequestFilter(url.Values{"on": {"yes"}})
	assert.True(t, errors.Is(err, filter.ECodeValueTypeMismatch), "%v", err)
}

func TestWireStringPatches(t *testing.T) {
	r := quotedItems(t)
	parsed, err := r.ParseMergePatch([]byte(`{"id":"9007199254740993","big":"18446744073709551615","on":"false","ratio":"2.5","opt":null}`))
	require.NoError(t, err)
	values := map[string]any{}
	for _, op := range parsed.Operations {
		values[op.Path] = op.Value
	}
	assert.Equal(t, map[string]any{
		"/id": int64(9007199254740993), "/big": uint64(math.MaxUint64), "/on": false, "/ratio": 2.5, "/opt": nil,
	}, values, "SQL binds the numbers")

	for name, body := range map[string]string{
		"a merge patch's bare number": `{"id":9007199254740993}`,
		"a merge patch past int64":    `{"id":"9223372036854775808"}`,
		"text for an unquoted field":  `{"count":"7"}`,
		"over an unquoted bound":      `{"count":101}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := r.ParseMergePatch([]byte(body))
			assert.True(t, errors.Is(err, patch.ECodePatchValueInvalid), "%s: %v", body, err)
		})
	}

	parsed, err = r.ParsePatch([]byte(`[{"op":"replace","path":"/id","value":"-9223372036854775808"},{"op":"add","path":"/opt","value":"4"}]`))
	require.NoError(t, err)
	assert.Equal(t, int64(math.MinInt64), parsed.Operations[0].Value)
	assert.Equal(t, int64(4), parsed.Operations[1].Value)
	_, err = r.ParsePatch([]byte(`[{"op":"replace","path":"/id","value":5}]`))
	assert.True(t, errors.Is(err, patch.ECodePatchValueInvalid), "%v", err)
}

// Applied to a struct, quoted values land in their fields, whether the
// patch runs on the struct or through its JSON document.
func TestWireStringApply(t *testing.T) {
	r := quotedItems(t)
	target := quotedItem{ID: 5, Big: 1, Ratio: 0.5, On: true, Count: 2}

	got, err := r.ApplyPatch([]byte(`[
		{"op":"test","path":"/id","value":"5"},
		{"op":"replace","path":"/id","value":"9007199254740993"},
		{"op":"replace","path":"/big","value":"18446744073709551615"},
		{"op":"replace","path":"/on","value":"false"},
		{"op":"copy","from":"/id","path":"/opt"}
	]`), target)
	require.NoError(t, err)
	assert.Equal(t, quotedItem{ID: 9007199254740993, Big: math.MaxUint64, Ratio: 0.5, On: false, Opt: new(int64(9007199254740993)), Count: 2}, got)

	_, err = r.ApplyPatch([]byte(`[{"op":"test","path":"/id","value":"4"}]`), target)
	assert.True(t, errors.Is(err, patch.ECodePatchValueInvalid), "a failed test: %v", err)
	_, err = r.ParsePatch([]byte(`[{"op":"copy","from":"/id","path":"/count"}]`))
	assert.True(t, errors.Is(err, patch.ECodePatchValueInvalid), "a quoted value into a number, refused at parse: %v", err)

	// A test compares canonical text: "5.0" is the 5 the field holds.
	_, err = r.ApplyPatch([]byte(`[{"op":"test","path":"/id","value":"5.0"}]`), target)
	assert.NoError(t, err)

	merge, err := r.ParseMergePatch([]byte(`{"id":"-9223372036854775808","opt":"6"}`))
	require.NoError(t, err)
	got, err = r.ApplyValidatedPatch(merge, target)
	require.NoError(t, err)
	assert.Equal(t, int64(math.MinInt64), got.ID)
	assert.Equal(t, new(int64(6)), got.Opt)

	data, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"id":"-9223372036854775808"`, "responses follow encoding/json")
}

func TestWireStringSchemaDocument(t *testing.T) {
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.Register[quotedItem](reg))
	doc := reg.SchemaFor("quoted_items")
	require.NotNil(t, doc)
	for name, want := range map[string]string{"id": "string", "big": "string", "ratio": "string", "on": "string", "opt": "string", "count": ""} {
		assert.Equal(t, want, string(doc.Filter.Fields[name].Wire), "filter %s", name)
		assert.Equal(t, want, string(doc.Patch.Paths["/"+name].Wire), "patch %s", name)
	}
	data, err := json.Marshal(doc.Filter.Fields["id"])
	require.NoError(t, err)
	assert.Contains(t, string(data), `"wire":"string"`)
	data, err = json.Marshal(doc.Filter.Fields["count"])
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"wire"`)
}

// Code passes Go values, not the wire form: maps, operations and server
// patches take the number.
func TestWireStringGoValues(t *testing.T) {
	r := quotedItems(t)
	parsed, err := r.ParseFilterMap(map[string]any{"id": int64(9007199254740993), "on": true})
	require.NoError(t, err)
	assert.NotNil(t, parsed.AST().Root)

	merge, err := r.MergePatchMap(map[string]any{"id": int64(9007199254740993), "big": uint64(math.MaxUint64)})
	require.NoError(t, err)
	assert.Len(t, merge.Operations, 2)
	_, err = r.ServerMergePatchMap(map[string]any{"id": int64(7)})
	require.NoError(t, err)
	ops, err := r.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/id", Value: int64(math.MinInt64)}})
	require.NoError(t, err)
	assert.Equal(t, int64(math.MinInt64), ops.Operations[0].Value)

	// A JSON number decoded into the map, as a request body holds one, is
	// still the bare form.
	_, err = r.ParseFilterMap(map[string]any{"id": json.Number("5")})
	assert.True(t, errors.Is(err, filter.ECodeValueTypeMismatch), "%v", err)
}

// A test op's validators see the number, so a quoted field's bounds apply.
func TestWireStringTestValidates(t *testing.T) {
	type bounded struct {
		ID  int64 `json:"id"         db:"id"`
		Qty int64 `json:"qty,string" db:"qty" parse:"patch" patch_ops:"replace,test" min:"1" max:"9007199254740993"`
	}
	r, err := parse.NewResource[bounded]("bounded")
	require.NoError(t, err)
	_, err = r.ParsePatch([]byte(`[{"op":"test","path":"/qty","value":"9007199254740993"}]`))
	assert.NoError(t, err)
	_, err = r.ParsePatch([]byte(`[{"op":"test","path":"/qty","value":"9007199254740994"}]`))
	assert.True(t, errors.Is(err, patch.ECodePatchValueInvalid), "%v", err)
}

type wireAuthor struct {
	ID   int64  `json:"id,string" db:"id"`
	Name string `json:"name"      db:"name" parse:"filter"`
}

type wireBook struct {
	ID       int64  `json:"id,string"        db:"id"`
	AuthorID *int64 `json:"author_id,string" db:"author_id" parse:"filter"`
}

// A json:",string" key reaches the request as text even untagged for
// filtering, and a relation's shorthand compares its target's key that way.
func TestWireStringKeysAndRelations(t *testing.T) {
	authors, err := parse.NewResource[wireAuthor]("authors")
	require.NoError(t, err)
	books, err := parse.NewResource[wireBook]("books")
	require.NoError(t, err)
	require.NoError(t, books.Relate("author", parse.BelongsTo(authors, "author_id")))

	_, err = books.ParseFilter(`{"author":"9007199254740993"}`)
	assert.NoError(t, err)
	_, err = books.ParseFilter(`{"author":{"$in":["1","2"]}}`)
	assert.NoError(t, err)
	_, err = books.ParseFilter(`{"author":9007199254740993}`)
	assert.True(t, errors.Is(err, filter.ECodeValueTypeMismatch), "a bare key: %v", err)
	_, err = books.ParseFilter(`{"author_id":{"$in":["1","2"]}}`)
	assert.NoError(t, err)
	_, err = authors.ParseFilter(`{"id":"9007199254740993"}`)
	assert.NoError(t, err, "the key, untagged for filtering")
	_, err = authors.ParseFilter(`{"id":9007199254740993}`)
	assert.True(t, errors.Is(err, filter.ECodeValueTypeMismatch), "the bare key: %v", err)
}

type wireSpec struct {
	Qty int64 `json:"qty,string"`
	Max int64 `json:"max"`
}

// A path declared onto a quoted struct member, in a tag or at runtime, is
// refused: the document holds it as text its path reads as NULL.
func TestWireStringDeclaredMemberRefused(t *testing.T) {
	type tagged struct {
		ID   string   `json:"id"   db:"id"`
		Spec wireSpec `json:"spec" db:"spec" parse:"filter" paths:"qty:int"`
	}
	_, err := parse.NewResource[tagged]("tagged")
	assert.ErrorContains(t, err, `json:",string"`)

	type plain struct {
		ID   string   `json:"id"   db:"id"`
		Spec wireSpec `json:"spec" db:"spec" parse:"filter"`
	}
	r, err := parse.NewResource[plain]("plain")
	require.NoError(t, err)
	_, err = r.WithPaths(filter.PathDecl{Path: "spec.qty", Type: core.FieldTypeInt})
	assert.ErrorContains(t, err, `json:",string"`)
	_, err = r.WithPaths(filter.PathDecl{Path: "spec.max", Type: core.FieldTypeInt})
	assert.NoError(t, err)
}
