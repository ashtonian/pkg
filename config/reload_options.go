package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"time"
)

// ReloadOption configures a [Reloader].
type ReloadOption func(*reloadOptions)

type reloadOptions struct {
	appliers     []applierHook
	onSuccess    []successHook
	onError      []func(trigger string, err error)
	fileWatch    bool
	debounce     time.Duration
	watched      []string
	pollInterval time.Duration
	signals      []os.Signal
	logger       *slog.Logger
	loggerSet    bool // WithReloadLogger was given, nil included
	provenance   bool
}

// successHook is a [WithOnReloadSuccess] callback with its config type
// erased, so ReloadOption stays non-generic; target is the *T it was written
// for, checked when the Reloader is built.
type successHook struct {
	target reflect.Type
	run    func(ctx context.Context, ev any)
}

func buildReloadOptions(opts []ReloadOption) *reloadOptions {
	o := &reloadOptions{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

func (o *reloadOptions) checkHookTypes(cfg reflect.Type) error {
	for i, h := range o.onSuccess {
		if h.target != cfg {
			return fmt.Errorf("config: WithOnReloadSuccess callback %d is for %s, the reloader loads %s", i, typeText(h.target), typeText(cfg))
		}
	}
	for _, a := range o.appliers {
		if a.target != cfg {
			return fmt.Errorf("config: WithApplier %q is for %s, the reloader loads %s", a.name, typeText(a.target), typeText(cfg))
		}
	}
	return nil
}

// WithProvenance records where every key's value came from on each load, as
// [LoadSnapshot] does: [Reloader.Snapshot] carries the origins of the config
// it publishes, and a refused reload keeps the previous ones. It costs a copy
// and a comparison of the decoded config around the secret populator and
// each post-load hook, per load. It needs [NewReloader]'s pipeline:
// NewReloaderFunc refuses it.
func WithProvenance() ReloadOption {
	return func(o *reloadOptions) { o.provenance = true }
}

// DefaultFileWatchDebounce is the [WithFileWatch] debounce window applied
// when the option's argument is zero or negative. 500ms is large enough to
// coalesce editor rename+write sequences yet small enough that operators
// see fresh config within one human reaction time.
const DefaultFileWatchDebounce = 500 * time.Millisecond

// WithOnReloadSuccess registers a callback invoked after every successful
// reload with the reload's context and a [ReloadEvent]: the trigger, the
// config replaced and the one published, and the keys that changed. It runs
// after the swap, so it reacts to a config already live and cannot veto it.
// Every successful reload publishes and calls it, a poll that changed nothing
// included — its event's Changes is then empty. Callbacks run in registration
// order, panics are recovered, and they run while the reload holds the
// single-flight slot: hand long work off, and never call [Reloader.Stop] or
// [Reloader.Arm] from one. A callback written for a type other than the
// reloader's fails construction.
func WithOnReloadSuccess[T any](fn func(ctx context.Context, ev ReloadEvent[T])) ReloadOption {
	hook := successHook{
		target: reflect.TypeFor[*T](),
		run:    func(ctx context.Context, ev any) { fn(ctx, ev.(ReloadEvent[T])) },
	}
	return func(o *reloadOptions) {
		o.onSuccess = append(o.onSuccess, hook)
	}
}

// WithOnReloadError registers a callback invoked after every failed reload,
// whatever triggered it — including the watchers [Reloader.Run] serves,
// whose errors would otherwise reach only the log. It fires in addition to
// the error [Reloader.Reload] returns. trigger is one of the Trigger*
// constants. Callbacks follow the [WithOnReloadSuccess] rules.
//
// Reloads that reach no verdict — [ErrReloadInProgress], [ErrReloaderStopped],
// or a reload abandoned because its context ended — do not invoke it.
func WithOnReloadError(fn func(trigger string, err error)) ReloadOption {
	return func(o *reloadOptions) {
		o.onError = append(o.onError, fn)
	}
}

// WithFileWatch reloads when a watched file changes: the config files of a
// [NewReloader], and any given to [WithWatchedFiles]. debounce coalesces a
// burst of filesystem events (editors that rename and write, atomic-write
// tools) into one reload; zero or negative selects
// [DefaultFileWatchDebounce]. A continuous stream of events delays a reload
// by at most ten debounce windows. A reloader with nothing to watch fails
// construction.
//
// Detected: direct writes, write-temp-then-rename saves, symlink swaps of the
// file or of any directory on its path, and a directory removed and recreated
// — including Kubernetes ConfigMap and Secret volume updates, where the
// watched path is a symlink into `..data` and the leaf file is never written.
// Paths resolve as the kernel resolves them, ".." after a symlink included,
// so the watch follows the file the loader opens. Every four debounce
// windows, and no more often than once a second (every 2s at the default
// debounce), the watch also resolves its paths again with no event to prompt
// it. That catches what sends the watch no event: an ancestor directory
// renamed away and recreated, whose event goes to the old tree; and, on
// kqueue platforms (macOS, *BSD), a relink whose only trace is a temporary
// name renamed over the link (`ln -s new link.tmp && mv -Tf link.tmp link`),
// which fsnotify there does not report because the directory's set of names
// is unchanged.
//
// NOT detected:
//
//   - ConfigMap/Secret volumes mounted with subPath. kubelet cannot swap a
//     symlink through a bind mount, so these never update in the container at
//     all — neither a watch nor polling can see a change. Mount the volume
//     without subPath.
//   - Sources that are not files: environment variables and providers. Use
//     [WithPollInterval], or [Reloader.SourceChanged] from whatever learns of
//     the change.
//   - A symlink directly under the filesystem root, such as /var or /tmp on
//     macOS, being re-pointed. Those belong to the OS; watching / would hold a
//     descriptor per entry on kqueue and can block on automount points.
func WithFileWatch(debounce time.Duration) ReloadOption {
	return func(o *reloadOptions) {
		o.fileWatch = true
		o.debounce = debounce
	}
}

// WithWatchedFiles adds files the file watch covers, and that Run compares
// with the last load once the watch is armed, beyond a NewReloader's config
// files: a file a secret populator or base provider reads, or, for
// [NewReloaderFunc], the files the caller's loader reads.
func WithWatchedFiles(paths ...string) ReloadOption {
	return func(o *reloadOptions) {
		o.watched = append(o.watched, paths...)
	}
}

// WithPollInterval reloads every interval: the fallback for sources without
// filesystem events (environment variables, providers) and for filesystems
// whose events are unreliable (NFS, some container runtimes). Every tick runs
// the whole pipeline, secrets included.
func WithPollInterval(interval time.Duration) ReloadOption {
	return func(o *reloadOptions) {
		o.pollInterval = interval
	}
}

// WithSignalReload reloads when the process receives one of signals,
// typically syscall.SIGHUP.
func WithSignalReload(signals ...os.Signal) ReloadOption {
	return func(o *reloadOptions) {
		o.signals = append(o.signals, signals...)
	}
}

// WithReloadLogger sets the logger for reload events. Without it, a
// NewReloader logs through its load options' [WithLogger] logger. A nil
// logger here, or no logger anywhere, means slog.Default() as it is when each
// line is written, so a reloader built before the process logger is
// configured logs through the configured one.
func WithReloadLogger(logger *slog.Logger) ReloadOption {
	return func(o *reloadOptions) {
		o.logger = logger
		o.loggerSet = true
	}
}
