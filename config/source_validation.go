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
// [WithBaseEntries] or [WithOverrideEntries] is one stored key. Entries are
// judged against the final configuration: every source of the load, the
// defaults included, merged in rank order, decoded and validated as Load
// does before secrets and post-load hooks. The entries of every source
// this option applies to are judged together, in rank order and by key
// within a source, over the rest of what the sources set.
//
// When the final configuration passes with every entry, all are kept.
// Otherwise an entry that passes with the rest alone is always kept, so
// entries each valid that break a rule together fail the load rather than
// one being chosen for discarding. When some set of the others passes
// beside those, the load goes on with the entries kept, and each one
// withheld fails when added to them. When none does, every entry merges
// but those a failure was attributed to, and the load fails as that
// configuration does. Every entry withheld is in the report.
//
// A failure is attributed to the entries setting its key, a key holding it
// or a key beneath it, from the highest-ranked source among them: an entry
// a higher one sets over is not at fault. A rule naming a field beside its
// own (ltefield, required_with) is attributed to the struct holding both;
// a Validate method's error, a WithValidator validator's that names no
// field and a rule naming another struct's field are attributed to none.
// The entries a failure names are set aside until the rest pass; then
// each entry set aside is retried, in order, until a pass keeps none more,
// so an entry that needs another — a list element after the one before
// it, a field its section requires — is kept with it whichever comes
// first.
//
// Every source is read before any entry is judged: report runs only once
// the last source is read, so a slow provider delays it, and a source
// that fails to read fails the load with nothing reported.
//
// Judging merges the sources from the lowest this option applies to up
// and decodes the result: once when every entry passes, and otherwise, for
// n entries of which k fail with the rest alone, at most
// 2 + n + k + k(k+1)/2 times. Each merge starts over from a copy of what
// lies beneath those sources and copies the configuration tree once for
// each source it merges and once more to decode.
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
	if err := ended(ctx); err != nil {
		return err
	}
	out := make([]KeyError, len(withheld))
	for i, ke := range sortKeyErrors(withheld) {
		out[i] = *ke
	}
	if err := report(ctx, out); err != nil {
		return fmt.Errorf("reporting invalid entries in %s: %w: %w", name, ErrSource, err)
	}
	return nil
}
