package config

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Reloader watches configuration sources and atomically reloads the config
// struct when changes are detected. Access the current config via Config(),
// which uses atomic.Pointer for zero read contention.
//
// On reload failure (parse error, validation failure), the previous config
// is preserved and the error is reported through [WithOnReloadError], the
// log, and — for [Reloader.Reload] — the return value.
//
// Reloads are serialized. A manual Reload that finds another reload in flight
// returns [ErrReloadInProgress]; an automatic trigger (file change, poll,
// signal) instead waits for it and then reloads, so a change that lands while
// another reload is reading the sources is never lost.
type Reloader[T any] struct {
	current    atomic.Pointer[T]
	configOpts []Option
	logger     *slog.Logger

	// Reload options.
	onChange      []func(old, new *T)
	onReloadError []func(trigger string, err error)
	fileWatch     bool
	debounce      time.Duration
	pollInterval  time.Duration
	signals       []os.Signal

	// slot is the single-flight token: holding it is the right to run the
	// load pipeline, publish, and fire callbacks. Stop takes it and never
	// gives it back.
	slot chan struct{}
	// loaded is what the last successful load read from each config file.
	// Guarded by slot.
	loaded fileDigests
	stats  atomic.Pointer[ReloadStats] // written while holding slot, read lock-free

	watchArmed atomic.Bool // set once Start has registered the watcher's dirs
	stopOnce   sync.Once
	stopCh     chan struct{}
	quiesced   chan struct{} // closed once Stop holds the slot
	// changed holds at most one pending SourceChanged request; further
	// requests coalesce into it.
	changed chan struct{}
}

// watcherReady reports whether Start has finished arming the fsnotify watcher.
// Start is asynchronous by design (callers run it in a goroutine), so tests
// that mutate the filesystem need a way to know the watch is live before they
// do. Deliberately unexported: production code that polls this is a smell —
// Start already returns an error when arming fails.
func (r *Reloader[T]) watcherReady() bool { return r.watchArmed.Load() }

// Reload trigger names. These form a closed vocabulary so consumers can use
// them directly as a bounded metric label.
const (
	TriggerManual        = "manual"         // an explicit Reload call (including ReloadHandler)
	TriggerFileChange    = "file_change"    // WithFileWatch saw the file or its symlink target change
	TriggerPoll          = "poll"           // WithPollInterval ticked
	TriggerSignal        = "signal"         // WithSignalReload received one of its signals
	TriggerSourceChanged = "source_changed" // the caller reported a change via SourceChanged
)

// ReloadStats is an immutable point-in-time snapshot of a Reloader's reload
// history, returned by [Reloader.Stats].
//
// Only reloads that ran to a verdict are counted. A manual Reload turned away
// by [ErrReloadInProgress] or [ErrReloaderStopped] ran no pipeline, and a
// reload abandoned because its context ended (including one cancelled by
// Stop) reached no verdict about the sources.
type ReloadStats struct {
	// Attempts is Successes+Failures — every reload that ran the pipeline.
	Attempts uint64
	// Successes counts reloads that produced a new config and swapped it in.
	Successes uint64
	// Failures counts reloads that left the previous config in place.
	Failures uint64
	// LastTrigger names what drove the most recent attempt: one of the
	// Trigger* constants. Empty before the first attempt.
	LastTrigger string
	// LastSuccess is when the most recent successful reload swapped. Zero if
	// no reload has succeeded since construction. Note that the initial load
	// performed by NewReloader is not a reload and does not set this.
	LastSuccess time.Time
	// LastFailure is when the most recent failed reload was rejected. Zero if
	// none has failed.
	LastFailure time.Time
	// LastError is the error from the most recent attempt, or nil when that
	// attempt succeeded. A non-nil LastError alongside a recent LastSuccess
	// cannot happen: this field is cleared on every success.
	LastError error
}

// ReloadOption configures a Reloader.
type ReloadOption[T any] func(*Reloader[T])

// DefaultFileWatchDebounce is the [WithFileWatch] debounce window applied
// when the option's argument is zero or negative. 500ms is large enough to
// coalesce editor rename+write sequences yet small enough that operators
// see fresh config within one human reaction time.
const DefaultFileWatchDebounce = 500 * time.Millisecond

