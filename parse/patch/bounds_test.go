//go:build unit

package patch_test

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

type boundedDoc struct {
	Currency string          `json:"currency" parse:"patch" minlen:"3" maxlen:"3"`
	Note     *string         `json:"note"     parse:"patch" maxlen:"4"`
	Codes    []string        `json:"codes"    parse:"patch" maxlen:"2"`
	Level    int8            `json:"level"    parse:"patch" min:"1" max:"10"`
	Count    uint64          `json:"count"    parse:"patch" max:"18446744073709551615"`
	Offset   int64           `json:"offset"   parse:"patch" min:"-9223372036854775808" max:"-1"`
	Rate     float32         `json:"rate"     parse:"patch" min:"0" max:"0.1"`
	Ratio    float64         `json:"ratio"    parse:"patch" min:"-1.5" max:"1.5"`
	Price    decimal.Decimal `json:"price"    parse:"patch" min:"0.01" max:"999.99"`
	Scores   []int16         `json:"scores"   parse:"patch" min:"0" max:"100"`
}

func boundedSchema(t *testing.T) *patch.PatchSchema {
	t.Helper()
	schema, err := patch.GeneratePatchSchema[boundedDoc](core.NewSchemaGenerator())
	require.NoError(t, err)
	return schema
}

// Bounds hold on every route a value takes into a path: a merge patch, a
// JSON Patch replace, and an element added to an array path.
func TestBounds_ValidatePatchValues(t *testing.T) {
	schema := boundedSchema(t)
	tests := []struct {
		name  string
		merge string
		want  string // empty: admitted
	}{
		{name: "length within", merge: `{"currency":"USD"}`},
		{name: "length counts code points, not bytes", merge: `{"currency":"€£¥"}`},
		{name: "length counts code points beyond the BMP", merge: `{"currency":"😀ab"}`},
		{name: "too long", merge: `{"currency":"USDX"}`, want: "4 characters is more than the maximum length 3"},
		{name: "too short", merge: `{"currency":"US"}`, want: "2 characters is fewer than the minimum length 3"},
		{name: "null clears a bounded nullable field", merge: `{"note":null}`},
		{name: "each element's length", merge: `{"codes":["ab","c"]}`},
		{name: "an element too long", merge: `{"codes":["ab","abc"]}`, want: "3 characters is more than the maximum length 2"},
		{name: "int at its bounds", merge: `{"level":10}`},
		{name: "int above max", merge: `{"level":11}`, want: "11 is greater than the maximum 10"},
		{name: "int below min", merge: `{"level":0}`, want: "0 is less than the minimum 1"},
		{name: "uint at the top of its range", merge: `{"count":18446744073709551615}`},
		{name: "int64 at the bottom of its range", merge: `{"offset":-9223372036854775808}`},
		{name: "int64 above max", merge: `{"offset":0}`, want: "0 is greater than the maximum -1"},
		{name: "float32 bound at float32 precision", merge: `{"rate":0.1}`},
		{name: "float32 above max", merge: `{"rate":0.11}`, want: "0.11 is greater than the maximum 0.1"},
		{name: "float64 within", merge: `{"ratio":-1.5}`},
		{name: "float64 below min", merge: `{"ratio":-1.6}`, want: "-1.6 is less than the minimum -1.5"},
		{name: "decimal at max", merge: `{"price":"999.99"}`},
		{name: "decimal exactly above max", merge: `{"price":"999.990000000000000001"}`, want: "999.990000000000000001 is greater than the maximum 999.99"},
		{name: "decimal in exponent notation", merge: `{"price":"9.9999e2"}`},
		{name: "decimal as a JSON number", merge: `{"price":0.01}`},
		{name: "decimal below min", merge: `{"price":"0.001"}`, want: "0.001 is less than the minimum 0.01"},
		{name: "each element's value", merge: `{"scores":[0,100]}`},
		{name: "an element out of range", merge: `{"scores":[0,101]}`, want: "101 is greater than the maximum 100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := patch.ParseMergePatch([]byte(tt.merge), schema, nil)
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code)
			assert.ErrorContains(t, err, tt.want)
		})
	}

	p := patch.NewParser(schema, nil)
	t.Run("JSON Patch replace", func(t *testing.T) {
		_, err := p.Parse([]byte(`[{"op":"replace","path":"/currency","value":"EURO"}]`))
		assert.ErrorContains(t, err, "more than the maximum length 3")
	})
	t.Run("an element appended to an array", func(t *testing.T) {
		_, err := p.Parse([]byte(`[{"op":"array_append","path":"/scores","value":7}]`))
		assert.NoError(t, err)
		_, err = p.Parse([]byte(`[{"op":"array_append","path":"/codes","value":"abc"}]`))
		assert.ErrorContains(t, err, "more than the maximum length 2")
	})
}

