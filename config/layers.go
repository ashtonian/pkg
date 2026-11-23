package config

import (
	"context"
	"fmt"
	"slices"
)

// A load reads and prepares every source before it merges any: each
// becomes a layer, the entries of the sources under WithInvalidEntries are
// judged against every layer (see judge.go), and then the layers merge in
// rank order through mergeLayer, the one path that builds a load's merge
// and each merge the judge tries.

// layer is one source, read and prepared to merge.
type layer struct {
	name  string
	label labeler
	// tree is what the source sets, keyed as the fields are (see keyWalk):
	// all of it, or for a source set entry by entry, what lies outside its
	// entries.
	tree map[string]any
	// unkeyed is the koanf:"-" fields of the typed sections tree sets.
	unkeyed []unkeyedFields
	// byEntry marks a source set entry by entry over the layers beneath it:
	// an entry source, or a provider WithInvalidEntries judges.
	byEntry bool
	entries []edit
	// withhold is set for a source WithInvalidEntries judges, whose report
	// receives what it withholds.
	withhold bool
	report   func(context.Context, []KeyError) error
	// refused is the entries refused as the source was prepared.
	refused []*KeyError
	// claimsUnknown is set for a source under WithUnknownEntries, whose
	// report receives the keys it sets that no field reads.
	claimsUnknown bool
	unknownReport func(context.Context, []UnknownKey) error
	// unknown is the keys the source sets that no field reads and that
	// never merge: an entry naming nothing config reads, and the keys
	// inside a withheld entry's value.
	unknown []UnknownKey
	// merged is what the source merged into the load, set as the load
	// merges it: tree, or for a source set entry by entry, the tree of the
	// entries set with tree merged over it.
	merged map[string]any
	// rank is the source's rank in the load's merge, set as it merges.
	rank int
}

// prepare is data, the tree of the source called name, as a layer whose
// values label attributes. A source that sets one field under two
// spellings fails.
func (l *loader) prepare(name string, data map[string]any, label labeler) (*layer, error) {
	w := keyWalk{schema: l.schema}
	tree, err := l.walk(&w, name, data)
	if err != nil {
		return nil, err
	}
	y := &layer{name: name, label: label, tree: tree, unkeyed: w.unkeyed}
	return y, nil
}

// mergeLayer merges y, with edits, the entries of y that merge, set over
// what l holds, and returns the tree it merged and the entries it cannot
// set. A layer not set entry by entry merges its tree whole. y is left as
// it was: the judge merges a layer once for every set of entries it tries.
func (l *loader) mergeLayer(y *layer, edits []edit) (merged map[string]any, refused []refusal, err error) {
	if !y.byEntry {
		return y.tree, nil, l.mergeSource(y.name, y.tree, y.unkeyed, y.label)
	}
	t := newEditTree(l.k.Raw)
	var unkeyed []unkeyedFields
	for i := range edits {
		err = t.set(&edits[i])
		if err != nil {
			refused = append(refused, refusal{at: i, err: err})
			continue
		}
		unkeyed = append(unkeyed, edits[i].unkeyed...)
	}
	tree := t.tree
	mergeTrees(deepCopyMap(y.tree), tree) // mergeTrees settles what it merges in place
	// The load merges a copy of tree (see merge_input.go), so tree stays
	// what this source merged.
	return tree, refused, l.mergeSource(y.name, tree, unkeyed, y.label)
}

// refusal is an entry a merge could not set: its index among the entries
// set, and why.
type refusal struct {
	at  int
	err error
}

// mergeLayers merges layers into the load in rank order, each with the
// entries its verdict keeps. A source WithInvalidEntries judges reports
// the others; any other source's entry that cannot be set fails the load.
func (l *loader) mergeLayers(ctx context.Context, layers []*layer, verdicts []entryVerdict) error {
	for i, y := range layers {
		v := verdicts[i]
		y.unknown = append(y.unknown, v.unknown...)
		y.rank = len(l.sources)
		for _, e := range v.kept {
			for _, a := range e.aliased {
				l.aliased = append(l.aliased, a+" in "+y.name)
			}
		}
		merged, refused, err := l.mergeLayer(y, v.kept)
		if err != nil {
			return err
		}
		y.merged = merged
		invalid := slices.Concat(y.refused, v.withheld)
		for _, r := range refused {
			invalid = append(invalid, newKeyError(l.schema, v.kept[r.at].path, false, r.err))
		}
		if y.withhold {
			err = l.reportWithheld(ctx, y.name, y.report, invalid)
			if err != nil {
				return err
			}
		} else if len(invalid) > 0 {
			return fmt.Errorf("loading %s: %w", y.name, joinKeyErrors(invalid))
		}
	}
	return nil
}
