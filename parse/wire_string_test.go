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
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// quotedItem's quoted fields travel as JSON strings, as encoding/json writes
// them; Count is a plain number.
type quotedItem struct {
	ID    int64   `json:"id,string"    db:"id"    parse:"filter,sort,patch" patch_ops:"replace,test,copy"`
	Big   uint64  `json:"big,string"   db:"big"   parse:"filter,patch"`
	Ratio float64 `json:"ratio,string" db:"ratio" parse:"filter,patch"`
	On    bool    `json:"on,string"    db:"on"    parse:"filter,patch"`
	Opt   *int64  `json:"opt,string"   db:"opt"   parse:"filter,patch" filter_ops:"eq,in,nin,null" patch_ops:"add,replace,remove,copy"`
	Count int64   `json:"count"        db:"count" parse:"filter,patch" patch_ops:"replace,copy" max:"100"`
}

func quotedItems(t *testing.T) *parse.Resource[quotedItem] {
	t.Helper()
	r, err := parse.NewResource[quotedItem]("quoted_items")
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
		{"a pointer", `{"opt":{"$nin":["3"]}}`, []any{int64(3)}},
		{"an unquoted field keeps numbers", `{"count":7}`, int64(7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := r.ParseFilter(tt.filter)
			require.NoError(t, err)
			assert.Equal(t, tt.want, parsed.AST.Root.Value)
		})
	}

	for name, f := range map[string]string{
		"a bare number":              `{"id":9223372036854775807}`,
		"a number past 2^53":         `{"id":9007199254740993}`,
		"a bare number in a set":     `{"id":{"$in":["1",2]}}`,
		"a bare bool":                `{"on":true}`,
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
	assert.Equal(t, int64(math.MaxInt64), parsed.AST.Root.Value)

	parsed, err = r.ParseRequestFilter(url.Values{"id_in": {"1,9007199254740993"}})
	require.NoError(t, err)
	assert.Equal(t, []any{int64(1), int64(9007199254740993)}, parsed.AST.Root.Value)

	parsed, err = r.ParseRequestFilter(url.Values{"on": {"false"}})
	require.NoError(t, err)
	assert.Equal(t, false, parsed.AST.Root.Value)

	_, err = r.ParseRequestFilter(url.Values{"on": {"1"}})
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
		"a merge patch's bare bool":   `{"on":false}`,
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
	_, err = r.ApplyPatch([]byte(`[{"op":"copy","from":"/id","path":"/count"}]`), target)
	assert.True(t, errors.Is(err, patch.ECodePatchValueInvalid), "a quoted value into a number: %v", err)

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
