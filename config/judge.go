package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
)

// entryVerdict is which entries of a layer merge: kept, and why each other
// entry is withheld, with the keys inside withheld values that no field
// reads.
type entryVerdict struct {
	kept     []edit
	withheld []*KeyError
	unknown  []UnknownKey
}

// judgeEntries is, for each of layers, the entries that merge: every entry
// of a source WithInvalidEntries does not judge, and of the sources it
// judges, those the judge keeps, as WithInvalidEntries describes. l holds
// the defaults the layers merge over; o is the load's options. err fails
// the load (see fatal).
func (l *loader) judgeEntries(ctx context.Context, o *options, layers []*layer) ([]entryVerdict, error) {
	verdicts := make([]entryVerdict, len(layers))
	j := &judge{ctx: ctx, layers: layers, from: len(layers)}
	for i, y := range layers {
		if !y.withhold {
			verdicts[i].kept = y.entries
			continue
		}
		for e := range y.entries {
			j.from = min(j.from, i)
			j.candidates = append(j.candidates, candidate{layer: i, entry: e})
		}
	}
	if len(j.candidates) == 0 {
		return verdicts, nil
	}
	j.checker = &keyChecker{root: l.root, schema: l.schema, defaults: l.k.Raw(), unkeyed: maps.Clone(l.unkeyed), options: o}
	beneath, err := j.loaderOver(j.checker.defaults, j.checker.unkeyed)
	if err != nil {
		return nil, err
	}
	for i, y := range layers[:j.from] {
		if _, err := beneath.mergeLayer(y, verdicts[i].kept); err != nil {
			return nil, err
		}
	}
	j.base, j.unkeyed = beneath.k.Raw(), beneath.unkeyed

	keep, withheld, err := j.run()
	if err != nil {
		return nil, err
	}
	for _, c := range keep {
		v := &verdicts[j.candidates[c].layer]
		v.kept = append(v.kept, *j.entry(c))
	}
	for _, c := range slices.Sorted(maps.Keys(withheld)) {
		e, v := j.entry(c), &verdicts[j.candidates[c].layer]
		v.withheld = append(v.withheld, &KeyError{Key: e.safe, Err: entryError(e, withheld[c])})
		v.unknown = append(v.unknown, e.unknown...)
	}
	return verdicts, nil
}

// entryError is err, why e is withheld, as its report gives it: a refusal
// of e's own key without the key, which the report names already.
func entryError(e *edit, err error) error {
	if ke, ok := err.(*KeyError); ok && ke.Key == e.safe {
		return ke.Err
	}
	return err
}

// fatal is why judging fails the load rather than an entry, given failed,
// an entry's failure: the context ended, or a validate tag the validator
// cannot apply (ErrSchema), which is the declaration's fault and no
// entry's. It is nil when failed is the entry's own.
func fatal(ctx context.Context, failed error) error {
	if err := ended(ctx); err != nil {
		return err
	}
	if errors.Is(failed, ErrSchema) {
		return failed
	}
	return nil
}

// judge decides which entries of the WithInvalidEntries sources merge. A
// candidate is one such entry; candidates are numbered in rank order, by
// source and within a source by key, and a set of them is a sorted slice
// of those numbers. A set is judged over the final merge: every layer, a
// judged source's entries narrowed to the set's.
type judge struct {
	ctx        context.Context
	checker    *keyChecker
	layers     []*layer
	candidates []candidate
	// from is the first layer with a candidate; base and unkeyed are the
	// merge of the defaults and the layers beneath it, which no set
	// changes.
	from    int
	base    map[string]any
	unkeyed unkeyedSections
}

// candidate is an entry of a judged source: the layer's index, and the
// entry's in its entries.
type candidate struct {
	layer, entry int
}

func (j *judge) entry(c int) *edit {
	at := j.candidates[c]
	return &j.layers[at.layer].entries[at.entry]
}

// verdict is how a set decodes and validates: err is nil when it does, and
// otherwise the failure as a load reports it, with at the key of each
// failure that names one.
type verdict struct {
	err error
	at  []keyPath
}

