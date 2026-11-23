package msgpack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ashtonian/pkg/httpx"
	wire "github.com/vmihailenco/msgpack/v5"
	"github.com/vmihailenco/msgpack/v5/msgpcode"
)

// Match encoding/json's nesting bound without letting the native decoder
// recurse or allocate containers from untrusted length headers.
const maxJSONDepth = 10000

type jsonCodec struct{}

// Preserve object order and duplicate members: encoding/json's typed decoder
// observes both, including case-insensitive field matches and earlier errors.
type jsonObject []jsonMember

type jsonMember struct {
	name  string
	value any
}

func (jsonCodec) ContentType() string { return httpx.ContentTypeMsgpack }

func (jsonCodec) Encode(v any) ([]byte, error) {
	doc, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(doc))
	decoder.UseNumber()
	value, err := readJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := encodeJSONValue(wire.NewEncoder(&out), value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// json.Marshal supplies valid values, including string object names.
func readJSONValue(dec *json.Decoder, depth int) (any, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		if depth >= maxJSONDepth {
			return nil, fmt.Errorf("nesting exceeds %d", maxJSONDepth)
		}
		members := jsonObject{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			value, err := readJSONValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			members = append(members, jsonMember{name: key.(string), value: value})
		}
		_, err := dec.Token()
		return members, err
	case json.Delim('['):
		if depth >= maxJSONDepth {
			return nil, fmt.Errorf("nesting exceeds %d", maxJSONDepth)
		}
		values := []any{}
		for dec.More() {
			value, err := readJSONValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		_, err := dec.Token()
		return values, err
	default:
		return token, nil
	}
}

func encodeJSONValue(enc *wire.Encoder, value any) error {
	switch v := value.(type) {
	case json.Number:
		// Preserve the complete signed/unsigned integer range rather than
		// routing integers through float64, which loses bits above 2^53.
		if !strings.ContainsAny(string(v), ".eE") && v != "-0" {
			if i, err := v.Int64(); err == nil {
				return enc.EncodeInt(i)
			}
			if u, err := strconv.ParseUint(string(v), 10, 64); err == nil {
				return enc.EncodeUint(u)
			}
			// encoding/json writes a float64 from 2^64 up to 1e21 in integer
			// syntax. Only that float's own spelling is sent as the float: it
			// decodes to the same float64 over both codecs and no integer
			// target accepts it over either. Any other integer this large
			// would round, so it is refused.
			if f, err := v.Float64(); err == nil && strconv.FormatFloat(f, 'f', -1, 64) == string(v) {
				return enc.EncodeFloat64(f)
			}
			return fmt.Errorf("msgpack JSON integer is outside the 64-bit range and is not a float64 as encoding/json writes one")
		}
		// A fraction or exponent makes the number a float: MessagePack's
		// float carries that, as JSON's syntax does for a typed decoder,
		// so an integer target refuses it over both codecs.
		f, err := v.Float64()
		if err != nil {
			return fmt.Errorf("msgpack JSON number: %w", err)
		}
		if !exactFloat(string(v), f) {
			return fmt.Errorf("msgpack JSON number: an integer written with a fraction or exponent has no exact float64 form; write it as an integer")
		}
		return enc.EncodeFloat64(f)
	case []any:
		if err := enc.EncodeArrayLen(len(v)); err != nil {
			return err
		}
		for _, item := range v {
			if err := encodeJSONValue(enc, item); err != nil {
				return err
			}
		}
		return nil
	case jsonObject:
		if err := enc.EncodeMapLen(len(v)); err != nil {
			return err
		}
		for _, member := range v {
			if err := enc.EncodeString(member.name); err != nil {
				return err
			}
			if err := encodeJSONValue(enc, member.value); err != nil {
				return err
			}
		}
		return nil
	default:
		return enc.Encode(v)
	}
}

// exactFloat reports whether f, the float64 nearest a JSON number literal
// written with a fraction or exponent, carries the value the literal names
// whenever that value is an integer in the 64-bit range, the values an
// integer-valued receiver could hold exactly. Fractions and integers
// outside that range round to float64 over both codecs, so they pass.
func exactFloat(literal string, f float64) bool {
	integer, ok := integerLiteral(literal)
	if !ok {
		return true
	}
	if i, err := strconv.ParseInt(integer, 10, 64); err == nil {
		return f >= math.MinInt64 && f < math.MaxInt64 && int64(f) == i
	}
	if u, err := strconv.ParseUint(integer, 10, 64); err == nil {
		return f >= 0 && f < math.MaxUint64 && uint64(f) == u
	}
	return true
}

// integerLiteral returns, in decimal, the integer a JSON number literal
// denotes when it denotes a non-zero integer of at most 20 digits, the most
// a 64-bit integer has. It works on the literal's digits, so an exponent of
// any size costs nothing.
func integerLiteral(literal string) (string, bool) {
	sign, s := "", literal
	if rest, ok := strings.CutPrefix(s, "-"); ok {
		sign, s = "-", rest
	}
	exponent := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil || e > 1<<20 || e < -(1<<20) {
			return "", false // no 20-digit integer has an exponent this large
		}
		exponent, s = e, s[:i]
	}
	whole, fraction, _ := strings.Cut(s, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	significant := strings.TrimRight(digits, "0")
	if significant == "" {
		return "", false // zero, which float64 holds exactly
	}
	// The value is significant × 10^shift.
	shift := exponent - len(fraction) + len(digits) - len(significant)
	if shift < 0 || len(significant)+shift > 20 {
		return "", false
	}
	return sign + significant + strings.Repeat("0", shift), true
}

func (jsonCodec) Decode(data []byte, v any) error {
	r := bytes.NewReader(data)
	value, err := decodeJSONValue(wire.NewDecoder(r), r, 0)
	if err != nil {
		return fmt.Errorf("msgpack JSON document: %w", err)
	}
	if r.Len() != 0 {
		return fmt.Errorf("msgpack JSON document: trailing data")
	}
	var doc bytes.Buffer
	if err := writeJSONValue(&doc, value); err != nil {
		return fmt.Errorf("msgpack JSON document: %w", err)
	}
	return json.Unmarshal(doc.Bytes(), v)
}

// Buffer writes cannot fail; only JSON encoding can.
func writeJSONValue(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case jsonObject:
		out.WriteByte('{')
		for i, member := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			key, err := json.Marshal(member.name)
			if err != nil {
				return err
			}
			out.Write(key)
			out.WriteByte(':')
			if err := writeJSONValue(out, member.value); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeJSONValue(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case float32, float64:
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		out.Write(data)
		// A MessagePack float stays a float in JSON syntax, so an integer
		// target refuses 1000.0 over this codec as it does over JSON. -0
		// is how encoding/json writes a negative-zero float64, which this
		// codec encodes as a float.
		if !bytes.ContainsAny(data, ".eE") && string(data) != "-0" {
			out.WriteString(".0")
		}
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		out.Write(data)
	}
	return nil
}

func decodeJSONValue(dec *wire.Decoder, r *bytes.Reader, depth int) (any, error) {
	c, err := dec.PeekCode()
	if err != nil {
		return nil, err
	}
	switch {
	case msgpcode.IsFixedArray(c) || c == msgpcode.Array16 || c == msgpcode.Array32:
		if depth >= maxJSONDepth {
			return nil, fmt.Errorf("nesting exceeds %d", maxJSONDepth)
		}
		n, err := dec.DecodeArrayLen()
		if err != nil {
			return nil, err
		}
		if n < 0 || n > r.Len() {
			return nil, io.ErrUnexpectedEOF
		}
		values := make([]any, n)
		for i := range values {
			values[i], err = decodeJSONValue(dec, r, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return values, nil
	case msgpcode.IsFixedMap(c) || c == msgpcode.Map16 || c == msgpcode.Map32:
		if depth >= maxJSONDepth {
			return nil, fmt.Errorf("nesting exceeds %d", maxJSONDepth)
		}
		n, err := dec.DecodeMapLen()
		if err != nil {
			return nil, err
		}
		if n < 0 || n > r.Len()/2 {
			return nil, io.ErrUnexpectedEOF
		}
		values := make(jsonObject, n)
		for i := range values {
			keyCode, err := dec.PeekCode()
			if err != nil {
				return nil, err
			}
			if !msgpcode.IsString(keyCode) {
				return nil, fmt.Errorf("map key is not a JSON string")
			}
			key, err := decodeJSONString(dec)
			if err != nil {
				return nil, err
			}
			values[i].name = key
			values[i].value, err = decodeJSONValue(dec, r, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return values, nil
	case msgpcode.IsString(c):
		return decodeJSONString(dec)
	case msgpcode.IsExt(c):
		// Timestamps have a standard JSON form. Decode them explicitly so
		// arbitrary process-global native extension callbacks never run.
		value, err := dec.DecodeTime()
		if err != nil {
			return nil, err
		}
		return value.UTC(), nil
	default:
		// Scalars only: no recursive native map/array decoding or extension
		// callbacks. JSON marshaling rejects nonfinite numbers.
		return dec.DecodeInterface()
	}
}

func decodeJSONString(dec *wire.Decoder) (string, error) {
	value, err := dec.DecodeString()
	if err != nil {
		return "", err
	}
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("invalid UTF-8 string")
	}
	return value, nil
}
