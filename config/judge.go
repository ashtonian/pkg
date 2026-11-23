package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
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
	j := &judge{ctx: ctx, layers: layers, from: len(layers), first: make([]int, len(layers)), entryAt: make([]map[string]int, len(layers))}
	for i, y := range layers {
		j.first[i] = len(j.candidates)
		if !y.withhold {
			verdicts[i].kept = y.entries
			continue
		}
		for e := range y.entries {
			j.from = min(j.from, i)
			j.candidates = append(j.candidates, entryRef{layer: i, entry: e})
		}
	}
	if len(j.candidates) == 0 {
		return verdicts, nil
	}
	for i := j.from; i < len(layers); i++ {
		if y := layers[i]; y.byEntry {
			j.entryAt[i] = make(map[string]int, len(y.entries))
			for e := range y.entries {
				j.entryAt[i][pathKey(y.entries[e].path)] = e
			}
		}
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
		v.withheld = append(v.withheld, &KeyError{Key: e.safe, Err: withheld[c]})
		v.unknown = append(v.unknown, e.unknown...)
	}
	return verdicts, nil
}

// fatal is why judging fails the load rather than an entry, given failed,
// a judgement's failure: the context ended, or a validate tag the validator
// cannot apply (ErrSchema), which is the declaration's fault and no
// entry's. It is nil when failed is the entries'.
func fatal(ctx context.Context, failed error) error {
	if err := abandoned(ctx); err != nil {
		return err
	}
	if errors.Is(failed, ErrSchema) {
		return failed
	}
	return nil
}

// judge decides which entries of the WithInvalidEntries sources merge. A
// candidate is one such entry; candidates are numbered in rank order, by
// source and within a source in the order its entries apply, and a set of
// them is a sorted slice of those numbers. A set is judged over the final
// merge: every layer, a judged source's entries narrowed to the set's.
type judge struct {
	ctx        context.Context
	checker    *keyChecker
	layers     []*layer
	candidates []entryRef
	// first is, for each layer, the number of its first candidate: entry e
	// of a judged layer i is candidate first[i]+e.
	first []int
	// entryAt indexes the entries of each layer from `from` up that is set
	// entry by entry, by key (see pathKey).
	entryAt []map[string]int
	// from is the first layer with a candidate; base and unkeyed are the
	// merge of the defaults and the layers beneath it, which no set
	// changes.
	from    int
	base    map[string]any
	unkeyed unkeyedSections
}

// entryRef is an entry of a layer: the layer's index, and the entry's in
// its entries.
type entryRef struct {
	layer, entry int
}

func (j *judge) entry(c int) *edit {
	at := j.candidates[c]
	return &j.layers[at.layer].entries[at.entry]
}

// verdict is how a set judges: the entries its merge could not set, and the
// failures of the configuration it makes that are at keys.
type verdict struct {
	unset    map[entryRef]error
	failures []failure
}

// failure is a failure of a judged configuration at a key: one, or for a
// rule comparing fields, each field's. err is the failure as a report
// gives it.
type failure struct {
	at  []keyPath
	err error
}

// run returns the candidates kept and the failure withholding each other
// candidate, as WithInvalidEntries describes. Each round judges the
// candidates left and tests each candidate it blames for the first time
// beside the candidates it does not blame: one blamed there is withheld,
// and one that is not is kept for good, since the conflict it is in is the
// final configuration's to fail. A round that withholds nothing ends it.
// Each candidate is tested at most once, so for k candidates ever blamed
// there are at most k tests and k+1 rounds: 2k+1 judgements.
func (j *judge) run() (keep []int, withheld map[int]error, err error) {
	keep = make([]int, len(j.candidates))
	for c := range keep {
		keep[c] = c
	}
	withheld = map[int]error{}
	tested := make([]bool, len(j.candidates))
	for {
		v, err := j.try(keep)
		if err != nil {
			return nil, nil, err
		}
		blamed := j.blame(v, keep)
		unblamed := slices.DeleteFunc(slices.Clone(keep), func(c int) bool { return blamed[c] != nil })
		progress := false
		for _, c := range slices.Sorted(maps.Keys(blamed)) {
			if tested[c] {
				continue
			}
			tested[c] = true
			set := union(unblamed, []int{c})
			beside, err := j.try(set)
			if err != nil {
				return nil, nil, err
			}
			if errs := j.blame(beside, set)[c]; errs != nil {
				withheld[c] = joinFailures(errs)
				keep = without(keep, []int{c})
				progress = true
			}
		}
		if !progress {
			return keep, withheld, nil
		}
	}
}

// joinFailures is errs as one error: the one, or all joined.
func joinFailures(errs []error) error {
	if len(errs) == 1 {
		return errs[0]
	}
	return errors.Join(errs...)
}

