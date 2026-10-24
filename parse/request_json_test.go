//go:build unit

package parse_test

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// a request document that names a member twice, or holds
// invalid UTF-8, is refused, so a request cannot have one of two values win.
func TestRequestDocuments_RefuseDuplicateNamesAndInvalidUTF8(t *testing.T) {
	r := widgets(t)
	listFilter := func(doc string) error {
		_, err := r.ParseList(url.Values{"filter": {doc}})
		return err
	}
	mergePatch := func(doc string) error { _, err := r.ParseMergePatch([]byte(doc)); return err }
	jsonPatch := func(doc string) error { _, err := r.ParsePatch([]byte(doc)); return err }
	for name, tc := range map[string]struct {
		parse func(string) error
		doc   string
		code  string
	}{
		"filter member twice":         {listFilter, `{"name":"a","name":"b"}`, filter.ECodeInvalidJSON.String()},
		"nested filter member twice":  {listFilter, `{"$or":[{"name":"a","name":"b"}]}`, filter.ECodeInvalidJSON.String()},
		"filter invalid UTF-8":        {listFilter, "{\"name\":\"\xff\"}", filter.ECodeInvalidJSON.String()},
		"merge patch member twice":    {mergePatch, `{"name":"a","name":"b"}`, patch.ECodeInvalidJSON.String()},
		"JSON Patch operation twice":  {jsonPatch, `[{"op":"replace","op":"remove","path":"/name","value":"a"}]`, patch.ECodeInvalidJSON.String()},
		"JSON Patch value invalid":    {jsonPatch, "[{\"op\":\"replace\",\"path\":\"/name\",\"value\":\"\xff\"}]", patch.ECodeInvalidJSON.String()},
		"data after the filter value": {listFilter, `{"name":"a"} {"name":"b"}`, filter.ECodeInvalidJSON.String()},
	} {
		t.Run(name, func(t *testing.T) {
			var pe *core.ParseError
			require.ErrorAs(t, tc.parse(tc.doc), &pe)
			assert.Equal(t, tc.code, pe.Code)
		})
	}
}

// Numbers keep their literal through the stricter decoder.
func TestStdlibJSON_KeepsNumbersExact(t *testing.T) {
	var v map[string]any
	require.NoError(t, core.StdlibJSON{}.Decode([]byte(`{"n":9007199254740993,"f":1.50,"a":[{"m":-0}]}`), &v))
	assert.Equal(t, json.Number("9007199254740993"), v["n"])
	assert.Equal(t, json.Number("1.50"), v["f"])
	array, arrayOK := v["a"].([]any)
	require.True(t, arrayOK)
	require.Len(t, array, 1)
	object, objectOK := array[0].(map[string]any)
	require.True(t, objectOK)
	assert.Equal(t, json.Number("-0"), object["m"])
}

type retryPolicy struct {
	Attempts int           `json:"attempts"`
	Backoff  time.Duration `json:"backoff"`
}

type retryingJob struct {
	ID     string      `json:"id"     db:"id"`
	Policy retryPolicy `json:"policy" db:"policy" parse:"patch"`
}

// Why StdlibJSON decodes with encoding/json's options: a typed document
// round-trips through the codec, and responses write a time.Duration as
// nanoseconds, which encoding/json/v2's defaults refuse to read back.
func TestStdlibJSON_ReadsDocumentsAsResponsesWriteThem(t *testing.T) {
	r, err := parse.NewResource[retryingJob]("jobs")
	require.NoError(t, err)
	p, err := r.ParseMergePatch([]byte(`{"policy":{"attempts":5}}`))
	require.NoError(t, err)
	got, err := r.ApplyValidatedPatch(p, retryingJob{ID: "j", Policy: retryPolicy{Attempts: 1, Backoff: 2 * time.Second}})
	require.NoError(t, err)
	assert.Equal(t, retryPolicy{Attempts: 5, Backoff: 2 * time.Second}, got.Policy)
}
