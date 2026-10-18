package config

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"
)

// TriggerInitial is the Trigger of the apply an [Applier] with ApplyInitial
// receives once, after the first load. It never names a reload.
const TriggerInitial = "initial"

// Applier applies the part of a published config it owns to the running
// process: a log level to a slog.LevelVar, limits to a pool, gates to a
// router. Register it with [WithApplier].
//
// After every published config the reloader calls each applier that owes
// something — once, in registration order, holding the reload's single-flight
// slot — with the keys it owns whose published value differs from what it
// last acknowledged. Returning nil acknowledges every change it was given
// except the RestartKeys it returns, which wait for a restart and are
// offered again on every later config. Returning an error, or panicking,
// acknowledges nothing and leaves the applier uncertain — what it did with
// the keys it was given may have half happened — so later calls give it
// those keys again, Retry set, until a call applies each of them.
type Applier[T any] struct {
	// Name identifies the applier in stats, errors and logs; unique within a
	// reloader.
	Name string
	// Prefixes are the keys the applier owns, in the package's key grammar:
	// a section (db), one value (logging.level), an entry
	// (features["audit"]) or a pattern (features[*].gate). It owns every key
	// at or beneath them unless another applier's prefix is more specific:
	// longer, or as long with more literal map keys and indexes.
	Prefixes []string
	// ApplyInitial applies the first load once, before the constructor
	// returns: every key the applier owns, as a change from nothing. Its
	// error, or any restart key, fails construction with [ErrApply]. Without
	// it the first load counts as applied: the caller builds what the applier
	// would set from [Reloader.Config] before serving.
	ApplyInitial bool
	// Apply applies ev.Changes. It must be idempotent: a retry passes
	// changes it may already have made.
	Apply func(ctx context.Context, ev ApplyEvent[T]) (ApplyResult, error)
}

// ApplyEvent is what an [Applier] is asked to apply.
type ApplyEvent[T any] struct {
	// Trigger names what published the config: a Trigger* constant, or
	// [TriggerInitial].
	Trigger string
	// Generation is the published config's generation.
	Generation uint64
	// Retry reports a call after a failure: a failed call may have half
	// applied the keys it was given. Changes then also lists each of those
	// keys whose published value is again what the applier acknowledged —
	// Old and New alike — so that it is applied again. A key that waits on
	// a structure the applier does not own whole is left out of every call
	// until it can be applied, and the applier stays uncertain until then.
	Retry bool
	// Previous is the config published before Desired, nil for the initial
	// apply. It is not what this applier applied — Changes are measured
	// from that.
	Previous *T
	// Desired is the config just published; shared and immutable.
	Desired *T
	// Changes are the keys the applier owns whose published value differs
	// from what it last acknowledged, as [Diff] reports them, and on a
	// retry the keys a failed call left uncertain. It is never empty.
	Changes []Change
}

// ApplyResult is what an [Applier] that returns nil applied.
type ApplyResult struct {
	// RestartKeys are keys of the event's Changes left unapplied because
	// they take a restart. Every other change is acknowledged.
	RestartKeys []string
}

// ApplyError reports the appliers that did not apply a published config. The
// config is live; each of them is called again on the next one. It wraps
// [ErrApply] and every failure's cause.
type ApplyError struct {
	Failures []ApplierFailure
}

// ApplierFailure is one applier's failure to apply a published config.
type ApplierFailure struct {
	Applier string
	Err     error
}

func (e *ApplyError) Error() string {
	var b strings.Builder
	b.WriteString(ErrApply.Error())
	for i, f := range e.Failures {
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		b.WriteString(f.Applier + ": " + f.Err.Error())
	}
	return b.String()
}

func (e *ApplyError) Unwrap() []error {
	errs := []error{ErrApply}
	for _, f := range e.Failures {
		errs = append(errs, f.Err)
	}
	return errs
}

