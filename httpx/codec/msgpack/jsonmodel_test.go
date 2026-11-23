//go:build unit

package msgpack

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
	wire "github.com/vmihailenco/msgpack/v5"
)

type embeddedFields struct {
	Name string `json:"name"`
	Hide string `json:"-"`
}

type jsonTextID [16]byte

func (jsonTextID) MarshalText() ([]byte, error) {
	return []byte("0198a3c2-7a1e-7c00-8000-000000000001"), nil
}

type jsonDocument struct{}

type orderedJSONDocument string

func (v orderedJSONDocument) MarshalJSON() ([]byte, error) { return []byte(v), nil }

func (jsonDocument) MarshalJSON() ([]byte, error) {
	return []byte(`{"custom":true,"id":9007199254740993}`), nil
}

func TestCodecJSONModel(t *testing.T) {
	name := "visible"
	row := struct {
		embeddedFields
		Enabled bool    `json:"enabled,omitempty"`
		Note    *string `json:"note"`
		//lint:ignore SA5008 Exercise the literal dash tag supported by the Go 1.26 contract.
		Dash    string     `json:"-,"`
		Alias   string     `json:"alias" msgpack:"different"`
		ID      jsonTextID `json:"id"`
		Created time.Time  `json:"created_at"`
		Bytes   []byte     `json:"bytes"`
	}{
		embeddedFields: embeddedFields{Name: name, Hide: "private-marker"},
		Note:           &name, Dash: "dash", Alias: "alias", Created: time.Date(2026, 10, 19, 1, 2, 3, 0, time.UTC),
		Bytes: []byte{1, 2, 3},
	}
	for _, tt := range []struct {
		name  string
		value any
	}{
		{"struct", row},
		{"pointer", &row},
		{"map and slice", map[string]any{"items": []any{row, &row, nil}}},
		{"custom JSON", jsonDocument{}},
		{"integers", []any{int64(math.MinInt64), uint64(math.MaxUint64), int64(9007199254740993)}},
		{"nil", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			want, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			data, err := Codec.Encode(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("private-marker")) {
				t.Fatal("hidden field reached the wire")
			}
			var document any
			if err := wire.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			assertJSONDocument(t, got, want)
		})
	}
}

func TestCodecDecodesJSONNamedResponse(t *testing.T) {
	data, err := wire.Marshal(map[string]any{"created_at": "2026-10-19T01:02:03Z", "hidden": "private-marker", "count": uint64(math.MaxUint64)})
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		CreatedAt time.Time `json:"created_at"`
		Hidden    string    `json:"-" msgpack:"hidden"`
		Count     uint64    `json:"count"`
	}
	if err := Codec.Decode(data, &row); err != nil {
		t.Fatal(err)
	}
	if row.CreatedAt.IsZero() || row.Hidden != "" || row.Count != math.MaxUint64 {
		t.Fatalf("decoded response: %+v", row)
	}
}

func assertJSONDocument(t *testing.T, got, want []byte) {
	t.Helper()
	decode := func(data []byte) any {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !reflect.DeepEqual(decode(got), decode(want)) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestCodecRejectsMalformedDocuments(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"truncated map", []byte{0xdf, 0xff, 0xff, 0xff, 0xff}},
		{"truncated array", []byte{0xdd, 0xff, 0xff, 0xff, 0xff}},
		{"truncated string", []byte{0xdb, 0xff, 0xff, 0xff, 0xff}},
		{"trailing value", []byte{0xc0, 0xc0}},
		{"non-string map key", []byte{0x81, 0x01, 0xc0}},
		{"invalid string", []byte{0xa1, 0xff}},
		{"invalid map key", []byte{0x81, 0xa1, 0xff, 0xc0}},
		{"native extension", []byte{0xd4, 0x7f, 0x00}},
		{"too deep", append(bytes.Repeat([]byte{0x91}, maxJSONDepth+1), 0xc0)},
		{"invalid format", []byte{0xc1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got any
			if err := Codec.Decode(tt.data, &got); err == nil {
				t.Fatal("accepted malformed/non-JSON document")
			}
		})
	}
	for _, value := range []any{math.NaN(), math.Inf(1), json.Number("1e999"), json.Number("18446744073709551616"), json.Number("-9223372036854775809")} {
		if _, err := Codec.Encode(value); err == nil {
			t.Fatalf("encoded unsupported number %v", value)
		}
	}
	for _, value := range []any{math.NaN(), math.Inf(1)} {
		data, err := wire.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var got any
		if err := Codec.Decode(data, &got); err == nil {
			t.Fatalf("decoded nonfinite number %v", value)
		}
	}
	var got any
	if err := Codec.Decode(append(bytes.Repeat([]byte{0x91}, maxJSONDepth), 0xc0), &got); err != nil {
		t.Fatalf("valid depth: %v", err)
	}
}

