package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/ashtonian/pkg/config/internal/deepcopy"
)

// SourceOption configures one base or override provider.
type SourceOption func(*providerSource)

// WithInvalidEntries withholds the source's invalid entries and reports
// them, preserving lower-precedence values. report receives the sorted
// rejected keys after each read, including an empty slice when none were
// rejected; nil discards the report. Its error fails the load with
// ErrSource. Report errors omit values; their wrapped causes are intended
// only for trusted diagnostics.
//
// An entry of a provider is a struct section's field, checked field by
// field; a collection or self-decoding value is one entry. An entry of
// [WithBaseEntries] or [WithOverrideEntries] is one stored key. Entries are
// judged over the layers merged beneath the source, as Load decodes and
// validates them: when the source's entries all apply together, all are
// kept. Otherwise an entry is withheld when it fails both on its own and
// with every entry of the source that passes on its own applied — so an
// entry that needs another, a list element after the one before it or a
// field its section requires, is kept with it. An entry that passes on its
// own is kept: a rule it breaks only together with other entries is the
// final configuration's to fail, and cross-field failures are never
// resolved by choosing entries to discard. Judging costs one decode when
// every entry applies, and at most one per entry and one more per entry
// failing on its own when one does not.
//
// Defaults must validate before any source is read, so a broken baseline
// cannot be mistaken for bad stored entries. Unknown entries remain
// available to the normal unknown-input handler, including declared unknown
// children of a withheld entry. Read and parser errors and cancellation
// still fail the load, with the cause the context ended with, and a load
// whose context has ended reports nothing. A validate tag the default
// validator cannot apply fails the load with ErrSchema, whichever entry
// brings the validator to it: it is the declaration's fault, not the
// entry's.
func WithInvalidEntries(report func(context.Context, []KeyError) error) SourceOption {
	return func(s *providerSource) { s.withhold, s.rejected = true, report }
}

