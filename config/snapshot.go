package config

import (
	"errors"
	"slices"
	"time"
)

// Snapshot is a Reloader's state at one moment: the published config, which
// publication it is, and the reload history up to it. Every field describes
// the same moment. It encodes to JSON without the config, for a status
// endpoint.
type Snapshot[T any] struct {
	// Config is the published config: shared by every reader, immutable.
	Config *T `json:"-"`
	// Generation counts publications: 1 for the initial load, and one more
	// for each successful reload.
	Generation uint64 `json:"generation"`
	// Stats is the reload history.
	Stats ReloadStats `json:"stats"`
	// Appliers is each [WithApplier] applier's account, in registration
	// order; empty without appliers.
	Appliers []ApplierStatus `json:"appliers,omitempty"`
	// Provenance maps each key that holds a value to where it came from:
	// the input source the merge kept and the stages that changed it. Only
	// [LoadSnapshot] and a reloader [WithProvenance] record it.
	Provenance map[string]Origin `json:"provenance,omitempty"`
	// PendingRestart lists, in walk order, what the published config
	// changes that the running process has not taken and will not before a
	// restart: a key no applier owns that differs from the first load, a
	// key an applier returned as a restart key that differs from what it
	// acknowledged, and every key of a structure that appeared, disappeared
	// or turned nil without one applier owning all of it. A key returns to
	// its old value, and it leaves the list. Empty without appliers.
	PendingRestart []Change `json:"pending_restart,omitempty"`
}

// ApplierStatus is one applier's account.
type ApplierStatus struct {
	Name string `json:"name"`
	// Invocations counts calls, the initial apply's included.
	Invocations uint64 `json:"invocations"`
	// Failures counts calls that returned an error, panicked, or returned a
	// restart key that was not one of their changes.
	Failures uint64 `json:"failures"`
	// LastApplied is when a call last returned nil, in UTC.
	LastApplied time.Time `json:"last_applied,omitzero"`
	// LastFailure is when a call last failed, in UTC.
	LastFailure time.Time `json:"last_failure,omitzero"`
	// LastError is the failure that left the applier uncertain or a config
	// it was never called for; nil once it applies everything it owes.
	LastError *Problem `json:"last_error,omitempty"`
	// Uncertain reports an applier that failed and has not applied
	// everything it owes since: what its failure did is unknown, so it is
	// called on every published config until it does.
	Uncertain bool `json:"uncertain,omitempty"`
	// Pending lists the keys it owns whose published value differs from
	// what it acknowledged: failed, deferred to a restart, or blocked by a
	// structure it does not own whole.
	Pending []Change `json:"pending,omitempty"`
}

func (s ApplierStatus) clone() ApplierStatus {
	if s.LastError != nil {
		p := *s.LastError
		s.LastError = &p
	}
	s.Pending = cloneChanges(s.Pending)
	return s
}

// ApplyOutcome is what one applier did for one published config.
type ApplyOutcome struct {
	Applier string `json:"applier"`
	// Applied are the keys it acknowledged.
	Applied []string `json:"applied,omitempty"`
	// Deferred are the keys it returned as restart keys.
	Deferred []string `json:"deferred,omitempty"`
	// Error is its failure; nil when it applied.
	Error *Problem `json:"error,omitempty"`
}

func (s Snapshot[T]) clone() Snapshot[T] {
	s.Stats = s.Stats.clone()
	if s.Provenance != nil {
		origins := make(map[string]Origin, len(s.Provenance))
		for k, o := range s.Provenance {
			o.Transforms = slices.Clone(o.Transforms)
			origins[k] = o
		}
		s.Provenance = origins
	}
	s.PendingRestart = cloneChanges(s.PendingRestart)
	if s.Appliers != nil {
		appliers := make([]ApplierStatus, len(s.Appliers))
		for i, a := range s.Appliers {
			appliers[i] = a.clone()
		}
		s.Appliers = appliers
	}
	return s
}

// cloneChanges copies changes so a caller changing one cannot reach
// another reader's.
func cloneChanges(changes []Change) []Change {
	if changes == nil {
		return nil
	}
	out := make([]Change, len(changes))
	for i, c := range changes {
		out[i] = c
		out[i].Field = cloneField(c.Field)
	}
	return out
}

