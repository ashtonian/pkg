//go:build unit

package parsehttp_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

// An answer is written as the SDK reads it: no row is [], no count is {},
// never null, and a batch with every key updated has no failed member.
func TestAnswers_AreWrittenAsTheSDKReadsThem(t *testing.T) {
	tests := []struct {
		name   string
		answer any
		want   string
	}{
		{"no rows", parsehttp.PatchAnswer[note]{}, `{"updated":[]}`},
		{"a row", parsehttp.PatchAnswer[note]{Updated: []note{{ID: 1, Text: "a"}}}, `{"updated":[{"id":1,"text":"a"}]}`},
		{"a key that updated none", parsehttp.PatchAnswer[note]{Failed: []parsehttp.PatchFailure{{ID: "9", Code: "E4004", Error: "note 9 not found"}}},
			`{"updated":[],"failed":[{"id":"9","code":"E4004","error":"note 9 not found"}]}`},
		{"no counts", parsehttp.FacetsAnswer{}, `{"facets":{}}`},
		{"counts", parsehttp.FacetsAnswer{Facets: map[string][]filter.FacetBucket{"body": {{Key: "a", Count: 2}}}}, `{"facets":{"body":[{"key":"a","count":2}]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.answer)
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(got))
		})
	}
}

// An answer reads back as it was written.
func TestAnswers_ReadBack(t *testing.T) {
	var patched parsehttp.PatchAnswer[note]
	require.NoError(t, json.Unmarshal([]byte(`{"updated":[{"id":1,"text":"a"}],"failed":[{"id":"9","code":"E4004","error":"gone"}]}`), &patched))
	assert.Equal(t, parsehttp.PatchAnswer[note]{Updated: []note{{ID: 1, Text: "a"}}, Failed: []parsehttp.PatchFailure{{ID: "9", Code: "E4004", Error: "gone"}}}, patched)
	var counted parsehttp.FacetsAnswer
	require.NoError(t, json.Unmarshal([]byte(`{"facets":{"body":[{"key":"a","count":2}]}}`), &counted))
	assert.Equal(t, map[string][]filter.FacetBucket{"body": {{Key: "a", Count: 2}}}, counted.Facets)
}
