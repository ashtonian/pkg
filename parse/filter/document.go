package filter

import "encoding/json"

// Document is a JSON document decoded once, for a backend that reads its
// paths many times: PathValue, ColumnValue, ColumnRow, DocumentElements and
// KeyValue read a Document without decoding it again, where JSON text is
// decoded on every read. Its numbers are exact, as json.Number. The zero
// Document is the JSON null document; a column that is SQL NULL is nil, not
// a Document.
type Document struct {
	root any
}

// DecodeDocument decodes data, JSON text, as every reader of a document
// decodes it: empty text and JSON null are the JSON null document.
func DecodeDocument(data []byte) (Document, error) {
	root, err := decodeDocument(json.RawMessage(data))
	if err != nil {
		return Document{}, err
	}
	return Document{root: root}, nil
}

// Root is the decoded document: a map[string]any, []any, string, bool,
// json.Number or nil. The Document shares it, so a caller must not change
// it.
func (d Document) Root() any { return d.root }
