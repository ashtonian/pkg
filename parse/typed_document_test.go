//go:build unit

package parse_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/patch"
)

type documentDimensions struct {
	W int `json:"w"`
	H int `json:"h"`
}

type documentRow struct {
	ID   string             `json:"id,omitempty" db:"id"`
	Dims documentDimensions `json:"dims" db:"dims" parse:"patch"`
}

func TestTypedDocumentReplacementCanonicalizes(t *testing.T) {
	p, err := parse.NewResource[documentRow]("document_rows")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := p.ParsePatch([]byte(`[{"op":"replace","path":"/dims","value":{"w":9}}]`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ApplyValidatedPatch(parsed, documentRow{Dims: documentDimensions{W: 2, H: 7}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"w": json.Number("9"), "h": json.Number("0")}
	if !reflect.DeepEqual(parsed.Operations[0].Value, want) || got.Dims != (documentDimensions{W: 9}) {
		t.Fatalf("replacement = %#v, typed apply = %#v", parsed.Operations[0].Value, got)
	}
}

func TestTypedDocumentMergePreservesSiblings(t *testing.T) {
	p, err := parse.NewResource[documentRow]("document_rows")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := p.ParseMergePatch([]byte(`{"dims":{"w":9}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ApplyValidatedPatch(parsed, documentRow{Dims: documentDimensions{W: 2, H: 7}})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := parsed.Operations[0].Value.(map[string]any)
	if !ok {
		t.Fatalf("merge value = %T, want map[string]any", parsed.Operations[0].Value)
	}
	if got.Dims != (documentDimensions{W: 9, H: 7}) || len(value) != 1 {
		t.Fatalf("merge = %#v, typed apply = %#v", parsed.Operations[0].Value, got)
	}
}

func TestTypedDocumentRejectsUnknownMembersAndRequiredDeletion(t *testing.T) {
	p, err := parse.NewResource[documentRow]("document_rows")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"dims":{"unknown":9}}`,
		`{"dims":{"w":null}}`,
		`{"dims":{"W":9}}`,
	} {
		if _, err := p.ParseMergePatch([]byte(body)); err == nil {
			t.Errorf("accepted lossy merge %s", body)
		}
	}
	if _, err := p.ParsePatch([]byte(`[{"op":"replace","path":"/dims","value":{"unknown":9}}]`)); err == nil {
		t.Fatal("accepted unknown replacement member")
	}
}

func TestTypedDocumentMergeNestedAndNullable(t *testing.T) {
	type nested struct {
		Dims     documentDimensions  `json:"dims"`
		Optional *documentDimensions `json:"optional,omitempty"`
		Label    string              `json:"label,omitempty"`
	}
	type row struct {
		ID  string `json:"id,omitempty" db:"id"`
		Doc nested `json:"doc" db:"doc" parse:"patch"`
	}
	p, err := parse.NewResource[row]("rows")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"nested partial", `{"doc":{"dims":{"w":9}}}`, true},
		{"nullable partial", `{"doc":{"optional":{"w":9}}}`, false},
		{"nullable complete", `{"doc":{"optional":{"w":9,"h":0}}}`, true},
		{"omitted deletion", `{"doc":{"optional":null,"label":null}}`, true},
		{"omitted zero", `{"doc":{"label":""}}`, false},
		{"unknown nested", `{"doc":{"dims":{"other":9}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.ParseMergePatch([]byte(tc.body))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

type customDocument struct {
	W int `json:"w"`
}

func (c *customDocument) UnmarshalJSON(data []byte) error {
	type plain customDocument
	return json.Unmarshal(data, (*plain)(c))
}

func TestTypedDocumentMergeFailsClosedForCustom(t *testing.T) {
	schema := &patch.PatchSchema{AllowedPaths: map[string]*patch.PathSchema{
		"/doc": {GoType: reflect.TypeFor[customDocument](), AllowedOps: []patch.PatchOp{patch.PatchOpReplace}},
	}}
	if _, err := patch.ParseMergePatch([]byte(`{"doc":{"w":9}}`), schema, nil); err == nil {
		t.Error("accepted a merge into a custom unmarshaler")
	}
	p := patch.NewParser(schema, nil)
	if _, err := p.Parse([]byte(`[{"op":"replace","path":"/doc","value":{"w":9}}]`)); err != nil {
		t.Errorf("replacement: %v", err)
	}
}

// An object merge names an embedded struct's members as encoding/json
// writes them: promoted, beside the embedding struct's own.
func TestTypedDocumentMergeNamesEmbeddedMembers(t *testing.T) {
	type Embedded struct {
		W int `json:"w"`
	}
	type embedded struct {
		Embedded
		H int `json:"h"`
	}
	schema := &patch.PatchSchema{AllowedPaths: map[string]*patch.PathSchema{
		"/doc": {GoType: reflect.TypeFor[embedded](), AllowedOps: []patch.PatchOp{patch.PatchOpReplace}},
	}}
	for body, valid := range map[string]bool{
		`{"doc":{"w":9}}`:         true,
		`{"doc":{"w":9,"h":1}}`:   true,
		`{"doc":{"Embedded":{}}}`: false,
	} {
		_, err := patch.ParseMergePatch([]byte(body), schema, nil)
		if (err == nil) != valid {
			t.Errorf("%s: valid=%v, error=%v", body, valid, err)
		}
	}
}

func TestTypedDocumentTestKeepsSuppliedShape(t *testing.T) {
	type row struct {
		ID   string             `json:"id,omitempty" db:"id"`
		Dims documentDimensions `json:"dims" db:"dims" parse:"patch" patch_ops:"replace,test"`
	}
	p, err := parse.NewResource[row]("rows")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := p.ParsePatch([]byte(`[{"op":"test","path":"/dims","value":{"w":9}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ApplyValidatedPatch(parsed, row{Dims: documentDimensions{W: 9}}); err == nil {
		t.Fatal("test accepted a document with a missing member")
	}
}

func TestTypedDocumentMergeHonorsJSONStringTag(t *testing.T) {
	type document struct {
		N int `json:"n,string"`
		H int `json:"h"`
	}
	type row struct {
		ID  string   `json:"id,omitempty" db:"id"`
		Doc document `json:"doc" db:"doc" parse:"patch"`
	}
	p, err := parse.NewResource[row]("rows")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := p.ParseMergePatch([]byte(`{"doc":{"n":"9"}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ApplyValidatedPatch(parsed, row{Doc: document{H: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Doc != (document{N: 9, H: 7}) {
		t.Fatalf("got %#v", got)
	}
	value, ok := parsed.Operations[0].Value.(map[string]any)
	if !ok {
		t.Fatalf("merge value = %T, want map[string]any", parsed.Operations[0].Value)
	}
	if v := value["n"]; v != "9" {
		t.Fatalf("wire value = %#v", v)
	}
}

func TestTypedDocumentMergeRejectsPotentiallyOmittedMap(t *testing.T) {
	type document struct {
		Labels map[string]string `json:"labels,omitempty"`
	}
	type row struct {
		ID  string   `json:"id,omitempty" db:"id"`
		Doc document `json:"doc" db:"doc" parse:"patch"`
	}
	p, err := parse.NewResource[row]("rows")
	if err != nil {
		t.Fatal(err)
	}
	if _, parseErr := p.ParseMergePatch([]byte(`{"doc":{"labels":{"a":null}}}`)); parseErr == nil {
		t.Fatal("accepted merge that can empty and omit the labels map")
	}
	parsed, err := p.ParseMergePatch([]byte(`{"doc":{"labels":{"a":null,"b":"new"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ApplyValidatedPatch(parsed, row{Doc: document{Labels: map[string]string{"a": "old"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Doc.Labels, map[string]string{"b": "new"}) {
		t.Fatalf("got %#v", got)
	}
}

func TestTypedDocumentRawMessageMerge(t *testing.T) {
	type row struct {
		ID  string          `json:"id,omitempty" db:"id"`
		Doc json.RawMessage `json:"doc" db:"doc" parse:"patch"`
	}
	p, err := parse.NewResource[row]("rows")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := p.ParseMergePatch([]byte(`{"doc":{"w":9}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ApplyValidatedPatch(parsed, row{Doc: json.RawMessage(`{"w":2,"h":7}`)})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]int
	if err := json.Unmarshal(got.Doc, &doc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc, map[string]int{"w": 9, "h": 7}) {
		t.Fatalf("got %s", got.Doc)
	}
}
