//go:build unit

package filter_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestParseFacetTag_RefusesWhatNoQueryCounts(t *testing.T) {
	for tag, want := range map[string]string{
		"range:,":          "range 1 is empty",
		"range:0-50,,50+":  "range 2 is empty",
		"range:50-10":      `range "50-10": its minimum is not below its maximum`,
		"range:10-10":      `range "10-10": its minimum is not below its maximum`,
		"range:NaN+":       `range "NaN+" has a non-finite bound`,
		"range:0-Inf":      `range "0-Inf" has a non-finite bound`,
		"range:0-100,5-20": `ranges "0-100" and "5-20" overlap`,
		"range:50+,0-60":   `ranges "0-60" and "50+" overlap`,
		"range:0-5,0-5":    `range "0-5" is declared twice`,
		"terms:0":          "terms facet limit 0 is below 1",
		"terms:-5":         "terms facet limit -5 is below 1",
	} {
		t.Run(tag, func(t *testing.T) {
			_, err := filter.ParseFacetTag(tag)
			assert.EqualError(t, err, want)
		})
	}
}

func TestParseFacetTag_ReadsWellFormedTags(t *testing.T) {
	terms, err := filter.ParseFacetTag("terms")
	require.NoError(t, err)
	assert.Equal(t, 0, terms.Limit)

	// A struct tag is not request input: its limit has no cap.
	big, err := filter.ParseFacetTag("terms:99999999")
	require.NoError(t, err)
	assert.Equal(t, 99999999, big.Limit)

	ranges, err := filter.ParseFacetTag("range:-10-0, 0-50 ,50+")
	require.NoError(t, err)
	keys := make([]string, 0, len(ranges.Ranges))
	for _, r := range ranges.Ranges {
		keys = append(keys, r.Key)
	}
	assert.Equal(t, []string{"-10-0", "0-50", "50+"}, keys)
}

func TestGenerateFilterSchema_FacetsOnlyWhatEveryBackendCounts(t *testing.T) {
	type jsonTerms struct {
		Meta json.RawMessage `json:"meta" parse:"filter,facet"`
	}
	type mapTerms struct {
		Attrs map[string]any `json:"attrs" parse:"filter,facet"`
	}
	type textRange struct {
		Name string `json:"name" parse:"filter,facet" facet:"range:0-1,1+"`
	}
	type listRange struct {
		Ranks []int `json:"ranks" parse:"filter,facet" facet:"range:0-1,1+"`
	}
	type unfaceted struct {
		Rank int `json:"rank" parse:"filter" facet:"range:0-1,1+"`
	}
	type badRanges struct {
		Rank int `json:"rank" parse:"filter,facet" facet:"range:0-10,5+"`
	}
	type settings struct {
		Mode string `json:"mode" parse:"filter,facet" facet:"range:0-1"`
	}
	type memberRange struct {
		Settings settings `json:"settings" parse:"filter"`
	}
	type unfacetedMember struct {
		Mode string `json:"mode" parse:"filter" facet:"terms"`
	}
	type memberTag struct {
		Settings unfacetedMember `json:"settings" parse:"filter"`
	}
	g := core.NewSchemaGenerator()
	for name, tc := range map[string]struct {
		build func() error
		want  string
	}{
		"terms on json":           {func() error { _, err := filter.GenerateFilterSchema[jsonTerms](g); return err }, "field Meta: json values have no facet key every backend reads alike"},
		"terms on a map":          {func() error { _, err := filter.GenerateFilterSchema[mapTerms](g); return err }, "field Attrs: json values have no facet key every backend reads alike"},
		"range on text":           {func() error { _, err := filter.GenerateFilterSchema[textRange](g); return err }, "field Name: a range facet buckets numbers, not string values"},
		"range on a list":         {func() error { _, err := filter.GenerateFilterSchema[listRange](g); return err }, "field Ranks: an array's facet is terms, not range"},
		"facet without facet":     {func() error { _, err := filter.GenerateFilterSchema[unfaceted](g); return err }, `field Rank: facet:"range:0-1,1+" on a field without the facet capability`},
		"overlapping ranges":      {func() error { _, err := filter.GenerateFilterSchema[badRanges](g); return err }, `field Rank: facet:"range:0-10,5+": ranges "0-10" and "5+" overlap`},
		"range on a text member":  {func() error { _, err := filter.GenerateFilterSchema[memberRange](g); return err }, "a range facet buckets numbers, not string values"},
		"facet on a plain member": {func() error { _, err := filter.GenerateFilterSchema[memberTag](g); return err }, `field Settings: member mode: facet:"terms" on a member without the facet capability`},
	} {
		t.Run(name, func(t *testing.T) {
			assert.ErrorContains(t, tc.build(), tc.want)
		})
	}

	type keyed struct {
		Status string         `json:"status" parse:"filter,facet"`
		Rank   int            `json:"rank"   parse:"filter,facet" facet:"range:0-10,10+"`
		Tags   []string       `json:"tags"   parse:"filter,facet" facet:"terms:5"`
		Opens  core.LocalTime `json:"opens"  parse:"filter,facet"`
		MAC    core.MACAddr   `json:"mac"    parse:"filter,facet"`
	}
	s, err := filter.GenerateFilterSchema[keyed](g)
	require.NoError(t, err)
	assert.True(t, s.Fields["opens"].Facetable)
	assert.Equal(t, filter.FacetTypeRange, s.Fields["rank"].FacetSchema.Type)
}

