//go:build unit

package patch_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type declaredLabel string

func (v declaredLabel) MarshalJSON() ([]byte, error) { return json.Marshal("encoded:" + string(v)) }
func (v *declaredLabel) UnmarshalJSON(raw []byte) error {
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return err
	}
	if !strings.HasPrefix(encoded, "encoded:") {
		return fmt.Errorf("missing encoded prefix")
	}
	*v = declaredLabel(strings.TrimPrefix(encoded, "encoded:"))
	return nil
}

type declaredItems []string

func (v declaredItems) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string][]string{"items": v})
}
func (v *declaredItems) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Items []string `json:"items"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*v = wire.Items
	return nil
}

type declaredPair [2]string

func (v declaredPair) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string][2]string{"pair": v})
}

type declaredObjectLabel string

func (v declaredObjectLabel) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"label": string(v)})
}
func (v *declaredObjectLabel) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*v = declaredObjectLabel(wire.Label)
	return nil
}
func (v *declaredPair) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Pair [2]string `json:"pair"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*v = wire.Pair
	return nil
}

type declaredWords []string

func (v declaredWords) MarshalJSON() ([]byte, error) {
	wire := make([]string, len(v))
	for i, word := range v {
		wire[i] = "encoded:" + word
	}
	return json.Marshal(wire)
}
func (v *declaredWords) UnmarshalJSON(raw []byte) error {
	var wire []declaredLabel
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*v = make(declaredWords, len(wire))
	for i, word := range wire {
		(*v)[i] = string(word)
	}
	return nil
}

type declaredNode struct {
	Label    declaredLabel `json:"label"`
	Width    int           `json:"w"`
	Height   int           `json:"h"`
	Next     *declaredNode `json:"next,omitempty"`
	Optional string        `json:"optional,omitempty"`
	Zero     int           `json:"zero,omitzero"`
	Quoted   int           `json:"quoted,string"`
}

type declaredMapKey string

func (v *declaredMapKey) UnmarshalText(raw []byte) error {
	*v = declaredMapKey(strings.ToLower(string(raw)))
	return nil
}
func (v declaredMapKey) MarshalText() ([]byte, error) { return []byte(v), nil }

type declaredDecodedInt int

func (v *declaredDecodedInt) UnmarshalJSON(raw []byte) error {
	var decoded json.Number
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	number, err := decoded.Int64()
	if err != nil {
		return err
	}
	*v = declaredDecodedInt(number)
	return nil
}

type declaredDecodedString string

func (v *declaredDecodedString) UnmarshalJSON(raw []byte) error {
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*v = declaredDecodedString(decoded)
	return nil
}

type declaredPointerMarshal int

func (*declaredPointerMarshal) MarshalJSON() ([]byte, error) { return json.Marshal("custom") }

func TestDeclaredJSONReplacementQuotedDefaults(t *testing.T) {
	type row struct {
		Int     declaredDecodedInt     `json:"int,string"`
		Text    declaredDecodedString  `json:"text,string"`
		Pointer declaredPointerMarshal `json:"pointer,string"`
	}
	zero, err := json.Marshal(row{})
	require.NoError(t, err)
	for _, tt := range []struct{ body, want string }{{`{}`, string(zero)}, {`{"int":"3","text":"\"x\"","pointer":"4"}`, `{"int":"3","text":"\"x\"","pointer":"4"}`}} {
		parsed, err := declaredJSONParser(reflect.TypeFor[row]()).Parse([]byte(`[{"op":"replace","path":"/value","value":` + tt.body + `}]`))
		require.NoError(t, err)
		raw, err := json.Marshal(parsed.Operations[0].Value)
		require.NoError(t, err)
		var want, got row
		require.NoError(t, json.Unmarshal([]byte(tt.want), &want))
		require.NoError(t, json.Unmarshal(raw, &got), "the normalized document must fit the actual storage decoder")
		assert.Equal(t, want, got)
		assert.JSONEq(t, tt.want, string(raw))
	}
}

