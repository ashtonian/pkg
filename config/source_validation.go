package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// SourceOption configures one base or override provider.
type SourceOption func(*providerSource)

// WithInvalidEntries withholds known entries that fail CheckKey, preserving
// lower-precedence values. report receives the sorted rejected keys after each
// read, including an empty slice when none were rejected; nil discards the
// report. Its error fails the load with ErrSource. Report errors omit values;
// their wrapped causes are intended only for trusted diagnostics.
//
// Struct sections are checked field by field; collections and self-decoding
// values are atomic entries. Defaults must validate before any provider is
// read, so a broken baseline cannot be mistaken for bad stored entries.
// Unknown entries remain available to the normal unknown-input handler,
// including declared unknown children of a withheld collection. Read/parser
// errors and cancellation still fail the load. The final merged configuration
// must validate: cross-field failures are never resolved by choosing entries
// to discard.
func WithInvalidEntries(report func(context.Context, []KeyError) error) SourceOption {
	return func(s *providerSource) { s.withhold, s.rejected = true, report }
}

func (c *keyChecker) filter(ctx context.Context, tree map[string]any) (map[string]any, []KeyError, error) {
	var rejected []KeyError
	var walk func(*schemaNode, map[string]any, keyPath) (map[string]any, error)
	walk = func(n *schemaNode, input map[string]any, path keyPath) (map[string]any, error) {
		kept := make(map[string]any, len(input))
		for _, key := range slices.Sorted(maps.Keys(input)) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			value := input[key]
			f := n.byKey[key]
			if f == nil {
				kept[key] = value
				continue
			}
			at := path.field(key)
			if child, ok := value.(map[string]any); ok && f.node.kind == nodeBranch && len(child) > 0 {
				filtered, err := walk(f.node, child, at)
				if err != nil {
					return nil, err
				}
				if len(filtered) > 0 {
					kept[key] = filtered
				}
				continue
			}
			_, err := c.check(ctx, at.String(), value)
			if err == nil {
				kept[key] = value
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var invalid *KeyError
			if !errors.As(err, &invalid) {
				return nil, err
			}
			var unknown *UnknownKeysError
			if errors.As(invalid.Err, &unknown) && !errors.Is(invalid.Err, ErrValidation) {
				kept[key] = value
				continue
			}
			rejected = append(rejected, *invalid)
		}
		return kept, nil
	}
	kept, err := walk(c.schema, tree, nil)
	slices.SortFunc(rejected, func(a, b KeyError) int { return strings.Compare(a.Key, b.Key) })
	return kept, rejected, err
}

func (l *loader) prepareProvider(ctx context.Context, name string, data map[string]any, p providerSource, label func(map[string]any) (labeler, error)) error {
	w := keyWalk{schema: l.schema}
	tree := w.tree(l.root, textKeyed(data))
	if len(w.twice) > 0 || !p.withhold && len(w.notJSON) > 0 {
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
		var rejected []KeyError
		tree, rejected, err = l.checker.filter(ctx, tree)
		if err != nil {
			return err
		}
		for _, u := range w.unknown {
			for _, r := range rejected {
				if u.Key == r.Key || strings.HasPrefix(u.Key, r.Key+".") || strings.HasPrefix(u.Key, r.Key+"[") {
					l.withheldUnknown = append(l.withheldUnknown, u)
					break
				}
			}
		}
		if p.rejected != nil {
			if err := p.rejected(ctx, rejected); err != nil {
				return fmt.Errorf("reporting invalid entries in %s: %w: %w", name, ErrSource, err)
			}
		}
	}
	return l.merge(name, tree, sources)
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
	if _, err := c.decode(ctx, deepCopy(reflect.ValueOf(c.defaults)).Interface().(map[string]any)); err != nil {
		return fmt.Errorf("invalid baseline for provider entry validation: %w: %w", ErrValidation, err)
	}
	l.checker = c
	return nil
}
