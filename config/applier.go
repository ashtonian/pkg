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
// acknowledges nothing and leaves the applier uncertain — what it did may
// have half happened — so it is called again on every later published
// config, Retry set, until it applies everything it owes with no restart
// keys.
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
	// Retry reports an applier that failed and has not applied everything
	// it owes since: reconcile all of Desired it owns, not only Changes.
	Retry bool
	// Previous is the config published before Desired, nil for the initial
	// apply. It is not what this applier applied — Changes are measured
	// from that.
	Previous *T
	// Desired is the config just published; shared and immutable.
	Desired *T
	// Changes are the keys the applier owns whose published value differs
	// from what it last acknowledged, as [Diff] reports them. It can be
	// empty on a retry.
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

// application is a reloader's account of its appliers: what each has
// acknowledged, and the first load as the baseline for keys none owns.
// Guarded by the reloader's slot.
type application struct {
	appliers []*applierState
	owners   ownership
	boot     []flatEntry // the first load's keys no applier owns
}

type applierState struct {
	hook applierHook
	// ack is what the applier has acknowledged, by key: entries of the
	// configs it applied them from.
	ack       map[string]flatEntry
	deferred  map[string]bool // the restart keys of its last successful apply
	uncertain bool
	status    ApplierStatus // counters and the last outcome; Pending is set per publication
}

