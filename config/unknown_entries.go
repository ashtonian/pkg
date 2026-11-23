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
// An unknown key is claimed only when every source that set any part of
// it — the key, or a key beneath it — has WithUnknownEntries, a source a
// higher one set over and an entry that never merged included, and no
// default is there; the highest-ranked of those sources reports it.
// Otherwise it is the load's, for [WithUnknownKeys] or the strict default
// that fails on it, so a claim never hides the same key in another source.
// Snapshot.UnknownKeys lists the keys this report accepts beside the
// load's.
//
// Finding a key's sources costs no decode: the load looks the key up in
// what each source merged and in an index of the keys it set that never
// merged, a few lookups per source for each unknown key, as deep as the
// key.
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
	if !slices.ContainsFunc(layers, func(y *layer) bool { return y.claimsUnknown }) {
		for _, y := range layers {
			additional = append(additional, y.unknown...)
		}
		return claimed, unused, additional
	}
	s := newUnknownSetters(layers, defaults)
	for i, y := range layers {
		for k, u := range y.unknown {
			if c, ok := s.claimant(s.unmergedRaw[i][k]); ok {
				claimed[c] = append(claimed[c], u)
				continue
			}
			additional = append(additional, u)
		}
	}
	for _, name := range unused {
		if c, ok := s.claimant(unusedPath(l.schema, merged, name)); ok {
			claimed[c] = append(claimed[c], unusedKey(l.schema, merged, name))
			continue
		}
		rest = append(rest, name)
	}
	return claimed, rest, additional
}

// unknownSetters finds the sources that set a key no field reads: what each
// merged, and the keys each set that never merged.
type unknownSetters struct {
	layers   []*layer
	defaults map[string]any
	// unmergedRaw is, for each layer, the raw path of each of its unknown
	// keys, in order. A key the grammar cannot parse, such as $, the root,
	// is the root's path, which every source sets part of.
	unmergedRaw [][][]string
	// unmerged indexes each layer's unknown keys by encoded raw path: true
	// at the key, false at each key holding it.
	unmerged []map[string]bool
}

func newUnknownSetters(layers []*layer, defaults map[string]any) *unknownSetters {
	s := &unknownSetters{layers: layers, defaults: defaults, unmergedRaw: make([][][]string, len(layers)), unmerged: make([]map[string]bool, len(layers))}
	for i, y := range layers {
		s.unmerged[i] = map[string]bool{}
		for _, u := range y.unknown {
			var raw []string
			if p, err := parseKeyPath(u.Key); err == nil {
				raw = p.raw()
			}
			s.unmergedRaw[i] = append(s.unmergedRaw[i], raw)
			for n := range raw {
				if at := string(encodePath(nil, raw[:n])); !s.unmerged[i][at] {
					s.unmerged[i][at] = false
				}
			}
			s.unmerged[i][string(encodePath(nil, raw))] = true
		}
	}
	return s
}

// claimant is the index of the layer whose WithUnknownEntries report
// receives raw, a raw path no field reads, and true: the highest-ranked of
// the layers that set any part of it, when each of them claims its unknown
// keys and the defaults hold nothing there. layers are in rank order.
func (s *unknownSetters) claimant(raw []string) (int, bool) {
	if holdsRaw(s.defaults, raw) {
		return 0, false
	}
	found := -1
	for i, y := range s.layers {
		if !holdsRaw(y.merged, raw) && !s.setUnmerged(i, raw) {
			continue
		}
		if !y.claimsUnknown {
			return 0, false
		}
		found = i
	}
	return found, found >= 0
}

// setUnmerged reports whether layer i set a key on raw's path that never
// merged: raw, a key beneath it, or a key holding it, whose value may hold
// raw.
func (s *unknownSetters) setUnmerged(i int, raw []string) bool {
	m := s.unmerged[i]
	if _, ok := m[string(encodePath(nil, raw))]; ok {
		return true
	}
	for n := range raw {
		if m[string(encodePath(nil, raw[:n]))] {
			return true
		}
	}
	return false
}

// holdsRaw reports whether tree, a source's tree or a merge of them, holds a
// value at raw, a raw path: the key's own, or one holding keys beneath it.
func holdsRaw(tree any, raw []string) bool {
	for _, key := range raw {
		var ok bool
		switch t := tree.(type) {
		case map[string]any:
			tree, ok = t[key]
		case wholeMap:
			tree, ok = t[key]
		case []any:
			if i, err := strconv.Atoi(key); err == nil && i >= 0 && i < len(t) {
				tree, ok = t[i], true
			}
		case listEdit:
			if i, err := strconv.Atoi(key); err == nil {
				tree, ok = t[i]
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// unusedPath is the raw path, as the merge keys it, of the key the decoder
// names name in tree: the key treeKey proves, through the one element of a
// list a map stands for as decoding reads it, and then the key no field
// took.
func unusedPath(root *schemaNode, tree any, name string) []string {
	p, _, used, exact := treeKey(root, tree, name)
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
