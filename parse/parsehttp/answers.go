package parsehttp

import (
	"encoding/json"

	"github.com/ashtonian/pkg/parse/filter"
)

// PatchAnswer is a patch's answer, as the SDK's patch and patchMany read
// it: Updated holds the rows the target matched as the update left them,
// and Failed, for a batch, the keys that updated no row. A nil Updated is
// written as [], never null.
type PatchAnswer[T any] struct {
	Updated []T            `json:"updated"`
	Failed  []PatchFailure `json:"failed,omitempty"`
}

// MarshalJSON writes Updated as [] when it is nil.
func (a PatchAnswer[T]) MarshalJSON() ([]byte, error) {
	updated := a.Updated
	if updated == nil {
		updated = []T{}
	}
	return json.Marshal(struct {
		Updated []T            `json:"updated"`
		Failed  []PatchFailure `json:"failed,omitempty"`
	}{updated, a.Failed})
}

// PatchFailure is one key of a batch patch that updated no row. Code and
// Error are the application's: parse has no code for a key that names no
// row.
type PatchFailure struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Error string `json:"error"`
}

// FacetsAnswer is a facets request's answer (ParseFacets): the counts by
// field. A nil Facets is written as {}, never null.
type FacetsAnswer struct {
	Facets map[string][]filter.FacetBucket `json:"facets"`
}

// MarshalJSON writes Facets as {} when it is nil.
func (a FacetsAnswer) MarshalJSON() ([]byte, error) {
	facets := a.Facets
	if facets == nil {
		facets = map[string][]filter.FacetBucket{}
	}
	return json.Marshal(struct {
		Facets map[string][]filter.FacetBucket `json:"facets"`
	}{facets})
}