func TestBounds_AdvertisedOnThePath(t *testing.T) {
	paths := boundedSchema(t).AllowedPaths
	assert.Equal(t, core.Bounds{MinLen: new(3), MaxLen: new(3)}, paths["/currency"].Bounds)
	assert.Equal(t, core.Bounds{Min: "0.01", Max: "999.99"}, paths["/price"].Bounds)
	assert.Equal(t, core.Bounds{Min: "1", Max: "10"}, paths["/level"].Bounds)
}

// A bound the field's type cannot hold fails schema generation.
func TestBounds_RefusedOnTypesTheyCannotBound(t *testing.T) {
	type lengthOnInt struct {
		N int `json:"n" parse:"patch" maxlen:"3"`
	}
	type numberOnString struct {
		S string `json:"s" parse:"patch" max:"3"`
	}
	type numberOnDocument struct {
		M map[string]any `json:"m" parse:"patch" max:"3"`
	}
	type onRelation struct {
		Tags []string `json:"tags" db:"-" parse:"patch" relation:"doc_tags,doc_id,tag_id" maxlen:"8"`
	}
	g := core.NewSchemaGenerator()
	_, err := patch.GeneratePatchSchema[lengthOnInt](g)
	assert.ErrorContains(t, err, "field N: minlen and maxlen bound strings and arrays of strings, not int")
	_, err = patch.GeneratePatchSchema[numberOnString](g)
	assert.ErrorContains(t, err, "field S: min and max bound numbers and arrays of numbers, not string")
	_, err = patch.GeneratePatchSchema[numberOnDocument](g)
	assert.ErrorContains(t, err, "field M: min and max bound numbers")
	_, err = patch.GeneratePatchSchema[onRelation](g)
	assert.ErrorContains(t, err, "field Tags: minlen, maxlen, min, max, minitems and maxitems do not apply to a relation")

	type countOnString struct {
		S string `json:"s" parse:"patch" minitems:"1"`
	}
	type countOnRelation struct {
		Tags []string `json:"tags" db:"-" parse:"patch" relation:"doc_tags,doc_id,tag_id" minitems:"1"`
	}
	_, err = patch.GeneratePatchSchema[countOnString](g)
	assert.ErrorContains(t, err, "field S: minitems and maxitems bound arrays, not string")
	_, err = patch.GeneratePatchSchema[countOnRelation](g)
	assert.ErrorContains(t, err, "do not apply to a relation")

	type removeFromRequired struct {
		Zones []string `json:"zones" parse:"patch" patch_ops:"replace,remove" minitems:"1"`
	}
	_, err = patch.GeneratePatchSchema[removeFromRequired](g)
	assert.ErrorContains(t, err, "field Zones: patch operation remove empties the array, which minitems 1 refuses")
}

type countedDoc struct {
	Zones  []string  `json:"zones"  parse:"patch" minitems:"1" maxitems:"3"`
	Spares []int32   `json:"spares" parse:"patch" maxitems:"2"`
	Hosts  *[]string `json:"hosts"  parse:"patch" minitems:"1"`
	Codes  []string  `json:"codes"  parse:"patch" minitems:"1" maxlen:"2"`
}

func countedSchema(t *testing.T) *patch.PatchSchema {
	t.Helper()
	schema, err := patch.GeneratePatchSchema[countedDoc](core.NewSchemaGenerator())
	require.NoError(t, err)
	return schema
}