type declaredRejectZero string

func (v *declaredRejectZero) UnmarshalJSON(raw []byte) error {
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	if decoded == "" {
		return fmt.Errorf("empty custom value is not representable")
	}
	*v = declaredRejectZero(decoded)
	return nil
}

func TestDeclaredJSONReplacementRejectsMissingCodecZero(t *testing.T) {
	type row struct {
		Value declaredRejectZero `json:"member"`
	}
	_, err := declaredJSONParser(reflect.TypeFor[row]()).Parse([]byte(`[{"op":"replace","path":"/value","value":{}}]`))
	require.ErrorContains(t, err, `member "member"`)
	require.ErrorContains(t, err, "empty custom value is not representable")
}

func declaredJSONParser(typ reflect.Type) *patch.Parser {
	return patch.NewParser(declaredJSONSchema(typ), nil)
}

func declaredJSONSchema(typ reflect.Type) *patch.PatchSchema {
	return &patch.PatchSchema{AllowedPaths: map[string]*patch.PathSchema{
		"/value": {GoType: typ, SQLType: core.SQLJSONB, SQLColumn: "value", DataType: core.FieldTypeJSON, AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove}},
	}}
}

func TestDeclaredJSONReplacementPreservesCustomCodecs(t *testing.T) {
	for _, tt := range []struct {
		name  string
		typ   reflect.Type
		value any
		wire  string
	}{
		{"scalar", reflect.TypeFor[declaredLabel](), declaredLabel("x"), `"encoded:x"`},
		{"slice", reflect.TypeFor[declaredItems](), declaredItems{"x"}, `{"items":["x"]}`},
		{"array", reflect.TypeFor[declaredPair](), declaredPair{"x", "y"}, `{"pair":["x","y"]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := declaredJSONParser(tt.typ)
			for _, input := range []string{"typed", "wire"} {
				t.Run(input, func(t *testing.T) {
					var parsed *patch.ParsedPatch
					var err error
					if input == "typed" {
						parsed, err = p.ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/value", Value: tt.value}})
					} else {
						parsed, err = p.Parse([]byte(`[{"op":"replace","path":"/value","value":` + tt.wire + `}]`))
					}
					require.NoError(t, err)
					for _, dialect := range []patch.SQLDialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
						plan, err := patch.BuildPatchSQL(parsed, dialect)
						require.NoError(t, err)
						require.Len(t, plan.Args, 1)
						raw, ok := plan.Args[0].(string)
						require.True(t, ok)
						assert.JSONEq(t, tt.wire, raw)
					}
				})
			}
		})
	}
}

func TestDeclaredJSONReplacementNestedCanonicalShape(t *testing.T) {
	p := declaredJSONParser(reflect.TypeFor[declaredNode]())
	parsed, err := p.Parse([]byte(`[{"op":"replace","path":"/value","value":{"label":"encoded:root","w":9,"next":{"label":"encoded:child"}}}]`))
	require.NoError(t, err)
	encoded, err := json.Marshal(parsed.Operations[0].Value)
	require.NoError(t, err)
	assert.JSONEq(t, `{"label":"encoded:root","w":9,"h":0,"quoted":"0","next":{"label":"encoded:child","w":0,"h":0,"quoted":"0"}}`, string(encoded))
	var stored declaredNode
	require.NoError(t, json.Unmarshal(encoded, &stored))
	assert.Equal(t, declaredLabel("root"), stored.Label)
	require.NotNil(t, stored.Next)
	assert.Equal(t, declaredLabel("child"), stored.Next.Label)

	for _, typ := range []reflect.Type{reflect.TypeFor[map[string]declaredNode](), reflect.TypeFor[[]declaredNode]()} {
		body := `{"one":{"label":"encoded:x"}}`
		if typ.Kind() == reflect.Slice {
			body = `[{"label":"encoded:x"}]`
		}
		parsed, err := declaredJSONParser(typ).Parse([]byte(`[{"op":"replace","path":"/value","value":` + body + `}]`))
		require.NoError(t, err)
		encoded, err := json.Marshal(parsed.Operations[0].Value)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"label":"encoded:x"`)
		assert.Contains(t, string(encoded), `"h":0`)
	}
}

