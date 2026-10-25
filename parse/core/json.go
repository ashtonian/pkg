package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// NormalizeJSON returns v as a generic JSON document: map[string]any, []any,
// string, bool, nil, and numbers as int64 (integers in range), uint64
// (integers above math.MaxInt64) or finite float64. It is the form JSON
// filter values are compared and bound in. Decoder output
// converts in place; other Go values (typed maps, slices, structs) are read
// back through their JSON encoding first. An integer literal beyond 64 bits
// is an error: binding it would silently round it. Decimal/exponent integers
// are also kept exact; fractional literals must round-trip through float64
// without changing their decimal value.
func NormalizeJSON(v any) (any, error) {
	switch x := v.(type) {
	case nil, string, bool:
		return x, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			n, err := NormalizeJSON(e)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = n
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			n, err := NormalizeJSON(e)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = n
		}
		return out, nil
	}
	if IsNumber(v) {
		return jsonNumber(v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("value is not JSON: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("value is not JSON: %w", err)
	}
	return NormalizeJSON(generic)
}

func jsonNumber(v any) (any, error) {
	switch n := v.(type) {
	case json.Number:
		s := string(n)
		if x, err := parseIntLiteral(s); err == nil {
			if x.isUint {
				return x.u, nil
			}
			return x.i, nil
		}
		if !strings.ContainsAny(s, ".eE") {
			return nil, fmt.Errorf("integer %s exceeds 64 bits", s)
		}
		if !PortableDecimal(s) {
			return nil, fmt.Errorf("%s cannot be represented without decimal rounding", s)
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.Trunc(f) == f {
			return nil, fmt.Errorf("%s is not a representable number", s)
		}
		return f, nil
	case float64, float32:
		return NormalizeFloat(n, 0)
	}
	x, err := nativeInt(v)
	if err != nil {
		return nil, err
	}
	if x.isUint {
		return x.u, nil
	}
	return x.i, nil
}
