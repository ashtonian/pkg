package config

import (
	"context"
	"fmt"
)

// WithBaseEntries adds stored entries keyed in this package's key grammar —
// the keys CanonicalKey spells and CheckKey, DecodeKey and DecodeEdits take,
// as a settings store persists them — read afresh with the load's context on
// every load. They load where [WithBaseProvider] does, among the base
// providers in the order added:
//
//	cfg, err := config.Load[AppConfig](ctx,
//	    config.WithBaseEntries(func(ctx context.Context) (map[string]any, error) {
//	        return loadSettings(ctx, db) // SELECT key, value FROM settings
//	    }),
//	    config.WithFile("config.yaml"),
//	)
//
// Each entry is one key set to its value over the sources merged beneath
// it, as DecodeKey sets one over defaults: `port`, `names["a.b"]` (a map key
// holding a dot), `addresses["first"].port`, `pool[1].host`. Field names
// resolve through aliases and letter case. A map[string]any value merges
// into what lies beneath key by key; anything else replaces it, and nil sets
// the zero value. Entries apply in the order DecodeEdits applies a batch: by
// key, an index by its number and a key before the keys beneath it, which
// then sets what it names in the value the other set.
//
// An index is a position in the list beneath the entries as that load
// merges it: it sets that element, or appends one immediately after the
// last, and leaves the other elements to the sources that set them. When a
// lower source's list shrinks so an index lies beyond its next element, the
// entry no longer applies and the load fails, or withholds it under
// [WithInvalidEntries]; store whole lists where the list beneath can change.
//
// An entry whose key names nothing config reads, and a key inside a value
// that no field reads, reach the unknown-input handler as any source's
// unknown keys do. An entry keyed by what names no one value — text that
// does not parse as a key, such as pool[x], or a key holding [*] — is no
// unknown key, whatever the source does with those: it cannot be set.
// Entries that cannot all apply fail the load with *KeyErrors, every one of
// them named: those, matching ErrKey, each message saying which and quoting
// the text that does not parse, redacted (see RedactText); and, matching
// ErrValidation, one value under two spellings, an index past the next
// element, a key beneath one set to a value that cannot hold it, keys
// selecting both elements and fields of one value, a value its field's
// format or sep tag cannot read.
// Under [WithInvalidEntries] those entries, and every entry that does not
// decode or validate as that option judges it, are withheld and reported
// instead. Diagnostics name keys, cut short beneath a sensitive container,
// and never a value.
//
// What read returns is copied; a nil map is an empty source, and an error
// fails the load with [ErrSource].
func WithBaseEntries(read func(ctx context.Context) (map[string]any, error), opts ...SourceOption) Option {
	s := entrySource(read, opts)
	return func(o *options) {
		o.base = append(o.base, s)
	}
}

// WithOverrideEntries adds stored entries as [WithBaseEntries] does, loaded
// where [WithOverrideProvider] does: above the environment, among the
// override providers in the order added. Stored settings an operator or an
// admin API writes at run time load here, so they win over the files and
// environment a deployment ships.
func WithOverrideEntries(read func(ctx context.Context) (map[string]any, error), opts ...SourceOption) Option {
	s := entrySource(read, opts)
	return func(o *options) {
		o.overrides = append(o.overrides, s)
	}
}

func entrySource(read func(ctx context.Context) (map[string]any, error), opts []SourceOption) providerSource {
	if read == nil {
		read = func(context.Context) (map[string]any, error) {
			return nil, fmt.Errorf("config: an entries option has no read function")
		}
	}
	s := providerSource{entries: read}
	for _, opt := range opts {
		opt(&s)
	}
	return s
}

// readEntries reads the entries p reads and prepares them as key edits (see
// edits.go), which set them over the layers beneath as the source called
// name merges. An entry naming nothing config reads is an unknown key; one
// whose key does not parse, or holds [*], names no one value, and is
// refused as an entry that cannot be set is.
func (l *loader) readEntries(ctx context.Context, name string, p providerSource, src Source) (*layer, error) {
	read, err := p.entries(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
	}
	edits, refused := prepareEdits(l.schema, deepCopyMap(read))
	y := &layer{name: name, label: constant(src), byEntry: true, entries: edits, withhold: p.withhold, report: p.rejected,
		claimsUnknown: p.claimsUnknown, unknownReport: p.unknown}
	for _, ke := range refused {
		if ke.namesNothing() {
			y.unknown = append(y.unknown, UnknownKey{Key: ke.Key, Suggestion: ke.suggestion})
		} else {
			y.refused = append(y.refused, ke)
		}
	}
	return y, nil
}