func TestDeclaredJSONReplacementPresence(t *testing.T) {
	p := declaredJSONParser(reflect.TypeFor[any]())
	for _, value := range []any{map[string]any(nil), []string(nil), (*declaredNode)(nil)} {
		parsed, err := p.ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/value", Value: value}})
		require.NoError(t, err)
		assert.Nil(t, parsed.Operations[0].Value)
	}
	_, err := p.ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/value", Value: json.RawMessage("null")}})
	require.ErrorContains(t, err, "use json.RawMessage to preserve JSON null")

	p = declaredJSONParser(reflect.TypeFor[json.RawMessage]())
	parsed, err := p.ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/value", Value: json.RawMessage("null")}})
	require.NoError(t, err)
	raw, err := json.Marshal(parsed.Operations[0].Value)
	require.NoError(t, err)
	assert.Equal(t, "null", string(raw))

	for _, typ := range []reflect.Type{reflect.TypeFor[map[string]any](), reflect.TypeFor[[]string]()} {
		p = declaredJSONParser(typ)
		parsed, err = p.ParseOps([]patch.RawPatchOp{{Op: "remove", Path: "/value"}})
		require.NoError(t, err)
		plan, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
		require.NoError(t, err)
		assert.NotContains(t, plan.SetClause, "NULL")
		require.Len(t, plan.Args, 1)
		assert.Contains(t, []any{"{}", "[]"}, plan.Args[0])
	}
}

func TestDeclaredJSONReplacementMapKeys(t *testing.T) {
	p := declaredJSONParser(reflect.TypeFor[map[int]declaredNode]())
	parsed, err := p.Parse([]byte(`[{"op":"replace","path":"/value","value":{"01":{"label":"encoded:x"}}}]`))
	require.NoError(t, err)
	raw, err := json.Marshal(parsed.Operations[0].Value)
	require.NoError(t, err)
	assert.JSONEq(t, `{"1":{"label":"encoded:x","w":0,"h":0,"quoted":"0"}}`, string(raw))
	_, err = p.Parse([]byte(`[{"op":"replace","path":"/value","value":{"01":{"label":"encoded:first"},"1":{"label":"encoded:second"}}}]`))
	require.ErrorContains(t, err, `JSON map keys "01" and "1" both encode as "1"`)

	p = declaredJSONParser(reflect.TypeFor[map[declaredMapKey]declaredNode]())
	parsed, err = p.Parse([]byte(`[{"op":"replace","path":"/value","value":{"UPPER":{"label":"encoded:x"}}}]`))
	require.NoError(t, err)
	raw, err = json.Marshal(parsed.Operations[0].Value)
	require.NoError(t, err)
	assert.JSONEq(t, `{"upper":{"label":"encoded:x","w":0,"h":0,"quoted":"0"}}`, string(raw))
	_, err = p.Parse([]byte(`[{"op":"replace","path":"/value","value":{"UPPER":{"label":"encoded:first"},"upper":{"label":"encoded:second"}}}]`))
	require.ErrorContains(t, err, `JSON map keys "UPPER" and "upper" both encode as "upper"`)
}