// WithOnChange registers a callback invoked after a successful reload.
// Multiple callbacks are invoked in registration order. The callback
// receives the old and new config pointers. Callbacks run while the reload
// holds the single-flight slot: long-running work should be handed off, and a
// callback must not call [Reloader.Stop], which waits for that slot.
func WithOnChange[T any](fn func(old, new *T)) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.onChange = append(r.onChange, fn)
	}
}

// WithFileWatch enables fsnotify-based watching of config files specified
// via WithConfigFile / WithMustConfigFile. When a watched file changes,
// the full GetConfig[T] pipeline is re-run after debounce elapses.
//
// debounce coalesces bursts of rapid filesystem events (editors that issue
// rename+write, atomic-write toolchains) into a single reload. Pass 0 to
// select [DefaultFileWatchDebounce] (500ms), or a positive value to override.
// Negative values are treated as the default.
//
// Detected: direct writes, write-temp-then-rename saves, and symlink swaps —
// including Kubernetes ConfigMap and Secret volume updates, where the watched
// path is a symlink into `..data` and the leaf file is never written.
//
// NOT detected, all three requiring [WithPollInterval] instead:
//
//   - ConfigMap/Secret volumes mounted with subPath. kubelet cannot swap a
//     symlink through a bind mount, so these never update in the container at
//     all — no watcher can help.
//   - Sources that change without a filesystem event: environment variables,
//     and any provider wired via [WithProvider].
//   - On kqueue platforms (macOS, *BSD) only: a symlink relink whose sole
//     trace is a temporary name that is renamed away, e.g.
//     `ln -s new link.tmp && mv -Tf link.tmp link`. fsnotify detects directory
//     changes there by diffing directory listings, so a change that leaves the
//     set of names unchanged produces no event at all. Kubernetes volumes are
//     unaffected — kubelet leaves a durable new `..<timestamp>` directory —
//     and Linux inotify reports the rename directly, so this is macOS/BSD
//     hand-rolled-deploy only.
func WithFileWatch[T any](debounce time.Duration) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.fileWatch = true
		r.debounce = debounce
	}
}

// WithPollInterval enables periodic reload at the given interval.
// On each tick the full GetConfig[T] pipeline is re-run. This is useful
// as a fallback for environments where fsnotify is unreliable (NFS, some
// container runtimes) or when config sources change without file events
// (e.g., environment variables).
func WithPollInterval[T any](interval time.Duration) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.pollInterval = interval
	}
}

// WithSignalReload triggers a reload when the specified OS signals are
// received (typically syscall.SIGHUP).
func WithSignalReload[T any](signals ...os.Signal) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.signals = append(r.signals, signals...)
	}
}

// WithOnReloadError registers a callback invoked after every FAILED reload,
// including the ones driven by the internal watchers in [Reloader.Start],
// where the error would otherwise reach nothing but the log. It fires in
// addition to — not instead of — the error [Reloader.Reload] returns, so a
// single registration covers every trigger.
//
// trigger is one of the Trigger* constants. Callbacks run in registration
// order and panics are recovered, matching [WithOnChange], including the rule
// that a callback must not call [Reloader.Stop]. Keep them cheap: they run on
// the reload path.
//
// Reloads that reach no verdict — [ErrReloadInProgress], [ErrReloaderStopped],
// or a reload abandoned because its context ended — do NOT invoke it.
func WithOnReloadError[T any](fn func(trigger string, err error)) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.onReloadError = append(r.onReloadError, fn)
	}
}

// NewReloader creates a Reloader that loads initial config via
// GetConfig[T](ctx, configOpts...) and optionally watches for changes.
// The configOpts are stored and re-used on every reload (each reload uses
// the ctx passed to Reload, or to Start for automatic triggers). The logger
// set by [WithLogger] in configOpts also receives every reload event.
func NewReloader[T any](ctx context.Context, reloadOpts []ReloadOption[T], configOpts ...Option) (*Reloader[T], error) {
	o := buildOptions(configOpts)
	cfg, files, err := load[T](ctx, o)
	if err != nil {
		return nil, err
	}

	r := &Reloader[T]{
		configOpts: configOpts,
		logger:     o.logger,
		slot:       make(chan struct{}, 1),
		loaded:     files,
		stopCh:     make(chan struct{}),
		changed:    make(chan struct{}, 1),
		quiesced:   make(chan struct{}),
	}
	r.current.Store(cfg)

	for _, opt := range reloadOpts {
		opt(r)
	}

	return r, nil
}

