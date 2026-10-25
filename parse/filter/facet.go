package filter

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// FacetType determines how aggregation SQL is generated.
type FacetType string

const (
	FacetTerms     FacetType = "terms"
	FacetTypeRange FacetType = "range"
)

// FacetSchema defines a single facetable field.
type FacetSchema struct {
	Field   string
	SQLExpr string
	Type    FacetType
	Limit   int
	Ranges  []FacetRange
}

// FacetRange defines a bucket boundary for range facets.
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

// FacetBuckets orders one facet's counts as BuildFacetQuery returns them,
// for a backend that counts in Go: a terms facet by count, ties by key in
// code-point order, cut to limit (none when limit <= 0); a range facet in
// its declared order. Keys without a count are dropped.
func FacetBuckets(cfg *FacetSchema, counts map[string]int64, limit int) []FacetBucket {
	out := make([]FacetBucket, 0, len(counts))
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
	slices.SortFunc(out, func(a, b FacetBucket) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
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

// ParseFacetTag parses the `facet` struct tag.
func ParseFacetTag(tag string) (*FacetSchema, error) {
	if tag == "" {
		return nil, nil
	}

	typePart, configPart, _ := strings.Cut(tag, ":")

	switch FacetType(typePart) {
	case FacetTerms:
		limit := 0
		if configPart != "" {
			n, err := strconv.Atoi(configPart)
			if err != nil {
				return nil, fmt.Errorf("invalid terms facet limit %q: %w", configPart, err)
			}
			limit = n
		}
		return &FacetSchema{Type: FacetTerms, Limit: limit}, nil

	case FacetTypeRange:
		if configPart == "" {
			return nil, fmt.Errorf("range facet requires bucket definitions (e.g., range:0-50,50-100,100+)")
		}
		ranges, err := parseRanges(configPart)
		if err != nil {
			return nil, err
		}
		return &FacetSchema{Type: FacetTypeRange, Ranges: ranges}, nil

	default:
		return nil, fmt.Errorf("unknown facet type %q", typePart)
	}
}

func parseRanges(s string) ([]FacetRange, error) {
	parts := strings.Split(s, ",")
	ranges := make([]FacetRange, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		r, err := parseOneRange(part)
		if err != nil {
			return nil, fmt.Errorf("invalid range %q: %w", part, err)
		}
		ranges = append(ranges, r)
	}
	return ranges, nil
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
