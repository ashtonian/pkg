package client

import "encoding/json"

// FacetsAnswer is a facets request's answer (an OPTIONS of the collection
// with the list's filter and facets): the counts by field.
type FacetsAnswer struct {
	Facets map[string][]FacetBucket `json:"facets"`
}

// MarshalJSON writes Facets as {} when it is nil, as the server does.
func (a FacetsAnswer) MarshalJSON() ([]byte, error) {
	facets := a.Facets
	if facets == nil {
		facets = map[string][]FacetBucket{}
	}
	answer := struct {
		Facets map[string][]FacetBucket `json:"facets"`
	}{facets}
	return json.Marshal(answer)
}

// PatchAnswer is a patch's answer, for one row or for the rows a batch
// names. Updated holds the rows the patch matched, as the update left them;
// none means the row was not found. Failed holds, for a batch, the keys that
// updated no row.
type PatchAnswer[T any] struct {
	Updated []T            `json:"updated"`
	Failed  []PatchFailure `json:"failed,omitempty"`
}

// MarshalJSON writes Updated as [] when it is nil, as the server does.
func (a PatchAnswer[T]) MarshalJSON() ([]byte, error) {
	updated := a.Updated
	if updated == nil {
		updated = []T{}
	}
	answer := struct {
		Updated []T            `json:"updated"`
		Failed  []PatchFailure `json:"failed,omitempty"`
	}{updated, a.Failed}
	return json.Marshal(answer)
}

// PatchFailure is one key of a batch patch that updated no row. Code and
// Error are the server application's: parse defines no code for a key that
// names no row.
type PatchFailure struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Error string `json:"error"`
}
