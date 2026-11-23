package config

import (
	"context"
	"errors"
	"fmt"
)

// NewEntryProvider returns a provider over stored entries keyed in this
// package's key grammar — the keys CheckKey, DecodeKey and DecodeEdits take,
// as a settings store persists them — read afresh on every load, for
// [WithBaseProvider] or [WithOverrideProvider] with a nil parser:
//
//	entries, err := config.NewEntryProvider(func(ctx context.Context) (map[string]any, error) {
//	    return loadSettings(ctx, db) // SELECT key, value FROM settings
//	})
//	cfg, err := config.Load[AppConfig](ctx, config.WithBaseProvider(entries, nil))
//
// Each entry is one key set to its value, over the sources merged beneath
// it, exactly as DecodeKey sets one over defaults: `port`,
// `names["a.b"]` (a map key holding a dot), `addresses["first"].port`,
// `pool[1].host`. Field names resolve through aliases and letter case. A map
// value merges into what lies beneath key by key; anything else replaces it.
// An index sets that element of the list beneath, or appends one immediately
// after its last, and leaves the other elements to the sources that set
// them. Entries apply in the order DecodeEdits applies a batch: by key, an
// index by its number and a key before the keys beneath it, which then sets
// what it names in the value the other set.
//
// An entry whose key names nothing config reads reaches the unknown-input
// handler as any source's unknown key does. Entries that cannot all apply
// fail the load with *KeyErrors matching ErrValidation: one value under two
// spellings, an index past the next element, a value its field's format or
// sep tag cannot read. Under [WithInvalidEntries]
// those entries, and every entry CheckKey refuses, are withheld and reported
// instead. Diagnostics name keys, cut short beneath a sensitive container,
// and never a value.
//
// read is called with the load's context; a nil map is an empty source, and
// an error fails the load with [ErrSource]. What read returns is copied.
// Only this package can resolve the keys, so the provider's koanf Read
// methods return an error.
func NewEntryProvider(read func(ctx context.Context) (map[string]any, error)) (ContextProvider, error) {
	if read == nil {
		return nil, errors.New("config: NewEntryProvider needs a read function")
	}
	return entryProvider{read: read}, nil
}

type entryProvider struct {
	read func(ctx context.Context) (map[string]any, error)
}

var errEntryRead = errors.New("config: an entry provider's keys resolve against the config type; load it with WithBaseProvider or WithOverrideProvider and a nil parser")

func (p entryProvider) entries(ctx context.Context) (map[string]any, error) {
	m, err := p.read(ctx)
	if err != nil {
		return nil, err
	}
	return deepCopyMap(m), nil
}

func (entryProvider) ReadContext(context.Context) (map[string]any, error) { return nil, errEntryRead }
func (entryProvider) ReadBytesContext(context.Context) ([]byte, error)    { return nil, errEntryRead }
func (entryProvider) Read() (map[string]any, error)                       { return nil, errEntryRead }
func (entryProvider) ReadBytes() ([]byte, error)                          { return nil, errEntryRead }

// addEntries sets the entries ep reads over the sources merged so far, as
// key edits (see edits.go), and merges them as the source called name.
func (l *loader) addEntries(ctx context.Context, name string, ep entryProvider, p providerSource, src Source) error {
	if p.parser != nil {
		return fmt.Errorf("loading %s: %w: an entry provider is read with a nil parser", name, ErrSource)
	}
	entries, err := ep.entries(ctx)
	if err != nil {
		return fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
	}
	edits, refused := resolveEdits(l.schema, entries)
	var invalid []*KeyError
	for _, ke := range refused {
		if ke.Unknown {
			l.unmergedUnknown = append(l.unmergedUnknown, UnknownKey{Key: ke.Key, Suggestion: ke.suggestion})
		} else {
			invalid = append(invalid, ke)
		}
	}
	if p.withhold {
		kept := edits[:0]
		for _, e := range edits {
			if _, err := l.checker.checkEdit(ctx, e); err != nil {
				var ke *KeyError
				if !errors.As(err, &ke) {
					return err
				}
				var unknown *UnknownKeysError
				if !errors.As(ke.Err, &unknown) || errors.Is(ke.Err, ErrValidation) {
					invalid = append(invalid, ke)
					l.unmergedUnknown = append(l.unmergedUnknown, e.unknownBeneath(l.schema)...)
					continue
				}
			}
			kept = append(kept, e)
		}
		edits = kept
	}
	for _, e := range edits {
		for _, a := range e.aliased {
			l.aliased = append(l.aliased, a+" in "+name)
		}
	}
	tree, unset := editTree(l.schema, l.k.Raw, edits)
	invalid = append(invalid, unset...)
	if !p.withhold {
		if len(invalid) > 0 {
			return fmt.Errorf("loading %s: %w", name, joinKeyErrors(invalid))
		}
	} else if p.rejected != nil {
		rejected := make([]KeyError, len(invalid))
		for i, ke := range sortKeyErrors(invalid) {
			rejected[i] = *ke
		}
		if err := p.rejected(ctx, rejected); err != nil {
			return fmt.Errorf("reporting invalid entries in %s: %w: %w", name, ErrSource, err)
		}
	}
	return l.merge(name, tree, constant(src))
}

// unknownBeneath is the keys e's value sets that config does not read.
func (e edit) unknownBeneath(schema *schemaNode) []UnknownKey {
	w := keyWalk{schema: schema}
	_ = e.rewrite(&w) // e is a copy; only what the walk finds is wanted
	return w.unknown
}