// Config returns the current configuration. This is an atomic load
// and safe to call from any goroutine without locking.
func (r *Reloader[T]) Config() *T {
	return r.current.Load()
}

// Reload triggers an immediate config reload. Returns the new config on
// success. On failure the previous config is preserved and an error wrapping
// both [ErrReload] and the underlying cause is returned.
//
// If another reload is in flight this call does not wait: it returns
// [ErrReloadInProgress], which deliberately does NOT wrap ErrReload — no
// pipeline ran and the config is unchanged. After Stop it returns
// [ErrReloaderStopped]. If ctx ends while the reload runs, the reload is
// abandoned: the error carries ctx.Err(), not ErrReload, and nothing is
// recorded. Callers that alert on breakage check ErrReload alone.
func (r *Reloader[T]) Reload(ctx context.Context) (*T, error) {
	if err := r.acquire(ctx, TriggerManual, false); err != nil {
		return nil, err
	}
	defer r.release()
	return r.reloadHeld(ctx, TriggerManual)
}

// SourceChanged requests a reload because a source changed that the reloader
// does not watch itself — a [WithProvider] source, or anything else the caller
// learns about through its own events. It never blocks: requests made while
// one is pending coalesce into it, and the reload they trigger starts after
// the latest of them, so it reads every change made before the call. Like the
// watchers, it waits for a reload already in flight instead of being dropped.
//
// Start serves the requests; one made before Start runs once Start does, and
// requests after Stop are ignored. Use Reload instead when the caller needs
// the outcome.
func (r *Reloader[T]) SourceChanged() {
	select {
	case r.changed <- struct{}{}:
	default: // a request is already pending; it will read this change too
	}
}

// Stats returns a snapshot of this reloader's reload history. Safe to call
// from any goroutine; the snapshot is internally consistent (every field
// describes the same moment).
func (r *Reloader[T]) Stats() ReloadStats {
	if s := r.stats.Load(); s != nil {
		return *s
	}
	return ReloadStats{}
}

// acquire takes the single-flight slot for trigger. With wait false it fails
// fast with ErrReloadInProgress; with wait true it blocks until the slot
// frees, Stop is called, or ctx ends.
func (r *Reloader[T]) acquire(ctx context.Context, trigger string, wait bool) error {
	select {
	case <-r.stopCh:
		return ErrReloaderStopped
	default:
	}

	select {
	case r.slot <- struct{}{}:
	default:
		if !wait {
			return ErrReloadInProgress
		}
		r.logger.DebugContext(ctx, "config reload waiting for the reload in flight", "trigger", trigger)
		select {
		case r.slot <- struct{}{}:
		case <-r.stopCh:
			return ErrReloaderStopped
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Stop may have been called while this caller waited; its own wait for the
	// slot proceeds as soon as the slot is handed back.
	select {
	case <-r.stopCh:
		r.release()
		return ErrReloaderStopped
	default:
		return nil
	}
}

func (r *Reloader[T]) release() { <-r.slot }

// reloadHeld runs one attempt attributed to trigger. The caller holds the
// slot. Every reload path funnels through here so stats, callbacks, and
// logging stay uniform across manual, file-watch, poll, and signal triggers.
//
// A reload whose context has ended is abandoned, not refuted: it publishes
// nothing, counts as neither success nor failure and fires no callback — even
// when the context ends after the sources were read, say inside a post-load
// hook that went on to return nil.
func (r *Reloader[T]) reloadHeld(ctx context.Context, trigger string) (*T, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("config reload abandoned: %w", err)
	}
	o := buildOptions(r.configOpts)
	o.requireFiles(r.loaded)

	cfg, files, err := load[T](ctx, o)
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Say nothing about the sources, which may be fine.
		if err != nil && !errors.Is(err, ctxErr) {
			ctxErr = errors.Join(ctxErr, err)
		}
		return nil, fmt.Errorf("config reload abandoned: %w", ctxErr)
	}
	if err != nil {
		reloadErr := fmt.Errorf("%w: %w", ErrReload, err)
		r.recordAttempt(trigger, reloadErr)
		r.notifyReloadError(trigger, reloadErr)
		return nil, reloadErr
	}

	r.loaded = files
	old := r.current.Swap(cfg)
	r.recordAttempt(trigger, nil)
	r.notifyCallbacks(old, cfg)
	return cfg, nil
}

