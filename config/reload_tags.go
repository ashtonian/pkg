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

// fieldReload is the reload class of a field declared sf, reached through
// the squashed structs parents: its own tag's, else the innermost of theirs.
func fieldReload(sf reflect.StructField, parents []reflect.StructField) string {
	if value, ok := sf.Tag.Lookup("reload"); ok {
		return value
	}
	for i := len(parents) - 1; i >= 0; i-- {
		if value, ok := parents[i].Tag.Lookup("reload"); ok {
			return value
		}
	}
	return ""
}

// checkReloadTags refuses an invalid reload class, and reload tags on a
// section type two keys reach, which the tags would claim for both. It checks
// every declaration on every route through view's type, the inline owners a
// koanf:",squash" tag names included.
func checkReloadTags(view *gowalker.Schema) error {
	paths := map[reflect.Type]string{}
	var failed error
	check := func(shape *gowalker.Shape, p keyPath) error {
		own := false
		tagged := func(f reflect.StructField) error {
			value, ok := f.Tag.Lookup("reload")
			if !ok {
				return nil
			}
			own = true
			if value == "" || len(strings.Fields(value)) != 1 || strings.TrimSpace(value) != value {
				return fmt.Errorf("%w: %s field %s has invalid reload class %q", ErrSchema, p.orRoot(), f.Name, value)
			}
			return nil
		}
		for _, f := range shape.DeclaredView() {
			if err := tagged(f.GoField()); err != nil {
				return err
			}
		}
		// The declared view skips a remain field, whose class the keys it
		// collects take.
		for _, d := range shape.Declarations() {
			if len(d.InlineParents) == 0 && koanfTag(d.GoField).Has("remain") {
				if err := tagged(d.GoField); err != nil {
					return err
				}
			}
		}
		if other, ok := paths[shape.Type()]; ok && own && other != p.orRoot() {
			return fmt.Errorf("%w: %s declares reload tags and is shared by %s and %s; put the tags on the owning fields", ErrSchema, gowalker.TypeText(shape.Type()), other, p.orRoot())
		}
		paths[shape.Type()] = p.orRoot()
		return nil
	}
	err := view.WalkType(func(v gowalker.TypeVisit) (gowalker.Control, error) {
		shape := v.Shape
		if v.Kind == gowalker.Reference {
			// A cycle closes on the type it re-enters, a pointer to the
			// section included: the section is reached again at this key.
			shape = shapeBase(shape)
		}
		if shape.Kind() != reflect.Struct || shape.Atomic() {
			return gowalker.Continue, nil
		}
		if failed = check(shape, routeKey(v.Route)); failed != nil {
			return gowalker.Stop, nil
		}
		return gowalker.Continue, nil
	}, gowalker.WithDeclaredView())
	if err != nil {
		return fmt.Errorf("%w: checking reload tags of %s: %w", ErrSchema, gowalker.TypeText(view.Type()), err)
	}
	return failed
}

// routeKey is the key pattern a type walk's route reaches: inline owners and
// pointers add no segment, and a collection's elements are any entry.
func routeKey(route []gowalker.TypeEdge) keyPath {
	var p keyPath
	for _, edge := range route {
		switch edge.Kind {
		case gowalker.FieldEdge:
			p = p.field(edge.Field.Name())
		case gowalker.ElementEdge:
			p = p.any()
		}
	}
	return p
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
		if r := n.remain; r != nil {
			if r.reload != "" {
				return true
			}
			if m, err := r.node(metadataOf); err == nil && walk(m) {
				return true
			}
		}
		return walk(n.elem)
	}
	return walk(root)
}

// owner is the applier that owns key p, whose declared field — its
// Change's Field — is f, or -1 for none: a key whose field waits for a
// restart has no applier under the reload tags.
func (a *application) owner(p keyPath, f Field) int {
	if a.reloadTags && f.Reload == ReloadRestart {
		return -1
	}
	return a.owners.of(p)
}

func (a *application) checkReloadOwnership() error {
	claimed := make([]bool, len(a.appliers))
	walking := map[*schemaNode]bool{}
	var walk func(*schemaNode, cursor) error
	// claim checks that an applier owns c, a value's key pattern.
	var claim func(cursor) error
	walk = func(n *schemaNode, c cursor) error {
		if !walking[n] {
			walking[n] = true
			defer delete(walking, n)
			switch n.kind {
			case nodeBranch:
				if len(n.fields) > 0 || n.remain != nil {
					for _, f := range n.fields {
						if err := walk(f.node, c.intoField(f)); err != nil {
							return err
						}
					}
					if n.remain == nil {
						return nil
					}
					m := n.remainMember(c.sensitive())
					if m.kind == memberRemain {
						return claim(m.cursor(c)) // compared whole, at section[*]
					}
					elem, err := m.node(metadataOf)
					if err != nil {
						return err
					}
					return walk(elem, m.cursor(c))
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
		return claim(c)
	}
	claim = func(c cursor) error {
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
// for a single map key, or for one key a remain field collects, cannot claim
// coverage of an unbounded collection. A remain field compared whole keeps
// its [*]: Diff names it so.
func (a *application) representative(pattern keyPath) (keyPath, error) {
	p := slices.Clone(pattern)
	for i, seg := range p {
		if seg.kind != segAny {
			continue
		}
		c, n, err := a.root.walkTo(cursor{}, p[:i])
		if err != nil {
			return nil, fmt.Errorf("%w: checking reload ownership: %w", ErrSchema, err)
		}
		if n.kind == nodeMap || n.kind == nodeBranch {
			if n.kind == nodeBranch && n.remainMember(c.sensitive()).kind == memberRemain {
				continue
			}
			name := ""
			for {
				collision := false
				for _, prefix := range a.owners {
					if len(prefix.path) > i && prefix.path[i].kind != segAny && prefix.path[i].kind != segIndex && prefix.path[i].name == name {
						collision = true
						break
					}
				}
				if !collision && (n.kind == nodeMap || n.collects(memberSegment(name))) {
					break
				}
				name += "_"
			}
			p[i] = segment{kind: segKey, name: name}
			if n.kind == nodeBranch {
				p[i] = memberSegment(name)
			}
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
