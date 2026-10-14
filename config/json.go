package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// jsonParser parses .json config files: one JSON object, read as
// encoding/json reads it, keeping what a plain decode into map[string]any
// loses. An integer decodes as an integer, as it does from YAML, rather than
// as a float64 that cannot hold every int64. A key set twice in one object
// fails rather than the last one silently winning. An error opens with the
// line it is on, so the file's ParseError names it.
type jsonParser struct{}

// jsonMaxDepth bounds nesting, and with it the reader's recursion: not every
// supported Go release's decoder bounds it for the token API.
const jsonMaxDepth = 1000

var errJSONEnd = errors.New("unexpected end of JSON input")

func (jsonParser) Unmarshal(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	r := jsonReader{dec: dec}
	m, err := r.document()
	if err != nil {
		return nil, fmt.Errorf("line %d: %w", jsonLine(b, dec.InputOffset()), err)
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
	v, err := r.value(0)
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

// token is the next token, where the input may not end.
func (r jsonReader) token() (json.Token, error) {
	tok, err := r.dec.Token()
	if err == io.EOF {
		return nil, errJSONEnd
	}
	return tok, err
}

func (r jsonReader) value(depth int) (any, error) {
	if depth >= jsonMaxDepth {
		return nil, fmt.Errorf("JSON nested more than %d deep", jsonMaxDepth)
	}
	tok, err := r.token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			return r.object(depth)
		}
		if t == '[' {
			return r.array(depth)
		}
		return nil, fmt.Errorf("unexpected %q", rune(t))
	case json.Number:
		return jsonNumber(t)
	default: // string, bool, nil
		return t, nil
	}
}

func (r jsonReader) object(depth int) (map[string]any, error) {
	m := map[string]any{}
	for r.dec.More() {
		tok, err := r.token()
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
		if m[key], err = r.value(depth + 1); err != nil {
			return nil, err
		}
	}
	if _, err := r.token(); err != nil { // the closing brace
		return nil, err
	}
	return m, nil
}

func (r jsonReader) array(depth int) ([]any, error) {
	list := []any{}
	for r.dec.More() {
		v, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	if _, err := r.token(); err != nil { // the closing bracket
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

// jsonLine is the line of the token the decoder failed on, or failed after:
// where the last token it returned ends (off), past the separators that
// follow. A SyntaxError's own offset is not used; Go releases place it
// differently.
func jsonLine(b []byte, off int64) int {
	i := int(min(max(off, 0), int64(len(b))))
	for i < len(b) && strings.IndexByte(" \t\r\n,:", b[i]) >= 0 {
		i++
	}
	return bytes.Count(b[:i], []byte("\n")) + 1
}
