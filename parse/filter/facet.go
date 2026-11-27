package filter

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// FacetType determines how aggregation SQL is generated.
type FacetType string

const (
	FacetTerms     FacetType = "terms"
	FacetTypeRange FacetType = "range"
)

// FacetSchema defines a single facetable field.
type FacetSchema struct {
	Field  string
	Type   FacetType
	Limit  int
	Ranges []FacetRange
}

// FacetRange defines a bucket boundary for range facets: a value is in it
// when it is at least Min and below Max, a nil bound being open. A facet's
// ranges do not overlap, so a value is in at most one.
type FacetRange struct {
	Key string
	Min *float64
	Max *float64
}

// FacetQuery is a generated facet query: SQL plus its bound arguments.
type FacetQuery struct {
	SQL  string
	Args []any
}

// FacetResult holds the aggregated facet data after query execution.
type FacetResult struct {
	Field   string        `json:"field"`
	Buckets []FacetBucket `json:"buckets"`
}

// FacetBucket is a single aggregation bucket.
type FacetBucket struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// FacetBuckets orders field's counts as BuildFacetQuery returns them, for
// a backend that counts in Go: a terms facet by count, ties in the order of
// the field's type, cut to limit (none when limit <= 0); a range facet in
// its declared order. Keys without a count are dropped. A nil field is a
// terms facet of strings.
func FacetBuckets(field *FieldSchema, counts map[string]int64, limit int) []FacetBucket {
	out := make([]FacetBucket, 0, len(counts))
	var cfg *FacetSchema
	if field != nil {
		cfg = field.FacetSchema
	}
	if cfg != nil && cfg.Type == FacetTypeRange {
		for _, r := range cfg.Ranges {
			if n := counts[r.Key]; n > 0 {
				out = append(out, FacetBucket{Key: r.Key, Count: n})
			}
		}
		return out
	}
	for k, n := range counts {
		if n > 0 {
			out = append(out, FacetBucket{Key: k, Count: n})
		}
	}
	keyType := core.TypeInfo{Type: core.FieldTypeString}
	if field != nil {
		keyType = core.TypeInfo{Type: facetValueType(field), NumberKind: field.NumberKind}
	}
	slices.SortFunc(out, func(a, b FacetBucket) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), compareFacetKeys(keyType, a.Key, b.Key))
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// facetValueType is the type of the values a terms facet on f counts: a
// relation's target keys of several columns their text (core.KeyText), an
// array's elements', or f's own.
func facetValueType(f *FieldSchema) core.FieldType {
	if f.Relation != nil && len(f.Relation.TargetColumns) > 1 {
		return core.FieldTypeString
	}
	if f.DataType == core.FieldTypeArray {
		return f.Elem
	}
	return f.DataType
}

func isNumber(t core.FieldType) bool {
	return t == core.FieldTypeInt || t == core.FieldTypeFloat || t == core.FieldTypeDecimal
}

// compareFacetKeys orders two keys of a terms facet whose values are of
// type t as their values order (core.CompareCanonicalTexts), then by text,
// so two keys naming one number ("1.5", "1.50") still order. FacetBuckets
// takes any counts: a key that names no value of t orders after every key
// that does.
func compareFacetKeys(t core.TypeInfo, a, b string) int {
	c, err := core.CompareCanonicalTexts(t, a, b)
	if err == nil {
		return cmp.Or(c, strings.Compare(a, b))
	}
	_, aerr := core.CompareCanonicalTexts(t, a, a)
	_, berr := core.CompareCanonicalTexts(t, b, b)
	return cmp.Or(compareFalseFirst(aerr != nil, berr != nil), strings.Compare(a, b))
}

// compareFalseFirst orders false before true.
func compareFalseFirst(x, y bool) int {
	switch {
	case x == y:
		return 0
	case x:
		return 1
	}
	return -1
}

// FacetMap keys facet results by field, the shape a list page and a facet
// response carry; nil for none.
func FacetMap(results []FacetResult) map[string][]FacetBucket {
	if len(results) == 0 {
		return nil
	}
	m := make(map[string][]FacetBucket, len(results))
	for _, r := range results {
		m[r.Field] = r.Buckets
	}
	return m
}

// ParseFacetTag parses the `facet` struct tag: "terms", "terms:<limit>"
// or "range:<lo>-<hi>,...,<lo>+". A terms limit is at least 1 and has no
// cap, as a struct tag is not request input; ranges are finite, each
// minimum below its maximum, and none overlaps another.
func ParseFacetTag(tag string) (*FacetSchema, error) {
	if tag == "" {
		return nil, nil
	}

	typePart, configPart, _ := strings.Cut(tag, ":")

	var fs *FacetSchema
	switch FacetType(typePart) {
	case FacetTerms:
		fs = &FacetSchema{Type: FacetTerms}
		if configPart != "" {
			n, err := strconv.Atoi(configPart)
			if err != nil {
				return nil, fmt.Errorf("invalid terms facet limit %q: %w", configPart, err)
			}
			if n < 1 {
				return nil, fmt.Errorf("terms facet limit %d is below 1", n)
			}
			fs.Limit = n
		}

	case FacetTypeRange:
		if configPart == "" {
			return nil, fmt.Errorf("range facet requires bucket definitions (e.g., range:0-50,50-100,100+)")
		}
		ranges, err := parseRanges(configPart)
		if err != nil {
			return nil, err
		}
		fs = &FacetSchema{Type: FacetTypeRange, Ranges: ranges}

	default:
		return nil, fmt.Errorf("unknown facet type %q", typePart)
	}
	if err := fs.validate(); err != nil {
		return nil, err
	}
	return fs, nil
}