func TestCodecJSONFieldSelection(t *testing.T) {
	type Fields struct {
		Name   string `json:"name"`
		Hidden string `json:"-"`
	}
	type Left struct {
		Shared string `json:"shared"`
	}
	type Right struct {
		Shared string `json:"shared"`
	}
	// Ambiguous embeddings are deliberate input to JSON's field selection.
	ambiguous := reflect.New(reflect.StructOf([]reflect.StructField{
		{Name: "Left", Type: reflect.TypeFor[Left](), Anonymous: true},
		{Name: "Right", Type: reflect.TypeFor[Right](), Anonymous: true},
	})).Elem()
	ambiguous.Field(0).Set(reflect.ValueOf(Left{"a"}))
	ambiguous.Field(1).Set(reflect.ValueOf(Right{"b"}))
	for _, value := range []any{
		struct {
			*Fields
			Empty *Fields `json:"empty,omitempty"`
		}{},
		struct {
			*Fields
			Named Fields `json:"nested"`
		}{Fields: &Fields{Name: "embedded", Hidden: "secret"}, Named: Fields{Name: "named", Hidden: "secret"}},
		ambiguous.Interface(),
		struct {
			Count int `json:"count,string"`
			Zero  int `json:"zero,omitzero"`
		}{Count: 7},
	} {
		want, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		data, err := Codec.Encode(value)
		if err != nil {
			t.Fatal(err)
		}
		var got any
		if err := Codec.Decode(data, &got); err != nil {
			t.Fatal(err)
		}
		document, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		assertJSONDocument(t, document, want)
	}
}

func TestCodecRejectsEncodedDepth(t *testing.T) {
	var nested any
	for range maxJSONDepth {
		nested = []any{nested}
	}
	if _, err := Codec.Encode(nested); err != nil {
		t.Fatalf("valid encoding depth: %v", err)
	}
	if _, err := Codec.Encode([]any{nested}); err == nil {
		t.Fatal("encoded document beyond the JSON nesting bound")
	}
}

func TestNativeCodec(t *testing.T) {
	type nativeValue struct {
		Bytes  []byte    `msgpack:"binary" json:"text"`
		Time   time.Time `msgpack:"timestamp" json:"time"`
		Native string    `msgpack:"native" json:"-"`
	}
	value := nativeValue{Bytes: []byte{1, 2, 3}, Time: time.Date(2026, 10, 19, 1, 2, 3, 0, time.UTC), Native: "native-only"}
	data, err := NativeCodec.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := wire.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["binary"].([]byte); !ok {
		t.Fatalf("binary=%T", fields["binary"])
	}
	if _, ok := fields["timestamp"].(time.Time); !ok {
		t.Fatalf("timestamp=%T", fields["timestamp"])
	}
	var got nativeValue
	if err := NativeCodec.Decode(data, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes, value.Bytes) || !got.Time.Equal(value.Time) || got.Native != value.Native {
		t.Fatalf("native=%+v", got)
	}
}