// An array set whole holds its path's element counts, and a path that must
// hold an element cannot be cleared: a slice clears to empty.
func TestItems_ValidateArraysSetWhole(t *testing.T) {
	schema := countedSchema(t)
	tests := []struct {
		name  string
		merge string
		want  string // empty: admitted
	}{
		{name: "fewest", merge: `{"zones":["a"]}`},
		{name: "most", merge: `{"zones":["a","b","c"]}`},
		{name: "empty", merge: `{"zones":[]}`, want: "0 elements is fewer than the minimum 1"},
		{name: "too many", merge: `{"zones":["a","b","c","d"]}`, want: "4 elements is more than the maximum 3"},
		{name: "null on a path that must hold an element", merge: `{"zones":null}`, want: "field cannot be cleared to an empty array (minitems 1)"},
		{name: "null clears a path with no least count", merge: `{"spares":null}`},
		{name: "empty under a greatest count", merge: `{"spares":[]}`},
		{name: "too many numbers", merge: `{"spares":[1,2,3]}`, want: "3 elements is more than the maximum 2"},
		{name: "null clears a pointer path to NULL", merge: `{"hosts":null}`},
		{name: "a pointer path's array counted", merge: `{"hosts":[]}`, want: "0 elements is fewer than the minimum 1"},
		{name: "element bounds beside the count", merge: `{"codes":["abc"]}`, want: "3 characters is more than the maximum length 2"},
		{name: "count beside element bounds", merge: `{"codes":[]}`, want: "0 elements is fewer than the minimum 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := patch.ParseMergePatch([]byte(tt.merge), schema, nil)
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code)
			assert.ErrorContains(t, err, tt.want)
		})
	}

	t.Run("a refusal names the path", func(t *testing.T) {
		for _, merge := range []string{`{"zones":null}`, `{"zones":[]}`} {
			_, err := patch.ParseMergePatch([]byte(merge), schema, nil)
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, "/zones", pe.Path, merge)
		}
	})

	p := patch.NewParser(schema, nil)
	jsonPatch := []struct {
		name string
		ops  string
		want string
	}{
		{name: "replace whole", ops: `[{"op":"replace","path":"/zones","value":["a","b"]}]`},
		{name: "replace with too many", ops: `[{"op":"replace","path":"/zones","value":["a","b","c","d"]}]`, want: "4 elements is more than the maximum 3"},
		{name: "add an empty array", ops: `[{"op":"add","path":"/zones","value":[]}]`, want: "0 elements is fewer than the minimum 1"},
		{name: "remove a path that must hold an element", ops: `[{"op":"remove","path":"/zones"}]`, want: "operation 'remove' not allowed on path '/zones'"},
		{name: "remove a path with no least count", ops: `[{"op":"remove","path":"/spares"}]`},
		// The parser does not see the array an element joins; the store counts it.
		{name: "an element appended", ops: `[{"op":"array_append","path":"/zones","value":"d"}]`},
	}
	for _, tt := range jsonPatch {
		t.Run("JSON Patch "+tt.name, func(t *testing.T) {
			_, err := p.Parse([]byte(tt.ops))
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestItems_AdvertisedOnThePath(t *testing.T) {
	paths := countedSchema(t).AllowedPaths
	assert.Equal(t, core.Bounds{MinItems: new(1), MaxItems: new(3)}, paths["/zones"].Bounds)
	assert.Equal(t, core.Bounds{MaxItems: new(2)}, paths["/spares"].Bounds)
	assert.False(t, paths["/zones"].Nullable(), "a slice that must hold an element has no empty value to clear to")
	assert.True(t, paths["/spares"].Nullable())
	assert.True(t, paths["/hosts"].Nullable(), "a pointer clears to NULL")
	assert.False(t, paths["/zones"].AllowsOp(patch.PatchOpRemove), "remove is not offered where it is refused")
	assert.True(t, paths["/spares"].AllowsOp(patch.PatchOpRemove))
	assert.Empty(t, paths["/zones"].Validators, "element counts are not a per-value validator")
}
