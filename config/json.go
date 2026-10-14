package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// jsonParser parses .json config files: one JSON object, read as
// encoding/json reads it, keeping what a plain decode into map[string]any
// loses. An integer decodes as an integer, as it does from YAML, rather than
// as a float64 that cannot hold every int64. A key set twice in one object
// fails rather than the last one silently winning. An error opens with the
// line it is on, so the file's ParseError names it. The decoder bounds
// nesting, and with it this reader's recursion.
type jsonParser struct{}

func (jsonParser) Unmarshal(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	r := jsonReader{dec: dec}
	m, err := r.document()
	if err != nil {
		return nil, fmt.Errorf("line %d: %w", jsonLine(b, dec, err), err)
	}
	return m, nil
}

func (jsonParser) Marshal(m map[string]any) ([]byte, error) {
	return json.Marshal(m)
}

type jsonReader struct {
	dec *json.Decoder
}

// document reads the one object the file holds, and nothing after it.
func (r jsonReader) document() (map[string]any, error) {
	v, err := r.value()
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("a config file holds a JSON object")
	}
	if _, err := r.dec.Token(); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("data after the JSON object")
	}
	return m, nil
}

func (r jsonReader) value() (any, error) {
	tok, err := r.dec.Token()
	if err == io.EOF {
		return nil, errors.New("unexpected end of JSON input")
	}
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			return r.object()
		}
		if t == '[' {
			return r.array()
		}
		return nil, fmt.Errorf("unexpected %q", rune(t))
	case json.Number:
		return jsonNumber(t)
	default: // string, bool, nil
		return t, nil
	}
}

func (r jsonReader) object() (map[string]any, error) {
	m := map[string]any{}
	for r.dec.More() {
		tok, err := r.dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected %v where an object key belongs", tok)
		}
		if _, dup := m[key]; dup {
			return nil, fmt.Errorf("key %q is set twice in one object", key)
		}
		if m[key], err = r.value(); err != nil {
			return nil, err
		}
	}
	if _, err := r.dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	return m, nil
}

func (r jsonReader) array() ([]any, error) {
	list := []any{}
	for r.dec.More() {
		v, err := r.value()
		if err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	if _, err := r.dec.Token(); err != nil { // the closing bracket
		return nil, err
	}
	return list, nil
}

// jsonNumber is n as the YAML parser would type it: int when it is an
// integer that fits, int64 or uint64 when it is one that does not, float64
// otherwise.
func jsonNumber(n json.Number) (any, error) {
	s := string(n)
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		if int64(int(i)) == i {
			return int(i), nil
		}
		return i, nil
	}
	if u, err := strconv.ParseUint(s, 10, 64); err == nil {
		return u, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("number out of range: %w", err)
	}
	return f, nil
}

// jsonLine is the line err is on: the offset a syntax error names, or else
// where the decoder stopped.
func jsonLine(b []byte, dec *json.Decoder, err error) int {
	off := dec.InputOffset()
	var se *json.SyntaxError
	if errors.As(err, &se) {
		off = se.Offset
	}
	off = min(max(off, 0), int64(len(b)))
	return bytes.Count(b[:off], []byte("\n")) + 1
}