// recordAttempt folds one completed attempt into the stats snapshot. Called
// only with the slot held, so the read-modify-write needs no further
// synchronisation; readers see the new snapshot atomically.
func (r *Reloader[T]) recordAttempt(trigger string, err error) {
	next := r.Stats()
	next.Attempts++
	next.LastTrigger = trigger
	next.LastError = err
	if err != nil {
		next.Failures++
		next.LastFailure = time.Now()
	} else {
		next.Successes++
		next.LastSuccess = time.Now()
	}
	r.stats.Store(&next)
}

// Start runs the reload loop until Stop is called or ctx ends: the configured
// watchers — file watch, poll interval, signals — and [Reloader.SourceChanged]
// requests. It returns nil after Stop (immediately if already stopped) and
// ctx.Err() when ctx ends. Typically called in a goroutine:
// go reloader.Start(ctx).
//
// Every automatic reload runs on Start's goroutine with a context that Stop
// also cancels, and the file-watch debounce timer is owned by the same loop,
// so no automatic reload can begin once Start has returned.
func (r *Reloader[T]) Start(ctx context.Context) error {
	paths := filePaths(r.configOpts)
	hasFileWatch := r.fileWatch && len(paths) > 0
	hasPoll := r.pollInterval > 0
	hasSignal := len(r.signals) > 0

	select {
	case <-r.stopCh:
		return nil
	default:
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Stop cancels runCtx so an automatic reload in flight abandons its
	// secret lookups instead of holding Stop up. Exits when Start returns.
	go func() {
		select {
		case <-r.stopCh:
			cancel()
		case <-runCtx.Done():
		}
	}()

	var (
		fw        *fileWatch
		events    <-chan fsnotify.Event
		watchErrs <-chan error
		settled   <-chan time.Time
	)
	if hasFileWatch {
		var err error
		fw, err = newFileWatch(paths, cmp.Or(max(r.debounce, 0), DefaultFileWatchDebounce), r.logger)
		if err != nil {
			return fmt.Errorf("setting up file watch: %w", err)
		}
		defer func() {
			if err := fw.close(); err != nil {
				r.logger.WarnContext(ctx, "closing config file watcher", "error", err)
			}
		}()
		events, watchErrs, settled = fw.watcher.Events, fw.watcher.Errors, fw.timer.C
		r.watchArmed.Store(true)
		// No event reports a change made before the watch existed — between
		// NewReloader's load and now — so compare the files with what was
		// loaded once, now that later changes cannot be missed.
		r.catchUp(runCtx)
	}

	var sigCh <-chan os.Signal
	if hasSignal {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, r.signals...)
		sigCh = ch
		defer signal.Stop(ch)
	}

	var tickCh <-chan time.Time
	if hasPoll {
		ticker := time.NewTicker(r.pollInterval)
		tickCh = ticker.C
		defer ticker.Stop()
	}

	r.logger.InfoContext(ctx, "config reloader started",
		"file_watch", hasFileWatch,
		"poll_interval", r.pollInterval,
		"signal_count", len(r.signals),
	)

	for {
		select {
		case <-runCtx.Done():
			select {
			case <-r.stopCh:
				r.logger.InfoContext(ctx, "config reloader stopped")
				return nil
			default:
				r.logger.InfoContext(ctx, "config reloader context cancelled")
				return ctx.Err()
			}

		case <-tickCh:
			r.autoReload(runCtx, TriggerPoll)

		case <-r.changed:
			r.autoReload(runCtx, TriggerSourceChanged)

		case sig := <-sigCh:
			r.logger.InfoContext(ctx, "config reload triggered by signal", "signal", sig)
			r.autoReload(runCtx, TriggerSignal)

		case ev, ok := <-events:
			if !ok {
				r.logger.ErrorContext(ctx, "config file watcher closed unexpectedly; file-change reloads are off")
				events, watchErrs, settled = nil, nil, nil
				continue
			}
			fw.observe(ev)

		case err, ok := <-watchErrs:
			if !ok {
				watchErrs = nil
				continue
			}
			r.logger.ErrorContext(ctx, "config file watcher error", "error", err)

		case <-settled:
			path, changed, err := fw.settle()
			if err != nil {
				r.logger.ErrorContext(ctx, "config file watch is missing directories; changes there will not reload", "error", err)
			}
			if changed {
				r.autoReload(runCtx, TriggerFileChange, "path", path)
			}
		}
	}
}