// WithApplier registers a; appliers run in registration order. An applier
// with no name or a repeated one, no Apply, or no prefix fails construction,
// as does a prefix that names no key of the config, reaches beneath an
// interface or a value compared whole, or is as specific as another
// applier's and could name the same key. See [Applier].
//
// With appliers registered, the reloader accounts for every key: a change no
// applier owns, or one an applier returned as a restart key, is reported in
// [Snapshot].PendingRestart until it is applied or reverted. Register an
// applier for keys your code reads from [Reloader.Config] on every use, so
// they do not read as waiting for a restart.
func WithApplier[T any](a Applier[T]) ReloadOption {
	h := applierHook{
		target:   reflect.TypeFor[*T](),
		name:     a.Name,
		prefixes: slices.Clone(a.Prefixes),
		initial:  a.ApplyInitial,
	}
	if apply := a.Apply; apply != nil {
		h.run = func(ctx context.Context, ev any) (ApplyResult, error) { return apply(ctx, ev.(ApplyEvent[T])) }
	}
	return func(o *reloadOptions) { o.appliers = append(o.appliers, h) }
}

// applierHook is an [Applier] with its config type erased, so ReloadOption
// stays non-generic; target is the *T it was written for.
type applierHook struct {
	target   reflect.Type
	name     string
	prefixes []string
	initial  bool
	run      func(ctx context.Context, ev any) (ApplyResult, error)
}

// application is a reloader's account of what its appliers have
// acknowledged. Each acknowledger — an applier, or the first load standing
// for the keys no applier owns — is held as the keys where what it
// acknowledged differs from the published config: an applier that has
// applied everything holds nothing, and a reload is accounted from its
// publication's changes alone. Guarded by the reloader's slot.
type application struct {
	root     *schemaNode
	appliers []*applierState
	owners   ownership
	// boot is the first load, for the keys no applier owns: it never
	// acknowledges, so it holds every such key that has changed since.
	boot ackDiff
}

type applierState struct {
	hook applierHook
	ack  ackDiff
	// deferred are the restart keys of its last successful call.
	deferred map[string]bool
	// unsure are the keys failed calls were given that no later call has
	// applied: what was done with them is unknown. The applier is uncertain
	// while any remains.
	unsure map[string]keyRef
	status ApplierStatus // counters and the last outcome
}

// keyRef is a key with its walk position.
type keyRef struct {
	path keyPath
	ord  []ordSeg
}

func newApplication(root *schemaNode, hooks []applierHook) (*application, error) {
	a := &application{root: root}
	names := map[string]bool{}
	for i, h := range hooks {
		switch {
		case h.name == "":
			return nil, fmt.Errorf("config: WithApplier %d has no Name", i)
		case names[h.name]:
			return nil, fmt.Errorf("config: WithApplier %q is registered twice", h.name)
		case h.run == nil:
			return nil, fmt.Errorf("config: WithApplier %q has no Apply", h.name)
		case len(h.prefixes) == 0:
			return nil, fmt.Errorf("config: WithApplier %q owns no key; give it Prefixes", h.name)
		}
		names[h.name] = true
		seen := map[string]bool{}
		for _, s := range h.prefixes {
			p, err := applierPrefix(root, s)
			if err != nil {
				return nil, fmt.Errorf("config: WithApplier %q: %w", h.name, err)
			}
			if seen[p.String()] {
				return nil, fmt.Errorf("config: WithApplier %q lists %s twice", h.name, p)
			}
			seen[p.String()] = true
			a.owners = append(a.owners, newOwnedPrefix(p, i))
		}
		a.appliers = append(a.appliers, &applierState{
			hook:     h,
			deferred: map[string]bool{},
			unsure:   map[string]keyRef{},
			status:   ApplierStatus{Name: h.name},
		})
	}
	if err := a.owners.check(hooks); err != nil {
		return nil, err
	}
	return a, nil
}

// applierPrefix parses and checks one prefix against the config's schema: it
// must name a key Diff can report.
func applierPrefix(root *schemaNode, s string) (keyPath, error) {
	p, err := parseKeyPath(s)
	if err != nil {
		return nil, err
	}
	for i := 1; i <= len(p); i++ {
		c, n, err := root.walkTo(cursor{}, p[:i])
		if err != nil {
			return nil, err
		}
		if len(c.path) < i {
			return nil, fmt.Errorf("%w %s: it reaches beneath %s, whose keys are not compared one by one", ErrKey, p, c.path.orRoot())
		}
		if i < len(p) && n.kind != nodeBranch && c.sensitive() {
			return nil, fmt.Errorf("%w %s: it reaches beneath %s, which is sensitive and compared whole", ErrKey, p, c.path)
		}
	}
	return p, nil
}

// ownership resolves which applier owns a key.
type ownership []ownedPrefix

type ownedPrefix struct {
	path     keyPath
	applier  int
	literals int // map keys and indexes named, not matched by [*]
}

