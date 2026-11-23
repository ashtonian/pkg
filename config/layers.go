package config

import (
	"context"
	"fmt"
	"slices"
)

// A load reads and prepares every source before it merges any: each
// becomes a layer, and the layers merge in rank order through mergeLayer,
// the one path that builds a load's merge.

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
	return &layer{name: name, label: label, tree: tree, unkeyed: w.unkeyed}, nil
}

// mergeLayer merges y, with edits, the entries of y that merge, set over
// what l holds, and returns the entries it cannot set. A layer not set
// entry by entry merges its tree whole. y is left as it was.
func (l *loader) mergeLayer(y *layer, edits []edit) ([]*KeyError, error) {
	if !y.byEntry {
		return nil, l.mergeSource(y.name, y.tree, y.unkeyed, y.label)
	}
	tree, refused := setEdits(l.k.Raw, edits)
	mergeTrees(deepCopyMap(y.tree), tree) // mergeTrees settles what it merges in place
	return refused, l.mergeSource(y.name, tree, editsUnkeyed(edits), y.label)
}

// mergeLayers merges layers into the load in rank order. A source
// WithInvalidEntries judges merges the entries it keeps, judged over the
// layers merged beneath it, and reports the others; any other source's
// entry that cannot be set fails the load.
func (l *loader) mergeLayers(ctx context.Context, layers []*layer) error {
	for _, y := range layers {
		edits, invalid := y.entries, slices.Clone(y.refused)
		if y.withhold {
			kept, rejected, unknown, err := l.checker.withhold(ctx, l.k.Raw(), l.unkeyed, edits)
			if err != nil {
				return err
			}
			edits, invalid = kept, append(invalid, rejected...)
			l.unmergedUnknown = append(l.unmergedUnknown, unknown...)
		}
		for _, e := range edits {
			for _, a := range e.aliased {
				l.aliased = append(l.aliased, a+" in "+y.name)
			}
		}
		refused, err := l.mergeLayer(y, edits)
		if err != nil {
			return err
		}
		invalid = append(invalid, refused...)
		if y.withhold {
			if err := l.reportWithheld(ctx, y.name, y.report, invalid); err != nil {
				return err
			}
		} else if len(invalid) > 0 {
			return fmt.Errorf("loading %s: %w", y.name, joinKeyErrors(invalid))
		}
	}
	return nil
}
