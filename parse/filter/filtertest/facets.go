package filtertest

// FacetOrder is a terms facet requested with Limit and the keys it returns,
// in order: by count, ties in the order of the field's type. Its fixture's
// counts tie across keys whose text order and value order differ, and Limit
// cuts through the tie, so a backend that breaks ties by text fails it.
type FacetOrder struct {
	Limit int
	Keys  []string
}
