//go:build unit

package client

import (
	"encoding/json"
	"errors"
	"testing"
)

// Every schema document parse records reads into Schema and encodes back
// to itself: the registry's full document, and the schemas its filter and
// patch corpora are judged against. The wire fixture's are its schema
// answers (TestWire_AnswersRoundTrip).
func TestSchema_DocumentsRoundTrip(t *testing.T) {
	roundTrip[Schema](t, schemaDocumentPath, readCorpus(t, schemaDocumentPath))

	var filters struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	}
	decodeCorpus(t, filterConformancePath, &filters)
	if len(filters.Schemas) == 0 {
		t.Fatalf("%s holds no schemas", filterConformancePath)
	}
	for name, doc := range filters.Schemas {
		roundTrip[Schema](t, filterConformancePath+" "+name, doc)
	}

	var patches struct {
		Schema json.RawMessage `json:"schema"`
	}
	decodeCorpus(t, patchConformancePath, &patches)
	roundTrip[Schema](t, patchConformancePath, patches.Schema)
}

func TestSchema_RefusesAnotherVersion(t *testing.T) {
	schema := func(doc []byte) error {
		var s Schema
		return json.Unmarshal(doc, &s)
	}
	list := func(doc []byte) error {
		var l SchemaList
		return json.Unmarshal(doc, &l)
	}
	tests := []struct {
		name   string
		decode func([]byte) error
		doc    string
		want   SchemaVersionError
	}{
		{"a later version", schema, `{"version":2,"resource":"devices"}`, SchemaVersionError{Resource: "devices", Version: 2}},
		{"no version", schema, `{"resource":"devices"}`, SchemaVersionError{Resource: "devices"}},
		{"in a list", list, `{"schemas":[{"version":1,"resource":"a"},{"version":3,"resource":"b"}]}`, SchemaVersionError{Resource: "b", Version: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.decode([]byte(tt.doc))
			version, ok := errors.AsType[*SchemaVersionError](err)
			if !ok {
				t.Fatalf("got %v, want a *SchemaVersionError", err)
			}
			check(t, tt.want, *version)
		})
	}
}