// autoReload runs one watcher-driven reload, waiting for any reload already in
// flight so the change that triggered this one is read after it lands.
func (r *Reloader[T]) autoReload(ctx context.Context, trigger string, attrs ...any) {
	if err := r.acquire(ctx, trigger, true); err != nil {
		return // stopping: Stop or ctx ended, and Start's loop exits next
	}
	defer r.release()
	r.reloadAndLog(ctx, trigger, attrs...)
}

// catchUp reloads if a config file no longer holds what the last load read.
func (r *Reloader[T]) catchUp(ctx context.Context) {
	if err := r.acquire(ctx, TriggerFileChange, true); err != nil {
		return
	}
	defer r.release()
	if path, changed := r.changedSinceLoad(); changed {
		r.reloadAndLog(ctx, TriggerFileChange, "path", path, "changed", "before the watch started")
	}
}

// changedSinceLoad compares every configured file with what the last load
// read from it: changed, newly present or gone. The caller holds the slot.
func (r *Reloader[T]) changedSinceLoad() (string, bool) {
	for _, path := range filePaths(r.configOpts) {
		was, loaded := r.loaded[path]
		data, err := os.ReadFile(path)
		switch {
		case err != nil && loaded, err == nil && !loaded:
			return path, true
		case err == nil && sha256.Sum256(data) != was:
			return path, true
		}
	}
	return "", false
}

// reloadAndLog runs one automatic reload and logs its outcome. The caller
// holds the slot.
func (r *Reloader[T]) reloadAndLog(ctx context.Context, trigger string, attrs ...any) {
	args := append([]any{"trigger", trigger}, attrs...)
	if _, err := r.reloadHeld(ctx, trigger); err != nil {
		if ctx.Err() != nil {
			return
		}
		r.logger.ErrorContext(ctx, "config reload failed", append(args, "error", err)...)
		return
	}
	level := slog.LevelInfo
	if trigger == TriggerPoll {
		level = slog.LevelDebug // a poll reloads on every tick; Info would flood
	}
	r.logger.Log(ctx, level, "config reloaded", args...)
}

// Stop stops the reloader and waits until no reload is running. After Stop
// returns nil, no reload — automatic or manual — will run again, and no
// WithOnChange or WithOnReloadError callback will fire: it is safe to tear
// down whatever the callbacks touch. Reload returns [ErrReloaderStopped] from
// then on, and Start returns.
//
// An automatic reload in flight is cancelled. A manual Reload in flight runs
// to completion under its caller's context; if ctx ends first, Stop returns
// ctx's error and the reloader finishes stopping in the background once that
// reload returns. Stop is idempotent and safe for concurrent use, but must
// not be called from a WithOnChange or WithOnReloadError callback: those run
// holding the slot Stop waits for.
func (r *Reloader[T]) Stop(ctx context.Context) error {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		go func() {
			// Taking the slot waits out whichever reload holds it; never
			// giving it back is what keeps later reloads from starting.
			r.slot <- struct{}{}
			close(r.quiesced)
		}()
	})
	select {
	case <-r.quiesced:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stopping config reloader: waiting for the reload in flight: %w", ctx.Err())
	}
}

// notifyCallbacks invokes all registered onChange callbacks, recovering
// from panics to prevent a misbehaving callback from crashing the
// application.
func (r *Reloader[T]) notifyCallbacks(old, new *T) {
	for _, fn := range r.onChange {
		func() {
			defer func() {
				if p := recover(); p != nil {
					r.logger.Error("config change callback panicked", "panic", p)
				}
			}()
			fn(old, new)
		}()
	}
}

// notifyReloadError invokes all registered onReloadError callbacks, with the
// same panic isolation notifyCallbacks applies.
func (r *Reloader[T]) notifyReloadError(trigger string, err error) {
	for _, fn := range r.onReloadError {
		func() {
			defer func() {
				if p := recover(); p != nil {
					r.logger.Error("config reload-error callback panicked", "panic", p)
				}
			}()
			fn(trigger, err)
		}()
	}
}

