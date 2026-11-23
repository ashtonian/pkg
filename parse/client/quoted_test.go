//go:build unit

package client

import (
	"encoding/json"
	"math"
	"testing"
)

// A Quoted operand travels as encoding/json writes a json:",string" field,
// exactly past 2^53 and at both 64-bit ends.
func TestQuoted_EncodesAsJSONText(t *testing.T) {
	id := NewOrdered[Quoted[int64]]("id")
	check(t, `{"id":{"$eq":"9223372036854775807"}}`, encode(t, id.Eq(Quote(int64(math.MaxInt64)))))
	check(t, `{"id":{"$gt":"-9223372036854775808"}}`, encode(t, id.Gt(Quote(int64(math.MinInt64)))))
	check(t, `{"id":{"$in":["1","9007199254740993"]}}`, encode(t, id.In(Quote(int64(1)), Quote(int64(9007199254740993)))))
	check(t, `{"big":{"$eq":"18446744073709551615"}}`, encode(t, NewField[Quoted[uint64]]("big").Eq(Quote(uint64(math.MaxUint64)))))
	check(t, `{"on":{"$eq":"true"}}`, encode(t, NewField[Quoted[bool]]("on").Eq(Quote(true))))
	check(t, `{"ratio":{"$eq":"0.5"}}`, encode(t, NewOrdered[Quoted[float64]]("ratio").Eq(Quote(0.5))))

	type patch struct {
		ID  Nullable[Quoted[int64]] `json:"id,omitzero"`
		Opt Nullable[Quoted[int64]] `json:"opt,omitzero"`
	}
	check(t, `{"id":"9007199254740993","opt":null}`, encode(t, patch{ID: Set(Quote(int64(9007199254740993))), Opt: Null[Quoted[int64]]()}))
	check(t, `{"op":"replace","path":"/id","value":"42"}`, encode(t, Replace("/id", Quote(int64(42)))))
}

// A response's quoted field decodes back exactly; a bare number does not.
func TestQuoted_DecodesTheServersForm(t *testing.T) {
	var row struct {
		ID  Quoted[int64]  `json:"id"`
		Big Quoted[uint64] `json:"big"`
	}
	must(t, json.Unmarshal([]byte(`{"id":"-9223372036854775808","big":"18446744073709551615"}`), &row))
	check(t, int64(math.MinInt64), row.ID.Value)
	check(t, uint64(math.MaxUint64), row.Big.Value)

	// A nullable quoted field writes null, which leaves the value as
	// encoding/json leaves one.
	var nullable struct {
		Opt Quoted[int64] `json:"opt"`
	}
	nullable.Opt = Quote(int64(3))
	must(t, json.Unmarshal([]byte(`{"opt":null}`), &nullable))
	check(t, int64(3), nullable.Opt.Value)

	var q Quoted[int64]
	for input, what := range map[string]string{
		`9007199254740993`:      "a bare number",
		`"seven"`:               "text that is no number",
		`"9223372036854775808"`: "past int64",
	} {
		err := json.Unmarshal([]byte(input), &q)
		if err == nil {
			t.Errorf("%s decoded", what)
		}
	}

	// It round-trips through the server's own struct encoding.
	type server struct {
		ID int64 `json:"id,string"`
	}
	data, err := json.Marshal(server{ID: 9007199254740993})
	must(t, err)
	var back struct {
		ID Quoted[int64] `json:"id"`
	}
	must(t, json.Unmarshal(data, &back))
	check(t, int64(9007199254740993), back.ID.Value)
	again, err := json.Marshal(back)
	must(t, err)
	checkJSON(t, string(data), string(again))
}
