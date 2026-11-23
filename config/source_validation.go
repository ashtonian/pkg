package config

import (
	"context"
	"fmt"
	"maps"
	"slices"
)

// SourceOption configures one base or override provider.
type SourceOption func(*providerSource)

// WithInvalidEntries withholds the source's invalid entries and reports
// them, preserving lower-precedence values. report receives the sorted
// rejected keys on every load, including an empty slice when none were
// rejected; nil discards the report. Its error fails the load with
// ErrSource. Report errors omit values; their wrapped causes are intended
// only for trusted diagnostics.
//
// An entry of a provider is a struct section's field, checked field by
// field; a collection or self-decoding value is one entry. An entry of
// [WithBaseEntries] or [WithOverrideEntries] is one stored key.
//
// Entries are judged in the configuration every source makes: the sources
// merged in rank order over the defaults, the entries of every source this
// option applies to among them, decoded as Load decodes and checked against
// the validator's field rules — the default validator's tags, or
// [WithValidator]'s. An entry is withheld when, beside the entries the
// configuration does not fail at, it fails at a key it sets or at a key
// holding a value of it the merge keeps. A failure at no key, and one at a
// key a source not judged sets above the entries with nothing of theirs
// kept beneath it, withholds nothing: the load fails with it.
//
// A failure is at keys: a value that does not decode at its key, a field
// rule at its field's, and a rule comparing fields (eqfield, ltefield,
// required_with, eqcsfield, ...) at each field it names as well. It is
// blamed on the entry that set the key last: the highest-ranked source to
// set it, and within a source the entry applied last. An entry sets a key
// when what it merges holds the key, or replaces a key holding it whole —
// with a value that is not a map, or a typed map or section; an entry
// whose map merges key by key and holds nothing at the key does not. A
// failure at a key is blamed as well on each entry beneath it whose own key
// it set last: a list's max or a map's len breaks when entries add
// elements or keys. An entry the merge cannot set, such as an index past
// the next element, is blamed for that. A Validate method's error, a WithValidator validator's
// that holds no field errors, and a failure the decoder places beneath a
// map it decodes whole are at no key. Under [WithSkipValidation] only
// values that do not decode and entries that cannot be set are blamed.
//
// Each round judges the entries left and tests every entry it blames for
// the first time beside the entries it does not blame: an entry blamed
// there is withheld, and one that is not is kept, so entries each valid
// that break a rule together are kept and the load fails on that rule.
// Rounds repeat until one withholds nothing, so an entry left unable to
// apply by one withheld — pool[2] once pool[1] goes — is found by the next.
//
// Entries are judged before secrets, post-load hooks and Validate methods,
// which run once, in the load: a field rule comparing an entry's key with
// a field they fill judges against the value before they fill it, and a
// required field they fill blames no entry.
//
// Every source is read before any entry is judged: report runs only once
// the last source is read, so a slow provider delays it, and a source that
// fails to read fails the load with nothing reported.
//
// A judgement merges the sources from the lowest this option applies to
// up, decodes the result and runs the field rules. Judging takes one
// judgement when it blames no entry and, for k entries it ever blames, at
// most 2k+1, however many entries there are: each blamed entry is tested
// at most once, and each round but the last withholds at least one, so
// there are at most k tests and k+1 rounds. Each judgement starts from a
// copy of what lies beneath those sources and copies the configuration
// tree once for each source it merges and once more to decode.
//
// Unknown entries remain available to the unknown-input handler — the
// load's, or this source's [WithUnknownEntries] report — including
// declared unknown children of a withheld entry. Read and parser
// errors and cancellation still fail the load, with the cause the context
// ended with, and a load whose context has ended reports nothing. A
// validate tag the default validator cannot apply fails the load with
// ErrSchema, whichever entry brings the validator to it: it is the
// declaration's fault, not the entry's.
func WithInvalidEntries(report func(context.Context, []KeyError) error) SourceOption {
	return func(s *providerSource) { s.withhold, s.rejected = true, report }
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
				child, ok := value.(map[string]any)
				if ok && len(child) > 0 {
					inner := map[string]any{}
					walk(f.node, child, path.field(f.key), inner)
					if len(inner) > 0 {
						rest[f.key] = inner
					}
					continue
				}
			}
			at := path.member(key)
			if f != nil {
				at = path.field(f.key)
			}
			parent, _, ke := resolveSchemaPath(schema, at)
			if ke != nil {
				refused = append(refused, ke)
				continue
			}
			edits = append(edits, edit{path: at, parent: parent, value: copyInput(value), safe: shownKey(schema, at)})
		}
	}
	walk(schema, data, nil, rest)
	edits, failed := rewriteEdits(schema, edits)
	return edits, rest, append(refused, failed...)
}

// prepareProvider is data, the tree of the provider p called name, as a
// layer whose values label attributes. Under WithInvalidEntries the
// provider is set entry by entry (see sourceEntries).
func (l *loader) prepareProvider(name string, data map[string]any, p providerSource, label func(map[string]any) (labeler, error)) (*layer, error) {
	w := keyWalk{schema: l.schema}
	tree := w.tree(l.root, textKeyed(data))
	if len(w.twice) > 0 || len(w.noText) > 0 || !p.withhold && len(w.notJSON) > 0 {
		_, err := l.rewrite(name, data)
		return nil, err
	}
	for _, a := range w.aliased {
		l.aliased = append(l.aliased, a+" in "+name)
	}
	sources, err := label(tree)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
	}
	y := &layer{name: name, label: sources, claimsUnknown: p.claimsUnknown, unknownReport: p.unknown}
	if !p.withhold {
		y.tree, y.unkeyed = tree, w.unkeyed
		return y, nil
	}
	edits, rest, refused := sourceEntries(l.schema, textKeyed(data))
	y.tree, y.byEntry, y.entries, y.refused = rest, true, edits, refused
	y.withhold, y.report = true, p.rejected
	return y, nil
}

// reportWithheld hands a WithInvalidEntries report the entries the source
// called name withheld, sorted by key. A load whose context has ended
// reports nothing: the report is the caller's code.
func (l *loader) reportWithheld(ctx context.Context, name string, report func(context.Context, []KeyError) error, withheld []*KeyError) error {
	if report == nil {
		return nil
	}
	err := abandoned(ctx)
	if err != nil {
		return err
	}
	out := make([]KeyError, len(withheld))
	for i, ke := range sortKeyErrors(withheld) {
		out[i] = *ke
	}
	err = report(ctx, out)
	if err != nil {
		return fmt.Errorf("reporting invalid entries in %s: %w: %w", name, ErrSource, err)
	}
	return nil
}