// run returns the candidates kept and the failure withholding each other
// candidate, as WithInvalidEntries describes.
func (j *judge) run() (keep []int, withheld map[int]error, err error) {
	all := make([]int, len(j.candidates))
	for c := range all {
		all[c] = c
	}
	v, err := j.try(all)
	if err != nil || v.err == nil {
		return all, nil, err
	}

	// A candidate that passes with the other sources alone is kept
	// whatever the others do; the rest are judged.
	var passing, failing []int
	last := map[int]error{}
	for _, c := range all {
		alone, err := j.try([]int{c})
		if err != nil {
			return nil, nil, err
		}
		if alone.err == nil {
			passing = append(passing, c)
		} else {
			failing = append(failing, c)
			last[c] = alone.err
		}
	}
	if len(failing) == 0 {
		return all, nil, nil
	}

	// Set aside the candidates each failure is attributed to, until the
	// rest pass or a failure is attributed to none of them. The set judged
	// first is every candidate.
	keep = failing
	var attributed []int
	for v.err != nil {
		named := j.named(v, union(passing, keep), keep)
		if len(named) == 0 {
			break
		}
		for _, c := range named {
			last[c] = v.err
		}
		attributed = union(attributed, named)
		keep = without(keep, named)
		if v, err = j.try(union(passing, keep)); err != nil {
			return nil, nil, err
		}
	}
	if v.err != nil && len(keep) > 0 {
		// The failure names none of the candidates left, so each is
		// retried on its own below.
		if v, err = j.try(passing); err != nil {
			return nil, nil, err
		}
		if v.err == nil {
			keep = nil
		}
	}
	if v.err != nil {
		// No set reached passes: every candidate merges but those a
		// failure was attributed to, and the load fails on that
		// configuration, the set judged last.
		withheld = make(map[int]error, len(attributed))
		for _, c := range attributed {
			withheld[c] = last[c]
		}
		return without(all, attributed), withheld, nil
	}

	// Retry every candidate set aside, in rank order, until a pass keeps
	// none more: one that needs another, whichever comes first, is kept
	// with it.
	rest := without(failing, keep)
	for {
		var still []int
		for _, c := range rest {
			v, err := j.try(union(passing, keep, []int{c}))
			if err != nil {
				return nil, nil, err
			}
			if v.err == nil {
				keep = union(keep, []int{c})
			} else {
				last[c] = v.err
				still = append(still, c)
			}
		}
		if len(still) == len(rest) {
			break
		}
		rest = still
	}
	withheld = make(map[int]error, len(rest))
	for _, c := range rest {
		withheld[c] = last[c]
	}
	return union(passing, keep), withheld, nil
}

// named is the candidates of among that v's failures are attributed to;
// judged is the set v judged. A failure at a key is attributed to the
// candidates of judged that set it, a key holding it or one beneath it,
// from the highest-ranked source among them: a lower one the merge
// shadows there is not at fault. A failure at no key, or at the root, is
// attributed to none.
func (j *judge) named(v verdict, judged, among []int) []int {
	var named []int
	for _, at := range v.at {
		if len(at) == 0 {
			continue
		}
		top := -1
		var meet []int
		for _, c := range judged {
			if !pathsMeet(j.entry(c).path, at) {
				continue
			}
			if l := j.candidates[c].layer; l > top {
				top, meet = l, []int{c}
			} else if l == top {
				meet = append(meet, c)
			}
		}
		for _, c := range meet {
			if slices.Contains(among, c) {
				named = union(named, []int{c})
			}
		}
	}
	return named
}

