package parsememory

import (
	"fmt"
	"math/bits"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// keyRange is the rows of a table whose first width key columns hold one
// prefix. probe holds the prefix in storage form and NULL in every later
// column, so it orders after every row of the range: NULL orders last, and
// no key holds one.
type keyRange struct {
	probe *record
	width int
}

// prefixRange is the range of rows whose first len(prefix) key columns
// hold prefix, each value normalized as its column's type
// (core.NormalizeKey).
func (def *tableDef) prefixRange(prefix core.Key) (keyRange, error) {
	if len(prefix) == 0 || len(prefix) > len(def.key) {
		return keyRange{}, fmt.Errorf("%d values for a prefix of the %d key columns", len(prefix), len(def.key))
	}
	normalized, err := core.NormalizeKey(def.keyTypes()[:len(prefix)], prefix)
	if err != nil {
		return keyRange{}, err
	}
	probe := &record{values: make([]any, len(def.columns))}
	for i, v := range normalized {
		at := def.key[i]
		probe.values[at], err = storageValue(v, def.columns[at].typ)
		if err != nil {
			return keyRange{}, fmt.Errorf("key column %s: %w", def.columns[at].name, err)
		}
	}
	r := keyRange{probe: probe, width: len(prefix)}
	return r, nil
}

// eachInRange calls visit with r's rows of t, from the last in key order
// to the first, until visit returns false, and reports whether it went
// through them all. A range of the whole key is one row at most.
func (def *tableDef) eachInRange(t *tree, r keyRange, visit func(*record) bool) bool {
	if r.width == len(def.key) {
		rec, ok := t.Get(r.probe)
		return !ok || visit(rec)
	}
	key, types := def.key[:r.width], def.keyTypes()[:r.width]
	through := true
	t.Descend(r.probe, func(rec *record) bool {
		if compareKeys(key, types, rec, r.probe) != 0 {
			return false
		}
		through = visit(rec)
		return through
	})
	return through
}

// lookupRows calls visit with the rows of t that cond may select, until
// visit returns false: the rows of cond's key ranges (lookupRanges), each
// range found in O(log n), when reading them costs less than a scan, and
// otherwise every row. The caller evaluates cond on each.
func (def *tableDef) lookupRows(t *tree, cond *filter.LookupCond, visit func(*record) bool) {
	ranges, ok := def.lookupRanges(cond, t.Len())
	if !ok {
		t.Scan(visit)
		return
	}
	for _, r := range ranges {
		if !def.eachInRange(t, r, visit) {
			return
		}
	}
}

// lookupRanges are the key ranges that hold every row cond can select, one
// per distinct value in key order, when cond holds the table's first key
// columns to a set of values (keyPrefixes) and finding the ranges, each in
// O(log rows), costs less than reading the table's rows. ok is false
// otherwise, and when a value is none its key columns hold: a scan then
// leaves the decision to the condition itself.
func (def *tableDef) lookupRanges(cond *filter.LookupCond, rows int) ([]keyRange, bool) {
	prefixes, ok := def.keyPrefixes(cond)
	if !ok || len(prefixes)*bits.Len(uint(rows)) >= rows {
		return nil, false
	}
	ranges := make([]keyRange, 0, len(prefixes))
	for _, prefix := range prefixes {
		r, err := def.prefixRange(prefix)
		if err != nil {
			return nil, false
		}
		ranges = append(ranges, r)
	}
	if len(ranges) == 0 {
		return ranges, true
	}
	width := ranges[0].width
	key, types := def.key[:width], def.keyTypes()[:width]
	cmp := func(a, b keyRange) int { return compareKeys(key, types, a.probe, b.probe) }
	slices.SortFunc(ranges, cmp)
	ranges = slices.CompactFunc(ranges, func(a, b keyRange) bool { return cmp(a, b) == 0 })
	return ranges, true
}

// keyPrefixes are the prefixes of def's key cond holds its rows to: the
// values of a filter.WhereEq, WhereIn or WhereKeyIn on the first key
// columns, in key order, alone or a conjunct of a filter.WhereAll, typed
// as the table types those columns (LookupCond.Types). A comparison typed
// so holds a row exactly when the table's key order (core.CompareCanonical)
// holds the row's key equal to a value; an untyped one compares by its
// values' Go types, so ok is false for it, as it is for a condition that
// pins no key prefix.
func (def *tableDef) keyPrefixes(cond *filter.LookupCond) ([]core.Key, bool) {
	if cond == nil || len(cond.Or) > 0 {
		return nil, false
	}
	if len(cond.And) > 0 {
		for _, c := range cond.And {
			prefixes, ok := def.keyPrefixes(c)
			if ok {
				return prefixes, true
			}
		}
		return nil, false
	}
	var columns []string
	var prefixes []core.Key
	switch cond.Op {
	case filter.LookupOpEq:
		columns, prefixes = []string{cond.Column}, []core.Key{{cond.Value}}
	case filter.LookupOpIn:
		columns = []string{cond.Column}
		prefixes = make([]core.Key, len(cond.Values))
		for i, v := range cond.Values {
			prefixes[i] = core.Key{v}
		}
	case filter.LookupOpKeyIn:
		columns, prefixes = cond.Columns, cond.Keys
	default:
		return nil, false
	}
	if len(columns) == 0 || len(columns) > len(def.key) || len(cond.Types) != len(columns) {
		return nil, false
	}
	for i, name := range columns {
		c := def.columns[def.key[i]]
		typed := c.typ != (core.TypeInfo{}) && cond.Types[i] != (core.TypeInfo{})
		if c.name != name || !typed || !typesAgree(c.typ, cond.Types[i]) {
			return nil, false
		}
	}
	return prefixes, true
}
