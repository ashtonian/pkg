package config

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// WithUnknownEntries lets the source set keys no field reads — the rows a
// settings store still holds for a field since removed, say — without
// failing the load: they go to report instead of the load's unknown-input
// handler. A nil return lets the load go on, and an error fails it with
// ErrValidation, the error kept as its cause. A nil report accepts them
// silently. report is called at every load and reload that finds any, with
// the keys sorted, each with the field it most likely misspells; a load
// whose context has ended reports nothing.
//
// A key is unknown exactly as [WithUnknownKeys] describes: the decoder
// leaves it unused as it decodes the merged sources, so a key beneath an
// interface, one a koanf:",remain" field collects and one a self-decoding
// value or a decode hook consumes are not. An entry of [WithBaseEntries] or
// [WithOverrideEntries] naming nothing config reads is unknown as well,
// and under [WithInvalidEntries] so are the keys inside a withheld
// entry's value: this source's report receives them in place of the
// load's handler.
//
// An unknown key the merge holds is the source's when the source set
// everything the merge keeps at it: a value, when it is the highest-ranked
// source to set it; a section no field holds, when it set every key the
// merge keeps in it. A key another source or a default set any part of is
// the load's, for [WithUnknownKeys] or the default that fails on it.
// Snapshot.UnknownKeys lists the keys this report accepts beside the
// load's.
//
// Finding each key's source costs no decode: the load records which
// source set each key as the sources merge, one node per key each source
// sets, and walks that record once per unknown key. A load that records
// provenance or finds shadowed inputs records it already.
func WithUnknownEntries(report func(ctx context.Context, unknown []UnknownKey) error) SourceOption {
	return func(s *providerSource) { s.claimsUnknown, s.unknown = true, report }
}

// claimUnknown divides the input no field reads between the sources under
// WithUnknownEntries and the load: unused, the decoder's names for the
// keys of merged it did not use, and each layer's keys that never merged.
// defaults is the merge of the defaults the layers merged over. claimed
// holds each layer's keys, by layer index; rest and additional are the
// load's, unused names and keys.
func (l *loader) claimUnknown(merged, defaults map[string]any, unused []string, layers []*layer) (claimed [][]UnknownKey, rest []string, additional []UnknownKey) {
	claimed = make([][]UnknownKey, len(layers))
	claimants := map[int]int{} // a claiming layer's index, by its rank
	for i, y := range layers {
		if !y.claimsUnknown {
			additional = append(additional, y.unknown...)
			continue
		}
		claimed[i] = append(claimed[i], y.unknown...)
		claimants[y.rank] = i
	}
	if len(claimants) == 0 {
		return claimed, unused, additional
	}
	for _, name := range unused {
		raw := unusedPath(l.schema, merged, name)
		if i, ok := claimants[l.soleSetter(raw, defaults)]; ok {
			claimed[i] = append(claimed[i], unusedKey(l.schema, merged, name))
			continue
		}
		rest = append(rest, name)
	}
	return claimed, rest, additional
}

// soleSetter is the rank of the one source that set everything the merge
// keeps at raw, a raw path, or -1 when no one source did: several did, or
// a default the merge keeps there, which defaults holds.
func (l *loader) soleSetter(raw []string, defaults map[string]any) int {
	node := l.origins.holder(string(encodePath(nil, raw)))
	if node == nil {
		return -1
	}
	rank, ok := node.setter()
	if !ok || !node.covers(rawEntry(defaults, raw)) {
		return -1
	}
	return rank
}

// covers reports whether nothing of defaults, what the defaults hold where
// n is, survives the merge beneath n: n replaced it whole, or every key of
// it is a key of n's that covers it in turn.
func (n *originNode) covers(defaults any) bool {
	m, ok := mergedMap(defaults)
	if !ok || !n.merges || n.whole {
		return true
	}
	for key, val := range m {
		child := n.children[key]
		if child == nil || !child.covers(val) {
			return false
		}
	}
	return true
}

// rawEntry is what tree holds at raw, a raw path; nil when it holds
// nothing there.
func rawEntry(tree any, raw []string) any {
	for _, key := range raw {
		switch t := tree.(type) {
		case map[string]any:
			tree = t[key]
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(t) {
				return nil
			}
			tree = t[i]
		default:
			return nil
		}
	}
	return tree
}

// setter is the rank of the one source that set everything n holds as the
// merge keeps it: n's own for a value that is no map and for a map its
// source set whole; for a map merged key by key, the one setter of all of
// its keys. It is false when several sources did.
func (n *originNode) setter() (int, bool) {
	if !n.merges || n.whole || len(n.children) == 0 {
		return n.rank, true
	}
	rank, first := 0, true
	for _, child := range n.children {
		r, ok := child.setter()
		if !ok || !first && r != rank {
			return 0, false
		}
		rank, first = r, false
	}
	return rank, true
}

// unusedPath is the raw path, as the merge keys it, of the key the decoder
// names name in tree: the key treeKey proves, through the one element of a
// list a map stands for as decoding reads it, and then the key no field
// took.
func unusedPath(root *schemaNode, tree any, name string) []string {
	p, used, exact := treeKey(root, tree, name)
	var raw []string
	data := tree
	for _, s := range p {
		switch s.kind {
		case segIndex:
			if _, ok := data.([]any); !ok {
				continue // a map decoding reads as the list's one element
			}
			raw, data = append(raw, strconv.Itoa(s.index)), treeElement(data, s.index)
		default:
			raw, data = append(raw, s.name), treeEntry(data, s.name)
		}
	}
	if exact {
		return raw
	}
	if rest := name[used:]; used == 0 || strings.HasPrefix(rest, ".") {
		raw = append(raw, strings.TrimPrefix(rest, "."))
	}
	return raw
}

// reportUnknown hands each layer's claimed keys to its WithUnknownEntries
// report, in rank order, and returns the keys the reports accepted. A load
// whose context has ended reports nothing: a report is the caller's code.
func reportUnknown(ctx context.Context, layers []*layer, claimed [][]UnknownKey) ([]UnknownKey, error) {
	var accepted []UnknownKey
	for i, y := range layers {
		keys := claimed[i]
		if len(keys) == 0 {
			continue
		}
		slices.SortFunc(keys, func(a, b UnknownKey) int { return strings.Compare(a.Key, b.Key) })
		keys = slices.Compact(keys)
		if y.unknownReport != nil {
			if err := abandoned(ctx); err != nil {
				return nil, err
			}
			if err := y.unknownReport(ctx, slices.Clone(keys)); err != nil {
				return nil, fmt.Errorf("reporting unknown keys in %s: %w: %w", y.name, ErrValidation, err)
			}
		}
		accepted = append(accepted, keys...)
	}
	return accepted, nil
}

// withKeys is e with keys, keys no field reads that a source's
// WithUnknownEntries report accepted, added: what the load accepted of the
// input no field reads. It is a new error for t when e is nil.
func (e *UnknownKeysError) withKeys(t reflect.Type, keys []UnknownKey) *UnknownKeysError {
	if len(keys) == 0 {
		return e
	}
	out := e.clone()
	if out == nil {
		out = &UnknownKeysError{Type: t}
	}
	out.Keys = append(out.Keys, keys...)
	slices.SortFunc(out.Keys, func(a, b UnknownKey) int { return strings.Compare(a.Key, b.Key) })
	out.Keys = slices.Compact(out.Keys)
	return out
}