// filePaths extracts config file paths from the config options.
func filePaths(opts []Option) []string {
	o := buildOptions(opts)
	paths := make([]string, 0, len(o.configFiles))
	for _, cf := range o.configFiles {
		paths = append(paths, cf.path)
	}
	return paths
}

// fileWatch is the file-watch state behind Start. It is driven entirely by
// Start's loop — events in through observe, the debounce timer out through
// settle — so it needs no locking, and closing it stops the timer for good.
//
// Config reaches a process three ways, and each is detected:
//
//   - Direct write to the file, including the write-temp-then-rename pattern
//     editors and atomic-write toolchains use, and removal. Caught by
//     watching the file's PARENT DIRECTORY and matching events by name.
//   - Symlink swap, where the watched path is a link whose target moves and
//     the leaf path itself is never written. This is how Kubernetes publishes
//     ConfigMap and Secret volumes (kubelet writes a new timestamped dir, then
//     renames `..data` over the old one; every key is a symlink into `..data`)
//     and how release-directory deploys work (`ln -sfn`). fsnotify does not
//     follow symlinks, so a name match never fires. Caught by resolving each
//     path, watching the resolved target's directory too, and re-resolving
//     after every burst of events in a watched directory.
//   - An edit of a symlink's current target, matched by the target's name.
//
// The subscriptions are reconciled after every burst: the parent of each
// declared path and of each current target — or, while one does not exist
// yet, its nearest existing ancestor, so a directory created later is still
// noticed — and nothing else, so retired release directories are dropped.
type fileWatch struct {
	watcher *fsnotify.Watcher
	logger  *slog.Logger

	// declared is every path the operator declared, absolute.
	declared []string
	// resolved maps each declared path to its symlink target as last
	// observed; a path that is not a symlink maps to itself.
	resolved map[string]string
	// reach maps each directory a declared path needs to the directory
	// watched for it: itself, or its nearest existing ancestor.
	reach map[string]string
	// watching is the set of directories subscribed.
	watching map[string]bool

	// timer is the debounce: every event resets it, so a burst collapses into
	// one settle. Created stopped; fires only once armed by observe.
	timer    *time.Timer
	debounce time.Duration
	// burstStart is when the pending burst's first event arrived, zero when
	// idle. A burst may postpone settle by at most maxDebounceWindows
	// debounce windows, so a steadily written sibling file in a watched
	// directory cannot hold a real change off forever.
	burstStart time.Time
	// pendingDirect names the declared path a write, create, removal or
	// rename of it or its target landed on since the last settle, or "" if
	// the pending events were all indirect.
	pendingDirect string
}

// newFileWatch subscribes to every directory paths need. A subscription that
// cannot be made fails construction: a watch that silently covers less than
// it was asked to would never report the changes it misses.
func newFileWatch(paths []string, debounce time.Duration, logger *slog.Logger) (*fileWatch, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating fsnotify watcher: %w", err)
	}

	timer := time.NewTimer(debounce)
	timer.Stop()
	w := &fileWatch{
		watcher:  watcher,
		logger:   logger,
		resolved: make(map[string]string, len(paths)),
		reach:    make(map[string]string),
		watching: make(map[string]bool),
		timer:    timer,
		debounce: debounce,
	}
	for _, p := range paths {
		absPath, err := filepath.Abs(p)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("resolving config file path %q: %w", p, err), w.close())
		}
		w.declared = append(w.declared, absPath)
		w.resolved[absPath] = resolveTarget(absPath)
	}
	if _, err := w.reconcile(); err != nil {
		return nil, errors.Join(err, w.close())
	}
	return w, nil
}