func newOwnedPrefix(p keyPath, applier int) ownedPrefix {
	o := ownedPrefix{path: p, applier: applier}
	for _, s := range p {
		if s.kind == segKey || s.kind == segIndex {
			o.literals++
		}
	}
	return o
}

// outranks reports whether o is more specific than q: longer, or as long
// with more literal selectors.
func (o ownedPrefix) outranks(q ownedPrefix) bool {
	if len(o.path) != len(q.path) {
		return len(o.path) > len(q.path)
	}
	return o.literals > q.literals
}

func (o ownedPrefix) matches(p keyPath) bool {
	if len(p) < len(o.path) {
		return false
	}
	for i, s := range o.path {
		k := p[i]
		if s.kind == segAny {
			if k.kind != segKey && k.kind != segIndex {
				return false
			}
			continue
		}
		if s != k {
			return false
		}
	}
	return true
}

// check rejects two appliers' prefixes that are equally specific and could
// name one key: neither would win it.
func (o ownership) check(hooks []applierHook) error {
	for i, a := range o {
		for _, b := range o[i+1:] {
			if a.applier == b.applier || a.outranks(b) || b.outranks(a) || !overlaps(a.path, b.path) {
				continue
			}
			return fmt.Errorf("config: WithApplier %q (%s) and %q (%s) are equally specific and both own keys like %s",
				hooks[a.applier].name, a.path, hooks[b.applier].name, b.path, a.path)
		}
	}
	return nil
}

// overlaps reports whether two prefixes of one length could name one key.
func overlaps(a, b keyPath) bool {
	for i := range a {
		if a[i].kind != segAny && b[i].kind != segAny && a[i] != b[i] {
			return false
		}
	}
	return true
}

// of returns the applier that owns key p, or -1 when none does.
func (o ownership) of(p keyPath) int {
	best := -1
	var bestPrefix ownedPrefix
	for _, op := range o {
		if op.matches(p) && (best < 0 || op.outranks(bestPrefix)) {
			best, bestPrefix = op.applier, op
		}
	}
	return best
}

// published is one key a publication changed, kept past the walk that found
// it: what was published there before, detached from that config, and what
// is published there now.
type published struct {
	keyRef
	key    string
	whole  bool
	held   heldValue // the previous value
	now    side      // the new value: a view into the new config
	change Change
}

func newPublished(root *schemaNode, d *delta) published {
	return published{
		keyRef: keyRef{path: slices.Clone(d.path), ord: slices.Clone(d.ord)},
		key:    d.path.String(),
		whole:  d.whole,
		held:   holdSide(d, 0),
		now:    d.sides[1],
		change: d.change(root),
	}
}

// heldValue is what an acknowledgement holds at a key, detached from every
// config: no pointer into one remains, so holding it keeps no old config
// alive.
type heldValue struct {
	present bool
	isNil   bool
	// shape reports a struct, map or list compared by presence and nil-ness
	// alone; typ is its type, so it can be rebuilt.
	shape bool
	typ   reflect.Type
	v     reflect.Value // a value compared by content: an unaddressable copy
}

// holdSide detaches side i of d.
func holdSide(d *delta, i int) heldValue {
	s := d.sides[i]
	h := heldValue{present: s.present, isNil: s.isNil}
	if s.v.IsValid() {
		h.typ = s.v.Type()
	}
	if d.mode == deltaShape && !d.whole {
		h.shape = true
		return h
	}
	if s.v.IsValid() {
		h.v = reflect.ValueOf(s.v.Interface())
	}
	return h
}

// side is h as a comparison side.
func (h heldValue) side() side { return side{v: h.v, present: h.present, isNil: h.isNil} }

// same reports whether h holds what s does.
func (h heldValue) same(s side) bool {
	if h.shape {
		return h.present == s.present && h.isNil == s.isNil
	}
	return sameValue(h.side(), s)
}

// ackEntry is one key where an acknowledgement differs from the published
// config.
type ackEntry struct {
	keyRef
	key string
	// whole reports a key compared whole: held is the whole value
	// acknowledged there, and nothing beneath it has an entry.
	whole bool
	held  heldValue
	// change is the difference as a Change: Old what was acknowledged, New
	// what is published.
	change Change
}

// ackDiff is where one acknowledgement differs from the published config,
// by key.
type ackDiff map[string]*ackEntry

