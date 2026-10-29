//go:build unit

package parse_test

// Go⇔TS merge-patch conformance corpus. The Go parser is
// authoritative: each input below is parsed by ParseMergePatch against
// confPatch's schema and its validity and error code recorded in
// testdata/patch_conformance.json, with the registry's schema response the
// TS validator reads. sdk/ts/src/patch/conformance.test.ts must agree on
// every case the client can judge (tsApplicable).
//
// Regenerate after an intentional change:
//
//	go test -tags=unit -run TestEmitPatchConformance -update ./

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

const patchConformancePath = "testdata/patch_conformance.json"

type confPatch struct {
	ID       string          `json:"id"        db:"id"`
	Name     string          `json:"name"      db:"name"      parse:"patch"`
	Nick     *string         `json:"nick"      db:"nick"      parse:"patch"`
	Status   string          `json:"status"    db:"status"    parse:"patch" enum:"active,offline"`
	Count    int32           `json:"count"     db:"count"     parse:"patch"`
	Small    int8            `json:"small"     db:"small"     parse:"patch"`
	Ratio    float64         `json:"ratio"     db:"ratio"     parse:"patch"`
	Price    decimal.Decimal `json:"price"     db:"price"     parse:"patch"`
	Active   bool            `json:"active"    db:"active"    parse:"patch"`
	Tags     []string        `json:"tags"      db:"tags"      parse:"patch"`
	Scores   []int16         `json:"scores"    db:"scores"    parse:"patch"`
	Labels   map[string]any  `json:"labels"    db:"labels"    parse:"patch"`
	SeenAt   *time.Time      `json:"seen_at"   db:"seen_at"   parse:"patch"`
	Readonly string          `json:"readonly"  db:"readonly"`
	Modes    []string        `json:"modes"     db:"modes"     parse:"patch" enum:"auto,manual"`
	Mode     *string         `json:"mode"      db:"mode"      parse:"patch" enum:"auto,manual"`
	Currency string          `json:"currency"  db:"currency"  parse:"patch" minlen:"3" maxlen:"3"`
	Level    int32           `json:"level"     db:"level"     parse:"patch" min:"1" max:"10"`
	Rate     float32         `json:"rate"      db:"rate"      parse:"patch" min:"0" max:"0.1"`
	Amount   decimal.Decimal `json:"amount"    db:"amount"    parse:"patch" min:"0.01" max:"999.99"`
	Codes    []string        `json:"codes"     db:"codes"     parse:"patch" maxlen:"2"`
	Zones    []string        `json:"zones"     db:"zones"     parse:"patch" minitems:"1" maxitems:"2"`
}

type patchCase struct {
	Name         string         `json:"name"`
	Input        map[string]any `json:"input"`
	Valid        bool           `json:"valid"`
	ErrorCode    string         `json:"errorCode,omitempty"`
	TSApplicable *bool          `json:"tsApplicable,omitempty"`
}

type patchCorpus struct {
	Schema *parse.SchemaResponse `json:"schema"`
	Cases  []patchCase           `json:"cases"`
}

