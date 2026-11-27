package filter

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ashtonian/pkg/parse/core"
)

// facetField is req's field of schema and its facet configuration, as
// BuildFacetQuery facets it: refused, with ECodeUnknownField, when schema
// has no such facetable field, and when its configuration counts nothing
// every backend counts alike (checkFacet) or its relation is invalid.
func facetField(schema *ResourceSchema, req FacetRequest) (*FieldSchema, *FacetSchema, error) {
	f, ok := schema.Fields[req.Field]
	if !ok || !f.Facetable {
		return nil, nil, core.NewError(ECodeUnknownField, fmt.Sprintf("field '%s' is not facetable", req.Field),
			core.WithField(req.Field))
	}
	cfg := f.facetConfig()
	err := checkFacet(f, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("facet %q: %w", req.Field, err)
	}
	if f.Relation != nil {
		err = f.Relation.Check()
		if err != nil {
			return nil, nil, fmt.Errorf("facet %q: %w", req.Field, err)
		}
	}
	return f, cfg, nil
}

// FacetCount counts one facet of a population for a backend that counts in
// Go, as BuildFacetQuery counts it: a terms facet's rows by the keys of
// their values (FacetKeys), a range facet's by the declared range their
// number falls in, and a relation facet's owners by the targets they pair
// with (ResolveRelationFacet, Merge).
type FacetCount struct {
	// Field is the facet's field, and Relation reports a relation facet,
	// whose counts come from its pairs (Merge) rather than a row's value.
	Field    *FieldSchema
	Relation bool
	cfg      *FacetSchema
	limit    int
	counts   map[string]int64
}

// NewFacetCounts are the counts of each facet of schema facets names, in
// order, refused as BuildFacetQuery refuses them: a field that is not
// facetable (ECodeUnknownField), and a facet no backend counts alike.
func NewFacetCounts(schema *ResourceSchema, facets []FacetRequest) ([]*FacetCount, error) {
	if schema == nil {
		return nil, fmt.Errorf("counting facets: schema is required")
	}
	out := make([]*FacetCount, len(facets))
	for i, req := range facets {
		f, cfg, err := facetField(schema, req)
		if err != nil {
			_, coded := errors.AsType[*core.ParseError](err)
			if coded {
				return nil, err
			}
			return nil, fmt.Errorf("counting facets: %w", err)
		}
		limit := req.TermsLimit(cfg)
		if cfg.Type == FacetTypeRange {
			limit = 0
		}
		out[i] = &FacetCount{Field: f, Relation: f.Relation != nil, cfg: cfg, limit: limit, counts: map[string]int64{}}
	}
	return out, nil
}

// Add counts one row of the population by v, its value of the field: the
// column's value as the store holds it, or for a JSON path its column's
// document. A NULL value, an absent member and a float's or a decimal's
// NaN or infinity count in no terms bucket; a number outside every range
// in no range bucket. A relation facet takes no row value (Merge).
func (c *FacetCount) Add(v any) error {
	if c.Relation {
		return fmt.Errorf("facet %s: a relation facet counts its pairs (Merge), not a row's value", c.Field.ExternalName)
	}
	if c.cfg.Type == FacetTypeRange {
		key, ok, err := c.rangeKey(v)
		if err != nil || !ok {
			return err
		}
		c.counts[key]++
		return nil
	}
	if c.Field.Path == nil && c.Field.DataType != core.FieldTypeArray {
		// One key, counted without the list FacetKeys would allocate for it
		// on every row.
		key, ok, err := columnFacetKey(v, c.Field)
		if ok {
			c.counts[key]++
		}
		return err
	}
	if c.Field.Path == nil {
		// An array column's stored JSON text is a list once read as the
		// column holds it.
		list, present, err := ColumnValue(c.Field, v)
		if err != nil || !present {
			return err
		}
		v = list
	}
	keys, err := FacetKeys(v, c.Field)
	if err != nil {
		return err
	}
	for _, k := range keys {
		c.counts[k]++
	}
	return nil
}

// Merge adds counts, a relation facet's owners per target key
// (ResolveRelationFacet), to the facet's.
func (c *FacetCount) Merge(counts map[string]int64) {
	for k, n := range counts {
		c.counts[k] += n
	}
}

// Result is the facet's buckets in the order and number BuildFacetQuery
// returns them (FacetBuckets).
func (c *FacetCount) Result() FacetResult {
	result := FacetResult{Field: c.Field.ExternalName, Buckets: FacetBuckets(c.Field, c.counts, c.limit)}
	return result
}

// rangeKey is the key of the declared range a number v falls in: at least
// its Min and below its Max, compared exactly with the bounds, as
// BuildFacetQuery's CASE compares them. NaN orders after every number, as
// PostgreSQL orders it. A path's v is its column's document, whose member
// (PathValue) compares as a decimal, as Evaluate compares one.
func (c *FacetCount) rangeKey(v any) (string, bool, error) {
	read := ColumnValue
	if c.Field.Path != nil {
		read = func(f *FieldSchema, doc any) (any, bool, error) { return PathValue(doc, f) }
	}
	value, present, err := read(c.Field, v)
	if err != nil || !present {
		return "", false, err
	}
	// The bounds are float64s compared with the value as it is held, not
	// rounded to a float32 field's precision.
	t := core.TypeInfo{Type: c.Field.DataType}
	_, member := value.(json.Number)
	if member {
		t = memberType(c.Field.DataType)
	}
	compare := func(bound float64) (int, error) {
		n, err := core.CompareCanonical(t, value, bound)
		if err != nil {
			return 0, fmt.Errorf("facet %s: range bound %v: %w", c.Field.ExternalName, bound, err)
		}
		return n, nil
	}
	for _, r := range c.cfg.Ranges {
		if r.Min != nil {
			n, err := compare(*r.Min)
			if err != nil {
				return "", false, err
			}
			if n < 0 {
				continue
			}
		}
		if r.Max != nil {
			n, err := compare(*r.Max)
			if err != nil {
				return "", false, err
			}
			if n >= 0 {
				continue
			}
		}
		return r.Key, true, nil
	}
	return "", false, nil
}