func (d ackDiff) sorted() []*ackEntry {
	entries := slices.Collect(maps.Values(d))
	slices.SortFunc(entries, func(a, b *ackEntry) int { return compareOrd(a.ord, b.ord) })
	return entries
}

// covering returns the entry compared whole that p lies strictly beneath.
func (d ackDiff) covering(p keyPath) *ackEntry {
	for i := len(p) - 1; i > 0; i-- {
		if e := d[p[:i].String()]; e != nil && e.whole {
			return e
		}
	}
	return nil
}

// atOrBeneath returns the entries at or beneath p, in walk order.
func (d ackDiff) atOrBeneath(p keyPath) []*ackEntry {
	var out []*ackEntry
	for _, e := range d {
		if len(e.path) >= len(p) && slices.Equal(e.path[:len(p)], p) {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b *ackEntry) int { return compareOrd(a.ord, b.ord) })
	return out
}

// rebaser moves acknowledgements from one published config to the next.
type rebaser struct {
	root *schemaNode
	prev reflect.Value // the config published before
	next reflect.Value // the config being published
}

// rebase returns cur, an acknowledgement's difference from the previous
// published config, as its difference from the next, given changes, the
// publication's changes among the keys it covers, in walk order. cur is
// not changed.
func (r rebaser) rebase(cur ackDiff, changes []published) (ackDiff, error) {
	if len(changes) == 0 {
		return cur, nil
	}
	out := maps.Clone(cur)
	if out == nil {
		out = ackDiff{}
	}
	var stale []string // entries compared whole whose published value changed
	wholes := 0
	for _, e := range out {
		if e.whole {
			wholes++
		}
	}
	for _, c := range changes {
		if wholes > 0 {
			if w := out.covering(c.path); w != nil {
				stale = append(stale, w.key)
				continue
			}
		}
		if c.whole {
			held := c.held
			if within := out.atOrBeneath(c.path); len(within) > 0 {
				if len(within) == 1 && within[0].key == c.key && within[0].whole {
					stale = append(stale, c.key)
					continue
				}
				var err error
				if held, err = r.merge(c, within); err != nil {
					return nil, err
				}
				for _, e := range within {
					delete(out, e.key)
				}
			}
			out[c.key] = &ackEntry{keyRef: c.keyRef, key: c.key, whole: true, held: held, change: c.change}
			wholes++
			stale = append(stale, c.key)
			continue
		}
		if e := out[c.key]; e != nil && e.whole {
			stale = append(stale, c.key)
			continue
		} else if e != nil {
			if e.held.same(c.now) {
				delete(out, c.key)
			} else {
				out[c.key] = e.publishedAs(c.change.New)
			}
			continue
		}
		out[c.key] = &ackEntry{keyRef: c.keyRef, key: c.key, held: c.held, change: c.change}
	}
	for _, key := range stale {
		if e := out[key]; e != nil {
			if err := r.restate(out, e); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// publishedAs is e with the published side of its change replaced.
func (e *ackEntry) publishedAs(v Value) *ackEntry {
	next := *e
	next.change.New = v
	return &next
}

// restate compares e, an entry compared whole, with what the next config
// publishes at its key: it goes when they are equal, stays whole while they
// still differ whole, and otherwise becomes the keys beneath that differ.
func (r rebaser) restate(out ackDiff, e *ackEntry) error {
	now, n, f := locate(r.root, r.next, e.path)
	var found []published
	err := compareFrom(e.keyRef, n, f, e.held.side(), now, func(d *delta) {
		found = append(found, newPublished(r.root, d))
	})
	if err != nil {
		return err
	}
	delete(out, e.key)
	if len(found) == 0 {
		return nil
	}
	if found[0].key == e.key && found[0].whole {
		out[e.key] = &ackEntry{keyRef: e.keyRef, key: e.key, whole: true, held: e.held, change: found[0].change}
		return nil
	}
	for _, c := range found {
		out[c.key] = &ackEntry{keyRef: c.keyRef, key: c.key, whole: c.whole, held: c.held, change: c.change}
	}
	return nil
}

// merge is what was acknowledged at c's key, which the publication changed
// whole while the acknowledgement differed at or beneath it: the previous
// published value with the acknowledged sides of within written in.
func (r rebaser) merge(c published, within []*ackEntry) (heldValue, error) {
	if own := within[0]; own.key == c.key && !own.held.present {
		return own.held, nil // nothing was acknowledged there
	}
	base, n, _ := locate(r.root, r.prev, c.path)
	if n == nil {
		return heldValue{}, fmt.Errorf("config: acknowledged key %s is not in the previous config", c.key)
	}
	slot := reflect.New(anyType).Elem()
	if base.v.IsValid() {
		slot.Set(deepCopy(base.v))
	}
	present := base.present
	for _, e := range within {
		rel := e.path[len(c.path):]
		if len(rel) == 0 {
			present = e.held.present
		}
		if err := writeHeld(slot, rel, e.held); err != nil {
			return heldValue{}, fmt.Errorf("config: acknowledged key %s: %w", e.key, err)
		}
	}
	s := resolve(slot)
	h := heldValue{present: present, isNil: present && s.isNil}
	if s.v.IsValid() {
		h.typ, h.v = s.v.Type(), reflect.ValueOf(s.v.Interface())
	}
	return h, nil
}

var anyType = reflect.TypeFor[any]()

// writeHeld writes h at rel beneath slot, a settable value, through
// pointers, interfaces, structs, maps and lists, rebuilding a container the
// path needs. A key beneath a nil or absent container is not written:
// nothing can hold it.
func writeHeld(slot reflect.Value, rel keyPath, h heldValue) error {
	if len(rel) == 0 {
		return setHeld(slot, h)
	}
	v := slot
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		inner := reflect.New(v.Elem().Type()).Elem()
		inner.Set(v.Elem())
		if err := writeHeld(inner, rel, h); err != nil {
			return err
		}
		v.Set(inner)
		return nil
	}
	n, err := schemaOf(v.Type())
	if err != nil {
		return err
	}
	seg, rest := rel[0], rel[1:]
	switch n.kind {
	case nodeBranch:
		f := n.byKey[seg.name]
		if f == nil {
			return fmt.Errorf("%s has no field %q", typeText(v.Type()), seg.name)
		}
		fv, err := f.value(v)
		if err != nil {
			return nil // beneath a nil embedded pointer
		}
		return writeHeld(fv, rest, h)
	case nodeMap:
		key := reflect.New(v.Type().Key()).Elem()
		key.SetString(seg.name)
		if len(rest) == 0 && !h.present {
			if !v.IsNil() {
				v.SetMapIndex(key, reflect.Value{})
			}
			return nil
		}
		cur := reflect.Value{}
		if !v.IsNil() {
			cur = v.MapIndex(key)
		}
		if !cur.IsValid() && len(rest) > 0 {
			return nil
		}
		elem := reflect.New(v.Type().Elem()).Elem()
		if cur.IsValid() {
			elem.Set(cur)
		}
		if err := writeHeld(elem, rest, h); err != nil {
			return err
		}
		if v.IsNil() {
			v.Set(reflect.MakeMap(v.Type()))
		}
		v.SetMapIndex(key, elem)
		return nil
	case nodeList:
		i := seg.index
		if len(rest) == 0 && !h.present {
			if v.Kind() == reflect.Slice && i < v.Len() {
				v.Set(v.Slice(0, i))
			}
			return nil
		}
		if i >= v.Len() {
			if v.Kind() != reflect.Slice || i > v.Len() || len(rest) > 0 {
				return nil
			}
			v.Set(reflect.Append(v, reflect.Zero(v.Type().Elem())))
		}
		return writeHeld(v.Index(i), rest, h)
	}
	return nil
}

// setHeld sets slot, a value of some declared type, to h.
func setHeld(slot reflect.Value, h heldValue) error {
	switch {
	case !h.present:
		slot.Set(reflect.Zero(slot.Type()))
		return nil
	case h.isNil:
		if h.typ != nil && (h.typ.Kind() == reflect.Map || h.typ.Kind() == reflect.Slice) {
			return setResolved(slot, reflect.Zero(h.typ)) // a typed nil keeps its type
		}
		slot.Set(reflect.Zero(slot.Type()))
		return nil
	case h.shape:
		if cur := resolve(slot); cur.v.IsValid() && !cur.isNil && cur.v.Type() == h.typ {
			return nil // the container is there; what it holds is written key by key
		}
		var empty reflect.Value
		switch h.typ.Kind() {
		case reflect.Map:
			empty = reflect.MakeMap(h.typ)
		case reflect.Slice:
			empty = reflect.MakeSlice(h.typ, 0, 0)
		default:
			empty = reflect.Zero(h.typ)
		}
		return setResolved(slot, empty)
	}
	return setResolved(slot, h.v)
}

// setResolved stores v, a dereferenced value, in slot, through the pointers
// slot's type declares.
func setResolved(slot, v reflect.Value) error {
	switch slot.Kind() {
	case reflect.Pointer:
		p := reflect.New(slot.Type().Elem())
		if err := setResolved(p.Elem(), v); err != nil {
			return err
		}
		slot.Set(p)
		return nil
	case reflect.Interface:
		if !v.Type().Implements(slot.Type()) {
			return fmt.Errorf("a %s cannot be held by a %s", typeText(v.Type()), typeText(slot.Type()))
		}
	default:
		if v.Type() != slot.Type() {
			return fmt.Errorf("a %s cannot be held by a %s", typeText(v.Type()), typeText(slot.Type()))
		}
	}
	slot.Set(v)
	return nil
}

// describeAt describes s, the value at a key of node n that f makes
// sensitive or not, as Diff would.
func describeAt(n *schemaNode, f frame, s side) Value {
	switch {
	case n == nil:
		return describe(s, true)
	case n.kind == nodeLeaf:
		return describe(s, f.sensitive() || uncheckedValue(s))
	case f.sensitive() && n.kind != nodeBranch:
		return describe(s, true)
	case composite(s), n.kind != nodeDynamic:
		return shape(s)
	}
	return describe(s, uncheckedValue(s))
}

// owed is one change an applier is called with or owes: an entry of its
// acknowledgement, or a key a failed call left uncertain offered again.
type owed struct {
	keyRef
	change Change
	entry  *ackEntry // nil for a key offered again
}

// applyPlan is what one published config asks of the appliers.
type applyPlan struct {
	owes      [][]*ackEntry     // per applier: what it owes, in walk order
	events    [][]owed          // per applier: what it is called with
	blocked   []map[string]bool // per applier: keys it owes but cannot apply live
	unclaimed []*ackEntry       // keys no applier owns that differ from the first load
}

// plan measures what the appliers owe against cfg, the published config. A
// key that appears, disappears or turns nil — a structure — changes live
// only when one applier owns it and every change beneath it: otherwise the
// whole transition waits for a restart, and no applier is called with its
// part. initial skips that rule: a first apply builds from nothing.
func (a *application) plan(cfg reflect.Value, initial bool) *applyPlan {
	n := len(a.appliers)
	p := &applyPlan{
		owes:      make([][]*ackEntry, n),
		events:    make([][]owed, n),
		blocked:   make([]map[string]bool, n),
		unclaimed: a.boot.sorted(),
	}
	type transition struct {
		path  keyPath
		owner int
	}
	var transitions []transition
	for i, st := range a.appliers {
		p.owes[i] = st.ack.sorted()
		for _, e := range p.owes[i] {
			if e.change.Structural {
				transitions = append(transitions, transition{e.path, i})
			}
		}
	}
	for _, e := range p.unclaimed {
		if e.change.Structural {
			transitions = append(transitions, transition{e.path, -1})
		}
	}
	// othersBeneath reports a change another applier owes strictly beneath
	// path.
	othersBeneath := func(i int, path keyPath) bool {
		for j, owes := range p.owes {
			if j == i {
				continue
			}
			for _, e := range owes {
				if beneath(e.path, path) {
					return true
				}
			}
		}
		return false
	}
	live := func(i int, path keyPath) bool {
		return initial || !slices.ContainsFunc(transitions, func(t transition) bool {
			if t.owner != i {
				return beneath(path, t.path)
			}
			return (beneath(path, t.path) || slices.Equal(path, t.path)) && othersBeneath(i, t.path)
		})
	}
	for i, st := range a.appliers {
		p.blocked[i] = map[string]bool{}
		var events []owed
		for _, e := range p.owes[i] {
			if live(i, e.path) {
				events = append(events, owed{keyRef: e.keyRef, change: e.change, entry: e})
			} else {
				p.blocked[i][e.key] = true
			}
		}
		for key, ref := range st.unsure {
			if st.ack[key] != nil || !live(i, ref.path) {
				continue
			}
			now, n, f := locate(a.root, cfg, ref.path)
			v := describeAt(n, f, now)
			events = append(events, owed{keyRef: ref, change: Change{Key: key, Field: ownerOf(a.root, ref.path), Old: v, New: v}})
		}
		slices.SortFunc(events, func(a, b owed) int { return compareOrd(a.ord, b.ord) })
		p.events[i] = events
	}
	return p
}

// beneath reports whether key p lies strictly beneath key prefix.
func beneath(p, prefix keyPath) bool {
	return len(p) > len(prefix) && slices.Equal(p[:len(prefix)], prefix)
}

// pending is what rebasing a publication would make the acknowledgements:
// committed only if the config is published.
type pending struct {
	appliers []ackDiff
	boot     ackDiff
}

// rebase measures every acknowledgement against next, published after prev
// with changes, without changing the application.
func (a *application) rebase(prev, next reflect.Value, changes []published) (*pending, error) {
	r := rebaser{root: a.root, prev: prev, next: next}
	byOwner := make([][]published, len(a.appliers))
	var unowned []published
	for _, c := range changes {
		if i := a.owners.of(c.path); i >= 0 {
			byOwner[i] = append(byOwner[i], c)
		} else {
			unowned = append(unowned, c)
		}
	}
	p := &pending{appliers: make([]ackDiff, len(a.appliers))}
	for i, st := range a.appliers {
		d, err := r.rebase(st.ack, byOwner[i])
		if err != nil {
			return nil, err
		}
		p.appliers[i] = d
	}
	d, err := r.rebase(a.boot, unowned)
	if err != nil {
		return nil, err
	}
	p.boot = d
	return p, nil
}

func (a *application) commit(p *pending) {
	for i, st := range a.appliers {
		st.ack = p.appliers[i]
	}
	a.boot = p.boot
}

// start records the first load, cfg: every applier without ApplyInitial has
// applied what it owns, and the keys no applier owns are the baseline of
// what waits for a restart. An applier with ApplyInitial owes each of its
// keys as a change from nothing; owned lists them, the first load compared
// with no config.
func (a *application) start(owned []published) {
	for _, c := range owned {
		st := a.appliers[a.owners.of(c.path)]
		if st.ack == nil {
			st.ack = ackDiff{}
		}
		st.ack[c.key] = &ackEntry{keyRef: c.keyRef, key: c.key, whole: c.whole, held: c.held, change: c.change}
	}
}

// initialOwner reports whether key p belongs to an applier with
// ApplyInitial.
func (a *application) initialOwner(p keyPath) bool {
	i := a.owners.of(p)
	return i >= 0 && a.appliers[i].hook.initial
}

// account reports each applier's state and what waits for a restart:
// changes no applier owns, transitions no one applier owns whole, and
// restart keys.
func (a *application) account(p *applyPlan) ([]ApplierStatus, []Change) {
	statuses := make([]ApplierStatus, len(a.appliers))
	waiting := slices.Clone(p.unclaimed)
	for i, st := range a.appliers {
		owes := st.ack.sorted()
		for _, e := range owes {
			if p.blocked[i][e.key] || st.deferred[e.key] {
				waiting = append(waiting, e)
			}
		}
		maps.DeleteFunc(st.deferred, func(k string, _ bool) bool { return st.ack[k] == nil })
		statuses[i] = st.status.clone()
		statuses[i].Uncertain = len(st.unsure) > 0
		statuses[i].Pending = entryChanges(owes)
	}
	slices.SortFunc(waiting, func(a, b *ackEntry) int { return compareOrd(a.ord, b.ord) })
	return statuses, entryChanges(waiting)
}

// entryChanges returns the changes of entries, nil for none.
func entryChanges(entries []*ackEntry) []Change {
	if len(entries) == 0 {
		return nil
	}
	out := make([]Change, len(entries))
	for i, e := range entries {
		out[i] = e.change
		out[i].Field = cloneField(e.change.Field)
	}
	return out
}

func owedChanges(events []owed) []Change {
	out := make([]Change, len(events))
	for i, e := range events {
		out[i] = e.change
		out[i].Field = cloneField(e.change.Field)
	}
	return out
}

// applyAll calls every applier with something to apply in p, for the config
// published as generation gen, and records what each did. It returns the
// outcomes in registration order and the failures.
func (r *Reloader[T]) applyAll(ctx context.Context, trigger string, gen uint64, prev, cfg *T, p *applyPlan) ([]ApplyOutcome, []ApplierFailure) {
	var outcomes []ApplyOutcome
	var failures []ApplierFailure
	for i, st := range r.app.appliers {
		events := p.events[i]
		if len(events) == 0 {
			continue
		}
		out := ApplyOutcome{Applier: st.hook.name}
		fail := func(err error) {
			out.Error = &Problem{Kind: KindApply, Message: err.Error()}
			st.status.LastError = out.Error
			failures = append(failures, ApplierFailure{Applier: st.hook.name, Err: err})
			r.log().ErrorContext(ctx, "config applier failed", "applier", st.hook.name, "trigger", trigger, "generation", gen, "error", err)
		}
		if err := ctx.Err(); err != nil {
			// Published, but this applier was never called: nothing of it
			// happened, and it is called on the next published config.
			fail(fmt.Errorf("not called: %w", err))
			outcomes = append(outcomes, out)
			continue
		}
		res, err := st.call(ctx, ApplyEvent[T]{
			Trigger:    trigger,
			Generation: gen,
			Retry:      len(st.unsure) > 0,
			Previous:   prev,
			Desired:    cfg,
			Changes:    owedChanges(events),
		})
		var restart map[string]bool
		if err == nil {
			restart, err = restartKeys(res.RestartKeys, events)
		}
		now := time.Now().UTC()
		if err != nil {
			for _, e := range events {
				st.unsure[e.change.Key] = e.keyRef
			}
			st.status.Failures++
			st.status.LastFailure = now
			fail(err)
			outcomes = append(outcomes, out)
			continue
		}
		st.acknowledge(events, restart)
		st.status.LastApplied = now
		if len(st.unsure) == 0 {
			st.status.LastError = nil
		}
		for _, e := range events {
			if restart[e.change.Key] {
				out.Deferred = append(out.Deferred, e.change.Key)
			} else {
				out.Applied = append(out.Applied, e.change.Key)
			}
		}
		outcomes = append(outcomes, out)
	}
	return outcomes, failures
}

// acknowledge records events applied, except the keys in restart: what was
// published there is now what the applier holds, and a key a failed call
// left uncertain is certain again. A restart key stays as it was.
func (st *applierState) acknowledge(events []owed, restart map[string]bool) {
	for _, e := range events {
		if restart[e.change.Key] {
			continue
		}
		delete(st.unsure, e.change.Key)
		if e.entry != nil {
			delete(st.ack, e.entry.key)
		}
	}
	st.deferred = restart
}

// call runs the applier once, a panic turned into its error.
func (st *applierState) call(ctx context.Context, ev any) (res ApplyResult, err error) {
	st.status.Invocations++
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panicked: %v", p)
		}
	}()
	return st.hook.run(ctx, ev)
}