func patchInputs() []patchCase {
	no := false
	return []patchCase{
		{Name: "string", Input: map[string]any{"name": "a"}},
		{Name: "err_string_type", Input: map[string]any{"name": 3}},
		{Name: "err_null_on_value_field", Input: map[string]any{"name": nil}},
		{Name: "null_on_pointer_field", Input: map[string]any{"nick": nil}},
		{Name: "enum", Input: map[string]any{"status": "active"}},
		{Name: "err_enum", Input: map[string]any{"status": "busy"}},
		{Name: "int", Input: map[string]any{"count": 5}},
		{Name: "err_int_fraction", Input: map[string]any{"count": 1.5}},
		{Name: "err_int_type", Input: map[string]any{"count": "5"}},
		{Name: "err_int32_range", Input: map[string]any{"count": 2147483648}},
		{Name: "err_int8_range", Input: map[string]any{"small": 128}},
		{Name: "int8_min", Input: map[string]any{"small": -128}},
		{Name: "float", Input: map[string]any{"ratio": 0.5}},
		{Name: "err_float_type", Input: map[string]any{"ratio": "half"}},
		{Name: "decimal_string", Input: map[string]any{"price": "10.25"}},
		{Name: "decimal_number", Input: map[string]any{"price": 10.5}},
		{Name: "decimal_exponent", Input: map[string]any{"price": "1.5e2"}},
		{Name: "err_decimal_word", Input: map[string]any{"price": "ten"}},
		{Name: "err_decimal_bool", Input: map[string]any{"price": true}},
		{Name: "err_decimal_null", Input: map[string]any{"price": nil}},
		{Name: "err_decimal_leading_plus", Input: map[string]any{"price": "+1"}},
		{Name: "err_decimal_beyond_digits", Input: map[string]any{"price": "1e1000"}},
		{Name: "bool", Input: map[string]any{"active": false}},
		{Name: "err_bool_type", Input: map[string]any{"active": "false"}},
		{Name: "array", Input: map[string]any{"tags": []any{"a", "b"}}},
		{Name: "err_array_type", Input: map[string]any{"tags": "a"}},
		{Name: "err_array_element_type", Input: map[string]any{"tags": []any{"a", 1}}},
		{Name: "err_array_element_range", Input: map[string]any{"scores": []any{1, 40000}}},
		{Name: "clear_array", Input: map[string]any{"tags": nil}},
		{Name: "document_merge", Input: map[string]any{"labels": map[string]any{"a": 1, "old": nil}}},
		{Name: "timestamp", Input: map[string]any{"seen_at": "2026-10-19T12:00:00Z"}},
		{Name: "err_timestamp_format", Input: map[string]any{"seen_at": "yesterday"}, TSApplicable: &no},
		{Name: "err_timestamp_type", Input: map[string]any{"seen_at": 5}},
		{Name: "err_not_patchable", Input: map[string]any{"readonly": "x"}},
		{Name: "err_unknown_field", Input: map[string]any{"colour": "red"}},
		{Name: "several", Input: map[string]any{"name": "a", "count": 1, "active": true}},
		{Name: "array_enum", Input: map[string]any{"modes": []any{"auto", "manual"}}},
		{Name: "err_array_enum", Input: map[string]any{"modes": []any{"auto", "off"}}},
		{Name: "null_clears_nullable_enum", Input: map[string]any{"mode": nil}},
		{Name: "length_bounds", Input: map[string]any{"currency": "USD"}},
		{Name: "length_counts_code_points", Input: map[string]any{"currency": "€😀ß"}},
		{Name: "err_maxlen", Input: map[string]any{"currency": "USDX"}},
		{Name: "err_minlen", Input: map[string]any{"currency": "US"}},
		{Name: "int_bounds", Input: map[string]any{"level": 10}},
		{Name: "err_int_max", Input: map[string]any{"level": 11}},
		{Name: "err_int_min", Input: map[string]any{"level": 0}},
		{Name: "float32_bound_at_its_precision", Input: map[string]any{"rate": 0.1}},
		{Name: "err_float_max", Input: map[string]any{"rate": 0.11}},
		{Name: "decimal_bounds", Input: map[string]any{"amount": "999.99"}},
		{Name: "decimal_bound_exponent", Input: map[string]any{"amount": "9.9999e2"}},
		{Name: "decimal_bound_number", Input: map[string]any{"amount": 0.01}},
		{Name: "err_decimal_max_exact", Input: map[string]any{"amount": "999.990000000000000001"}},
		{Name: "err_decimal_min", Input: map[string]any{"amount": "0.001"}},
		{Name: "array_element_length", Input: map[string]any{"codes": []any{"ab", "c"}}},
		{Name: "err_array_element_length", Input: map[string]any{"codes": []any{"ab", "abc"}}},
		{Name: "items_bounds", Input: map[string]any{"zones": []any{"a", "b"}}},
		{Name: "err_items_min", Input: map[string]any{"zones": []any{}}},
		{Name: "err_items_max", Input: map[string]any{"zones": []any{"a", "b", "c"}}},
		{Name: "err_items_clear", Input: map[string]any{"zones": nil}},
	}
}

func buildPatchCorpus(t *testing.T) []byte {
	t.Helper()
	res := parse.MustResource[confPatch]("conf_patches")
	reg := parse.NewSchemaRegistry()
	parse.RegisterResource(reg, res)
	out := patchCorpus{Schema: reg.SchemaFor("conf_patches")}
	for _, c := range patchInputs() {
		body, err := json.Marshal(c.Input)
		require.NoError(t, err)
		_, err = res.ParseMergePatch(body)
		c.Valid = err == nil
		if pe, ok := errors.AsType[*core.ParseError](err); ok {
			c.ErrorCode = pe.Code
		} else if err != nil {
			t.Fatalf("case %s: not a parse error: %v", c.Name, err)
		}
		out.Cases = append(out.Cases, c)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	return append(data, '\n')
}

func TestEmitPatchConformance(t *testing.T) {
	got := buildPatchCorpus(t)
	if *updateConformance {
		require.NoError(t, os.WriteFile(patchConformancePath, got, 0o644))
		return
	}
	want, err := os.ReadFile(patchConformancePath)
	require.NoError(t, err, "missing corpus; regenerate with -update")
	require.JSONEq(t, string(want), string(got), "patch corpus is stale; regenerate with -update")
}
