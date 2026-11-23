package parsehttp

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"net/http"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// WriteList answers a list request with its page, as WriteJSON answers a
// value with status 200. Under a field mask (parse.ListRequest.Fields) each
// item is narrowed, as MaskPage narrows it, to the JSON members the mask's
// fields encode as (FieldMembers). A nil req, or a request without a mask,
// writes the page as it is. A page that cannot be encoded is returned with
// nothing written.
func WriteList[T any](w http.ResponseWriter, r *http.Request, req *parse.ListRequest, page *filter.Page[T], opts ...Option) error {
	if req == nil || len(req.Fields()) == 0 {
		return WriteJSON(w, r, http.StatusOK, page, opts...)
	}
	masked, err := MaskPage(page, req.FieldMembers())
	if err != nil {
		return err
	}
	return WriteJSON(w, r, http.StatusOK, masked, opts...)
}

// MaskPage returns page with each item narrowed to the JSON members names
// lists, for a handler that encodes the page itself; a field mask's are
// parse.ListRequest.FieldMembers. Each item is encoded as encoding/json
// encodes it, through its MarshalJSON, its members' encoders and their tag
// options, and keeps the members of that object names lists, in the order
// they were written; a member the encoder leaves out (omitempty) stays out.
// The cursors, total, includes and facets are page's, whatever names lists,
// and a page of no items has items []. An item that does not encode as a
// JSON object is an error.
func MaskPage[T any](page *filter.Page[T], names []string) (*filter.Page[json.RawMessage], error) {
	masked := &filter.Page[json.RawMessage]{
		Items:     []json.RawMessage{},
		PageInfo:  page.PageInfo,
		Relations: page.Relations,
		Truncated: page.Truncated,
		Facets:    page.Facets,
	}
	if len(page.Items) == 0 {
		return masked, nil
	}
	encoded, err := json.Marshal(page.Items)
	if err != nil {
		return nil, fmt.Errorf("parsehttp: encoding list items: %w", err)
	}
	keep := make(map[string]bool, len(names))
	for _, name := range names {
		keep[name] = true
	}
	masked.Items, err = maskItems(encoded, keep, len(page.Items))
	if err != nil {
		return nil, err
	}
	return masked, nil
}

// maskItems narrows each object of encoded, the JSON array n items encode
// to, to the members keep names, copying the members' bytes as the encoder
// wrote them. The items are written over encoded, which maskItems owns.
func maskItems(encoded []byte, keep map[string]bool, n int) ([]json.RawMessage, error) {
	// json.Marshal's output is valid JSON; a MarshalJSON may still write a
	// name twice, which encoding/json passes through.
	dec := jsontext.NewDecoder(bytes.NewReader(encoded), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	_, err := dec.ReadToken()
	if err != nil {
		return nil, fmt.Errorf("parsehttp: masking list items: %w", err)
	}
	// Each write lands on bytes the decoder has already consumed: maskObject
	// writes no byte before the source byte it stands for, and the decoder
	// copies what it reads from the bytes.Reader into a buffer of its own,
	// so the bytes it has yet to read are untouched.
	out := encoded[:0]
	ends := make([]int, 0, n)
	var name []byte
	for i := 0; dec.PeekKind() != jsontext.KindEndArray; i++ {
		kind := dec.PeekKind()
		if kind != jsontext.KindBeginObject {
			return nil, fmt.Errorf("parsehttp: masking list item %d: it encodes as %s, not an object", i, kind)
		}
		out, name, err = maskObject(dec, out, name, keep)
		if err != nil {
			return nil, fmt.Errorf("parsehttp: masking list item %d: %w", i, err)
		}
		ends = append(ends, len(out))
	}
	items := make([]json.RawMessage, len(ends))
	start := 0
	for i, end := range ends {
		items[i] = out[start:end:end]
		start = end
	}
	return items, nil
}

// maskObject appends the object dec is at to out with only the members keep
// names, each byte after the decoder has read the byte it stands for. name
// is scratch space for a member's unquoted name, returned for reuse.
func maskObject(dec *jsontext.Decoder, out, name []byte, keep map[string]bool) ([]byte, []byte, error) {
	_, err := dec.ReadToken()
	if err != nil {
		return out, name, err
	}
	out = append(out, '{')
	kept := 0
	for dec.PeekKind() != jsontext.KindEndObject {
		raw, err := dec.ReadValue()
		if err != nil {
			return out, name, err
		}
		name, err = jsontext.AppendUnquote(name[:0], raw)
		if err != nil {
			return out, name, err
		}
		if !keep[string(name)] {
			err = dec.SkipValue()
			if err != nil {
				return out, name, err
			}
			continue
		}
		// A member after a kept one follows a comma the decoder has read.
		if kept > 0 {
			out = append(out, ',')
		}
		kept++
		out = append(out, raw...)
		value, err := dec.ReadValue()
		if err != nil {
			return out, name, err
		}
		out = append(out, ':')
		out = append(out, value...)
	}
	_, err = dec.ReadToken()
	return append(out, '}'), name, err
}