func TestDeclaredJSONMergeCustomMembers(t *testing.T) {
	for _, tt := range []struct {
		typ   reflect.Type
		typed any
		wire  string
	}{
		{reflect.TypeFor[map[string]declaredLabel](), declaredLabel("x"), `{"one":"encoded:x"}`},
		{reflect.TypeFor[map[string]declaredWords](), declaredWords{"x"}, `{"one":["encoded:x"]}`},
	} {
		t.Run(tt.typ.String(), func(t *testing.T) {
			schema := declaredJSONSchema(tt.typ)
			parsed, err := patch.ParseMergePatchMap(map[string]any{"value": map[string]any{"one": tt.typed}}, schema, nil)
			require.NoError(t, err)
			assert.True(t, parsed.Operations[0].Merge)
			raw, err := json.Marshal(parsed.Operations[0].Value)
			require.NoError(t, err)
			assert.JSONEq(t, tt.wire, string(raw))
			wire, err := patch.ParseMergePatch([]byte(`{"value":`+tt.wire+`}`), schema, nil)
			require.NoError(t, err)
			assert.Equal(t, parsed.Operations[0].Value, wire.Operations[0].Value)
			_, err = patch.ParseMergePatch([]byte(`{"value":{"one":"missing-prefix"}}`), schema, nil)
			require.Error(t, err)
		})
	}
	parsed, err := patch.ParseMergePatchMap(map[string]any{"value": map[string]any{"label": declaredLabel("x"), "w": 9}}, declaredJSONSchema(reflect.TypeFor[declaredNode]()), nil)
	require.NoError(t, err)
	raw, err := json.Marshal(parsed.Operations[0].Value)
	require.NoError(t, err)
	assert.JSONEq(t, `{"label":"encoded:x","w":9}`, string(raw), "merge retains only supplied members")
}

func TestDeclaredJSONMergeRejectsCustomObjects(t *testing.T) {
	type parent struct {
		Items declaredItems `json:"items"`
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[parent](), reflect.TypeFor[map[string]declaredItems](), reflect.TypeFor[map[string]any]()} {
		_, err := patch.ParseMergePatchMap(map[string]any{"value": map[string]any{"items": declaredItems{"x"}}}, declaredJSONSchema(typ), nil)
		require.ErrorContains(t, err, "use JSON Patch replace")
	}
	_, err := patch.ParseMergePatchMap(map[string]any{"value": map[string]any{"one": declaredObjectLabel("x")}}, declaredJSONSchema(reflect.TypeFor[map[string]any]()), nil)
	require.ErrorContains(t, err, "use JSON Patch replace")
	parsed, err := patch.ParseMergePatchMap(map[string]any{"value": declaredItems{"x"}}, declaredJSONSchema(reflect.TypeFor[declaredItems]()), nil)
	require.NoError(t, err)
	assert.False(t, parsed.Operations[0].Merge, "a top-level typed value replaces its complete encoded document")
	raw, err := json.Marshal(parsed.Operations[0].Value)
	require.NoError(t, err)
	assert.JSONEq(t, `{"items":["x"]}`, string(raw))
}

func TestDeclaredJSONMergeCanonicalMapKeys(t *testing.T) {
	schema := declaredJSONSchema(reflect.TypeFor[map[declaredMapKey]declaredLabel]())
	parsed, err := patch.ParseMergePatchMap(map[string]any{"value": map[string]any{"UPPER": declaredLabel("x"), "REMOVE": nil}}, schema, nil)
	require.NoError(t, err)
	raw, err := json.Marshal(parsed.Operations[0].Value)
	require.NoError(t, err)
	assert.JSONEq(t, `{"upper":"encoded:x","remove":null}`, string(raw))
	_, err = patch.ParseMergePatchMap(map[string]any{"value": map[string]any{"UPPER": declaredLabel("x"), "upper": nil}}, schema, nil)
	require.ErrorContains(t, err, `JSON map keys "UPPER" and "upper" both encode as "upper"`)
	_, err = patch.ParseMergePatchMap(map[string]any{"value": map[string]any{"1": declaredLabel("x")}}, declaredJSONSchema(reflect.TypeFor[map[int]declaredLabel]()), nil)
	require.ErrorContains(t, err, "object merge requires string map keys")
}