// withhold judges edits, the entries of one source in key order, over
// below, the tree of the layers merged beneath it, and belowUnkeyed, the
// koanf:"-" fields they hold, as WithInvalidEntries describes, and returns
// those it keeps; each withheld one is in rejected, and the keys inside its
// value that no field reads in unknown. It does not change below or the
// edits; err fails the load (see fatal).
func (c *keyChecker) withhold(ctx context.Context, below map[string]any, belowUnkeyed unkeyedSections, edits []edit) (kept []edit, rejected []*KeyError, unknown []UnknownKey, err error) {
	if len(edits) == 0 {
		return nil, nil, nil, nil
	}
	failed := c.passes(ctx, below, belowUnkeyed, edits)
	if failed == nil {
		return edits, nil, nil, nil
	}
	if err := fatal(ctx, failed); err != nil {
		return nil, nil, nil, err
	}
	alone := make([]error, len(edits)) // nil: the edit passes on its own
	for i := range edits {
		alone[i] = c.passes(ctx, below, belowUnkeyed, edits[i:i+1])
		if err := fatal(ctx, alone[i]); err != nil {
			return nil, nil, nil, err
		}
	}
	for i, e := range edits {
		if alone[i] == nil {
			kept = append(kept, e)
			continue
		}
		with := make([]edit, 0, len(edits))
		for j := range edits {
			if alone[j] == nil || j == i {
				with = append(with, edits[j])
			}
		}
		failed := c.passes(ctx, below, belowUnkeyed, with)
		if failed == nil {
			kept = append(kept, e)
			continue
		}
		if err := fatal(ctx, failed); err != nil {
			return nil, nil, nil, err
		}
		rejected = append(rejected, &KeyError{Key: e.safe, Err: alone[i]})
		unknown = append(unknown, e.unknown...)
	}
	return kept, rejected, unknown, nil
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

// passes is nil when edits, set over below, decode and validate; keys no
// field reads are the unknown-input handler's, not a failure. Otherwise it
// is the first edit refused, the candidate's cause or why ctx ended.
func (c *keyChecker) passes(ctx context.Context, below map[string]any, belowUnkeyed unkeyedSections, edits []edit) error {
	if err := ended(ctx); err != nil {
		return err
	}
	tree, unset := setEdits(func() map[string]any { return below }, edits)
	if len(unset) > 0 {
		return unset[0].Err
	}
	unkeyed := belowUnkeyed.over(tree, editsUnkeyed(edits)) // before mergeTrees settles tree's whole maps
	candidate := deepcopy.Value(reflect.ValueOf(below)).Interface().(map[string]any)
	mergeTrees(tree, candidate)
	_, _, err := c.decode(ctx, candidate, unkeyed)
	return err
}

// sourceEntries is data, a provider's tree, as the entries WithInvalidEntries
// judges: a struct section field by field and anything else whole, each
// resolved and rewritten. The keys of a section no field and no remain
// field reads are in rest, kept as written, and an entry whose value does
// not rewrite is refused.
func sourceEntries(schema *schemaNode, data map[string]any) (edits []edit, rest map[string]any, refused []*KeyError) {
	rest = map[string]any{}
	var walk func(n *schemaNode, input map[string]any, path keyPath, rest map[string]any)
	walk = func(n *schemaNode, input map[string]any, path keyPath, rest map[string]any) {
		for _, key := range slices.Sorted(maps.Keys(input)) {
			value := input[key]
			f, _ := n.lookup(key)
			switch {
			case f == nil && !n.acceptsUnknown:
				rest[key] = value
				continue
			case f != nil && f.node.kind == nodeBranch:
				if child, ok := value.(map[string]any); ok && len(child) > 0 {
					inner := map[string]any{}
					walk(f.node, child, path.field(f.key), inner)
					if len(inner) > 0 {
						rest[f.key] = inner
					}
					continue
				}
			}
			at := path.field(key)
			if f != nil {
				at = path.field(f.key)
			} else if !validFieldKey(key) {
				at = path.key(key)
			}
			parent, _, ke := resolveSchemaPath(schema, at)
			if ke != nil {
				refused = append(refused, ke)
				continue
			}
			edits = append(edits, edit{path: at, parent: parent, value: copyInput(value), safe: diagnosticPath(schema, at).String()})
		}
	}
	walk(schema, data, nil, rest)
	edits, failed := rewriteEdits(schema, edits)
	return edits, rest, append(refused, failed...)
}

func (l *loader) prepareProvider(ctx context.Context, name string, data map[string]any, p providerSource, label func(map[string]any) (labeler, error)) error {
	w := keyWalk{schema: l.schema}
	tree := w.tree(l.root, textKeyed(data))
	if len(w.twice) > 0 || len(w.noText) > 0 || !p.withhold && len(w.notJSON) > 0 {
		_, err := l.rewrite(name, data)
		return err
	}
	for _, a := range w.aliased {
		l.aliased = append(l.aliased, a+" in "+name)
	}
	sources, err := label(tree)
	if err != nil {
		return fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
	}
	if p.withhold {
		edits, rest, refused := sourceEntries(l.schema, textKeyed(data))
		kept, rejected, unknown, err := l.checker.withhold(ctx, l.k.Raw(), l.unkeyed, edits)
		if err != nil {
			return err
		}
		l.unmergedUnknown = append(l.unmergedUnknown, unknown...)
		tree, _ = setEdits(func() map[string]any { return nil }, kept) // a field holds no index to bound
		mergeTrees(rest, tree)
		if err := l.reportWithheld(ctx, name, p, slices.Concat(refused, rejected)); err != nil {
			return err
		}
	}
	return l.mergeSource(name, tree, w.unkeyed, sources)
}

// reportWithheld hands a WithInvalidEntries report the entries the source
// called name withheld, sorted by key. A load whose context has ended
// reports nothing: the report is the caller's code.
func (l *loader) reportWithheld(ctx context.Context, name string, p providerSource, withheld []*KeyError) error {
	if p.rejected == nil {
		return nil
	}
	if err := ended(ctx); err != nil {
		return err
	}
	report := make([]KeyError, len(withheld))
	for i, ke := range sortKeyErrors(withheld) {
		report[i] = *ke
	}
	if err := p.rejected(ctx, report); err != nil {
		return fmt.Errorf("reporting invalid entries in %s: %w: %w", name, ErrSource, err)
	}
	return nil
}

func (l *loader) prepareValidation(ctx context.Context, o *options) error {
	if !slices.ContainsFunc(o.base, func(p providerSource) bool { return p.withhold }) &&
		!slices.ContainsFunc(o.overrides, func(p providerSource) bool { return p.withhold }) {
		return nil
	}
	c, err := newKeyChecker(l.root, o)
	if err != nil {
		return err
	}
	_, unknown, err := c.decode(ctx, c.defaultsTree(), c.unkeyed)
	if ctxErr := ended(ctx); ctxErr != nil && (err != nil || unknown != nil) {
		return ctxErr
	}
	if errors.Is(err, ErrSchema) {
		return err
	}
	if unknown != nil {
		err = errors.Join(err, unknown)
	}
	if err != nil {
		return fmt.Errorf("invalid baseline for provider entry validation: %w: %w", ErrValidation, err)
	}
	l.checker = c
	return nil
}