// reconcile subscribes to the directories the declared paths need now and
// drops every other subscription. appeared reports a needed directory that
// came into existence since the last reconcile: a file inside it may have
// appeared with it, before any watch could see it.
func (w *fileWatch) reconcile() (appeared bool, err error) {
	want := make(map[string]bool, 2*len(w.declared))
	for _, p := range w.declared {
		want[filepath.Dir(p)] = true
		want[filepath.Dir(w.resolved[p])] = true
	}
	desired := make(map[string]bool, len(want))
	reach := make(map[string]string, len(want))
	for dir := range want {
		watched := nearestExistingDir(dir)
		reach[dir] = watched
		desired[watched] = true
		if was, ok := w.reach[dir]; ok && was != dir && watched == dir {
			appeared = true
		}
	}
	w.reach = reach

	var errs []error
	for dir := range desired {
		if w.watching[dir] {
			continue
		}
		if err := w.watcher.Add(dir); err != nil {
			errs = append(errs, fmt.Errorf("watching config directory %q: %w", dir, err))
			continue
		}
		w.watching[dir] = true
	}
	for dir := range w.watching {
		if desired[dir] {
			continue
		}
		// A directory that was deleted has already lost its watch;
		// removing it again fails harmlessly.
		_ = w.watcher.Remove(dir)
		delete(w.watching, dir)
	}
	return appeared, errors.Join(errs...)
}

// nearestExistingDir returns dir, or its closest ancestor that exists.
func nearestExistingDir(dir string) string {
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// owner returns the declared path an event on name affects directly: name is
// that path or its current symlink target.
func (w *fileWatch) owner(name string) (string, bool) {
	for _, p := range w.declared {
		if name == p || name == w.resolved[p] {
			return p, true
		}
	}
	return "", false
}

// observe records one filesystem event and (re)arms the debounce. A write,
// create, removal or rename of a declared file or its target is a definite
// change. Anything else in a watched directory is only a hint — it may be
// the first step of a symlink swap, a directory on the way to a declared
// path, or an unrelated sibling file — decided after the debounce, in settle.
func (w *fileWatch) observe(ev fsnotify.Event) {
	absName, err := filepath.Abs(ev.Name)
	if err != nil {
		return
	}
	if p, ok := w.owner(absName); ok && ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
		w.pendingDirect = p
	}
	now := time.Now()
	if w.burstStart.IsZero() {
		w.burstStart = now
	}
	deadline := w.burstStart.Add(maxDebounceWindows * w.debounce)
	w.timer.Reset(max(min(w.debounce, deadline.Sub(now)), 0))
}

// maxDebounceWindows bounds how long a continuous stream of events can delay
// a file-change reload, in debounce windows.
const maxDebounceWindows = 10

// settle decides, once the filesystem has stopped moving, whether anything the
// reloader reads actually changed, and names the path that did. A
// subscription that can no longer be made is returned as err; the change is
// still reported.
//
// Deferring the symlink check to here rather than doing it per event is
// load-bearing. A Kubernetes ConfigMap update emits its events BEFORE the swap
// lands: the new `..2026_…` directory and `..data_tmp` are both created, and
// only then is `..data` renamed — an operation that on some platforms produces
// no further event at all. Re-resolving at event time therefore always reads
// the pre-swap target and concludes nothing changed. Re-resolving after the
// debounce reads the settled state.
func (w *fileWatch) settle() (path string, changed bool, err error) {
	path = w.pendingDirect
	w.pendingDirect = ""
	w.burstStart = time.Time{}
	movedFrom, movedTo, moved := w.refreshResolved()
	appeared, err := w.reconcile()

	if moved {
		w.logger.Debug("config file symlink target changed",
			"old_target", movedFrom, "new_target", movedTo)
	}
	switch {
	case path != "":
		return path, true, err
	case moved:
		return movedTo, true, err
	case appeared:
		return "", true, err
	default:
		// Events fired but nothing we read moved — a sibling file in a
		// shared directory, or the first half of a swap we already handled.
		return "", false, err
	}
}

// refreshResolved re-resolves every declared path and records new targets.
// Reports the last move observed, if any.
func (w *fileWatch) refreshResolved() (from, to string, moved bool) {
	for _, path := range w.declared {
		was, now := w.resolved[path], resolveTarget(path)
		if now == was {
			continue
		}
		w.resolved[path] = now
		from, to, moved = was, now, true
	}
	return from, to, moved
}

func (w *fileWatch) close() error {
	w.timer.Stop()
	return w.watcher.Close()
}

// resolveTarget returns path with symlinks resolved, or path itself when it
// cannot be resolved. Returning the input on failure is deliberate: a path
// that is briefly dangling mid-swap must not read as "the target changed" on
// the way out and again on the way back, which would reload twice for one swap.
func resolveTarget(path string) string {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return target
}