// try merges the layers with the candidates of set, as the load merges its
// sources, decodes the result as the load does and runs the validator's
// field rules over it: not the Validate method, the secret populator or the
// post-load hooks, which run once, in the load. err is why judging fails
// the load (see fatal).
func (j *judge) try(set []int) (verdict, error) {
	if err := abandoned(j.ctx); err != nil {
		return verdict{}, err
	}
	s, err := j.loaderOver(j.base, j.unkeyed)
	if err != nil {
		return verdict{}, err
	}
	v := verdict{unset: map[entryRef]error{}}
	next := 0
	for i := j.from; i < len(j.layers); i++ {
		y := j.layers[i]
		edits, refs := y.entries, []entryRef(nil)
		if y.withhold {
			edits = nil
			for ; next < len(set) && j.candidates[set[next]].layer == i; next++ {
				ref := j.candidates[set[next]]
				edits, refs = append(edits, y.entries[ref.entry]), append(refs, ref)
			}
		}
		refused, err := s.mergeLayer(y, edits)
		if err != nil {
			return verdict{}, err
		}
		for _, r := range refused {
			ref := entryRef{layer: i, entry: r.at}
			if y.withhold {
				ref = refs[r.at]
			}
			v.unset[ref] = r.err
		}
	}
	cfg, _, err := j.checker.decodeTree(s.k.Raw(), s.unkeyed)
	if err == nil {
		err = j.checker.options.fieldRules(j.ctx, cfg.Interface())
	}
	if fatal := fatal(j.ctx, err); fatal != nil {
		return verdict{}, fatal
	}
	v.failures = placeFailures(cfg, err)
	return v, nil
}

// placeFailures is each failure in err, a judgement's, at the keys it is
// at; cfg is the decoded config when decoding succeeded. A value that does
// not decode is at its key, and a field rule at its field's key and, for a
// rule comparing fields, the key of each field its param names. A failure
// the decoder or validator names no key for exactly — a Validate method's
// error, a WithValidator validator's that holds no field errors, a key
// beneath a map decoded whole — is at none and left out: it blames no
// entry.
func placeFailures(cfg reflect.Value, err error) []failure {
	var (
		out      []failure
		decoding *decodeFailures
		rules    validator.ValidationErrors
	)
	switch {
	case err == nil:
	case errors.As(err, &decoding):
		for _, f := range decoding.each {
			if f.exact && len(f.at) > 0 {
				out = append(out, failure{at: []keyPath{f.at}, err: fmt.Errorf("%w: %w", ErrValidation, f.err)})
			}
		}
	case cfg.IsValid() && errors.As(err, &rules):
		for _, fe := range rules {
			if at := ruleKeys(cfg, fe); at != nil {
				out = append(out, failure{at: at, err: ruleViolation(cfg, validator.ValidationErrors{fe})})
			}
		}
	}
	return out
}

// ruleKeys is the key of each field fe, a failed rule of cfg, is about: its
// own, and for a rule comparing fields (eqfield, ltefield, eqcsfield,
// required_with, ...) each field its param names. It is nil when one of
// them cannot be placed exactly.
func ruleKeys(cfg reflect.Value, fe validator.FieldError) []keyPath {
	own, exact := validatorKey(cfg, fe.StructNamespace())
	if !exact || len(own) == 0 {
		return nil
	}
	at := []keyPath{own}
	_, fields := ruleFieldParams(fe)
	for _, f := range fields {
		p, exact := validatorKey(cfg, f.ns)
		if !exact || len(p) == 0 {
			return nil
		}
		at = append(at, p)
	}
	return at
}

// blame is, for each candidate of set that v, set's verdict, blames, the
// failures it is blamed for. An entry the merge could not set is blamed for
// that. A failure at a key is blamed on the candidate that set the key last
// (see setter); a rule comparing fields on the one that set each field it
// names, when a candidate did.
func (j *judge) blame(v verdict, set []int) map[int][]error {
	in := make([]bool, len(j.candidates))
	for _, c := range set {
		in[c] = true
	}
	out := map[int][]error{}
	for ref, err := range v.unset {
		if j.layers[ref.layer].withhold {
			c := j.first[ref.layer] + ref.entry
			out[c] = append(out[c], err)
		}
	}
	for _, f := range v.failures {
		var by []int
		for _, k := range f.at {
			if c, ok := j.setter(k, in, v.unset); ok && !slices.Contains(by, c) {
				by = append(by, c)
			}
		}
		for _, c := range by {
			out[c] = append(out[c], f.err)
		}
	}
	return out
}