func newApplication(root *schemaNode, hooks []applierHook) (*application, error) {
	a := &application{}
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
		a.appliers = append(a.appliers, &applierState{hook: h, status: ApplierStatus{Name: h.name}})
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

// applyPlan is what one published config asks of the appliers.
type applyPlan struct {
	owned     [][]flatEntry          // per applier: the published keys it owns
	byKey     []map[string]flatEntry // the same, by key
	owes      [][]entryChange        // per applier: what it owes, measured from its acknowledgement
	events    [][]entryChange        // per applier: what it is called with
	blocked   []map[string]bool      // per applier: keys it owes but cannot apply live
	unclaimed []entryChange          // keys no applier owns that differ from the first load
}

// plan measures what the appliers owe for published, a config's entries. A
// key that appears, disappears or turns nil — a structure — changes live
// only when one applier owns it and every change beneath it: otherwise the
// whole transition waits for a restart, and no applier is called with its
// part. initial skips that rule: a first apply builds from nothing.
func (a *application) plan(published []flatEntry, initial bool) *applyPlan {
	n := len(a.appliers)
	p := &applyPlan{
		owned:   make([][]flatEntry, n),
		byKey:   make([]map[string]flatEntry, n),
		owes:    make([][]entryChange, n),
		events:  make([][]entryChange, n),
		blocked: make([]map[string]bool, n),
	}
	var unowned []flatEntry
	for _, e := range published {
		if i := a.owners.of(e.path); i >= 0 {
			p.owned[i] = append(p.owned[i], e)
		} else {
			unowned = append(unowned, e)
		}
	}
	type transition struct {
		path  keyPath
		owner int
	}
	var transitions []transition
	for i, st := range a.appliers {
		p.byKey[i] = entriesByKey(p.owned[i])
		owes := compareEntries(st.acknowledged(), p.owned[i])
		p.owes[i] = owes
		for _, c := range owes {
			if c.Structural {
				transitions = append(transitions, transition{c.path, i})
			}
		}
	}
	unclaimed := compareEntries(a.boot, unowned)
	p.unclaimed = unclaimed
	for _, c := range unclaimed {
		if c.Structural {
			transitions = append(transitions, transition{c.path, -1})
		}
	}
	// othersBeneath reports a change another applier owes strictly beneath
	// path.
	othersBeneath := func(i int, path keyPath) bool {
		for j, owes := range p.owes {
			if j == i {
				continue
			}
			for _, c := range owes {
				if beneath(c.path, path) {
					return true
				}
			}
		}
		return false
	}
	for i := range a.appliers {
		p.blocked[i] = map[string]bool{}
		for _, c := range p.owes[i] {
			live := initial || !slices.ContainsFunc(transitions, func(t transition) bool {
				if t.owner != i {
					return beneath(c.path, t.path)
				}
				return (beneath(c.path, t.path) || slices.Equal(c.path, t.path)) && othersBeneath(i, t.path)
			})
			if live {
				p.events[i] = append(p.events[i], c)
			} else {
				p.blocked[i][c.Key] = true
			}
		}
	}
	return p
}

// beneath reports whether key p lies strictly beneath key prefix.
func beneath(p, prefix keyPath) bool {
	return len(p) > len(prefix) && slices.Equal(p[:len(prefix)], prefix)
}

func entriesByKey(entries []flatEntry) map[string]flatEntry {
	m := make(map[string]flatEntry, len(entries))
	for _, e := range entries {
		m[e.path.String()] = e
	}
	return m
}

// acknowledged is what st has acknowledged, in walk order.
func (st *applierState) acknowledged() []flatEntry {
	entries := slices.Collect(maps.Values(st.ack))
	slices.SortFunc(entries, func(a, b flatEntry) int { return compareOrd(a.ord, b.ord) })
	return entries
}

// acknowledge records changes, applied from the published entries
// published, except the keys in restart.
func (st *applierState) acknowledge(changes []entryChange, restart map[string]bool, published map[string]flatEntry) {
	for _, c := range changes {
		if restart[c.Key] {
			continue
		}
		if c.subtree {
			// Compared whole: what lies beneath it on either side goes with it.
			for k, e := range st.ack {
				if within(e.ord, c.ord) {
					delete(st.ack, k)
				}
			}
			for k, e := range published {
				if within(e.ord, c.ord) {
					st.ack[k] = e
				}
			}
			continue
		}
		if e, ok := published[c.Key]; ok {
			st.ack[c.Key] = e
		} else {
			delete(st.ack, c.Key)
		}
	}
}

// start records the first load: the keys no applier owns become the
// baseline of what waits for a restart, and each applier without
// ApplyInitial acknowledges what it owns.
func (a *application) start(published []flatEntry) {
	for _, e := range published {
		i := a.owners.of(e.path)
		switch {
		case i < 0:
			a.boot = append(a.boot, e)
		case !a.appliers[i].hook.initial:
			if a.appliers[i].ack == nil {
				a.appliers[i].ack = map[string]flatEntry{}
			}
			a.appliers[i].ack[e.path.String()] = e
		}
	}
	for _, st := range a.appliers {
		if st.ack == nil {
			st.ack = map[string]flatEntry{}
		}
		st.deferred = map[string]bool{}
	}
}

// account reports each applier's state after p, and what waits for a
// restart: changes no applier owns, transitions no one applier owns whole,
// and restart keys.
func (a *application) account(p *applyPlan) ([]ApplierStatus, []Change) {
	statuses := make([]ApplierStatus, len(a.appliers))
	waiting := slices.Clone(p.unclaimed)
	for i, st := range a.appliers {
		owes := compareEntries(st.acknowledged(), p.owned[i])
		owed := map[string]bool{}
		for _, c := range owes {
			owed[c.Key] = true
			if p.blocked[i][c.Key] || st.deferred[c.Key] {
				waiting = append(waiting, c)
			}
		}
		maps.DeleteFunc(st.deferred, func(k string, _ bool) bool { return !owed[k] })
		statuses[i] = st.status.clone()
		statuses[i].Uncertain = st.uncertain
		statuses[i].Pending = changesOf(owes)
	}
	slices.SortFunc(waiting, func(a, b entryChange) int { return compareOrd(a.ord, b.ord) })
	return statuses, changesOf(waiting)
}

// changesOf returns the public changes of found, nil for none.
func changesOf(found []entryChange) []Change {
	if len(found) == 0 {
		return nil
	}
	out := make([]Change, len(found))
	for i, c := range found {
		out[i] = c.Change
		out[i].Field = cloneField(c.Field)
	}
	return out
}

// applyAll calls every applier that owes something in p, for the config
// published as generation gen, and records what each did. It returns the
// outcomes in registration order and the failures.
func (r *Reloader[T]) applyAll(ctx context.Context, trigger string, gen uint64, prev, cfg *T, p *applyPlan) ([]ApplyOutcome, []ApplierFailure) {
	var outcomes []ApplyOutcome
	var failures []ApplierFailure
	for i, st := range r.app.appliers {
		changes := p.events[i]
		if len(changes) == 0 && !st.uncertain {
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
			Retry:      st.uncertain,
			Previous:   prev,
			Desired:    cfg,
			Changes:    changesOf(changes),
		})
		var restart map[string]bool
		if err == nil {
			restart, err = restartKeys(res.RestartKeys, changes)
		}
		now := time.Now().UTC()
		if err != nil {
			st.uncertain = true
			st.status.Failures++
			st.status.LastFailure = now
			fail(err)
			outcomes = append(outcomes, out)
			continue
		}
		st.acknowledge(changes, restart, p.byKey[i])
		st.deferred = restart
		st.status.LastApplied = now
		if len(restart) == 0 {
			// Everything it owed applied: whatever an earlier failure left
			// half done is reconciled.
			st.uncertain = false
			st.status.LastError = nil
		}
		for _, c := range changes {
			if restart[c.Key] {
				out.Deferred = append(out.Deferred, c.Key)
			} else {
				out.Applied = append(out.Applied, c.Key)
			}
		}
		outcomes = append(outcomes, out)
	}
	return outcomes, failures
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
func restartKeys(keys []string, changes []entryChange) (map[string]bool, error) {
	given := map[string]bool{}
	for _, c := range changes {
		given[c.Key] = true
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
			Changes:    changesOf(p.events[i]),
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
		st.acknowledge(p.events[i], nil, p.byKey[i])
		st.status.LastApplied = time.Now().UTC()
	}
	if len(failures) > 0 {
		return &ApplyError{Failures: failures}
	}
	return nil
}