func TestCodecDecodesNativeJSONValues(t *testing.T) {
	data, err := wire.Marshal(map[string]any{"blob": []byte{1, 2, 3}, "time": time.Date(2026, 10, 19, 1, 2, 3, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := Codec.Decode(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["blob"] != "AQID" || got["time"] != "2026-10-19T01:02:03Z" {
		t.Fatalf("JSON values=%+v", got)
	}
}

func TestCodecPreservesJSONMembers(t *testing.T) {
	for _, document := range []string{
		`{"name":"first","Name":"second"}`,
		`{"Name":"first","name":"second"}`,
		`{"name":false,"name":"second"}`,
	} {
		t.Run(document, func(t *testing.T) {
			data, err := Codec.Encode(orderedJSONDocument(document))
			if err != nil {
				t.Fatal(err)
			}
			var raw json.RawMessage
			if err := Codec.Decode(data, &raw); err != nil {
				t.Fatal(err)
			}
			if string(raw) != document {
				t.Fatalf("members changed: %s", raw)
			}
			var got, want struct{ Name string }
			wantErr := json.Unmarshal([]byte(document), &want)
			gotErr := Codec.Decode(data, &got)
			if got != want || (gotErr != nil) != (wantErr != nil) {
				t.Fatalf("got=%+v err=%v; want=%+v err=%v", got, gotErr, want, wantErr)
			}
		})
	}
	// A peer can send duplicate members without using this encoder.
	var wireData bytes.Buffer
	enc := wire.NewEncoder(&wireData)
	for _, write := range []func() error{
		func() error { return enc.EncodeMapLen(2) },
		func() error { return enc.EncodeString("name") },
		func() error { return enc.EncodeBool(false) },
		func() error { return enc.EncodeString("name") },
		func() error { return enc.EncodeString("second") },
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	var value struct{ Name string }
	if err := Codec.Decode(wireData.Bytes(), &value); err == nil {
		t.Fatal("duplicate member hid an earlier type error")
	}
}

// A number literal means the same over both codecs: a receiver decoding
// the msgpack body gets what decoding the JSON body gives it, or the
// msgpack encoder refuses the literal rather than change its value.
func TestCodecNumbersMatchJSON(t *testing.T) {
	type targets struct {
		Any     any
		Float   float64
		Int     int64
		Uint    uint64
		IntErr  bool
		UintErr bool
	}
	decode := func(codec httpx.Codec, data []byte) targets {
		var got targets
		var a struct{ N any }
		var f struct{ N float64 }
		var i struct{ N int64 }
		var u struct{ N uint64 }
		if err := codec.Decode(data, &a); err != nil {
			t.Fatalf("%T into any: %v", codec, err)
		}
		if err := codec.Decode(data, &f); err != nil {
			t.Fatalf("%T into float64: %v", codec, err)
		}
		got.Any, got.Float = a.N, f.N
		got.IntErr = codec.Decode(data, &i) != nil
		got.UintErr = codec.Decode(data, &u) != nil
		got.Int, got.Uint = i.N, u.N
		return got
	}
	for _, tt := range []struct {
		literal string
		refused bool
	}{
		{"9007199254740993", false},
		{"9007199254740993.0", true},
		{"9.007199254740993e15", true},
		{"1e3", false},
		{"1000.0", false},
		{"-1e3", false},
		{"9007199254740992.0", false}, // 2^53, which float64 holds
		{"9007199254740994e0", false}, // 2^53+2, which float64 holds
		{"-9007199254740993.0", true},
		{"9223372036854775807.0", true}, // int64 max
		{"-9223372036854775808.0", false},
		{"9223372036854775808.0", false},  // 2^63, which float64 holds
		{"18446744073709551615e0", true},  // uint64 max
		{"18446744073709551616.0", false}, // 2^64, outside the 64-bit range
		{"18446744073709552000", false},   // 2^64 as encoding/json writes the float64
		{"-100000000000000000000", false}, // -1e20 as encoding/json writes it
		{"18446744073709551616", true},    // 2^64 spelled exactly: a json.Number would keep it
		{"100000000000000000001", true},
		{"1.8446744073709551617e19", false}, // outside the range: a float over both
		{"6.02e23", false},
		{"0.1", false},
		{"1.5", false},
		{"0.0", false},
		{"-0", false},
		{"1e-400", false},
		{"0e99999999999999999999", false},
		{"1e-99999999999999999999", false},
	} {
		t.Run(tt.literal, func(t *testing.T) {
			value := map[string]json.Number{"N": json.Number(tt.literal)}
			viaMsgpack, err := Codec.Encode(value)
			if tt.refused {
				if err == nil {
					t.Fatal("encoded a value float64 cannot hold exactly")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			viaJSON, err := httpx.JSON.Encode(value)
			if err != nil {
				t.Fatal(err)
			}
			want, got := decode(httpx.JSON, viaJSON), decode(Codec, viaMsgpack)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("msgpack decodes %+v, JSON %+v", got, want)
			}
		})
	}
}

// Every finite float64 a Go value holds encodes over Codec, including those
// encoding/json writes in integer syntax beyond the 64-bit range, and
// decodes to the float64 the JSON codec gives.
func TestCodecEncodesEveryFloat64(t *testing.T) {
	for _, f := range []float64{1e20, -1e20, 1 << 64, math.MaxFloat64, -math.MaxFloat64, 1e21, 9.999999999999999e20, math.SmallestNonzeroFloat64, 0.5} {
		data, err := Codec.Encode(struct{ N float64 }{f})
		if err != nil {
			t.Fatalf("%v: %v", f, err)
		}
		var got struct{ N float64 }
		if err := Codec.Decode(data, &got); err != nil || got.N != f {
			t.Fatalf("%v decoded as %v, %v", f, got.N, err)
		}
	}
}

// MessagePack has one negative zero, the float encoding/json writes as -0,
// an integer syntax: -0.0 decodes like it, so an integer target accepts 0
// where JSON refuses -0.0. The value is the same.
func TestCodecNegativeZero(t *testing.T) {
	data, err := Codec.Encode(map[string]json.Number{"N": "-0.0"})
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ N float64 }
	var i struct{ N int64 }
	if err := Codec.Decode(data, &f); err != nil || !math.Signbit(f.N) || f.N != 0 {
		t.Fatalf("float64 %v, %v", f.N, err)
	}
	if err := Codec.Decode(data, &i); err != nil || i.N != 0 {
		t.Fatalf("int64 %v, %v", i.N, err)
	}
}

// A float from another MessagePack peer stays a float: integer targets
// refuse an integral one, as they refuse 1000.0 in JSON.
func TestCodecDecodesFloatsAsFloats(t *testing.T) {
	for _, tt := range []struct {
		value   any
		wantInt bool
	}{
		{float64(1000), false},
		{float32(1000), false},
		{float64(1.5), false},
		{math.Copysign(0, -1), true}, // encoding/json writes -0
		{int64(1000), true},
	} {
		data, err := NativeCodec.Encode(map[string]any{"N": tt.value})
		if err != nil {
			t.Fatal(err)
		}
		var i struct{ N int64 }
		var f struct{ N float64 }
		if err := Codec.Decode(data, &f); err != nil {
			t.Fatalf("%T(%v) into float64: %v", tt.value, tt.value, err)
		}
		if got := Codec.Decode(data, &i) == nil; got != tt.wantInt {
			t.Fatalf("%T(%v) decodes into int64: %v, want %v", tt.value, tt.value, got, tt.wantInt)
		}
	}
}