// try merges the layers with the candidates of set, as the load merges its
// sources, and decodes and validates the result. err is why judging fails
// the load (see fatal).
func (j *judge) try(set []int) (verdict, error) {
	if err := ended(j.ctx); err != nil {
		return verdict{}, err
	}
	s, err := j.loaderOver(j.base, j.unkeyed)
	if err != nil {
		return verdict{}, err
	}
	var (
		v       verdict
		refused []*KeyError
		next    int
	)
	for i := j.from; i < len(j.layers); i++ {
		y := j.layers[i]
		edits := y.entries
		if y.withhold {
			edits = nil
			for ; next < len(set) && j.candidates[set[next]].layer == i; next++ {
				edits = append(edits, *j.entry(set[next]))
			}
		}
		failed, err := s.mergeLayer(y, edits)
		if err != nil {
			return verdict{}, err
		}
		for _, r := range failed {
			e := &edits[r.at]
			v.at = append(v.at, e.path)
			refused = append(refused, &KeyError{Key: e.safe, Err: r.err})
		}
	}
	if len(refused) > 0 {
		v.err = joinKeyErrors(refused)
		return v, nil
	}
	result, _, err := j.checker.decode(j.ctx, s.k.Raw(), s.unkeyed)
	if err == nil {
		return verdict{}, nil
	}
	if fatal := fatal(j.ctx, err); fatal != nil {
		return verdict{}, fatal
	}
	return verdict{err: err, at: failedAt(err, result)}, nil
}

// loaderOver is a loader holding tree, a merge, and unkeyed, its koanf:"-"
// fields, for a set to merge over; it attributes nothing.
func (j *judge) loaderOver(tree map[string]any, unkeyed unkeyedSections) (*loader, error) {
	s := &loader{k: koanf.New("."), root: j.checker.root, schema: j.checker.schema, unkeyed: maps.Clone(unkeyed)}
	if err := s.k.Load(confmap.Provider(tree, ""), nil, koanf.WithMergeFunc(koanfMerge)); err != nil {
		return nil, fmt.Errorf("judging entries: %w: %w", ErrSource, err)
	}
	return s, nil
}

// failedAt is the key of each failure in err, a set's failure, that names
// one; result is the decoded config when decoding succeeded. A value that
// does not decode is at its key, and a validate rule at its field's key or
// for a rule naming a field beside its own the struct holding both;
// anything else — a Validate method's error, a WithValidator validator's
// that holds no field errors, a rule naming a field of another struct —
// names none.
func failedAt(err error, result any) []keyPath {
	var decoding *decodeFailures
	if errors.As(err, &decoding) {
		return decoding.at
	}
	var (
		invalid *validationError
		rules   validator.ValidationErrors
		at      []keyPath
	)
	if result != nil && errors.As(err, &invalid) && errors.As(invalid.cause, &rules) {
		cfg := reflect.ValueOf(result)
		for _, fe := range rules {
			at = append(at, ruleKey(cfg, fe))
		}
	}
	return at
}

// ruleKey is the key a failed validate rule is attributed to: its field's;
// the struct holding it for a rule naming a sibling field, which either
// field's entry may have broken; none for a rule naming a field of another
// struct, which only the validator resolves.
func ruleKey(cfg reflect.Value, fe validator.FieldError) keyPath {
	tag, ns := fe.Tag(), fe.StructNamespace()
	switch {
	case strings.HasSuffix(tag, "csfield"):
		return nil
	case fieldParamRules[tag] || fieldValueParamRules[tag] || fieldListParamRules[tag]:
		if i := strings.LastIndex(ns, "."); i >= 0 {
			ns = ns[:i]
		}
	}
	p, _ := validatorKey(cfg, ns)
	return p
}

// pathsMeet reports whether one of a and b is the other or beneath it, a
// field and a map key of one name being one segment.
func pathsMeet(a, b keyPath) bool {
	for i := range min(len(a), len(b)) {
		s, t := a[i], b[i]
		switch {
		case s.kind == segIndex || t.kind == segIndex:
			if s.kind != t.kind || s.index != t.index {
				return false
			}
		case s.name != t.name:
			return false
		}
	}
	return true
}

// union is the numbers in any of sets, sorted, each once.
func union(sets ...[]int) []int {
	out := slices.Concat(sets...)
	slices.Sort(out)
	return slices.Compact(out)
}

// without is set, sorted, less the numbers in drop.
func without(set, drop []int) []int {
	return slices.DeleteFunc(slices.Clone(set), func(c int) bool { return slices.Contains(drop, c) })
}
