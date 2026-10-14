package config

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// NewMapProvider returns a provider over the configuration read returns,
// read afresh on every load — a database table, a key-value store, a
// cluster's replicated settings — for [WithBaseProvider] or
// [WithOverrideProvider] with a nil parser:
//
//	settings, err := config.NewMapProvider(func(ctx context.Context) (map[string]any, error) {
//	    return loadSettings(ctx, db) // e.g. SELECT key, value FROM settings
//	}, ".")
//	reloader, err := config.NewReloader[AppConfig](ctx,
//	    []config.Option{config.WithBaseProvider(settings, nil)},
//	    config.WithPollInterval(30*time.Second))
//
// read is called with the load's context, so a slow store stops when the
// load is cancelled; a nil map is an empty source, and an error fails the
// load with [ErrSource]. What read returns is copied, so the caller may keep
// and change it. With delimiter "." the keys are dotted paths (db.max_conns)
// and are unflattened into the key tree; a key with an empty segment, or one
// that is a prefix of another (db and db.host), is an error, since it would
// set a value and a section at once. With delimiter "" the map is already
// nested. Any other delimiter is an error. It is a source, not a watch: call
// [Reloader.SourceChanged] when the store says it changed, or use
// [WithPollInterval].
func NewMapProvider(read func(ctx context.Context) (map[string]any, error), delimiter string) (ContextProvider, error) {
	if read == nil {
		return nil, errors.New("config: NewMapProvider needs a read function")
	}
	if delimiter != "" && delimiter != "." {
		return nil, fmt.Errorf("config: NewMapProvider delimiter %q: want \".\" or \"\"", delimiter)
	}
	return mapProvider{read: read, dotted: delimiter == "."}, nil
}

type mapProvider struct {
	read   func(ctx context.Context) (map[string]any, error)
	dotted bool
}

func (p mapProvider) ReadContext(ctx context.Context) (map[string]any, error) {
	m, err := p.read(ctx)
	if err != nil {
		return nil, err
	}
	m = deepCopyMap(m)
	if m == nil {
		return map[string]any{}, nil
	}
	if !p.dotted {
		return m, nil
	}
	return unflattenDotted(m)
}

func (p mapProvider) Read() (map[string]any, error) { return p.ReadContext(context.Background()) }

func (mapProvider) ReadBytes() ([]byte, error) {
	return nil, errors.New("config: a NewMapProvider source has no bytes; load it with a nil parser")
}

func (p mapProvider) ReadBytesContext(context.Context) ([]byte, error) { return p.ReadBytes() }

// unflattenDotted nests a map keyed by dotted paths, refusing keys that
// cannot nest: an empty segment, or a key that is another's prefix.
func unflattenDotted(flat map[string]any) (map[string]any, error) {
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		segs := strings.Split(k, ".")
		if slices.Contains(segs, "") {
			return nil, fmt.Errorf("key %q has an empty segment", k)
		}
		for i := 1; i < len(segs); i++ {
			if prefix := strings.Join(segs[:i], "."); hasKey(flat, prefix) {
				return nil, fmt.Errorf("key %q is both a value and the section of %q", prefix, k)
			}
		}
	}
	out := map[string]any{}
	for _, k := range keys {
		m := out
		segs := strings.Split(k, ".")
		for _, seg := range segs[:len(segs)-1] {
			next, ok := m[seg].(map[string]any)
			if !ok {
				next = map[string]any{}
				m[seg] = next
			}
			m = next
		}
		m[segs[len(segs)-1]] = flat[k]
	}
	return out, nil
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

// deepCopyMap copies m and everything it holds.
func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return deepCopy(reflect.ValueOf(m)).Interface().(map[string]any)
}