// ReloadStats is a Reloader's reload history at one moment, as returned by
// [Reloader.Stats] and carried by a [Snapshot]. It encodes to JSON as is.
//
// Only reloads that ran to a verdict are counted. A manual Reload turned away
// by [ErrReloadInProgress] or [ErrReloaderStopped] ran no pipeline, and a
// reload abandoned because its context ended (including one cancelled by
// Stop) reached no verdict about the sources. The initial load is not a
// reload.
type ReloadStats struct {
	// Attempts is Successes+Failures — every reload that ran the pipeline.
	Attempts uint64 `json:"attempts"`
	// Successes counts reloads that published a new config.
	Successes uint64 `json:"successes"`
	// Failures counts reloads that left the previous config in place.
	Failures uint64 `json:"failures"`
	// LastTrigger names what drove the most recent attempt: one of the
	// Trigger* constants. Empty before the first attempt.
	LastTrigger string `json:"last_trigger,omitempty"`
	// LastSuccess is when the most recent successful reload published, in
	// UTC; zero, and absent from JSON, when none has.
	LastSuccess time.Time `json:"last_success,omitzero"`
	// LastFailure is when the most recent failed reload was rejected, in
	// UTC; zero, and absent from JSON, when none has failed.
	LastFailure time.Time `json:"last_failure,omitzero"`
	// LastError is the most recent attempt's failure, nil when that attempt
	// succeeded. A non-nil LastError beside an earlier LastSuccess means the
	// config from that success is still live. Each call that returns stats
	// returns its own copy.
	LastError *Problem `json:"last_error,omitempty"`
}

// clone returns s with its own copy of LastError, so a caller that changes
// what it was given cannot change what every other reader sees.
func (s ReloadStats) clone() ReloadStats {
	if s.LastError != nil {
		p := *s.LastError
		s.LastError = &p
	}
	return s
}

// Problem is an error as data, for a status endpoint or a stats snapshot: its
// category and its message.
type Problem struct {
	// Kind is the error's category: one of the Kind* constants.
	Kind string `json:"kind"`
	// Message is the error's text. The package's own text quotes no value
	// meant for a sensitive field; text from your providers, secret
	// populator, hooks and validator is yours, as written.
	Message string `json:"message"`
}

// Problem kinds: the category sentinel an error wraps.
const (
	KindApply          = "apply"           // ErrApply
	KindValidation     = "validation"      // ErrValidation
	KindFileRead       = "file_read"       // ErrFileRead
	KindFileParse      = "file_parse"      // ErrFileParse
	KindSource         = "source"          // ErrSource
	KindSecretProvider = "secret_provider" // ErrSecretProvider
	KindPostLoad       = "post_load"       // ErrPostLoad
	KindSchema         = "schema"          // ErrSchema
	KindUnknown        = "unknown"         // none of them
)

// Diagnose returns err as a [Problem]: the category of the sentinel it wraps
// and its message. A reload's error is categorized by what failed beneath
// [ErrReload]; an [ApplyError] is KindApply whatever its causes wrap. A nil
// error has no problem.
func Diagnose(err error) *Problem {
	if err == nil {
		return nil
	}
	kind := KindUnknown
	for _, c := range []struct {
		sentinel error
		kind     string
	}{
		{ErrApply, KindApply},
		{ErrValidation, KindValidation},
		{ErrFileParse, KindFileParse},
		{ErrFileRead, KindFileRead},
		{ErrSource, KindSource},
		{ErrSecretProvider, KindSecretProvider},
		{ErrPostLoad, KindPostLoad},
		{ErrSchema, KindSchema},
	} {
		if errors.Is(err, c.sentinel) {
			kind = c.kind
			break
		}
	}
	return &Problem{Kind: kind, Message: err.Error()}
}

// ReloadResult is what one reload published: the snapshot it produced, the
// keys that changed from the config before it, and what each applier did.
type ReloadResult[T any] struct {
	Snapshot Snapshot[T] `json:"snapshot"`
	// Changes lists every key whose value differs from the config the
	// reload replaced, as [Diff] reports them; empty when the sources did
	// not change.
	Changes []Change `json:"changes"`
	// Applied lists, in registration order, every applier the reload
	// called or owed a call it could not make; an applier that owed nothing
	// is absent.
	Applied []ApplyOutcome `json:"applied,omitempty"`
}

func (r ReloadResult[T]) clone() ReloadResult[T] {
	r.Snapshot = r.Snapshot.clone()
	r.Changes = cloneChanges(r.Changes)
	if r.Applied != nil {
		applied := make([]ApplyOutcome, len(r.Applied))
		for i, a := range r.Applied {
			a.Applied, a.Deferred = slices.Clone(a.Applied), slices.Clone(a.Deferred)
			if a.Error != nil {
				p := *a.Error
				a.Error = &p
			}
			applied[i] = a
		}
		r.Applied = applied
	}
	return r
}

// ReloadEvent is what a [WithOnReloadSuccess] callback receives: the trigger,
// the config replaced and the one published, and the reload's result.
type ReloadEvent[T any] struct {
	// Trigger names what drove the reload: one of the Trigger* constants.
	Trigger string `json:"trigger"`
	// Old is the config the reload replaced; New the one it published. Both
	// are shared and immutable.
	Old *T `json:"-"`
	New *T `json:"-"`
	// Result is what Reload returns for the same reload.
	Result ReloadResult[T] `json:"result"`
}
