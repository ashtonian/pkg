package config

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
)

const (
	// ReloadRestart is the default reload class: a process restart is required.
	ReloadRestart = "restart"
	// ReloadLive declares that an applier applies changes in place.
	ReloadLive = "live"
)

// WithReloadTags enables the schema's reload classes for change routing.
// Restart keys are withheld from appliers and reported in PendingRestart.
// Other classes, including caller-defined labels, are live. A schema with no
// reload tags retains prefix-based routing. Metadata is available without this
// option; opting in separates declarations from existing caller-owned policy.
func WithReloadTags() ReloadOption {
	return func(o *reloadOptions) { o.reloadTags = true }
}

// WithReloadTagChecks enables WithReloadTags and rejects live keys with no
// applier, and appliers that own no live keys, with ErrSchema. It checks the
// whole schema, including collection patterns, before reading any sources.
func WithReloadTagChecks() ReloadOption {
	return func(o *reloadOptions) {
		o.reloadTags = true
		o.reloadTagChecks = true
	}
}

// RestartChanges returns caller-owned changes whose resolved class is restart.
// It is an offline declaration check, independent of runtime applier ownership.
func RestartChanges(changes []Change) []Change {
	return cloneChanges(slices.DeleteFunc(slices.Clone(changes), func(c Change) bool { return c.Field.Reload != ReloadRestart }))
}

func fieldReload(f *gowalker.Field) string {
	if value, ok := f.GoField().Tag.Lookup("reload"); ok {
		return value
	}
	parents := f.InlineParents()
	for i := len(parents) - 1; i >= 0; i-- {
		if value, ok := parents[i].Tag.Lookup("reload"); ok {
			return value
		}
	}
	return ""
}

func checkReloadTags(root *schemaNode) error {
	view, err := compileView(root.typ)
	if err != nil {
		return err
	}
	paths := map[reflect.Type]string{}
	walking := map[*gowalker.Shape]bool{}
	var walk func(*gowalker.Shape, keyPath) error
	walk = func(shape *gowalker.Shape, p keyPath) error {
		shape = shapeBase(shape)
		if shape.Atomic() {
			return nil
		}
		own := false
		for _, f := range shape.DeclaredView() {
			if value, ok := f.Tags().Lookup("reload"); ok {
				own = true
				if value == "" || len(strings.Fields(value)) != 1 || strings.TrimSpace(value) != value {
					return fmt.Errorf("%w: %s field %s has invalid reload class %q", ErrSchema, p.orRoot(), f.GoField().Name, value)
				}
			}
		}
		if other, ok := paths[shape.Type()]; ok && own && other != p.orRoot() {
			return fmt.Errorf("%w: %s declares reload tags and is shared by %s and %s; put the tags on the owning fields", ErrSchema, typeText(shape.Type()), other, p.orRoot())
		}
		paths[shape.Type()] = p.orRoot()
		if walking[shape] {
			return nil
		}
		walking[shape] = true
		defer delete(walking, shape)
		for _, f := range shape.DeclaredView() {
			path := p
			if !f.Inline() {
				path = p.field(f.Name())
			}
			if err := walk(f.Shape(), path); err != nil {
				return err
			}
		}
		if shape.Elem() != nil {
			return walk(shape.Elem(), p.any())
		}
		return nil
	}
	return walk(view.Root(), nil)
}

func hasReloadTags(root *schemaNode) bool {
	seen := map[*schemaNode]bool{}
	var walk func(*schemaNode) bool
	walk = func(n *schemaNode) bool {
		if n == nil || seen[n] {
			return false
		}
		seen[n] = true
		for _, f := range n.fields {
			if f.reload != "" || walk(f.node) {
				return true
			}
		}
		return walk(n.elem)
	}
	return walk(root)
}

func (a *application) owner(p keyPath) int {
	if a.reloadTags && ownerOf(a.root, p).Reload == ReloadRestart {
		return -1
	}
	return a.owners.of(p)
}

func (a *application) checkReloadOwnership() error {
	claimed := make([]bool, len(a.appliers))
	walking := map[*schemaNode]bool{}
	var walk func(*schemaNode, cursor) error
	walk = func(n *schemaNode, c cursor) error {
		if !walking[n] {
			walking[n] = true
			defer delete(walking, n)
			switch n.kind {
			case nodeBranch:
				if len(n.fields) > 0 {
					for _, f := range n.fields {
						if err := walk(f.node, c.intoField(f)); err != nil {
							return err
						}
					}
					return nil
				}
			case nodeMap, nodeList:
				if n.typ.Kind() != reflect.Array {
					return walk(n.elem, c.intoEntry(segment{kind: segAny}, n.typ.Elem()))
				}
				if n.typ.Len() > 0 {
					for i := range n.typ.Len() {
						if err := walk(n.elem, c.intoEntry(segment{kind: segIndex, index: i}, n.typ.Elem())); err != nil {
							return err
						}
					}
					return nil
				}
			}
		}
		if cmp.Or(c.owner.Reload, ReloadRestart) == ReloadRestart {
			return nil
		}
		representative, err := a.representative(c.path)
		if err != nil {
			return err
		}
		if i := a.owners.of(representative); i >= 0 {
			claimed[i] = true
		} else {
			return fmt.Errorf("%w: live key %s has no applier", ErrSchema, c.path)
		}
		for _, prefix := range a.owners {
			if len(prefix.path) > len(c.path) || !overlaps(prefix.path, c.path) {
				continue
			}
			candidate := slices.Clone(representative)
			for j, seg := range prefix.path {
				if seg.kind != segAny {
					candidate[j] = seg
				}
			}
			if i := a.owners.of(candidate); i >= 0 {
				claimed[i] = true
			}
		}
		return nil
	}
	if err := walk(a.root, cursor{}); err != nil {
		return err
	}
	for i, live := range claimed {
		if !live {
			return fmt.Errorf("%w: applier %q owns no live key", ErrSchema, a.appliers[i].hook.name)
		}
	}
	return nil
}

// representative picks one entry outside every literal selector, so an applier
// for a single map key cannot claim coverage of an unbounded collection.
func (a *application) representative(pattern keyPath) (keyPath, error) {
	p := slices.Clone(pattern)
	for i, seg := range p {
		if seg.kind != segAny {
			continue
		}
		_, n, err := a.root.walkTo(cursor{}, p[:i])
		if err != nil {
			return nil, fmt.Errorf("%w: checking reload ownership: %w", ErrSchema, err)
		}
		if n.kind == nodeMap {
			name := ""
			for {
				collision := false
				for _, prefix := range a.owners {
					if len(prefix.path) > i && prefix.path[i].kind == segKey && prefix.path[i].name == name {
						collision = true
						break
					}
				}
				if !collision {
					break
				}
				name += "_"
			}
			p[i] = segment{kind: segKey, name: name}
		} else {
			index := 0
			for {
				collision := false
				for _, prefix := range a.owners {
					if len(prefix.path) > i && prefix.path[i].kind == segIndex && prefix.path[i].index == index {
						collision = true
						break
					}
				}
				if !collision {
					break
				}
				index++
			}
			p[i] = segment{kind: segIndex, index: index}
		}
	}
	return p, nil
}