// restartKeys checks an applier's restart keys against the changes it was
// given.
func restartKeys(keys []string, events []owed) (map[string]bool, error) {
	given := map[string]bool{}
	for _, e := range events {
		given[e.change.Key] = true
	}
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		switch {
		case !given[k]:
			return nil, fmt.Errorf("returned restart key %s, which is not one of its changes", k)
		case out[k]:
			return nil, fmt.Errorf("returned restart key %s twice", k)
		}
		out[k] = true
	}
	return out, nil
}

// applyInitial runs the ApplyInitial appliers on the first load, published
// as generation 1. Any failure or restart key fails construction.
func (r *Reloader[T]) applyInitial(ctx context.Context, cfg *T, p *applyPlan) error {
	var failures []ApplierFailure
	for i, st := range r.app.appliers {
		if !st.hook.initial {
			continue
		}
		res, err := st.call(ctx, ApplyEvent[T]{
			Trigger:    TriggerInitial,
			Generation: 1,
			Desired:    cfg,
			Changes:    owedChanges(p.events[i]),
		})
		var restart map[string]bool
		if err == nil {
			restart, err = restartKeys(res.RestartKeys, p.events[i])
		}
		if err == nil && len(restart) > 0 {
			err = fmt.Errorf("cannot apply %s at start: a first apply takes no restart keys", strings.Join(slices.Sorted(maps.Keys(restart)), ", "))
		}
		if err != nil {
			failures = append(failures, ApplierFailure{Applier: st.hook.name, Err: err})
			continue
		}
		st.acknowledge(p.events[i], nil)
		st.status.LastApplied = time.Now().UTC()
	}
	if len(failures) > 0 {
		return &ApplyError{Failures: failures}
	}
	return nil
}