func TestWithPaths_FacetsOnlyWhatEveryBackendCounts(t *testing.T) {
	type doc struct {
		Labels map[string]any `json:"labels" parse:"filter"`
	}
	s, err := filter.GenerateFilterSchema[doc](core.NewSchemaGenerator())
	require.NoError(t, err)
	_, err = s.WithPaths(filter.PathDecl{Path: "labels.tier", Type: core.FieldTypeString, Facet: &filter.FacetSchema{
		Type: filter.FacetTypeRange, Ranges: []filter.FacetRange{{Key: "low", Max: new(1.0)}},
	}})
	assert.ErrorContains(t, err, "a range facet buckets numbers, not string values")
	_, err = s.WithPaths(filter.PathDecl{Path: "labels.size", Type: core.FieldTypeFloat, Facet: &filter.FacetSchema{Type: filter.FacetTypeRange}})
	assert.ErrorContains(t, err, "a range facet needs at least one range")
	_, err = s.WithPaths(filter.PathDecl{Path: "labels.size", Type: core.FieldTypeFloat, Facet: &filter.FacetSchema{
		Type: filter.FacetTypeRange, Ranges: []filter.FacetRange{{Key: "small", Max: new(1.0)}, {Key: "big", Min: new(1.0)}},
	}})
	assert.NoError(t, err)
}

// A hand-built schema meets the rule generation applies. Its faults are the
// server's, so the error is no ParseError.
func TestBuildFacetQuery_ChecksHandBuiltFacets(t *testing.T) {
	ten := 10.0
	for name, tc := range map[string]struct {
		field *filter.FieldSchema
		want  string
	}{
		"terms on json": {
			&filter.FieldSchema{DataType: core.FieldTypeJSON},
			"json values have no facet key every backend reads alike",
		},
		"terms on inet elements": {
			&filter.FieldSchema{DataType: core.FieldTypeArray, Elem: core.FieldTypeInet},
			"inet values have no facet key every backend reads alike",
		},
		"range on text": {
			&filter.FieldSchema{DataType: core.FieldTypeString, FacetSchema: &filter.FacetSchema{Type: filter.FacetTypeRange, Ranges: []filter.FacetRange{{Key: "low", Max: &ten}}}},
			"a range facet buckets numbers, not string values",
		},
		"no ranges": {
			&filter.FieldSchema{DataType: core.FieldTypeFloat, FacetSchema: &filter.FacetSchema{Type: filter.FacetTypeRange}},
			"a range facet needs at least one range",
		},
		"an unbounded range": {
			&filter.FieldSchema{DataType: core.FieldTypeDecimal, FacetSchema: &filter.FacetSchema{Type: filter.FacetTypeRange, Ranges: []filter.FacetRange{{Key: "all"}}}},
			`range "all" has neither bound`,
		},
		"a negative limit": {
			&filter.FieldSchema{DataType: core.FieldTypeString, FacetSchema: &filter.FacetSchema{Type: filter.FacetTerms, Limit: -1}},
			"terms facet limit -1 is negative",
		},
		"no type": {
			&filter.FieldSchema{DataType: core.FieldTypeString, FacetSchema: &filter.FacetSchema{}},
			`unknown facet type ""`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := tc.field
			f.ExternalName, f.Table, f.Column, f.Facetable = "x", "items", "x", true
			rs := &filter.ResourceSchema{Name: "items", Fields: map[string]*filter.FieldSchema{"x": f}}
			_, err := filter.BuildFacetQuery(rs, filter.SQLiteDialect{}, "1=1", nil, []filter.FacetRequest{{Field: "x"}})
			assert.ErrorContains(t, err, `BuildFacetQuery: facet "x": `+tc.want)
			_, isParseError := errors.AsType[*core.ParseError](err)
			assert.False(t, isParseError)
		})
	}
}