// setter is the candidate that set k last in the merge of a judgement — in,
// the candidates it merged, and unset, the entries it could not set — and
// true; false when what the merge holds at k is no candidate's: a source not
// judged, a judged source's keys outside its entries, the defaults, or
// nothing. A source or an entry sets k when what it merges holds k, or
// replaces a key holding k whole (see spine).
func (j *judge) setter(k keyPath, in []bool, unset map[entryRef]error) (int, bool) {
	s := newSpine(k, j.base)
	for i := j.from; i < len(j.layers); i++ {
		y := j.layers[i]
		if !y.byEntry {
			s.apply(0, y.tree, -1)
			continue
		}
		// An entry sets k only from k or a key holding it, and those apply
		// in the order of their keys, a key before the keys beneath it.
		for n := 1; n <= len(k); n++ {
			e, ok := j.entryAt[i][pathKey(k[:n])]
			if !ok {
				continue
			}
			if _, refused := unset[entryRef{layer: i, entry: e}]; refused {
				continue
			}
			owner := -1
			if y.withhold {
				if owner = j.first[i] + e; !in[owner] {
					continue
				}
			}
			s.apply(n, y.entries[e].value, owner)
		}
		if y.tree != nil {
			s.apply(0, y.tree, -1) // the keys outside its entries, which merge over them
		}
	}
	owner := s.nodes[len(k)].owner
	return owner, owner >= 0
}

// spine is what a merge holds along one key k, by depth: the root at 0, k
// itself at len(k). Each node records whether a value is there, whether it
// is a map that a map merged over merges into key by key, and the
// candidate that set it last, or -1.
//
// Merges are applied one by one, an entry's value at its key, as
// mergeValue merges a source's tree. Within a source, its entries merge
// into one another before they merge into the layers beneath (see
// within); the two agree wherever each key holds a map in every source
// that sets it or in none, and a key that holds a map in one source and a
// value in another fails to decode at that key, not beneath it.
type spine struct {
	k     keyPath
	nodes []spineNode
}

type spineNode struct {
	present bool
	isMap   bool
	owner   int
}

// newSpine is the spine of k in base, which no candidate set.
func newSpine(k keyPath, base map[string]any) *spine {
	s := &spine{k: k, nodes: make([]spineNode, len(k)+1)}
	for d := range s.nodes {
		s.nodes[d].owner = -1
	}
	s.place(0, base, -1)
	return s
}

// apply merges u, which owner sets at depth d of k, over what is there: a
// map[string]any into a map key by key, setting what it holds; anything
// else in its place.
func (s *spine) apply(d int, u any, owner int) {
	for a := 1; a < d; a++ {
		if !s.nodes[a].present { // made to hold what u sets beneath it
			s.nodes[a] = spineNode{present: true, isMap: s.k[a].kind != segIndex, owner: -1}
		}
	}
	for {
		m, merges := u.(map[string]any)
		if !merges || !s.nodes[d].isMap {
			s.place(d, u, owner)
			return
		}
		s.nodes[d].owner = owner
		if d == len(s.k) || s.k[d].kind == segIndex {
			return
		}
		next, ok := m[s.k[d].name]
		if !ok {
			return
		}
		u, d = next, d+1
	}
}

// place puts u, which owner sets, at depth d in place of what is there.
// Beneath it the spine is what u holds: a value u holds whole, such as a
// typed map or a scalar, is owner's at every depth, and a key u does not
// hold that was there before is gone by owner's doing.
func (s *spine) place(d int, u any, owner int) {
	for ; d < len(s.nodes); d++ {
		s.nodes[d] = spineNode{present: true, isMap: isMergedMap(u), owner: owner}
		if d == len(s.k) {
			return
		}
		next, ok, walkable := childAt(u, s.k[d])
		switch {
		case !walkable:
			for d++; d < len(s.nodes); d++ {
				s.nodes[d] = spineNode{present: true, owner: owner}
			}
			return
		case !ok:
			for d++; d < len(s.nodes); d++ {
				gone := -1
				if s.nodes[d].present {
					gone = owner
				}
				s.nodes[d] = spineNode{owner: gone}
			}
			return
		}
		u = next
	}
}

// isMergedMap reports whether u is a map whose keys a later source's map
// sets one by one.
func isMergedMap(u any) bool {
	_, ok := mergedMap(u)
	return ok
}

// childAt is what u holds at seg. walkable is false when u is a value the
// merge holds whole, which it does not look inside.
func childAt(u any, seg segment) (child any, ok, walkable bool) {
	switch v := u.(type) {
	case map[string]any:
		child, ok = v[seg.name]
		return child, ok && seg.kind != segIndex, true
	case wholeMap:
		child, ok = v[seg.name]
		return child, ok && seg.kind != segIndex, true
	case []any:
		if seg.kind == segIndex && seg.index < len(v) {
			return v[seg.index], true, true
		}
		return nil, false, true
	}
	return nil, false, false
}

// pathKey is p as entryAt keys it: a field and a map key of one name are one
// segment, as they are one key in a merged tree.
func pathKey(p keyPath) string {
	var b strings.Builder
	for _, s := range p {
		if s.kind == segIndex {
			b.WriteString("[" + strconv.Itoa(s.index) + "]")
		} else {
			b.WriteString(strconv.Quote(s.name))
		}
	}
	return b.String()
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