func parseRanges(s string) ([]FacetRange, error) {
	parts := strings.Split(s, ",")
	ranges := make([]FacetRange, 0, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("range %d is empty", i+1)
		}
		r, err := parseOneRange(part)
		if err != nil {
			return nil, fmt.Errorf("invalid range %q: %w", part, err)
		}
		ranges = append(ranges, r)
	}
	return ranges, nil
}

// validate refuses a configuration no facet query counts correctly: an
// unknown type, a negative terms limit (0 takes the default), or ranges
// that are missing, unbounded, non-finite, inverted, keyed twice or
// overlapping.
func (s *FacetSchema) validate() error {
	switch s.Type {
	case FacetTerms:
		if s.Limit < 0 {
			return fmt.Errorf("terms facet limit %d is negative", s.Limit)
		}
		return nil
	case FacetTypeRange:
		return validateRanges(s.Ranges)
	}
	return fmt.Errorf("unknown facet type %q", s.Type)
}

func validateRanges(ranges []FacetRange) error {
	if len(ranges) == 0 {
		return errors.New("a range facet needs at least one range")
	}
	keys := make(map[string]bool, len(ranges))
	for _, r := range ranges {
		if r.Key == "" {
			return errors.New("a range has no key")
		}
		if keys[r.Key] {
			return fmt.Errorf("range %q is declared twice", r.Key)
		}
		keys[r.Key] = true
		if r.Min == nil && r.Max == nil {
			return fmt.Errorf("range %q has neither bound", r.Key)
		}
		for _, b := range []*float64{r.Min, r.Max} {
			if b != nil && (math.IsNaN(*b) || math.IsInf(*b, 0)) {
				return fmt.Errorf("range %q has a non-finite bound", r.Key)
			}
		}
		if r.Min != nil && r.Max != nil && *r.Min >= *r.Max {
			return fmt.Errorf("range %q: its minimum is not below its maximum", r.Key)
		}
	}
	byMin := slices.Clone(ranges)
	slices.SortFunc(byMin, func(a, b FacetRange) int { return cmp.Compare(lowerBound(a), lowerBound(b)) })
	for i := 1; i < len(byMin); i++ {
		prev, next := byMin[i-1], byMin[i]
		if prev.Max == nil || lowerBound(next) < *prev.Max {
			return fmt.Errorf("ranges %q and %q overlap", prev.Key, next.Key)
		}
	}
	return nil
}

func lowerBound(r FacetRange) float64 {
	if r.Min == nil {
		return math.Inf(-1)
	}
	return *r.Min
}

// facetKeyTypes are the value types FacetKeys keys, whose text every
// backend reads alike. Interval, inet and cidr are not among them:
// PostgreSQL's interval text follows the session's IntervalStyle, inet
// writes a host's /32, and both network types write an IPv4-compatible
// IPv6 address in dotted form. A document's text differs by backend too.
var facetKeyTypes = map[core.FieldType]bool{
	core.FieldTypeString: true, core.FieldTypeBool: true,
	core.FieldTypeInt: true, core.FieldTypeFloat: true, core.FieldTypeDecimal: true,
	core.FieldTypeDate: true, core.FieldTypeTimestamp: true, core.FieldTypeTime: true,
	core.FieldTypeUUID: true, core.FieldTypeMACAddr: true,
}

// facetConfig is f's facet configuration, terms where none is declared.
func (f *FieldSchema) facetConfig() *FacetSchema {
	if f.FacetSchema != nil {
		return f.FacetSchema
	}
	return &FacetSchema{Type: FacetTerms}
}

// checkFacet refuses cfg on f where no backend counts it alike: a terms
// facet's values need a key every backend reads the same (facetKeyTypes),
// and a range facet's bounds are numbers, so it buckets a scalar number
// field. Generation, path declaration and BuildFacetQuery all apply it.
func checkFacet(f *FieldSchema, cfg *FacetSchema) error {
	switch cfg.Type {
	case FacetTerms:
		if t := facetValueType(f); !facetKeyTypes[t] {
			return fmt.Errorf("%s values have no facet key every backend reads alike", t)
		}
	case FacetTypeRange:
		switch {
		case f.Relation != nil:
			return errors.New("a relation's facet is terms, not range")
		case f.DataType == core.FieldTypeArray:
			return errors.New("an array's facet is terms, not range")
		case !isNumber(f.DataType):
			return fmt.Errorf("a range facet buckets numbers, not %s values", f.DataType)
		}
	}
	return cfg.validate()
}

func parseOneRange(s string) (FacetRange, error) {
	if minStr, ok := strings.CutSuffix(s, "+"); ok {
		min, err := strconv.ParseFloat(minStr, 64)
		if err != nil {
			return FacetRange{}, err
		}
		return FacetRange{Key: s, Min: &min, Max: nil}, nil
	}
	dashIdx := strings.Index(s[1:], "-")
	if dashIdx < 0 {
		return FacetRange{}, fmt.Errorf("expected format like '0-50' or '100+'")
	}
	dashIdx++
	minStr := s[:dashIdx]
	maxStr := s[dashIdx+1:]
	min, err := strconv.ParseFloat(minStr, 64)
	if err != nil {
		return FacetRange{}, err
	}
	max, err := strconv.ParseFloat(maxStr, 64)
	if err != nil {
		return FacetRange{}, err
	}
	return FacetRange{Key: s, Min: &min, Max: &max}, nil
}
