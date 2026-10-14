package config

import (
	"errors"
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
// [ErrReload]. A nil error has no problem.
func Diagnose(err error) *Problem {
	if err == nil {
		return nil
	}
	kind := KindUnknown
	for _, c := range []struct {
		sentinel error
		kind     string
	}{
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

// ReloadResult is what one successful reload published: the snapshot it
// produced and the keys that changed from the config before it.
type ReloadResult[T any] struct {
	Snapshot Snapshot[T] `json:"snapshot"`
	// Changes lists every key whose value differs from the config the
	// reload replaced, as [Diff] reports them; empty when the sources did
	// not change.
	Changes []Change `json:"changes"`
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
